package toolworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aimount/aimount-go/tool"
)

type input struct {
	Value string `json:"value"`
}
type labeledInput struct {
	Value    string `json:"value"`
	ClientID string `json:"clientId,omitempty"`
}
type output struct {
	Value string `json:"value"`
}

type nilReceiverError struct{ message string }

func (e *nilReceiverError) Error() string { return e.message }

func executable(t *testing.T, name, version string, handler tool.Handler[input, output]) tool.Tool {
	t.Helper()
	serverTool, err := tool.New(tool.Metadata{Name: name, Version: version, Description: name}, handler)
	if err != nil {
		t.Fatal(err)
	}
	return serverTool
}

func namespace(t *testing.T, name string, tools ...tool.Tool) tool.Namespace {
	t.Helper()
	ns, err := tool.NewNamespace(name, tools...)
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func config(baseURL string) ClientConfig {
	return ClientConfig{BaseURL: baseURL, AgentID: "agent", AgentAPIKey: "awi_tst_secret"}
}

func boolPointer(value bool) *bool { return &value }

func TestClientConfigValidationAndDefaults(t *testing.T) {
	valid := config("https://example.com/api/")
	publisher, err := NewPublisher(valid)
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if publisher.client.baseURL != "https://example.com/api" {
		t.Fatalf("base URL = %q", publisher.client.baseURL)
	}
	if publisher.client.http.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s", publisher.client.http.Timeout)
	}

	custom := &http.Client{}
	publisher, err = NewPublisher(ClientConfig{BaseURL: "http://example.com", AgentID: "agent", AgentAPIKey: "key", HTTPClient: custom})
	if err != nil || publisher.client.http != custom {
		t.Fatalf("custom client: %v", err)
	}

	for _, tc := range []struct {
		name   string
		cfg    ClientConfig
		target error
	}{
		{"base URL", ClientConfig{AgentID: "a", AgentAPIKey: "k"}, ErrInvalidBaseURL},
		{"scheme", ClientConfig{BaseURL: "ftp://example.com", AgentID: "a", AgentAPIKey: "k"}, ErrInvalidBaseURL},
		{"relative", ClientConfig{BaseURL: "/api", AgentID: "a", AgentAPIKey: "k"}, ErrInvalidBaseURL},
		{"query", ClientConfig{BaseURL: "https://example.com?q=1", AgentID: "a", AgentAPIKey: "k"}, ErrInvalidBaseURL},
		{"agent", ClientConfig{BaseURL: "https://example.com", AgentAPIKey: "k"}, ErrMissingAgentID},
		{"key", ClientConfig{BaseURL: "https://example.com", AgentID: "a"}, ErrMissingAgentAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPublisher(tc.cfg)
			if !errors.Is(err, tc.target) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDefaultMaintenanceStepHeartbeatsStandardRegistration(t *testing.T) {
	var registers, heartbeats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			registers.Add(1)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			_ = json.NewEncoder(w).Encode(heartbeatExecutorAckWire{ExecutorTokenExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
		}
	}))
	defer server.Close()
	w, err := New(WorkerConfig{Client: config(server.URL)}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	if err != nil {
		t.Fatal(err)
	}
	registration := registerExecutorAck{ExecutorToken: "token", ExecutorTokenExpiresAt: time.Now().Add(time.Minute)}
	var mu sync.RWMutex
	w.maintainRegistration(context.Background(), &registration, &mu)
	if registers.Load() != 0 || heartbeats.Load() != 1 {
		t.Fatalf("registers=%d heartbeats=%d", registers.Load(), heartbeats.Load())
	}
}

func TestPublisherPublishesGeneratedNamespace(t *testing.T) {
	var body publishManifestRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/agents/agent/server/tools/namespaces/crm/manifest" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(PublishManifestAck{ManifestToken: "mt", ManifestHash: "mh"})
	}))
	defer server.Close()

	publisher, err := NewPublisher(config(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	serverTool := executable(t, "search", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })
	ack, err := publisher.Publish(context.Background(), namespace(t, "crm", serverTool), PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ManifestHash != "mh" || len(body.Tools) != 1 || body.Tools[0].Name != "search" {
		t.Fatalf("ack/body = %+v %+v", ack, body)
	}
	if body.IfMatchManifestToken != nil || body.ConflictResolutionPolicy != "" {
		t.Fatalf("zero options changed: %+v", body)
	}
}

func TestExecutorRequestsUseCanonicalServerToolPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(client) error
	}{
		{"register", "/agent/v1/agents/agent%2F1/server/tools/executors/register", func(c client) error {
			_, err := c.registerExecutor(context.Background(), []string{"crm"})
			return err
		}},
		{"heartbeat", "/agent/v1/agents/agent%2F1/server/tools/executors/heartbeat", func(c client) error {
			_, err := c.heartbeatExecutor(context.Background(), "executor")
			return err
		}},
		{"claim", "/agent/v1/agents/agent%2F1/server/tools/claim", func(c client) error {
			_, err := c.claim(context.Background(), "executor", []string{"crm"}, "key")
			return err
		}},
		{"outcome", "/agent/v1/agents/agent%2F1/server/tools/outcome", func(c client) error {
			return c.submitOutcome(context.Background(), "outcome", succeeded(nil), "key")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.EscapedPath() != tt.path {
					t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
				}
				switch tt.name {
				case "register":
					writeRegistration(w, "executor", time.Now().Add(time.Hour))
				case "heartbeat":
					_ = json.NewEncoder(w).Encode(heartbeatExecutorAckWire{ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
				case "claim":
					_, _ = io.WriteString(w, `{"kind":"none"}`)
				case "outcome":
					_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
				}
			}))
			defer server.Close()

			if err := tt.call(client{baseURL: server.URL, agentID: "agent/1", token: "key", http: server.Client()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublisherRejectsMultipleVersionsAndInvalidOptions(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	publisher, _ := NewPublisher(config(server.URL))
	h := func(context.Context, tool.Call[input]) (output, error) { return output{}, nil }
	_, err := publisher.Publish(context.Background(), namespace(t, "crm", executable(t, "search", "1", h), executable(t, "search", "2", h)), PublishOptions{})
	if !errors.Is(err, ErrMultipleToolVersions) || calls.Load() != 0 {
		t.Fatalf("error/calls = %v/%d", err, calls.Load())
	}
	_, err = publisher.Publish(context.Background(), namespace(t, "crm", executable(t, "other", "1", h)), PublishOptions{IfMatchManifestToken: "mt", ConflictResolutionPolicy: ManifestConflictReplace})
	if err == nil || calls.Load() != 0 {
		t.Fatalf("invalid options error/calls = %v/%d", err, calls.Load())
	}
}

func TestPublisherRejectsZeroNamespace(t *testing.T) {
	publisher, err := NewPublisher(config("https://example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), tool.Namespace{}, PublishOptions{}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("error = %v", err)
	}
}

func TestWorkerConstructor(t *testing.T) {
	h := func(context.Context, tool.Call[input]) (output, error) { return output{}, nil }
	ns := namespace(t, "crm", executable(t, "search", "1", h))
	if _, err := New(WorkerConfig{Client: config("https://example.com")}); !errors.Is(err, ErrNamespaceRequired) {
		t.Fatalf("missing namespace: %v", err)
	}
	if _, err := New(WorkerConfig{Client: config("https://example.com")}, tool.Namespace{}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("zero namespace: %v", err)
	}
	if _, err := New(WorkerConfig{Client: config("https://example.com")}, ns, ns); !errors.Is(err, ErrDuplicateNamespace) {
		t.Fatalf("duplicate namespace: %v", err)
	}
	w, err := New(WorkerConfig{Client: config("https://example.com")}, ns)
	if err != nil {
		t.Fatal(err)
	}
	if w.config.MaxConcurrentCalls != 1 || w.config.Logger != slog.Default() {
		t.Fatalf("defaults: %+v", w.config)
	}
	tools := ns.Tools()
	tools[0] = tool.Tool{}
	if _, ok := w.tools[identity{"crm", "search", "1"}]; !ok {
		t.Fatal("registry changed through namespace snapshot")
	}
}

func TestPublisherSendsConflictPolicies(t *testing.T) {
	requests := make(chan publishManifestRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body publishManifestRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		_ = json.NewEncoder(w).Encode(PublishManifestAck{ManifestToken: "mt", ManifestHash: "same-hash"})
	}))
	defer server.Close()
	publisher, _ := NewPublisher(config(server.URL))
	ns := namespace(t, "crm", executable(t, "search", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil }))
	for _, options := range []PublishOptions{
		{IfMatchManifestToken: "expected", ConflictResolutionPolicy: ManifestConflictReplaceIfTokenMatch},
		{ConflictResolutionPolicy: ManifestConflictReplace},
	} {
		ack, err := publisher.Publish(context.Background(), ns, options)
		if err != nil || ack.ManifestHash != "same-hash" {
			t.Fatalf("publish: ack=%+v err=%v", ack, err)
		}
		body := <-requests
		if body.ConflictResolutionPolicy != options.ConflictResolutionPolicy {
			t.Fatalf("policy = %q", body.ConflictResolutionPolicy)
		}
		if options.IfMatchManifestToken != "" && (body.IfMatchManifestToken == nil || *body.IfMatchManifestToken != options.IfMatchManifestToken) {
			t.Fatalf("if-match = %#v", body.IfMatchManifestToken)
		}
	}
}

func TestShutdownStopsWaitingAtClaimDeadline(t *testing.T) {
	started := make(chan struct{})
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			_ = json.NewEncoder(w).Encode(struct {
				ExecutorToken          string `json:"executorToken"`
				ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
			}{"token", time.Now().Add(time.Hour).Format(time.RFC3339)})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			writeClaim(w, "out", time.Now().Add(100*time.Millisecond), "crm", "stuck", "1", `{"value":"x"}`, "user")
		}
	}))
	defer server.Close()
	release := make(chan struct{})
	defer close(release)
	h := func(context.Context, tool.Call[input]) (output, error) {
		close(started)
		<-release
		return output{}, nil
	}
	w, _ := New(WorkerConfig{Client: config(server.URL), Logger: slog.New(slog.NewTextHandler(&logs, nil)), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "stuck", "1", h)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run waited beyond claim deadline")
	}
	if text := logs.String(); !strings.Contains(text, "handler outlived claim deadline") || strings.Contains(text, "awi_") {
		t.Fatalf("logs = %s", text)
	}
}

