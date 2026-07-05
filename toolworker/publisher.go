package toolworker

import (
	"context"

	"github.com/aimount/aimount-go/tool"
)

type ManifestPublisher struct {
	client client
}

func NewManifestPublisher(config PublisherConfig) *ManifestPublisher {
	return &ManifestPublisher{client: client{baseURL: config.BaseURL, agentID: config.AgentID, token: config.AgentAPIKey, http: config.HTTPClient}}
}

func (p *ManifestPublisher) Publish(ctx context.Context, manifest tool.Manifest, options ...PublishOptions) (PublishManifestAck, error) {
	defs := make([]tool.Definition, len(manifest.Definitions))
	copy(defs, manifest.Definitions)
	for i := range defs {
		if defs[i].Name == "" {
			return PublishManifestAck{}, errInvalidDefinition
		}
	}
	var opts PublishOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return p.client.publishManifest(ctx, manifest.Namespace, defs, opts)
}
