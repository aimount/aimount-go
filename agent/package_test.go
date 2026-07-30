package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aimount/aimount-go/agent"
)

type publicInput struct {
	Value string `json:"value"`
}

func TestPublicToolSurfaceCompiles(t *testing.T) {
	var handler agent.ToolHandler[publicInput, string] = func(_ context.Context, call agent.ToolCall[publicInput]) (string, error) {
		_ = call.Subject.UserID
		_ = call.Context.SessionLabels
		return call.Input.Value, nil
	}
	tool, err := agent.NewTool(agent.ToolMetadata{Name: "public", Version: "1", Description: "Public tool"}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.NewToolNamespace("public", tool); err != nil {
		t.Fatal(err)
	}
	var _ agent.ToolDefinition = tool.Definition()
	var _ *agent.ToolError = agent.NewToolError("public", "public failure", nil)
	encoded, err := json.Marshal(agent.AgentUserAccessToken{AccessToken: "token", TokenType: "Bearer"})
	if err != nil || string(encoded) != `{"accessToken":"token","tokenType":"Bearer","expiresAt":"0001-01-01T00:00:00Z"}` {
		t.Fatalf("access token JSON = %s, %v", encoded, err)
	}
}