func TestWorkerUsesAllNamespacesAndRoutesFullIdentity(t *testing.T) {
	var registerNamespaces, claimNamespaces []string
	outcomes := make(chan submitOutcomeRequest, 2)
	var claims atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			var body struct {
				Namespaces []string `json:"namespaces"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			registerNamespaces = body.Namespaces
			writeRegistration(w, "awi_tex_exec", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			var body struct {
				Namespaces []string `json:"namespaces"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			claimNamespaces = body.Namespaces
			n := claims.Add(1)
			ns, version := "crm", "1"
			if n == 2 {
				ns, version = "billing", "2"
			}
			writeClaim(w, fmt.Sprint(n), time.Now().Add(time.Second), ns, "same", version, `{"value":"x"}`, "user")
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			var body submitOutcomeRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			outcomes <- body
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	h1 := func(context.Context, tool.Call[input]) (output, error) { return output{Value: "crm"}, nil }
	h2 := func(context.Context, tool.Call[input]) (output, error) {
		cancel()
		return output{Value: "billing"}, nil
	}
	w, _ := New(WorkerConfig{Client: config(server.URL), MaxConcurrentCalls: 1, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "same", "1", h1)), namespace(t, "billing", executable(t, "same", "2", h2)))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(registerNamespaces, ",") != "crm,billing" || strings.Join(claimNamespaces, ",") != "crm,billing" {
		t.Fatalf("namespaces register=%v claim=%v", registerNamespaces, claimNamespaces)
	}
	for range 2 {
		select {
		case <-outcomes:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for outcome")
		}
	}
}

