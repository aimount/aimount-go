package toolworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aimount/aimount-go/tool"
)

type identity struct{ namespace, name, version string }

type Worker struct {
	config     WorkerConfig
	client     client
	namespaces []string
	tools      map[identity]tool.Tool
	run        atomic.Bool
	afterClaim func()
}

func New(config WorkerConfig, namespaces ...tool.Namespace) (*Worker, error) {
	c, err := newClient(config.Client)
	if err != nil {
		return nil, err
	}
	if len(namespaces) == 0 {
		return nil, ErrNamespaceRequired
	}
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
		config.RefreshSkew = 10 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	w := &Worker{config: config, client: c, tools: make(map[identity]tool.Tool)}
	seen := map[string]struct{}{}
	for _, ns := range namespaces {
		if ns.Name() == "" || len(ns.Tools()) == 0 {
			return nil, ErrInvalidNamespace
		}
		if _, ok := seen[ns.Name()]; ok {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateNamespace, ns.Name())
		}
		seen[ns.Name()] = struct{}{}
		w.namespaces = append(w.namespaces, ns.Name())
		for _, serverTool := range ns.Tools() {
			d := serverTool.Definition()
			w.tools[identity{ns.Name(), d.Name(), d.Version()}] = serverTool
		}
	}
	return w, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if !w.run.CompareAndSwap(false, true) {
		return ErrWorkerAlreadyRun
	}
	registration, err := w.client.registerExecutor(ctx, w.namespaces)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var registrationMu sync.RWMutex
	heartbeatDone := make(chan struct{})
	go func() { defer close(heartbeatDone); w.heartbeatLoop(runCtx, &registration, &registrationMu) }()
	sem := make(chan struct{}, w.config.MaxConcurrentCalls)
	var wg sync.WaitGroup
	nonce, attempt := processNonce(), 0
	claimKey, claimToken := "", ""
	for runCtx.Err() == nil {
		select {
		case sem <- struct{}{}:
		case <-runCtx.Done():
			break
		}
		if runCtx.Err() != nil {
			break
		}
		registrationMu.RLock()
		executorToken := registration.ExecutorToken
		registrationMu.RUnlock()
		if claimKey == "" || claimToken != executorToken {
			attempt++
			claimKey = idempotencyKey("claim", nonce, executorToken, fmt.Sprint(attempt))
			claimToken = executorToken
		}
		claim, claimErr := w.client.claim(runCtx, executorToken, w.namespaces, claimKey)
		if w.afterClaim != nil {
			w.afterClaim()
		}
		if claimErr != nil {
			<-sem
			if runCtx.Err() != nil {
				break
			}
			w.config.Logger.Error("claim failed", "error", Redact(claimErr.Error()))
			if !IsRetryable(claimErr) {
				claimKey = ""
			}
			sleep(runCtx, w.config.ClaimPollInterval)
			continue
		}
		claimKey = ""
		if claim.Kind != "claimed" {
			<-sem
			sleep(runCtx, w.config.ClaimPollInterval)
			continue
		}
		if !claim.ClaimExpiryIsValid {
			w.config.Logger.Error("invalid claim deadline", "namespace", claim.ToolCall.Namespace, "name", claim.ToolCall.Name, "version", claim.ToolCall.Version)
			if claim.OutcomeToken != "" {
				outcomeCtx, cancelOutcome := context.WithTimeout(runCtx, 30*time.Second)
				err := w.submitOutcomeWithRetry(outcomeCtx, claim.OutcomeToken, internalFailure(), claim.ClaimExpiresAt)
				cancelOutcome()
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					w.config.Logger.Error("outcome failed", "error", Redact(err.Error()))
				}
			}
			<-sem
			continue
		}
		wg.Add(1)
		go func(claim claimAck) {
			defer wg.Done()
			callCtx, cancelCall := context.WithDeadline(runCtx, claim.ClaimExpiresAt)
			defer cancelCall()
			resultDone := make(chan outcome, 1)
			go func() {
				defer func() { <-sem }()
				resultDone <- w.execute(callCtx, identity{claim.ToolCall.Namespace, claim.ToolCall.Name, claim.ToolCall.Version}, claim.ToolCall.Input, claim.ToolCall.Subject, claim.ToolCall.Context)
			}()
			result, completed := awaitResult(resultDone, callCtx.Done())
			if !completed {
				deadline := claim.ClaimExpiresAt
				if deadline.IsZero() || !time.Now().Before(deadline) {
					return
				}
				timer := time.NewTimer(time.Until(deadline))
				defer timer.Stop()
				select {
				case result = <-resultDone:
				case <-timer.C:
					w.config.Logger.Warn("handler outlived claim deadline", "namespace", claim.ToolCall.Namespace, "name", claim.ToolCall.Name, "version", claim.ToolCall.Version, "user_id", claim.ToolCall.Subject.UserID)
					return
				}
			}
			outcomeCtx, cancelOutcome := outcomeContext(claim.ClaimExpiresAt)
			defer cancelOutcome()
			if err := w.submitOutcomeWithRetry(outcomeCtx, claim.OutcomeToken, result, claim.ClaimExpiresAt); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				w.config.Logger.Error("outcome failed", "error", Redact(err.Error()))
			}
		}(claim)
	}
	cancel()
	wg.Wait()
	<-heartbeatDone
	return nil
}

