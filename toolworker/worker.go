package toolworker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var errInvalidDefinition = errors.New("invalid tool definition")

type Worker struct {
	config Config
	client client
	mu     sync.RWMutex
	tools  map[string]registeredTool
	run    bool

	afterClaim func()
}

type registeredTool struct {
	definition Definition
	handler    Handler
}

func New(config Config) *Worker {
	if config.MaxConcurrentCalls <= 0 {
		config.MaxConcurrentCalls = 1
	}
	if config.ClaimPollInterval <= 0 {
		config.ClaimPollInterval = time.Second
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	if config.RefreshSkew <= 0 {
		config.RefreshSkew = time.Minute
	}
	if config.ManifestPublishPolicy == "" {
		config.ManifestPublishPolicy = ManifestPublishNever
	}
	return &Worker{
		config: config,
		client: client{baseURL: config.BaseURL, agentID: config.AgentID, token: config.ToolServiceToken, http: config.HTTPClient},
		tools:  map[string]registeredTool{},
	}
}

func (w *Worker) Handle(name string, definition Definition, handler Handler) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run {
		return errors.New("toolworker: handlers cannot be registered after Run starts")
	}
	if name == "" || definition.Version == "" || definition.Description == "" || definition.InputSchema == nil || handler == nil {
		return errInvalidDefinition
	}
	if _, ok := w.tools[name]; ok {
		return fmt.Errorf("toolworker: duplicate handler for %q", name)
	}
	definition.Name = name
	w.tools[name] = registeredTool{definition: definition, handler: handler}
	return nil
}

func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	if w.run {
		w.mu.Unlock()
		return errors.New("toolworker: Run already started")
	}
	w.run = true
	w.mu.Unlock()

	if w.config.ManifestPublishPolicy == ManifestPublishOnStart {
		if _, err := w.client.publishManifest(ctx, w.config.Namespace, w.definitions(), w.config.ManifestPublishOptions); err != nil {
			return err
		}
	}
	registration, err := w.client.registerExecutor(ctx, []string{w.config.Namespace})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var active int32
	var wg sync.WaitGroup
	var registrationMu sync.RWMutex
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeatLoop(ctx, &registration, &registrationMu)
	}()

	nonce := processNonce()
	claimAttempt := 0
	claimKey := ""
	claimKeyExecutorToken := ""
	for {
		if ctx.Err() != nil {
			wg.Wait()
			<-heartbeatDone
			return nil
		}
		if int(atomic.LoadInt32(&active)) >= w.config.MaxConcurrentCalls {
			if !sleep(ctx, time.Millisecond) {
				continue
			}
			continue
		}
		registrationMu.RLock()
		executorToken := registration.ExecutorToken
		registrationMu.RUnlock()
		if claimKey == "" || claimKeyExecutorToken != executorToken {
			claimAttempt++
			claimKey = idempotencyKey("claim", nonce, executorToken, fmt.Sprint(claimAttempt))
			claimKeyExecutorToken = executorToken
		}
		claim, err := w.claimWithRetry(ctx, executorToken, claimKey)
		if err != nil {
			w.logf("claim failed: %s", Redact(err.Error()))
			if !IsRetryable(err) {
				claimKey = ""
				claimKeyExecutorToken = ""
			}
			if !sleep(ctx, w.config.ClaimPollInterval) {
				continue
			}
			continue
		}
		claimKey = ""
		if claim.Kind != "claimed" {
			if !sleep(ctx, w.config.ClaimPollInterval) {
				continue
			}
			continue
		}
		tool, ok := w.tool(claim.ToolCall.Name)
		if !ok {
			_ = w.submitOutcomeWithRetry(ctx, claim.OutcomeToken, Failed("tool.unknown", "tool handler is not registered", nil), claim.ClaimExpiresAt)
			continue
		}
		atomic.AddInt32(&active, 1)
		wg.Add(1)
		go func(claim claimAck, tool registeredTool) {
			defer wg.Done()
			defer atomic.AddInt32(&active, -1)
			callCtx, call, cancelCall := callContext(ctx, claim)
			defer cancelCall()
			outcome, err := tool.handler(callCtx, call)
			if err != nil {
				outcome = w.mapError(err)
			}
			outcomeCtx, cancelOutcome := outcomeContext(claim.ClaimExpiresAt)
			defer cancelOutcome()
			if err := w.submitOutcomeWithRetry(outcomeCtx, claim.OutcomeToken, outcome, claim.ClaimExpiresAt); err != nil {
				w.logf("outcome failed: %s", Redact(err.Error()))
			}
		}(claim, tool)
	}
}

