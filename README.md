# aimount-go

Go SDK for Aimount Agent integrations.

## Packages

- `agent`: shared Agent entities and typed executable tools.
- `agent/serverapi`: trusted-backend Agent Server API client, manifest publication, and tool workers.

The SDK does not publish empty Client API or Console API packages. Those packages will be added when they expose real operations.

## Server API Client

```go
client, err := serverapi.New(serverapi.Config{
	BaseURL:     "https://api.aimount.dev",
	AgentID:     "agent_123",
	AgentAPIKey: "...",
})
if err != nil {
	panic(err)
}

token, err := client.IssueClientAPIAccessToken(context.Background(), serverapi.IssueClientAPIAccessTokenParams{
	ProfileID: "profile_123",
	UserID:    "user_from_client_product",
})
if err != nil {
	panic(err)
}

_ = token.AccessToken
```

`AgentAPIKey` is a backend-only credential. Do not expose it to browser, mobile, or other end-user clients; send only the issued Agent User access token payload to those clients.

## Server Tools

`agent.NewTool` derives an input schema from a named Go struct and binds it to a typed handler. `serverapi.ToolWorker` registers live executor availability, claims calls, runs handlers, and submits outcomes.

```go
type LookupOrderInput struct {
	OrderID string `json:"orderId" jsonschema:"required,description=Order id"`
}

type LookupOrderOutput struct {
	Status string `json:"status"`
	UserID string `json:"userId"`
}

lookupOrder, err := agent.NewTool(agent.ToolMetadata{
	Name: "lookup_order", Version: "1", Description: "Look up an order by id.",
}, func(ctx context.Context, call agent.ToolCall[LookupOrderInput]) (LookupOrderOutput, error) {
	if call.Input.OrderID == "" {
		return LookupOrderOutput{}, agent.NewToolError("order.id_required", "order id is required", nil)
	}
	return LookupOrderOutput{Status: "paid", UserID: call.Subject.UserID}, nil
})
if err != nil {
	panic(err)
}

crm, err := agent.NewToolNamespace("crm", lookupOrder)
if err != nil {
	panic(err)
}

client, err := serverapi.New(serverapi.Config{
	BaseURL: "https://api.aimount.dev", AgentID: "agent_123", AgentAPIKey: "...",
})
if err != nil {
	panic(err)
}

worker, err := serverapi.NewToolWorker(client, serverapi.ToolWorkerOptions{
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

Production deployments should publish desired manifests separately from worker availability:

```go
published, err := client.PublishToolManifest(ctx, crm, serverapi.PublishToolManifestOptions{})
```

`ToolWorker.Run` owns executor registration, heartbeat, claim, and outcome protocol operations. It is one-shot: cancel its context during shutdown to stop new claims and cancel active handler contexts.

Handlers must observe context cancellation and finish promptly. Go cannot forcibly terminate a handler goroutine that ignores its context; after its claim deadline the worker may return while that goroutine continues until the handler exits. Outcome delivery already in progress may delay graceful shutdown until its bounded delivery context ends.

## Migration From v0.2

| Previous | Current |
| --- | --- |
| `agent.Config`, `agent.Client`, `agent.New` | `serverapi.Config`, `serverapi.Client`, `serverapi.New` |
| `agent.IssueUserAccessTokenRequest` | `serverapi.IssueClientAPIAccessTokenParams` |
| `Client.IssueUserAccessToken` | `Client.IssueClientAPIAccessToken` |
| `agent.UserAccessToken` | `agent.AgentUserAccessToken` |
| `agent.APIError`, `agent.IsRetryable` | `serverapi.Error`, `serverapi.IsRetryable` |
| `tool.Metadata`, `tool.New` | `agent.ToolMetadata`, `agent.NewTool` |
| `tool.Definition`, `tool.Tool` | `agent.ToolDefinition`, `agent.Tool` |
| `tool.Subject`, `tool.CallContext` | `agent.ToolSubject`, `agent.ToolCallContext` |
| `tool.Call`, `tool.Handler` | `agent.ToolCall`, `agent.ToolHandler` |
| `tool.Namespace`, `tool.NewNamespace` | `agent.ToolNamespace`, `agent.NewToolNamespace` |
| `tool.Error`, `tool.NewError` | `agent.ToolError`, `agent.NewToolError` |
| `tool.ErrInvalidMetadata`, `tool.ErrInvalidInput`, `tool.ErrInvalidNamespace` | `agent.ErrInvalidToolMetadata`, `agent.ErrInvalidToolInput`, `agent.ErrInvalidToolNamespace` |
| `toolworker.ClientConfig` | `serverapi.Config` |
| `toolworker.WorkerConfig`, `toolworker.New` | `serverapi.ToolWorkerOptions`, `serverapi.NewToolWorker` |
| `toolworker.NewPublisher`, `Publisher.Publish` | `serverapi.New`, `Client.PublishToolManifest` |
| `toolworker.PublishOptions`, `toolworker.PublishManifestAck` | `serverapi.PublishToolManifestOptions`, `serverapi.PublishedToolManifest` |
| `toolworker.APIError`, `toolworker.IsRetryable` | `serverapi.Error`, `serverapi.IsRetryable` |

This is a breaking pre-v1 redesign. The removed top-level `tool` and `toolworker` packages have no compatibility facades.
