package serverapi_test

import (
	"context"

	"github.com/aimount/aimount-go/agent"
	"github.com/aimount/aimount-go/agent/serverapi"
)

type lookupInput struct {
	OrderID string `json:"orderId" jsonschema:"required"`
}

type lookupOutput struct {
	Status string `json:"status"`
}

func Example() {
	lookup, _ := agent.NewTool(agent.ToolMetadata{
		Name: "lookup_order", Version: "1", Description: "Look up an order.",
	}, func(context.Context, agent.ToolCall[lookupInput]) (lookupOutput, error) {
		return lookupOutput{Status: "paid"}, nil
	})
	namespace, _ := agent.NewToolNamespace("orders", lookup)
	client, _ := serverapi.New(serverapi.Config{
		BaseURL: "https://api.aimount.dev", AgentID: "agent_123", AgentAPIKey: "secret",
	})
	worker, _ := serverapi.NewToolWorker(client, serverapi.ToolWorkerOptions{}, namespace)

	_ = client.PublishToolManifest
	_ = worker.Run
}
