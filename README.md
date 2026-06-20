# aimount-go

Go SDK packages for Aimount.

## Packages

- `toolworker`: primitives for building Agent API server tool execution workers in Go.

## Toolworker Quickstart

`toolworker` lets a Go backend declare server tools, register live executor availability, claim tool calls, run handlers, and submit terminal outcomes.

Local and demo workers can publish their manifest on startup:

```go
worker := toolworker.New(toolworker.Config{
	BaseURL:          "https://api.aimount.dev",
	AgentID:          "agent_123",
	ToolServiceToken: "...",
	Namespace:        "crm",
	ManifestMode:     toolworker.ManifestPublishOnStart,
})

err := worker.Handle("lookup_order", toolworker.Definition{
	Version:     "1",
	Description: "Look up an order by id.",
	InputSchema: map[string]any{"type": "object"},
}, func(ctx context.Context, call toolworker.Call) (toolworker.Outcome, error) {
	return toolworker.Succeeded(map[string]any{"userId": call.Subject.UserID}), nil
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
  -> runs with ManifestExternal
  -> registers executor availability
  -> heartbeats, claims, executes, submits outcomes
```

The manifest publisher can reuse the same definitions as the worker:

```go
publisher := toolworker.NewManifestPublisher(toolworker.PublisherConfig{
	BaseURL:          "https://api.aimount.dev",
	AgentID:          "agent_123",
	ToolServiceToken: "...",
})

_, err := publisher.Publish(ctx, "crm", definitions)
```

The runtime worker then uses `ManifestExternal`:

```go
worker := toolworker.New(toolworker.Config{
	BaseURL:          "https://api.aimount.dev",
	AgentID:          "agent_123",
	ToolServiceToken: "...",
	Namespace:        "crm",
	ManifestMode:     toolworker.ManifestExternal,
	MaxConcurrentCalls: 4,
})
```

## Boundaries

`toolworker` does not provide Runtime session APIs, Console APIs, client/browser tool execution, per-call heartbeat, or operator/debug reads. Handlers should finish before their call deadline because the Agent API does not expose per-call heartbeat in the current MVP.

The existing `services/go-tool-executor` repository remains a local Runtime API v2 verification worker for now. It can be migrated to consume `toolworker` in a later follow-up once this package is established.