func TestWorkerUsesTrustedClaimLabelsInsteadOfModelInput(t *testing.T) {
	var submitted submitOutcomeRequest
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_, _ = fmt.Fprintf(w, `{"kind":"claimed","outcomeToken":"out","claimExpiresAt":%q,"toolCall":{"namespace":"crm","name":"authorize","version":"1","input":{"value":"x","clientId":"attacker"},"subject":{"userId":"user"},"context":{"sessionLabels":{"client_id":"trusted"}},"future":true}}`, time.Now().Add(time.Second).Format(time.RFC3339Nano))
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			_ = json.NewDecoder(r.Body).Decode(&submitted)
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
			cancel()
		}
	}))
	defer server.Close()

	serverTool, err := tool.New(tool.Metadata{Name: "authorize", Version: "1", Description: "Authorize"}, func(_ context.Context, call tool.Call[labeledInput]) (output, error) {
		if call.Context.SessionLabels["client_id"] != "trusted" {
			return output{Value: "denied"}, nil
		}
		return output{Value: "allowed:trusted"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", serverTool))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if string(submitted.Outcome.Result) != `{"value":"allowed:trusted"}` {
		t.Fatalf("outcome = %s", submitted.Outcome.Result)
	}
}

func TestSuccessfulOutcomePreservesExactJSONResult(t *testing.T) {
	var wire []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			writeClaim(w, "out", time.Now().Add(time.Second), "crm", "exact", "1", `{"value":"x"}`, "user")
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			wire, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	exact, _ := tool.New(tool.Metadata{Name: "exact", Version: "1", Description: "exact"}, func(context.Context, tool.Call[input]) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"max":18446744073709551615}`), nil
	})
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", exact))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wire, []byte(`"result":{"max":18446744073709551615}`)) {
		t.Fatalf("wire = %s", wire)
	}
}

func TestClaimPreservesNullInputAndExecutionRejectsIt(t *testing.T) {
	var claim claimAck
	if err := json.Unmarshal([]byte(`{"kind":"claimed","toolCall":{"namespace":"crm","name":"work","version":"1","input":null}}`), &claim); err != nil {
		t.Fatal(err)
	}
	if string(claim.ToolCall.Input) != "null" {
		t.Fatalf("raw input = %q", claim.ToolCall.Input)
	}
	called := false
	w, _ := New(WorkerConfig{Client: config("https://example.com")}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { called = true; return output{}, nil })))
	got := w.execute(context.Background(), identity{"crm", "work", "1"}, claim.ToolCall.Input, tool.Subject{}, tool.CallContext{})
	if called || got.Error == nil {
		t.Fatalf("called=%v outcome=%+v", called, got)
	}
}

func TestAPIClientRejectsTrailingSuccessfulJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"recorded":true}{"extra":true}`))
	}))
	defer server.Close()
	c := client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}
	if err := c.do(context.Background(), http.MethodPost, "/response", map[string]any{}, &submitOutcomeAck{}, ""); err == nil {
		t.Fatal("expected trailing response JSON error")
	}
}

