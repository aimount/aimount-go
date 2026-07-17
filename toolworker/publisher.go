package toolworker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aimount/aimount-go/tool"
)

var (
	ErrInvalidBaseURL       = errors.New("toolworker: invalid base URL")
	ErrMissingAgentID       = errors.New("toolworker: agent ID required")
	ErrMissingAgentAPIKey   = errors.New("toolworker: agent API key required")
	ErrMultipleToolVersions = errors.New("toolworker: multiple tool versions")
	ErrNamespaceRequired    = errors.New("toolworker: namespace required")
	ErrInvalidNamespace     = errors.New("toolworker: invalid namespace")
	ErrDuplicateNamespace   = errors.New("toolworker: duplicate namespace")
	ErrWorkerAlreadyRun     = errors.New("toolworker: worker already run")
)

type Publisher struct {
	client client
}

func newClient(config ClientConfig) (client, error) {
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

func NewPublisher(config ClientConfig) (*Publisher, error) {
	c, err := newClient(config)
	if err != nil {
		return nil, err
	}
	return &Publisher{client: c}, nil
}

func (p *Publisher) Publish(ctx context.Context, namespace tool.Namespace, options PublishOptions) (PublishManifestAck, error) {
	if namespace.Name() == "" || len(namespace.Tools()) == 0 {
		return PublishManifestAck{}, ErrInvalidNamespace
	}
	seen := map[string]struct{}{}
	definitions := make([]wireDefinition, 0, len(namespace.Tools()))
	for _, serverTool := range namespace.Tools() {
		d := serverTool.Definition()
		if _, ok := seen[d.Name()]; ok {
			return PublishManifestAck{}, fmt.Errorf("%w: %s", ErrMultipleToolVersions, d.Name())
		}
		seen[d.Name()] = struct{}{}
		definitions = append(definitions, wireDefinition{Name: d.Name(), Version: d.Version(), Description: d.Description(), InputSchema: d.InputSchema()})
	}
	return p.client.publishManifest(ctx, namespace.Name(), definitions, options)
}
