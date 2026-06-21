package toolworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleRejectsInvalidRegistration(t *testing.T) {
	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})

	if err := worker.Handle("", Definition{Version: "1", Description: "desc", InputSchema: map[string]any{}}, noopHandler); err == nil {
		t.Fatal("expected empty name to fail")
	}
	if err := worker.Handle("search", Definition{Version: "", Description: "desc", InputSchema: map[string]any{}}, noopHandler); err == nil {
		t.Fatal("expected missing version to fail")
	}
	if err := worker.Handle("search", Definition{Version: "1", Description: "desc", InputSchema: map[string]any{}}, nil); err == nil {
		t.Fatal("expected nil handler to fail")
	}
	if err := worker.Handle("search", Definition{Version: "1", Description: "desc", InputSchema: map[string]any{}}, noopHandler); err != nil {
		t.Fatalf("register search: %v", err)
	}
	if err := worker.Handle("search", Definition{Version: "1", Description: "desc", InputSchema: map[string]any{}}, noopHandler); err == nil {
		t.Fatal("expected duplicate handler to fail")
	}
}

func TestOutcomeHelpers(t *testing.T) {
	if got := Succeeded(map[string]any{"ok": true}); got.Status != "succeeded" || got.Result == nil {
		t.Fatalf("unexpected succeeded outcome: %+v", got)
	}
	if got := Failed("x", "failed", nil); got.Status != "failed" || got.Error == nil || got.Error.Code != "x" {
		t.Fatalf("unexpected failed outcome: %+v", got)
	}
	if got := Denied("not_allowed", nil); got.Status != "denied" || got.Denial == nil || got.Denial.Code != "not_allowed" {
		t.Fatalf("unexpected denied outcome: %+v", got)
	}
	if got := Cancelled("stopped", nil); got.Status != "cancelled" || got.Cancellation == nil || got.Cancellation.Code != "stopped" {
		t.Fatalf("unexpected cancelled outcome: %+v", got)
	}
}

func TestDefaultErrorMapperIsSafe(t *testing.T) {
	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})
	outcome := worker.mapError(errors.New("database password leaked"))
	if outcome.Status != "failed" || outcome.Error == nil {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if outcome.Error.Code != "tool.internal_error" {
		t.Fatalf("unexpected code: %s", outcome.Error.Code)
	}
	if strings.Contains(outcome.Error.Message, "database") || strings.Contains(outcome.Error.Message, "password") {
		t.Fatalf("default mapper leaked raw error: %q", outcome.Error.Message)
	}
}

func TestRedactSecrets(t *testing.T) {
	input := "token awi_tst_secret executor awi_tex_exec outcome awi_tco_out hash sha256:abcdef"
	redacted := Redact(input)
	for _, secret := range []string{"awi_tst_secret", "awi_tex_exec", "awi_tco_out", "sha256:abcdef"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redaction leaked %s in %q", secret, redacted)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	if !IsRetryable(errors.New("network")) {
		t.Fatal("plain transport errors should be retryable")
	}
	if !IsRetryable(APIError{StatusCode: http.StatusTooManyRequests}) {
		t.Fatal("429 should be retryable")
	}
	if !IsRetryable(APIError{StatusCode: http.StatusBadGateway}) {
		t.Fatal("5xx should be retryable")
	}
	if IsRetryable(APIError{StatusCode: http.StatusConflict}) {
		t.Fatal("409 should not be retryable")
	}
}