func (w *Worker) claimWithRetry(ctx context.Context, executorToken string, claimKey string) (claimAck, error) {
	retried := false
	for {
		claim, err := w.client.claim(ctx, executorToken, []string{w.config.Namespace}, claimKey)
		if w.afterClaim != nil {
			w.afterClaim()
		}
		if err == nil || !IsRetryable(err) || retried {
			return claim, err
		}
		retried = true
		w.logf("claim retryable failure: %s", Redact(err.Error()))
		if !sleep(ctx, w.config.ClaimPollInterval) {
			return claim, ctx.Err()
		}
	}
}

func (w *Worker) definitions() []Definition {
	w.mu.RLock()
	defer w.mu.RUnlock()
	definitions := make([]Definition, 0, len(w.tools))
	for _, tool := range w.tools {
		definitions = append(definitions, tool.definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].Name == definitions[j].Name {
			return definitions[i].Version < definitions[j].Version
		}
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

func (w *Worker) tool(name string) (registeredTool, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	tool, ok := w.tools[name]
	return tool, ok
}

func (w *Worker) mapError(err error) Outcome {
	mapper := w.config.ErrorMapper
	if mapper == nil {
		mapper = defaultErrorMapper
	}
	return mapper(err)
}

func (w *Worker) heartbeatLoop(ctx context.Context, registration *registerExecutorAck, registrationMu *sync.RWMutex) {
	timer := time.NewTimer(w.config.HeartbeatInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			registrationMu.RLock()
			executorToken := registration.ExecutorToken
			executorTokenExpiresAt := registration.ExecutorTokenExpiresAt
			registrationMu.RUnlock()
			if shouldRefresh(executorTokenExpiresAt, w.config.RefreshSkew) {
				refreshed, err := w.client.registerExecutor(ctx, []string{w.config.Namespace})
				if err == nil {
					registrationMu.Lock()
					*registration = refreshed
					registrationMu.Unlock()
				} else {
					w.logf("register executor failed: %s", Redact(err.Error()))
				}
			} else if heartbeat, err := w.client.heartbeatExecutor(ctx, executorToken); err != nil {
				w.logf("heartbeat failed: %s", Redact(err.Error()))
			} else if heartbeat.ExecutorTokenExpiresAt != "" {
				registrationMu.Lock()
				registration.ExecutorTokenExpiresAt = heartbeat.ExecutorTokenExpiresAt
				registrationMu.Unlock()
			}
			timer.Reset(w.config.HeartbeatInterval)
		}
	}
}

func (w *Worker) submitOutcomeWithRetry(ctx context.Context, outcomeToken string, outcome Outcome, claimExpiresAt string) error {
	key := idempotencyKey("outcome", outcomeToken)
	deadline, _ := time.Parse(time.RFC3339, claimExpiresAt)
	for {
		err := w.client.submitOutcome(ctx, outcomeToken, outcome, key)
		if err == nil || !IsRetryable(err) {
			return err
		}
		if !deadline.IsZero() && time.Now().Add(w.config.ClaimPollInterval).After(deadline) {
			return err
		}
		if !sleep(ctx, w.config.ClaimPollInterval) {
			return ctx.Err()
		}
	}
}

func (w *Worker) logf(format string, args ...any) {
	logger := w.config.Logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf(format, args...)
}

func callContext(ctx context.Context, claim claimAck) (context.Context, Call, context.CancelFunc) {
	deadline, _ := time.Parse(time.RFC3339, claim.ClaimExpiresAt)
	callCtx := ctx
	cancel := func() {}
	if !deadline.IsZero() {
		callCtx, cancel = context.WithDeadline(ctx, deadline)
	}
	return callCtx, Call{Namespace: claim.ToolCall.Namespace, Name: claim.ToolCall.Name, Version: claim.ToolCall.Version, Input: claim.ToolCall.Input, Subject: claim.ToolCall.Subject, Deadline: deadline}, cancel
}

func outcomeContext(claimExpiresAt string) (context.Context, context.CancelFunc) {
	deadline, err := time.Parse(time.RFC3339, claimExpiresAt)
	if err == nil && !deadline.IsZero() && time.Now().Before(deadline) {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func shouldRefresh(expiresAt string, skew time.Duration) bool {
	expires, err := time.Parse(time.RFC3339, expiresAt)
	return err != nil || time.Now().Add(skew).After(expires)
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
