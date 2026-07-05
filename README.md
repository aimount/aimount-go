# aimount-go

Go SDK packages for Aimount.

## Packages

- `toolworker`: primitives for building Agent API server tool execution workers in Go.
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

`tool` lets a Go backend declare server tools and typed handler results. `toolworker` registers live executor availability, claims tool calls, runs handlers, and submits terminal outcomes.

Local and demo workers can publish their manifest on startup:

```go
worker := toolworker.New(toolworker.Config{
	BaseURL:               "https://api.aimount.dev",
	AgentID:               "agent_123",
	AgentAPIKey:           "...",
	Namespace:             "crm",
	ManifestPublishPolicy: toolworker.ManifestPublishOnStart,
})

type LookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
}

type LookupOrderOutput struct {
	Status string `json:"status"`
	UserID string `json:"userId"`
}

lookupOrder, err := tool.Define[LookupOrderInput]("lookup_order", "1", "Look up an order by id.")
if err != nil {
	panic(err)
}

err = toolworker.Handle(worker, lookupOrder, func(ctx context.Context, call tool.Call[LookupOrderInput]) tool.Out {
	if call.Input.OrderID == "" {
		return tool.Err("order.id_required", "order id is required", nil)
	}

	return tool.OK(LookupOrderOutput{Status: "paid", UserID: call.Subject.UserID})
})
if err != nil {
	panic(err)
}

if err := worker.Run(context.Background()); err != nil {
	panic(err)
}
```

## Production Manifest Flow

Production deployments should keep desired catalog state separate from live worker availability.

```text
cmd/publish-manifests
  -> publishes namespace manifests during CI/CD or deploy
  -> uses manifest-publish authority
  -> exits

cmd/tool-worker
  -> runs with ManifestPublishNever
  -> registers executor availability
  -> heartbeats, claims, executes, submits outcomes
```

The manifest publisher can reuse the same definitions as the worker:

```go
publisher := toolworker.NewManifestPublisher(toolworker.PublisherConfig{
	BaseURL:          "https://api.aimount.dev",
	AgentID:          "agent_123",
	AgentAPIKey:     "...",
})

_, err := publisher.Publish(ctx, tool.Manifest{Namespace: "crm", Definitions: definitions})
```

The runtime worker then uses `ManifestPublishNever`:

```go
worker := toolworker.New(toolworker.Config{
	BaseURL:               "https://api.aimount.dev",
	AgentID:               "agent_123",
	AgentAPIKey:           "...",
	Namespace:             "crm",
	ManifestPublishPolicy: toolworker.ManifestPublishNever,
	MaxConcurrentCalls:    4,
})
```

## Boundaries

`toolworker` does not provide Runtime session APIs, Console APIs, client/browser tool execution, per-call heartbeat, or operator/debug reads. Handlers should finish before their call deadline because the Agent API does not expose per-call heartbeat in the current MVP.

The existing `services/go-tool-executor` repository is a local Runtime API v2 verification worker and sample consumer of `toolworker`. It is still verification-oriented, not a recommended production application template.
