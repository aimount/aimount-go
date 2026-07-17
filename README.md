# aimount-go

Go SDK packages for Aimount.

## Packages

- `tool`: typed executable server tools and namespaces.
- `toolworker`: Agent API namespace publication and execution workers.
- `runtimeauth`: server-side helper for issuing Runtime API v2 user tokens from a Go backend.

## Runtime Auth Quickstart

`runtimeauth` lets a trusted Go backend request a short-lived runtime user token for an end user of the client product.

```go
issuer := runtimeauth.New(runtimeauth.Config{
	BaseURL:      "https://api.aimount.dev",
	AgentID:      "agent_123",
	AgentAPIKey: "...",
})

token, err := issuer.IssueUserToken(context.Background(), runtimeauth.IssueUserTokenRequest{
	ProfileID: "profile_123",
	UserID:    "user_from_client_product",
})
if err != nil {
	panic(err)
}

_ = token.RuntimeToken
```

`AgentAPIKey` is a backend-only credential. Do not expose it to browser, mobile, or other end-user runtime clients; send only the issued runtime user token payload to those clients.

## Toolworker Quickstart

`tool` binds generated input schemas to typed handlers. `toolworker` registers live executor availability, claims calls, runs handlers, and submits outcomes.

Tool inputs intentionally match Cloud's current flat schema subset: use a named struct with unique, named `string`, `bool`, `float32`, or `float64` JSON fields. `tool.New` rejects integers, nested or anonymous structs, pointers, collections, maps, interfaces, custom JSON, text, or schema hooks, invalid JSON tag names, case-fold-equivalent field names, and `json:",string"` fields. Execution requires exact case-sensitive property names and rejects duplicate keys.

Schema tags may mark a field `required` and add `description` metadata. Other validation metadata is rejected because Cloud does not enforce it. Direct `Tool.Execute` enforces object shape, exact keys, duplicate-key rejection, and Go primitive decoding; Cloud enforces published required fields before worker claims, while handlers validate decoded domain values.

```go
type LookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
}

type LookupOrderOutput struct {
	Status string `json:"status"`
	UserID string `json:"userId"`
}

lookupOrder, err := tool.New(tool.Metadata{
	Name: "lookup_order", Version: "1", Description: "Look up an order by id.",
}, func(ctx context.Context, call tool.Call[LookupOrderInput]) (LookupOrderOutput, error) {
	if call.Input.OrderID == "" {
		return LookupOrderOutput{}, tool.NewError("order.id_required", "order id is required", nil)
	}
	return LookupOrderOutput{Status: "paid", UserID: call.Subject.UserID}, nil
})
if err != nil {
	panic(err)
}

crm, err := tool.NewNamespace("crm", lookupOrder)
if err != nil {
	panic(err)
}

worker, err := toolworker.New(toolworker.WorkerConfig{
	Client: toolworker.ClientConfig{
		BaseURL: "https://api.aimount.dev", AgentID: "agent_123", AgentAPIKey: "...",
	},
	MaxConcurrentCalls: 4,
}, crm)
if err != nil {
	panic(err)
}
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
if err := worker.Run(ctx); err != nil {
	panic(err)
}
```

## Production Manifest Flow

Production deployments should keep desired catalog state separate from live worker availability.

```text
cmd/publish-tools
  -> publishes namespaces during CI/CD or deploy
  -> Agent API key scope: runtime:v2:tool:manifest
  -> exits

cmd/tool-worker
  -> registers executor availability
  -> heartbeats, claims, executes, submits outcomes
  -> Agent API key scopes: runtime:v2:tool:executor, runtime:v2:tool:claim, runtime:v2:tool:outcome
```

The publisher and workers reuse the same executable namespace values. Publish one namespace per call:

```go
publisher, err := toolworker.NewPublisher(toolworker.ClientConfig{
	BaseURL: "https://api.aimount.dev", AgentID: "agent_123", AgentAPIKey: "...",
})
if err != nil {
	panic(err)
}
_, err = publisher.Publish(ctx, crm, toolworker.PublishOptions{})
```

Pass multiple namespaces to the same `toolworker.New` call, for example `toolworker.New(config, crm, billing)`. `MaxConcurrentCalls` remains one global limit across all namespaces.

`Worker.Run` is one-shot. Cancel its context during shutdown to stop new claims and cancel active handler contexts. Handlers must observe context cancellation and finish promptly. Go cannot forcibly terminate an uncooperative handler goroutine: after its claim deadline the worker warns and may stop waiting during shutdown, but the goroutine continues and keeps its `MaxConcurrentCalls` slot until it exits.

## Boundaries

`toolworker` does not provide Runtime session APIs, Console APIs, client/browser tool execution, per-call heartbeat, or operator/debug reads. Handlers should finish before their call deadline because the Agent API does not expose per-call heartbeat.
