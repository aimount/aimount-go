package serverapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aimount/aimount-go/agent"
	"github.com/aimount/aimount-go/internal/httpjson"
)

type client struct {
	baseURL string
	agentID string
	token   string
	http    *http.Client
}

type publishManifestRequest struct {
	IfMatchManifestToken     *string                          `json:"ifMatchManifestToken,omitempty"`
	ConflictResolutionPolicy ManifestConflictResolutionPolicy `json:"conflictResolutionPolicy,omitempty"`
	Tools                    []wireDefinition                 `json:"tools"`
}

type wireDefinition struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type PublishedToolManifest struct {
	ManifestToken string `json:"manifestToken"`
	ManifestHash  string `json:"manifestHash"`
}

type registerExecutorAck struct {
	ExecutorToken          string
	ExecutorTokenExpiresAt time.Time
}

type claimAck struct {
	Kind               string
	OutcomeToken       string
	ClaimExpiresAt     time.Time
	ClaimExpiryIsValid bool
	ToolCall           claimedCall
}

type claimedCall struct {
	Namespace string                `json:"namespace"`
	Name      string                `json:"name"`
	Version   string                `json:"version"`
	Input     json.RawMessage       `json:"input"`
	Subject   agent.ToolSubject     `json:"subject"`
	Context   agent.ToolCallContext `json:"context"`
}

type submitOutcomeRequest struct {
	OutcomeToken string  `json:"outcomeToken"`
	Outcome      outcome `json:"outcome"`
}

type submitOutcomeAck struct {
	Recorded *bool `json:"recorded"`
}

type heartbeatExecutorAckWire struct {
	ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
}

type heartbeatExecutorAck struct{ ExecutorTokenExpiresAt time.Time }

func (c client) publishManifest(ctx context.Context, namespace string, definitions []wireDefinition, options PublishToolManifestOptions) (PublishedToolManifest, error) {
	var ack PublishedToolManifest
	policy := options.ConflictResolutionPolicy
	switch policy {
	case "":
		if options.IfMatchManifestToken != "" {
			return ack, fmt.Errorf("conflict resolution policy is required with ifMatchManifestToken")
		}
	case ManifestConflictReplace:
		if options.IfMatchManifestToken != "" {
			return ack, fmt.Errorf("ifMatchManifestToken is not allowed with replace conflict policy")
		}
	case ManifestConflictReplaceIfTokenMatch:
		if options.IfMatchManifestToken == "" {
			return ack, fmt.Errorf("ifMatchManifestToken is required with replace-if-token-match conflict policy")
		}
	default:
		return ack, fmt.Errorf("unsupported manifest conflict resolution policy %q", policy)
	}
	var ifMatch *string
	if options.IfMatchManifestToken != "" {
		ifMatch = &options.IfMatchManifestToken
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/agent/v1/agents/%s/server/tools/namespaces/%s/manifest", escape(c.agentID), escape(namespace)), publishManifestRequest{IfMatchManifestToken: ifMatch, ConflictResolutionPolicy: policy, Tools: definitions}, &ack, ""); err != nil {
		return ack, err
	}
	if strings.TrimSpace(ack.ManifestToken) == "" || strings.TrimSpace(ack.ManifestHash) == "" {
		return ack, protocolError{"serverapi: malformed manifest response"}
	}
	return ack, nil
}

func (c client) registerExecutor(ctx context.Context, namespaces []string) (registerExecutorAck, error) {
	var wire struct {
		ExecutorToken          string `json:"executorToken"`
		ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/server/tools/executors/register", escape(c.agentID)), map[string]any{"namespaces": namespaces}, &wire, ""); err != nil {
		return registerExecutorAck{}, err
	}
	expires, err := time.Parse(time.RFC3339, wire.ExecutorTokenExpiresAt)
	if strings.TrimSpace(wire.ExecutorToken) == "" || err != nil || !time.Now().Before(expires) {
		return registerExecutorAck{}, protocolError{"serverapi: malformed registration response"}
	}
	return registerExecutorAck{ExecutorToken: wire.ExecutorToken, ExecutorTokenExpiresAt: expires}, nil
}