func TestMalformedSuccessfulAckJSONIsProtocolError(t *testing.T) {
	for _, body := range []string{"", `{`, `{} {}`} {
		for _, endpoint := range []string{"registration", "claim", "outcome"} {
			t.Run(endpoint+"/"+body, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
				defer server.Close()
				c := client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}
				var err error
				switch endpoint {
				case "registration":
					_, err = c.registerExecutor(context.Background(), []string{"crm"})
				case "claim":
					_, err = c.claim(context.Background(), "token", []string{"crm"}, "key")
				case "outcome":
					err = c.submitOutcome(context.Background(), "out", internalFailure(), "key")
				}
				if err == nil || IsRetryable(err) || !errors.Is(err, errMalformedProtocol) {
					t.Fatalf("err=%v retryable=%v", err, IsRetryable(err))
				}
			})
		}
	}
}

func TestRegisterExecutorValidatesSuccessfulResponse(t *testing.T) {
	for _, response := range []string{
		`{"executorTokenExpiresAt":"` + time.Now().Add(time.Minute).Format(time.RFC3339Nano) + `"}`,
		`{"executorToken":"token"}`,
		`{"executorToken":"token","executorTokenExpiresAt":"invalid"}`,
		`{"executorToken":"token","executorTokenExpiresAt":"` + time.Now().Add(-time.Minute).Format(time.RFC3339Nano) + `"}`,
	} {
		t.Run(response, func(t *testing.T) {
			var claims atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/claim") {
					claims.Add(1)
				}
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			w, _ := New(WorkerConfig{Client: config(server.URL)}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
			err := w.Run(context.Background())
			if err == nil || IsRetryable(err) || claims.Load() != 0 {
				t.Fatalf("err=%v retryable=%v claims=%d", err, IsRetryable(err), claims.Load())
			}
		})
	}
}

func TestWorkerTreatsInitialRegistrationDeadlineAsNormalShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	worker, err := New(WorkerConfig{Client: config(server.URL)}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run error = %v", err)
	}
}

func TestHeartbeatExecutorValidatesSuccessfulResponse(t *testing.T) {
	for _, response := range []string{
		`{}`,
		`{"executorTokenExpiresAt":null}`,
		`{"executorTokenExpiresAt":"invalid"}`,
		`{"executorTokenExpiresAt":"` + time.Now().Add(-time.Minute).Format(time.RFC3339Nano) + `"}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			_, err := (client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}).heartbeatExecutor(context.Background(), "token")
			if err == nil || IsRetryable(err) {
				t.Fatalf("err=%v retryable=%v", err, IsRetryable(err))
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"executorTokenExpiresAt":"`+time.Now().Add(time.Minute).Format(time.RFC3339Nano)+`","future":true}`)
	}))
	defer server.Close()
	if _, err := (client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}).heartbeatExecutor(context.Background(), "token"); err != nil {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestMaintainRegistrationRetainsStateOnMalformedHeartbeat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) }))
	defer server.Close()
	worker, err := New(WorkerConfig{Client: config(server.URL)}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	if err != nil {
		t.Fatal(err)
	}
	original := registerExecutorAck{ExecutorToken: "token", ExecutorTokenExpiresAt: time.Now().Add(time.Hour)}
	registration := original
	var mu sync.RWMutex
	worker.maintainRegistration(context.Background(), &registration, &mu)
	if registration != original {
		t.Fatalf("registration changed: before=%+v after=%+v", original, registration)
	}
}

