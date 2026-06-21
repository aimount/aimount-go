package toolworker

import "context"

type ManifestPublisher struct {
	client client
}

func NewManifestPublisher(config PublisherConfig) *ManifestPublisher {
	return &ManifestPublisher{client: client{baseURL: config.BaseURL, agentID: config.AgentID, token: config.ToolServiceToken, http: config.HTTPClient}}
}

func (p *ManifestPublisher) Publish(ctx context.Context, namespace string, definitions []Definition, options ...PublishOptions) (PublishManifestAck, error) {
	defs := make([]Definition, len(definitions))
	copy(defs, definitions)
	for i := range defs {
		if defs[i].Name == "" {
			return PublishManifestAck{}, errInvalidDefinition
		}
	}
	var opts PublishOptions
	if len(options) > 0 {
		opts = options[0]
	}
	return p.client.publishManifest(ctx, namespace, defs, opts)
}