func awaitResult(resultDone <-chan outcome, canceled <-chan struct{}) (outcome, bool) {
	select {
	case result := <-resultDone:
		return result, true
	default:
	}
	select {
	case result := <-resultDone:
		return result, true
	case <-canceled:
		select {
		case result := <-resultDone:
			return result, true
		default:
			return outcome{}, false
		}
	}
}

func (w *Worker) execute(ctx context.Context, id identity, input json.RawMessage, subject tool.Subject, callContext tool.CallContext) outcome {
	serverTool, ok := w.tools[id]
	if !ok {
		w.config.Logger.Error("unknown tool identity", "namespace", id.namespace, "name", id.name, "version", id.version)
		return internalFailure()
	}
	result, err := serverTool.ExecuteWithContext(ctx, input, subject, callContext)
	if err == nil {
		return succeeded(result)
	}
	if isNilError(err) {
		return internalFailure()
	}
	var public *tool.Error
	if errors.As(err, &public) {
		if public == nil {
			return internalFailure()
		}
		code, message := public.Code(), public.Message()
		if strings.TrimSpace(code) == "" || strings.TrimSpace(message) == "" {
			return internalFailure()
		}
		return failed(code, message, public.Details())
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		w.config.Logger.Error("tool execution failed", "namespace", id.namespace, "name", id.name, "version", id.version, "user_id", subject.UserID, "error", Redact(err.Error()), "error_type", fmt.Sprintf("%T", err))
	}
	return internalFailure()
}

func isNilError(err error) bool {
	value := reflect.ValueOf(err)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context, registration *registerExecutorAck, mu *sync.RWMutex) {
	timer := time.NewTimer(w.config.HeartbeatInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w.maintainRegistration(ctx, registration, mu)
			timer.Reset(w.config.HeartbeatInterval)
		}
	}
}

func (w *Worker) maintainRegistration(ctx context.Context, registration *registerExecutorAck, mu *sync.RWMutex) {
	mu.RLock()
	token, expires := registration.ExecutorToken, registration.ExecutorTokenExpiresAt
	mu.RUnlock()
	if time.Now().Add(w.config.RefreshSkew).After(expires) {
		if fresh, err := w.client.registerExecutor(ctx, w.namespaces); err == nil {
			mu.Lock()
			*registration = fresh
			mu.Unlock()
		} else if ctx.Err() == nil {
			w.config.Logger.Error("register executor failed", "error", Redact(err.Error()))
		}
	} else if heartbeat, err := w.client.heartbeatExecutor(ctx, token); err != nil {
		if ctx.Err() == nil {
			w.config.Logger.Error("heartbeat failed", "error", Redact(err.Error()))
		}
	} else if !heartbeat.ExecutorTokenExpiresAt.IsZero() {
		mu.Lock()
		registration.ExecutorTokenExpiresAt = heartbeat.ExecutorTokenExpiresAt
		mu.Unlock()
	}
}

func (w *Worker) submitOutcomeWithRetry(ctx context.Context, token string, result outcome, deadline time.Time) error {
	key := idempotencyKey("outcome", token)
	for {
		err := w.client.submitOutcome(ctx, token, result, key)
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

func outcomeContext(deadline time.Time) (context.Context, context.CancelFunc) {
	if time.Now().Before(deadline) {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithTimeout(context.Background(), 30*time.Second)
}
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