func TestClaimValidatesSuccessfulResponse(t *testing.T) {
	future := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	validClaimed := `{"kind":"claimed","outcomeToken":"out","claimExpiresAt":"` + future + `","toolCall":{"namespace":"crm","name":"work","version":"1","input":{"value":"x"},"subject":{"userId":"user"}}}`
	for _, tc := range []struct {
		name     string
		response string
		valid    bool
	}{
		{"none", `{"kind":"none"}`, true},
		{"none outcome", `{"kind":"none","outcomeToken":"out"}`, false},
		{"none expiry", `{"kind":"none","claimExpiresAt":"later"}`, false},
		{"none namespace", `{"kind":"none","toolCall":{"namespace":"crm"}}`, false},
		{"none name", `{"kind":"none","toolCall":{"name":"work"}}`, false},
		{"none version", `{"kind":"none","toolCall":{"version":"1"}}`, false},
		{"none input", `{"kind":"none","toolCall":{"input":{}}}`, false},
		{"none subject", `{"kind":"none","toolCall":{"subject":{"userId":"user"}}}`, false},
		{"claimed", validClaimed, true},
		{"claimed context", strings.Replace(validClaimed, `"subject":{"userId":"user"}`, `"subject":{"userId":"user"},"context":{"sessionLabels":{"client_id":"client"}}`, 1), true},
		{"missing kind", `{}`, false},
		{"unknown kind", `{"kind":"other"}`, false},
		{"missing outcome", strings.Replace(validClaimed, `"outcomeToken":"out",`, "", 1), false},
		{"missing expiry", strings.Replace(validClaimed, `"claimExpiresAt":"`+future+`",`, "", 1), true},
		{"bad expiry", strings.Replace(validClaimed, future, "invalid", 1), true},
		{"expired", strings.Replace(validClaimed, future, time.Now().Add(-time.Minute).Format(time.RFC3339Nano), 1), true},
		{"missing namespace", strings.Replace(validClaimed, `"namespace":"crm",`, "", 1), false},
		{"missing name", strings.Replace(validClaimed, `"name":"work",`, "", 1), false},
		{"missing version", strings.Replace(validClaimed, `"version":"1",`, "", 1), false},
		{"missing input", strings.Replace(validClaimed, `"input":{"value":"x"},`, "", 1), false},
		{"missing subject", strings.Replace(validClaimed, `"subject":{"userId":"user"}`, `"subject":{}`, 1), false},
		{"blank subject", strings.Replace(validClaimed, `"userId":"user"`, `"userId":" "`, 1), false},
		{"unknown field", strings.Replace(validClaimed, `"kind":"claimed"`, `"kind":"claimed","future":true`, 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.response) }))
			defer server.Close()
			ack, err := (client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}).claim(context.Background(), "token", []string{"crm"}, "key")
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && (err == nil || IsRetryable(err)) {
				t.Fatalf("ack=%+v err=%v retryable=%v", ack, err, IsRetryable(err))
			}
		})
	}
}

func TestWorkerSubmitsSafeOutcomeForInvalidClaimDeadline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expiry string
	}{
		{"missing", `""`},
		{"malformed", `"invalid"`},
		{"expired", fmt.Sprintf("%q", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))},
		{"number", `123`},
		{"null", `null`},
		{"object", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called atomic.Bool
			outcomeReceived := make(chan submitOutcomeRequest, 1)
			ctx, cancel := context.WithCancel(context.Background())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/register"):
					writeRegistration(w, "token", time.Now().Add(time.Minute))
				case strings.HasSuffix(r.URL.Path, "/claim"):
					_, _ = fmt.Fprintf(w, `{"kind":"claimed","outcomeToken":"out","claimExpiresAt":%s,"toolCall":{"namespace":"crm","name":"work","version":"1","input":{"value":"x"},"subject":{"userId":"user"}}}`, tc.expiry)
				case strings.HasSuffix(r.URL.Path, "/outcome"):
					var request submitOutcomeRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode outcome: %v", err)
					}
					outcomeReceived <- request
					_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
					cancel()
				}
			}))
			defer server.Close()

			worker, err := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) {
				called.Store(true)
				return output{}, nil
			})))
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Run(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-outcomeReceived:
				if request.OutcomeToken != "out" || request.Outcome.Error == nil || request.Outcome.Error.Code != tool.UnknownErrorCode || request.Outcome.Error.Message != tool.UnknownErrorMessage {
					t.Fatalf("outcome = %+v", request)
				}
			default:
				t.Fatal("safe outcome was not submitted")
			}
			if called.Load() {
				t.Fatal("handler was invoked")
			}
		})
	}
}

func TestWorkerDoesNotInvokeHandlerForMissingSubject(t *testing.T) {
	var called atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	var claims atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Minute))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			if claims.Add(1) == 2 {
				cancel()
			}
			writeClaim(w, "out", time.Now().Add(time.Minute), "crm", "work", "1", `{"value":"x"}`, "")
		}
	}))
	defer server.Close()
	worker, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) {
		called.Store(true)
		return output{}, nil
	})))
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if called.Load() {
		t.Fatal("handler was invoked")
	}
}

func TestPublisherValidatesSuccessfulResponse(t *testing.T) {
	for _, response := range []string{
		`{}`,
		`{"manifestToken":"token"}`,
		`{"manifestHash":"hash"}`,
		`{"manifestToken":" ","manifestHash":"hash"}`,
		`{"manifestToken":"token","manifestHash":" "}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, response) }))
			defer server.Close()
			publisher, _ := NewPublisher(config(server.URL))
			_, err := publisher.Publish(context.Background(), namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })), PublishOptions{})
			if err == nil || IsRetryable(err) {
				t.Fatalf("err=%v retryable=%v", err, IsRetryable(err))
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"manifestToken":"token","manifestHash":"hash","future":true}`)
	}))
	defer server.Close()
	publisher, _ := NewPublisher(config(server.URL))
	if _, err := publisher.Publish(context.Background(), namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })), PublishOptions{}); err != nil {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestMalformedClaimUsesFreshIdempotencyKey(t *testing.T) {
	var keys []string
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Minute))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			keys = append(keys, r.Header.Get("idempotency-key"))
			if len(keys) == 1 {
				_, _ = io.WriteString(w, `{`)
				return
			}
			cancel()
			_, _ = io.WriteString(w, `{"kind":"none"}`)
		}
	}))
	defer server.Close()
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Fatalf("keys=%v", keys)
	}
}

