package toolworker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aimount/aimount-go/tool"
)

type lookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
	Verbose bool   `json:"verbose,omitempty" jsonschema:"description=Include verbose order details"`
}

type lookupOrderOutput struct {
	Status string `json:"status"`
}

type exactNumberInput struct {
	Sequence int64 `json:"sequence" jsonschema:"required,description=Exact sequence number"`
}

func TestTypedHandleDerivesSchemaAndDispatchesTypedInput(t *testing.T) {
	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})
	definition, err := tool.Define[lookupOrderInput]("lookup_order", "1", "Look up order")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := Handle(worker, definition, func(ctx context.Context, call tool.Call[lookupOrderInput]) tool.Out {
		if call.Input.OrderID != "ord_1" || !call.Input.Verbose || call.Subject.UserID != "user_1" || call.Deadline.IsZero() {
			t.Fatalf("unexpected typed call: %+v", call)
		}
		return tool.OK(lookupOrderOutput{Status: "paid"})
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	definitions := worker.definitions()
	if len(definitions) != 1 || definitions[0].InputSchema == nil {
		t.Fatalf("typed definition did not derive schema: %+v", definitions)
	}

	callCtx, call, cancel := callContext(context.Background(), claimAck{ClaimExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), ToolCall: claimedCall{Namespace: "crm", Name: "lookup_order", Version: "1", Input: map[string]any{"orderId": "ord_1", "verbose": true}, Subject: tool.Subject{UserID: "user_1"}}})
	defer cancel()
	tool, ok := worker.tool("lookup_order")
	if !ok {
		t.Fatal("typed tool was not registered")
	}
	outcome, err := tool.handler(callCtx, call)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if outcome.Status != "succeeded" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	result, ok := outcome.Result.(lookupOrderOutput)
	if !ok || result.Status != "paid" {
		t.Fatalf("unexpected typed result: %#v", outcome.Result)
	}
}

func TestTypedHandleDecodeFailureDoesNotInvokeHandler(t *testing.T) {
	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})
	called := false
	definition, err := tool.Define[lookupOrderInput]("lookup_order", "1", "Look up order")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := Handle(worker, definition, func(ctx context.Context, call tool.Call[lookupOrderInput]) tool.Out {
		called = true
		return tool.OK(lookupOrderOutput{})
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	_, call, cancel := callContext(context.Background(), claimAck{ToolCall: claimedCall{Name: "lookup_order", Input: map[string]any{"orderId": 42}}})
	defer cancel()
	registered, _ := worker.tool("lookup_order")
	outcome, err := registered.handler(context.Background(), call)
	if err != nil {
		t.Fatalf("decode failure should map to outcome, got error: %v", err)
	}
	if called {
		t.Fatal("handler should not be called after typed decode failure")
	}
	if outcome.Status != "failed" || outcome.Error == nil || outcome.Error.Code != tool.InternalErrorCode || outcome.Error.Message != tool.InternalErrorMessage {
		t.Fatalf("unexpected decode failure outcome: %+v", outcome)
	}
}

func TestTypedHandleErrOutMapsToFailedOutcome(t *testing.T) {
	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})
	definition, err := tool.Define[lookupOrderInput]("lookup_order", "1", "Look up order")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := Handle(worker, definition, func(ctx context.Context, call tool.Call[lookupOrderInput]) tool.Out {
		return tool.Err("order.not_found", "order was not found", map[string]any{"orderId": call.Input.OrderID})
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	_, call, cancel := callContext(context.Background(), claimAck{ToolCall: claimedCall{Name: "lookup_order", Input: map[string]any{"orderId": "ord_1"}}})
	defer cancel()
	tool, _ := worker.tool("lookup_order")
	outcome, err := tool.handler(context.Background(), call)
	if err != nil {
		t.Fatalf("tool out should map to outcome, got error: %v", err)
	}
	if outcome.Status != "failed" || outcome.Error == nil || outcome.Error.Code != "order.not_found" || outcome.Error.Message != "order was not found" || outcome.Error.Details["orderId"] != "ord_1" {
		t.Fatalf("unexpected tool out outcome: %+v", outcome)
	}
}

func TestTypedHandleDecodesFromRawJSONToPreserveLargeIntegers(t *testing.T) {
	var claim claimAck
	if err := json.Unmarshal([]byte(`{"kind":"claimed","outcomeToken":"awi_tco_out","claimExpiresAt":"2026-06-19T09:01:30Z","toolCall":{"namespace":"crm","name":"exact","version":"1","input":{"sequence":9007199254740993},"subject":{"userId":"user_1"}}}`), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}

	worker := New(Config{Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever})
	definition, err := tool.Define[exactNumberInput]("exact", "1", "Exact")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := Handle(worker, definition, func(ctx context.Context, call tool.Call[exactNumberInput]) tool.Out {
		if call.Input.Sequence != 9007199254740993 {
			t.Fatalf("sequence lost precision: %d", call.Input.Sequence)
		}
		return tool.OK(lookupOrderOutput{Status: "ok"})
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}

	callCtx, call, cancel := callContext(context.Background(), claim)
	defer cancel()
	tool, _ := worker.tool("exact")
	if outcome, err := tool.handler(callCtx, call); err != nil || outcome.Status != "succeeded" {
		t.Fatalf("unexpected outcome: %+v err=%v", outcome, err)
	}
}

func TestTypedRunSubmitsTypedHandlerOutcome(t *testing.T) {
	var outcomeBody submitOutcomeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/agents/agent/tool/server/executors/register":
			_ = json.NewEncoder(w).Encode(registerExecutorAck{ExecutorToken: "awi_tex_exec", ExecutorTokenExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
		case "/agent/v1/agents/agent/tool/server/claim":
			_ = json.NewEncoder(w).Encode(claimAck{Kind: "claimed", OutcomeToken: "awi_tco_out", ClaimExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339), ToolCall: claimedCall{Namespace: "crm", Name: "lookup_order", Version: "1", Input: map[string]any{"orderId": "ord_1"}, Subject: tool.Subject{UserID: "user_1"}}})
		case "/agent/v1/agents/agent/tool/server/outcome":
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
	worker := New(Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "awi_tst_secret", Namespace: "crm", ManifestPublishPolicy: ManifestPublishNever, ClaimPollInterval: time.Millisecond, HeartbeatInterval: time.Hour})
	definition, err := tool.Define[lookupOrderInput]("lookup_order", "1", "Look up order")
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := Handle(worker, definition, func(ctx context.Context, call tool.Call[lookupOrderInput]) tool.Out {
		cancel()
		return tool.OK(lookupOrderOutput{Status: "paid"})
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcomeBody.Outcome.Status != "succeeded" {
		t.Fatalf("unexpected outcome: %+v", outcomeBody)
	}
}
