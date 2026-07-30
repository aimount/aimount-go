package serverapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aimount/aimount-go/agent"
)

var (
	ErrInvalidBaseURL       = errors.New("serverapi: invalid base URL")
	ErrMissingAgentID       = errors.New("serverapi: agent ID required")
	ErrMissingAgentAPIKey   = errors.New("serverapi: agent API key required")
	ErrMultipleToolVersions = errors.New("serverapi: multiple tool versions")
	ErrNamespaceRequired    = errors.New("serverapi: namespace required")
	ErrInvalidNamespace     = errors.New("serverapi: invalid namespace")
	ErrDuplicateNamespace   = errors.New("serverapi: duplicate namespace")
	ErrWorkerAlreadyRun     = errors.New("serverapi: worker already run")
)

func newClient(config Config) (client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u.IsAbs() == false || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return client{}, fmt.Errorf("%w: %q", ErrInvalidBaseURL, config.BaseURL)
	}
	if strings.TrimSpace(config.AgentID) == "" {
		return client{}, ErrMissingAgentID
	}
	if strings.TrimSpace(config.AgentAPIKey) == "" {
		return client{}, ErrMissingAgentAPIKey
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return client{baseURL: strings.TrimRight(config.BaseURL, "/"), agentID: config.AgentID, token: config.AgentAPIKey, http: httpClient}, nil
}

func (c *Client) PublishToolManifest(ctx context.Context, namespace agent.ToolNamespace, options PublishToolManifestOptions) (PublishedToolManifest, error) {
	if c == nil {
		return PublishedToolManifest{}, errors.New("serverapi: client is required")
	}
	if namespace.Name() == "" || len(namespace.Tools()) == 0 {
		return PublishedToolManifest{}, ErrInvalidNamespace
	}
	seen := map[string]struct{}{}
	definitions := make([]wireDefinition, 0, len(namespace.Tools()))
	for _, serverTool := range namespace.Tools() {
		d := serverTool.Definition()
		if _, ok := seen[d.Name()]; ok {
			return PublishedToolManifest{}, fmt.Errorf("%w: %s", ErrMultipleToolVersions, d.Name())
		}
		seen[d.Name()] = struct{}{}
		definitions = append(definitions, wireDefinition{Name: d.Name(), Version: d.Version(), Description: d.Description(), InputSchema: d.InputSchema()})
	}
	return c.client.publishManifest(ctx, namespace.Name(), definitions, options)
}