func TestWorkerMapsErrorsSafelyAndLogsOnlyInternalFailures(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	w, _ := New(WorkerConfig{Client: config("https://example.com"), Logger: logger}, namespace(t, "crm",
		executable(t, "public", "1", func(context.Context, tool.Call[input]) (output, error) {
			return output{}, fmt.Errorf("wrapped: %w", tool.NewError("safe", "safe message", map[string]any{"x": 1}))
		}),
		executable(t, "internal", "1", func(context.Context, tool.Call[input]) (output, error) {
			return output{}, errors.New("database secret")
		}),
	))
	public := w.execute(context.Background(), identity{"crm", "public", "1"}, json.RawMessage(`{"value":"x"}`), tool.Subject{UserID: "u"}, tool.CallContext{})
	internal := w.execute(context.Background(), identity{"crm", "internal", "1"}, json.RawMessage(`{"value":"secret input"}`), tool.Subject{UserID: "u"}, tool.CallContext{})
	unknown := w.execute(context.Background(), identity{"crm", "missing", "1"}, json.RawMessage(`{"password":"secret"}`), tool.Subject{}, tool.CallContext{})
	if public.Error.Code != "safe" || public.Error.Details["x"] != json.Number("1") {
		t.Fatalf("public = %+v", public)
	}
	for _, got := range []outcome{internal, unknown} {
		if got.Error == nil || got.Error.Code != tool.UnknownErrorCode || got.Error.Message != tool.UnknownErrorMessage || got.Error.Details != nil {
			t.Fatalf("unsafe = %+v", got)
		}
	}
	text := logs.String()
	if strings.Contains(text, "safe message") || strings.Contains(text, "secret input") || strings.Contains(text, "password") || !strings.Contains(text, "database secret") {
		t.Fatalf("logs = %s", text)
	}
}

func TestWorkerRedactsOrdinaryHandlerError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	secret := "awi_tst_api awi_tex_executor awi_tco_outcome sha256:tokenhash"
	w, _ := New(WorkerConfig{Client: config("https://example.com"), Logger: logger}, namespace(t, "crm",
		executable(t, "lookup", "7", func(context.Context, tool.Call[input]) (output, error) {
			return output{}, errors.New("backend failed with " + secret)
		}),
	))

	w.execute(context.Background(), identity{"crm", "lookup", "7"}, json.RawMessage(`{"value":"x"}`), tool.Subject{UserID: "user_42"}, tool.CallContext{})
	text := logs.String()
	for _, leaked := range strings.Fields(secret) {
		if strings.Contains(text, leaked) {
			t.Fatalf("log leaked %q: %s", leaked, text)
		}
	}
	for _, safe := range []string{"crm", "lookup", "7", "user_42", "backend failed with"} {
		if !strings.Contains(text, safe) {
			t.Fatalf("log omitted %q: %s", safe, text)
		}
	}
}

func TestWorkerCancellationAndOneShotLifecycle(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			writeClaim(w, "out", time.Now().Add(time.Second), "crm", "wait", "1", `{"value":"x"}`, "user")
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
		}
	}))
	defer server.Close()
	h := func(ctx context.Context, _ tool.Call[input]) (output, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return output{}, ctx.Err()
	}
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "wait", "1", h)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	<-stopped
	if err := w.Run(context.Background()); !errors.Is(err, ErrWorkerAlreadyRun) {
		t.Fatalf("second run: %v", err)
	}
}

func TestAwaitResultPrefersCompletedOutcomeOverCancellation(t *testing.T) {
	resultDone := make(chan outcome, 1)
	resultDone <- succeeded(json.RawMessage(`{"completed":true}`))
	canceled := make(chan struct{})
	close(canceled)

	result, ok := awaitResult(resultDone, canceled)
	if !ok || string(result.Result) != `{"completed":true}` {
		t.Fatalf("result=%+v ok=%v", result, ok)
	}
}

func TestIsRetryable(t *testing.T) {
	if !IsRetryable(errors.New("network")) || !IsRetryable(APIError{StatusCode: 429}) || IsRetryable(APIError{StatusCode: 409}) {
		t.Fatal("retry classification")
	}
}

func TestRedactSecrets(t *testing.T) {
	redacted := Redact("awi_tst_secret awi_tex_exec awi_tco_out sha256:abcdef")
	if strings.Contains(redacted, "awi_") || strings.Contains(redacted, "sha256:abcdef") {
		t.Fatal(redacted)
	}
}