func (c client) heartbeatExecutor(ctx context.Context, executorToken string) (heartbeatExecutorAck, error) {
	var wire heartbeatExecutorAckWire
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/server/tools/executors/heartbeat", escape(c.agentID)), map[string]any{"executorToken": executorToken}, &wire, ""); err != nil {
		return heartbeatExecutorAck{}, err
	}
	expires, err := time.Parse(time.RFC3339, wire.ExecutorTokenExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return heartbeatExecutorAck{}, protocolError{"serverapi: malformed heartbeat response"}
	}
	return heartbeatExecutorAck{ExecutorTokenExpiresAt: expires}, nil
}

func (c client) claim(ctx context.Context, executorToken string, namespaces []string, key string) (claimAck, error) {
	var wire struct {
		Kind           string          `json:"kind"`
		OutcomeToken   string          `json:"outcomeToken"`
		ClaimExpiresAt json.RawMessage `json:"claimExpiresAt"`
		ToolCall       claimedCall     `json:"toolCall"`
	}
	body := map[string]any{"executorToken": executorToken}
	if namespaces != nil {
		body["namespaces"] = namespaces
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/server/tools/claim", escape(c.agentID)), body, &wire, key); err != nil {
		return claimAck{}, err
	}
	var expiry string
	expiryErr := json.Unmarshal(wire.ClaimExpiresAt, &expiry)
	expires, parseErr := time.Parse(time.RFC3339, expiry)
	ack := claimAck{Kind: wire.Kind, OutcomeToken: wire.OutcomeToken, ClaimExpiresAt: expires, ClaimExpiryIsValid: expiryErr == nil && parseErr == nil && time.Now().Before(expires), ToolCall: wire.ToolCall}
	switch ack.Kind {
	case "none":
		if ack.OutcomeToken != "" || len(wire.ClaimExpiresAt) != 0 || ack.ToolCall.Namespace != "" || ack.ToolCall.Name != "" || ack.ToolCall.Version != "" || len(ack.ToolCall.Input) != 0 || ack.ToolCall.Subject.UserID != "" || len(ack.ToolCall.Context.SessionLabels) != 0 {
			return ack, protocolError{"serverapi: malformed empty claim response"}
		}
		return ack, nil
	case "claimed":
		if strings.TrimSpace(ack.OutcomeToken) == "" || strings.TrimSpace(ack.ToolCall.Namespace) == "" || strings.TrimSpace(ack.ToolCall.Name) == "" || strings.TrimSpace(ack.ToolCall.Version) == "" || len(ack.ToolCall.Input) == 0 || strings.TrimSpace(ack.ToolCall.Subject.UserID) == "" {
			return ack, protocolError{"serverapi: malformed claimed response"}
		}
		return ack, nil
	default:
		return ack, protocolError{"serverapi: malformed claim response"}
	}
}

func (c client) submitOutcome(ctx context.Context, outcomeToken string, outcome outcome, key string) error {
	path := fmt.Sprintf("/agent/v1/agents/%s/server/tools/outcome", escape(c.agentID))
	body, err := json.Marshal(submitOutcomeRequest{OutcomeToken: outcomeToken, Outcome: outcome})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.token)
	if key != "" {
		req.Header.Set("idempotency-key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Error{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Code: httpjson.ErrorCode(responseBody)}
	}
	if resp.StatusCode != http.StatusOK {
		return protocolError{fmt.Sprintf("serverapi: malformed outcome response: unexpected status %d", resp.StatusCode)}
	}
	var ack submitOutcomeAck
	if err := httpjson.Decode(resp.Body, &ack); err != nil {
		return protocolError{fmt.Sprintf("serverapi: malformed outcome response: %v", err)}
	}
	if ack.Recorded == nil {
		return protocolError{"serverapi: malformed outcome response: recorded must be boolean"}
	}
	return nil
}

func (c client) do(ctx context.Context, method string, path string, body any, result any, idempotencyKey string) error {
	requestErr, err := httpjson.Do(ctx, c.http, method, c.baseURL, path, c.token, idempotencyKey, body, result)
	if err != nil {
		var decodeErr httpjson.DecodeError
		if errors.As(err, &decodeErr) {
			return protocolError{fmt.Sprintf("serverapi: malformed successful response: %v", err)}
		}
		return err
	}
	if requestErr != nil {
		return Error{Method: method, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	return nil
}

func escape(segment string) string { return url.PathEscape(segment) }
