package serverapi_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aimount/aimount-go/agent"
	"github.com/aimount/aimount-go/agent/serverapi"
)

func TestPublicServerAPISurfaceCompiles(t *testing.T) {
	client, err := serverapi.New(serverapi.Config{
		BaseURL: "https://api.example.test", AgentID: "agent", AgentAPIKey: "key", HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ *serverapi.Client = client
	var _ func(context.Context, serverapi.IssueClientAPIAccessTokenParams) (agent.AgentUserAccessToken, error) = client.IssueClientAPIAccessToken
	var _ serverapi.PublishToolManifestOptions
	var _ serverapi.PublishedToolManifest
	var _ serverapi.ToolWorkerOptions
	var _ *serverapi.ToolWorker
	var _ func(context.Context, serverapi.ListMemoryParams) (serverapi.ListMemoryResult, error) = client.ListMemory
	var _ func(context.Context, serverapi.CreateMemoryParams) (serverapi.CreateMemoryResult, error) = client.CreateMemory
	var _ func(context.Context, serverapi.UpdateMemoryParams) (serverapi.UpdateMemoryResult, error) = client.UpdateMemory
	var _ func(context.Context, serverapi.DeleteMemoryParams) (serverapi.DeleteMemoryResult, error) = client.DeleteMemory
	var _ serverapi.AgentMemoryBlock
	var _ serverapi.MemoryActor = serverapi.MemoryActorService
	apiErr := serverapi.Error{StatusCode: http.StatusTooManyRequests, Code: "rate_limited"}
	if serverapi.ErrorCode(apiErr) != "rate_limited" || serverapi.HTTPStatus(apiErr) != http.StatusTooManyRequests || !serverapi.IsRetryable(apiErr) {
		t.Fatal("server API error helpers changed")
	}
	var target serverapi.Error
	if !errors.As(error(apiErr), &target) {
		t.Fatal("server API error is not classifiable")
	}
}