func TestGlobalConcurrency(t *testing.T) {
	var running, max, claims atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			n := claims.Add(1)
			ns := "crm"
			if n%2 == 0 {
				ns = "billing"
			}
			writeClaim(w, fmt.Sprint(n), time.Now().Add(time.Second), ns, "work", "1", `{"value":"x"}`, "user")
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
		}
	}))
	defer server.Close()
	h := func(context.Context, tool.Call[input]) (output, error) {
		n := running.Add(1)
		for {
			old := max.Load()
			if n <= old || max.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return output{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := New(WorkerConfig{Client: config(server.URL), MaxConcurrentCalls: 2, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", h)), namespace(t, "billing", executable(t, "work", "1", h)))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for running.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if claims.Load() != 2 || max.Load() != 2 {
		t.Fatalf("claims=%d max=%d", claims.Load(), max.Load())
	}
	close(release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExpiredUncooperativeHandlerKeepsConcurrencySlot(t *testing.T) {
	var claims, starts atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			n := claims.Add(1)
			writeClaim(w, fmt.Sprint(n), time.Now().Add(50*time.Millisecond), "crm", "work", "1", fmt.Sprintf(`{"value":%q}`, fmt.Sprint(n)), "user")
		case strings.HasSuffix(r.URL.Path, "/outcome"):
			_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
		}
	}))
	defer server.Close()

	h := func(_ context.Context, call tool.Call[input]) (output, error) {
		n := starts.Add(1)
		if n == 1 {
			close(firstStarted)
			<-releaseFirst
		} else if n == 2 {
			close(secondStarted)
		}
		return output{Value: call.Input.Value}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := New(WorkerConfig{Client: config(server.URL), MaxConcurrentCalls: 1, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", h)))
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-firstStarted
	time.Sleep(100 * time.Millisecond)
	select {
	case <-secondStarted:
		t.Fatal("second handler started while expired first handler was still alive")
	default:
	}
	if claims.Load() != 1 {
		t.Fatalf("claims = %d, want 1 while first handler is alive", claims.Load())
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second handler did not start after first handler exited")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExecutionFailureMappingAndLogging(t *testing.T) {
	tests := []struct {
		name    string
		handler tool.Handler[input, output]
		raw     json.RawMessage
		cancel  bool
		wantLog string
	}{
		{"decode", func(context.Context, tool.Call[input]) (output, error) {
			t.Fatal("handler called")
			return output{}, nil
		}, json.RawMessage(`{"unknown":1}`), false, "decode input"},
		{"ordinary", func(context.Context, tool.Call[input]) (output, error) {
			return output{}, errors.New("internal marker")
		}, json.RawMessage(`{"value":"x"}`), false, "internal marker"},
		{"panic", func(context.Context, tool.Call[input]) (output, error) { panic("panic marker") }, json.RawMessage(`{"value":"x"}`), false, "panic marker"},
		{"canceled", func(context.Context, tool.Call[input]) (output, error) { return output{}, context.Canceled }, json.RawMessage(`{"value":"x"}`), false, ""},
		{"deadline", func(context.Context, tool.Call[input]) (output, error) { return output{}, context.DeadlineExceeded }, json.RawMessage(`{"value":"x"}`), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			w, _ := New(WorkerConfig{Client: config("https://example.com"), Logger: slog.New(slog.NewTextHandler(&logs, nil))}, namespace(t, "crm", executable(t, "work", "1", tt.handler)))
			got := w.execute(context.Background(), identity{"crm", "work", "1"}, tt.raw, tool.Subject{UserID: "u"}, tool.CallContext{})
			if got.Error == nil || got.Error.Code != tool.UnknownErrorCode || got.Error.Message != tool.UnknownErrorMessage || got.Error.Details != nil {
				t.Fatalf("outcome=%+v", got)
			}
			if tt.wantLog == "" && logs.Len() != 0 {
				t.Fatalf("unexpected log: %s", logs.String())
			}
			if tt.wantLog != "" && !strings.Contains(logs.String(), tt.wantLog) {
				t.Fatalf("log=%s", logs.String())
			}
		})
	}

	var logs bytes.Buffer
	encodeTool, err := tool.New(tool.Metadata{Name: "encode", Version: "1", Description: "encode"}, func(context.Context, tool.Call[input]) (chan int, error) { return make(chan int), nil })
	if err != nil {
		t.Fatal(err)
	}
	w, _ := New(WorkerConfig{Client: config("https://example.com"), Logger: slog.New(slog.NewTextHandler(&logs, nil))}, namespace(t, "crm", encodeTool))
	got := w.execute(context.Background(), identity{"crm", "encode", "1"}, json.RawMessage(`{"value":"x"}`), tool.Subject{}, tool.CallContext{})
	if got.Error == nil || !strings.Contains(logs.String(), "encode output") {
		t.Fatalf("outcome=%+v log=%s", got, logs.String())
	}
}

func TestClaimRetryPreservesIdempotencyKeyAndPolling(t *testing.T) {
	var keys []string
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			writeRegistration(w, "token", time.Now().Add(time.Hour))
		case strings.HasSuffix(r.URL.Path, "/claim"):
			keys = append(keys, r.Header.Get("idempotency-key"))
			n := calls.Add(1)
			if n <= 2 {
				http.Error(w, `{"code":"temporary"}`, http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(w, `{"kind":"none"}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	w.afterClaim = func() {
		if calls.Load() >= 3 {
			cancel()
		}
	}
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("keys=%v", keys)
	}
}

func TestOutcomeRetryPreservesIdempotencyKey(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("idempotency-key"))
		if len(keys) == 1 {
			http.Error(w, `{"code":"temporary"}`, http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(true)})
	}))
	defer server.Close()
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	if err := w.submitOutcomeWithRetry(context.Background(), "out", succeeded(nil), time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys=%v", keys)
	}
}

func TestOutcomeRecordedFalseIsIdempotentSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(submitOutcomeAck{Recorded: boolPointer(false)})
	}))
	defer server.Close()
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, nil })))
	err := w.submitOutcomeWithRetry(context.Background(), "out", internalFailure(), time.Now().Add(time.Second))
	if err != nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestOutcomeRequiresExplicitRecordedBoolean(t *testing.T) {
	for _, response := range []string{`{}`, `{"recorded":null}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			err := (client{baseURL: server.URL, agentID: "agent", token: "key", http: server.Client()}).submitOutcome(context.Background(), "out", internalFailure(), "key")
			if err == nil || IsRetryable(err) {
				t.Fatalf("err=%v retryable=%v", err, IsRetryable(err))
			}
		})
	}
}

func TestTypedNilToolErrorMapsSafely(t *testing.T) {
	var publicErr *tool.Error
	w, _ := New(WorkerConfig{Client: config("https://example.com")}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, publicErr })))
	got := w.execute(context.Background(), identity{"crm", "work", "1"}, json.RawMessage(`{"value":"x"}`), tool.Subject{}, tool.CallContext{})
	if got.Error == nil || got.Error.Code != tool.UnknownErrorCode || got.Error.Message != tool.UnknownErrorMessage || got.Error.Details != nil {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestBlankPublicToolErrorMapsToUnknownWithoutDetails(t *testing.T) {
	handlerErr := &tool.Error{}
	w, _ := New(WorkerConfig{Client: config("https://example.com")}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, handlerErr })))
	got := w.execute(context.Background(), identity{"crm", "work", "1"}, json.RawMessage(`{"value":"x"}`), tool.Subject{}, tool.CallContext{})
	if got.Error == nil || got.Error.Code != tool.UnknownErrorCode || got.Error.Message != tool.UnknownErrorMessage || got.Error.Details != nil {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestTypedNilOrdinaryErrorMapsSafely(t *testing.T) {
	var handlerErr *nilReceiverError
	var logs bytes.Buffer
	w, _ := New(WorkerConfig{Client: config("https://example.com"), Logger: slog.New(slog.NewTextHandler(&logs, nil))}, namespace(t, "crm", executable(t, "work", "1", func(context.Context, tool.Call[input]) (output, error) { return output{}, handlerErr })))
	got := w.execute(context.Background(), identity{"crm", "work", "1"}, json.RawMessage(`{"value":"x"}`), tool.Subject{}, tool.CallContext{})
	if got.Error == nil || got.Error.Code != tool.UnknownErrorCode || logs.Len() != 0 {
		t.Fatalf("outcome=%+v logs=%q", got, logs.String())
	}
}

func TestHeartbeatAndRefreshUseAllNamespaces(t *testing.T) {
	var registers, heartbeats atomic.Int32
	var refreshed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/register"):
			var body struct {
				Namespaces []string `json:"namespaces"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			refreshed = body.Namespaces
			n := registers.Add(1)
			expiry := time.Now().Add(time.Hour)
			if n == 1 {
				expiry = time.Now().Add(20 * time.Millisecond)
			}
			writeRegistration(w, fmt.Sprint(n), expiry)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			_ = json.NewEncoder(w).Encode(heartbeatExecutorAckWire{ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			_, _ = io.WriteString(w, `{"kind":"none"}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	h := func(context.Context, tool.Call[input]) (output, error) { return output{}, nil }
	w, _ := New(WorkerConfig{Client: config(server.URL), ClaimPollInterval: time.Millisecond, HeartbeatInterval: 5 * time.Millisecond, RefreshSkew: 50 * time.Millisecond}, namespace(t, "crm", executable(t, "a", "1", h)), namespace(t, "billing", executable(t, "b", "1", h)))
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if registers.Load() < 2 || heartbeats.Load() < 1 || strings.Join(refreshed, ",") != "crm,billing" {
		t.Fatalf("registers=%d heartbeats=%d namespaces=%v", registers.Load(), heartbeats.Load(), refreshed)
	}
}

func writeRegistration(w io.Writer, token string, expires time.Time) {
	_ = json.NewEncoder(w).Encode(struct {
		ExecutorToken          string `json:"executorToken"`
		ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
	}{token, expires.Format(time.RFC3339Nano)})
}

func writeClaim(w io.Writer, token string, expires time.Time, namespace, name, version, input, userID string) {
	_, _ = fmt.Fprintf(w, `{"kind":"claimed","outcomeToken":%q,"claimExpiresAt":%q,"toolCall":{"namespace":%q,"name":%q,"version":%q,"input":%s,"subject":{"userId":%q}}}`, token, expires.Format(time.RFC3339Nano), namespace, name, version, input, userID)
}
