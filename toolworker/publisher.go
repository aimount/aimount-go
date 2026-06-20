package toolworker

import "context"

type ManifestPublisher struct {
	client client
}

func NewManifestPublisher(config PublisherConfig) *ManifestPublisher {
	return &ManifestPublisher{client: client{baseURL: config.BaseURL, agentID: config.AgentID, token: config.ToolServiceToken, http: config.HTTPClient}}
}

func (p *ManifestPublisher) Publish(ctx context.Context, namespace string, definitions []Definition) (PublishManifestAck, error) {
	defs := make([]Definition, len(definitions))
	copy(defs, definitions)
	for i := range defs {
		if defs[i].Name == "" {
			return PublishManifestAck{}, errInvalidDefinition
		}
	}
	return p.client.publishManifest(ctx, namespace, defs)
}