func TestManifestPublisherSendsManifest(t *testing.T) {
	var method, path, auth string
	var body publishManifestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(PublishManifestAck{ManifestToken: "mt", ManifestHash: "mh"})
	}))
	defer server.Close()

	publisher := NewManifestPublisher(PublisherConfig{BaseURL: server.URL, AgentID: "agent/1", ToolServiceToken: "awi_tst_secret"})
	ack, err := publisher.Publish(context.Background(), "crm/tools", []Definition{{Name: "search", Version: "1", Description: "Search", InputSchema: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if method != http.MethodPut || path != "/agent/v1/agents/agent/1/tool/namespaces/crm/tools/manifest" {
		t.Fatalf("unexpected request %s %s", method, path)
	}
	if auth != "Bearer awi_tst_secret" {
		t.Fatalf("unexpected auth header: %q", auth)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "search" || body.IfMatchManifestToken != nil || body.ConflictResolutionPolicy != ManifestConflictReplaceIfTokenMatch {
		t.Fatalf("unexpected body: %+v", body)
	}
	if ack.ManifestToken != "mt" || ack.ManifestHash != "mh" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestManifestPublisherRejectsReplaceWithIfMatchToken(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(PublishManifestAck{ManifestToken: "mt", ManifestHash: "mh"})
	}))
	defer server.Close()

	publisher := NewManifestPublisher(PublisherConfig{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret"})
	_, err := publisher.Publish(context.Background(), "crm", []Definition{{Name: "search", Version: "1", Description: "Search", InputSchema: map[string]any{"type": "object"}}}, PublishOptions{
		IfMatchManifestToken:     "manifest_1",
		ConflictResolutionPolicy: ManifestConflictReplace,
	})
	if err == nil {
		t.Fatal("expected publish option validation error")
	}
	if called {
		t.Fatal("server should not be called for invalid publish options")
	}
}

func TestHeartbeatExecutorReadsExtendedExpiry(t *testing.T) {
	expiresAt := "2026-06-19T09:01:30.000Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/agents/agent/tool/server/executors/heartbeat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HeartbeatExecutorAck{ExecutorTokenExpiresAt: expiresAt})
	}))
	defer server.Close()

	ack, err := (client{baseURL: server.URL, agentID: "agent", token: "awi_tst_secret"}).heartbeatExecutor(context.Background(), "awi_tex_exec")
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if ack.ExecutorTokenExpiresAt != expiresAt {
		t.Fatalf("unexpected heartbeat ack: %+v", ack)
	}
}

func TestClaimRejectsNonObjectInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"kind":"claimed","outcomeToken":"awi_tco_out","claimExpiresAt":"2026-06-19T09:01:30Z","toolCall":{"namespace":"crm","name":"search","version":"1","input":["bad"],"subject":{"userId":"user_1"}}}`))
	}))
	defer server.Close()

	ack, err := (client{baseURL: server.URL, agentID: "agent", token: "awi_tst_secret"}).claim(context.Background(), "awi_tex_exec", []string{"crm"}, "claim_key")
	if err == nil {
		t.Fatalf("expected claim decode error, got ack %+v", ack)
	}
}

func TestRunRetriesClaimWithSameIdempotencyKey(t *testing.T) {
	var claimKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			claimKeys = append(claimKeys, r.Header.Get("idempotency-key"))
			if len(claimKeys) < 4 {
				http.Error(w, `{"code":"temporary"}`, http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "none"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	worker.afterClaim = func() {
		if len(claimKeys) >= 4 {
			cancel()
		}
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(claimKeys) != 4 || claimKeys[0] == "" {
		t.Fatalf("expected four claim attempts: %v", claimKeys)
	}
	for _, key := range claimKeys[1:] {
		if key != claimKeys[0] {
			t.Fatalf("expected same claim idempotency key on retry: %v", claimKeys)
		}
	}
}

func TestRunClearsClaimKeyAfterNonRetryableClaimError(t *testing.T) {
	var claimKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			claimKeys = append(claimKeys, r.Header.Get("idempotency-key"))
			if len(claimKeys) == 1 {
				http.Error(w, `{"code":"bad_request"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "none"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	worker.afterClaim = func() {
		if len(claimKeys) >= 2 {
			cancel()
		}
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(claimKeys) != 2 || claimKeys[0] == "" || claimKeys[0] == claimKeys[1] {
		t.Fatalf("expected new key after non-retryable claim error: %v", claimKeys)
	}
}

func TestDefinitionsAreSorted(t *testing.T) {
	worker := New(Config{ManifestPublishPolicy: ManifestPublishNever})
	if err := worker.Handle("zeta", Definition{Version: "2", Description: "Z", InputSchema: map[string]any{"type": "object"}}, noopHandler); err != nil {
		t.Fatalf("handle zeta: %v", err)
	}
	if err := worker.Handle("alpha", Definition{Version: "1", Description: "A", InputSchema: map[string]any{"type": "object"}}, noopHandler); err != nil {
		t.Fatalf("handle alpha: %v", err)
	}

	definitions := worker.definitions()
	if len(definitions) != 2 || definitions[0].Name != "alpha" || definitions[1].Name != "zeta" {
		t.Fatalf("definitions not sorted: %+v", definitions)
	}
}

func TestRunExternalSkipsManifestAndHandlesClaim(t *testing.T) {
	var paths []string
	var outcomeBody submitOutcomeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			if r.Header.Get("idempotency-key") == "" {
				t.Fatal("claim missing idempotency key")
			}
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "claimed", OutcomeToken: "awi_tco_out", ClaimExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), ToolCall: claimedCall{Namespace: "crm", Name: "search", Version: "1", Input: map[string]any{"q": "abc"}, Subject: Subject{UserID: "user_1"}}})
		case "/agent/v1/agents/agent/tool/server/outcome":
			if r.Header.Get("idempotency-key") == "" {
				t.Fatal("outcome missing idempotency key")
			}
			if err := json.NewDecoder(r.Body).Decode(&outcomeBody); err != nil {
				t.Fatalf("decode outcome: %v", err)
			}
			_ = json.NewEncoder(w).Encode(SubmitOutcomeAck{Recorded: true})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	if err := worker.Handle("search", Definition{Version: "1", Description: "Search", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, call Call) (Outcome, error) {
		if call.Subject.UserID != "user_1" || call.Input["q"] != "abc" || call.Deadline.IsZero() {
			t.Fatalf("unexpected call: %+v", call)
		}
		cancel()
		return Succeeded(map[string]any{"ok": true}), nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, path := range paths {
		if strings.Contains(path, "/manifest") {
			t.Fatalf("external mode published manifest: %v", paths)
		}
	}
	if outcomeBody.Outcome.Status != "succeeded" || outcomeBody.Outcome.Result == nil {
		t.Fatalf("unexpected outcome body: %+v", outcomeBody)
	}
}

func TestRunPublishOnStartPublishesBeforeRegister(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/namespaces/crm/manifest":
			_ = json.NewEncoder(w).Encode(PublishManifestAck{ManifestToken: "mt", ManifestHash: "mh"})
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "none"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	calls := int32(0)
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishOnStart, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	if err := worker.Handle("search", Definition{Version: "1", Description: "Search", InputSchema: map[string]any{"type": "object"}}, noopHandler); err != nil {
		t.Fatalf("handle: %v", err)
	}
	go func() {
		for atomic.LoadInt32(&calls) < 1 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	worker.afterClaim = func() { atomic.AddInt32(&calls, 1) }
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(paths) < 2 || !strings.Contains(paths[0], "/manifest") || !strings.Contains(paths[1], "/register") {
		t.Fatalf("manifest was not published before register: %v", paths)
	}
}

func TestRunBoundsParallelHandlers(t *testing.T) {
	var running int32
	var maxRunning int32
	var claimed int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			n := atomic.AddInt32(&claimed, 1)
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "claimed", OutcomeToken: "awi_tco_out" + string(rune('a'+n)), ClaimExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), ToolCall: claimedCall{Namespace: "crm", Name: "work", Version: "1", Input: map[string]any{}, Subject: Subject{UserID: "user"}}})
		case "/agent/v1/agents/agent/tool/server/outcome":
			_ = json.NewEncoder(w).Encode(SubmitOutcomeAck{Recorded: true})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", ToolServiceToken: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever, MaxConcurrentCalls: 2, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	if err := worker.Handle("work", Definition{Version: "1", Description: "Work", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, call Call) (Outcome, error) {
		wg.Add(1)
		defer wg.Done()
		current := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&maxRunning)
			if current <= old || atomic.CompareAndSwapInt32(&maxRunning, old, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		if atomic.LoadInt32(&claimed) >= 4 {
			cancel()
		}
		return Succeeded(nil), nil
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	wg.Wait()
	if maxRunning > 2 {
		t.Fatalf("expected at most 2 concurrent handlers, saw %d", maxRunning)
	}
}

func noopHandler(context.Context, Call) (Outcome, error) { return Succeeded(nil), nil }
