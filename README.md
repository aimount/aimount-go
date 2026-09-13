# aimount-go

Go SDK for Aimount Agent integrations.

## Packages

- `agent`: shared Agent entities and typed executable tools.
- `agent/serverapi`: trusted-backend Agent Server API client, manifest publication, and tool workers.

The SDK does not publish an empty Client API package. It will be added when it exposes real operations.

## Aimount CLI

The `aimount` CLI provides the first imperative operator workflow for Aimount Console API resources. Build it as a standalone binary with no Node.js or Bun runtime:

```bash
make build
./dist/aimount help
```

Configure a Console API token and optionally override the production API URL:

```bash
export AIMOUNT_TOKEN="awi_pat_..."
export AIMOUNT_API_URL="https://api.aimount.dev" # optional default
```

Supported MVP commands:

```bash
aimount auth whoami
aimount agents list
aimount agents set support --name "Support"
aimount agents instruction set support --file instruction.md
```

If the token can access exactly one organization, resource commands select it automatically. Otherwise provide the global option before the command:

```bash
aimount --org org_123 agents list
```

`agents set` idempotently creates or updates the agent through the Console API. `agents instruction set` creates an instruction version and immediately releases it; if release fails, the error includes the created version ID.

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

## User Memory

Server API memory belongs to one agent and client-provided end user, independently of sessions, profiles, and surfaces. Keys need `agent:memory:read` for `ListMemory` and `agent:memory:write` for mutations; neither scope implies the other or is granted to existing keys automatically.

| Method | Params | Result |
| --- | --- | --- |
| `ListMemory` | `ListMemoryParams{UserID}` | `ListMemoryResult{Blocks}` |
| `CreateMemory` | `CreateMemoryParams{UserID, Text, ManagedBy, IdempotencyKey}` | `CreateMemoryResult{Operation, Block, IdempotencyKey}` |
| `UpdateMemory` | `UpdateMemoryParams{UserID, MemoryID, Version, Text}` | `UpdateMemoryResult{Operation, Block}` |
| `DeleteMemory` | `DeleteMemoryParams{UserID, MemoryID, Version}` | `DeleteMemoryResult{Operation, ID, Version}` |

Each method takes `context.Context` and returns `(Result, error)`. `AgentMemoryBlock` exposes opaque string `ID`, exact `Text`, `int64` `Version`, `ManagedBy`, `CreatedBy`, `UpdatedBy`, and UTC `time.Time` timestamps `CreatedAt`/`UpdatedAt`. `MemoryActorUser`, `MemoryActorAgent`, and `MemoryActorService` are the three management/author kinds. Server creation defaults to service management; selecting another manager does not change the backend's service authorship. Unknown response fields are ignored, while required fields are validated.

Create/update text must be nonblank valid UTF-8 without NUL, at most 2,048 bytes; accepted whitespace and formatting are preserved. Cloud additionally enforces 50 blocks and 16,384 text bytes per owner. Update/delete require the version read from the block, from 1 through `MaxMemoryVersion` (9007199254740991). The SDK sends it as a quoted `If-Match` header; PATCH sends only text and DELETE sends no body. Identical-text updates can return the unchanged version; deletion returns the last deleted version.

Memory success responses require valid UTF-8 wire bytes and NUL-free block text. Escaped lone UTF-16 surrogates retain Go `encoding/json` behavior: they decode as U+FFFD, which is also allowed as legitimate text. Valid Cloud responses never contain lone surrogates; the SDK does not implement a custom JSON decoder.

### Creation Replay

`CreateMemory` sends one POST, generating an idempotency key unless supplied. The key is returned in `CreateMemoryResult.IdempotencyKey` even on failure after generation, and in `serverapi.Error.IdempotencyKey` for request/response errors. Retain it with the original user, exact text, and effective management kind; an application retry must supply that same key, not start another keyless call:

```go
params := serverapi.CreateMemoryParams{
	UserID: "user_123", Text: "Prefers concise answers.",
	ManagedBy: serverapi.MemoryActorAgent,
}
created, err := client.CreateMemory(ctx, params)
params.IdempotencyKey = created.IdempotencyKey // retain even when err != nil
if err != nil {
	// Persist params for an application-decided retry with this same key.
	return err
}
_ = created.Block
```

Cloud replays the original successful creation for 24 hours, including across authorized key rotation. Changed content conflicts; after retention the key can create a new block. Replay does not restore an edited/deleted block or represent its current state. Use `ListMemory` for current state.

### Conflicts And Uncertainty

Memory errors preserve `serverapi.Error.Type`, `Code`, `Message`, and `Details` (`json.RawMessage`), plus `Method`, `Path`, and `StatusCode`. `ErrorCode` and `HTTPStatus` also work through wrapping. A known `412` version conflict includes `details.currentBlock`; missing server preconditions return `428`. Local invalid versions are rejected before any request. Decode conflict details into a struct with `CurrentBlock serverapi.AgentMemoryBlock` tagged `json:"currentBlock"` when needed; never silently substitute its newer version and repeat the old write.

PATCH/DELETE never automatically retry or follow redirects. A lost/truncated response, malformed success, timeout, or server/gateway failure can leave `Error.OutcomeUnknown` true. `IsRetryable` is false for uncertain writes, including wrapped errors. A context already canceled before dispatch is not an unknown outcome. Inspect uncertainty before treating the request as rejected:

```go
_, err := client.UpdateMemory(ctx, serverapi.UpdateMemoryParams{
	UserID: "user_123", MemoryID: observed.ID,
	Version: observed.Version, Text: "Prefers detailed examples.",
})
var apiErr serverapi.Error
if errors.As(err, &apiErr) && apiErr.OutcomeUnknown {
	// Use a fresh live context if the write's context expired.
	current, readErr := client.ListMemory(reconcileCtx, serverapi.ListMemoryParams{UserID: "user_123"})
	if readErr != nil {
		return readErr // write outcome remains unknown
	}
	_ = current // reconcile; do not automatically repeat the original mutation
}
if err != nil {
	return err
}
```

GET can show current text or absence but cannot prove which writer caused it. A further write is a new application decision against current state. Memory mutations do not trigger an SDK refresh or live subscription, and deleting memory does not erase conversation history or prevent later re-learning. Blocks have no TTL or first-version undo.

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
