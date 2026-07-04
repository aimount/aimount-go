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

`toolworker` lets a Go backend declare server tools, register live executor availability, claim tool calls, run handlers, and submit terminal outcomes.

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

err := toolworker.Handle(worker, "lookup_order", toolworker.Definition{
	Version:     "1",
	Description: "Look up an order by id.",
}, func(ctx context.Context, call toolworker.TypedCall[LookupOrderInput]) (LookupOrderOutput, error) {
	if call.Input.OrderID == "" {
		return LookupOrderOutput{}, toolworker.NewToolError("order.id_required", "order id is required", nil)
	}

	return LookupOrderOutput{Status: "paid", UserID: call.Subject.UserID}, nil
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

_, err := publisher.Publish(ctx, "crm", definitions)
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
