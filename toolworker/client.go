package toolworker

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

	"github.com/aimount/aimount-go/tool"
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

type PublishManifestAck struct {
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
	Namespace string           `json:"namespace"`
	Name      string           `json:"name"`
	Version   string           `json:"version"`
	Input     json.RawMessage  `json:"input"`
	Subject   tool.Subject     `json:"subject"`
	Context   tool.CallContext `json:"context"`
}

type submitOutcomeRequest struct {
	OutcomeToken string  `json:"outcomeToken"`
	Outcome      outcome `json:"outcome"`
}

type SubmitOutcomeAck struct {
	Recorded *bool `json:"recorded"`
}

type HeartbeatExecutorAck struct {
	ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
}

type heartbeatExecutorAck struct{ ExecutorTokenExpiresAt time.Time }

func (c client) publishManifest(ctx context.Context, namespace string, definitions []wireDefinition, options PublishOptions) (PublishManifestAck, error) {
	var ack PublishManifestAck
	policy := options.ConflictResolutionPolicy
	if policy == ManifestConflictReplace && options.IfMatchManifestToken != "" {
		return ack, fmt.Errorf("ifMatchManifestToken is not allowed with replace conflict policy")
	}
	var ifMatch *string
	if options.IfMatchManifestToken != "" {
		ifMatch = &options.IfMatchManifestToken
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/agent/v1/agents/%s/tool/namespaces/%s/manifest", escape(c.agentID), escape(namespace)), publishManifestRequest{IfMatchManifestToken: ifMatch, ConflictResolutionPolicy: policy, Tools: definitions}, &ack, ""); err != nil {
		return ack, err
	}
	if strings.TrimSpace(ack.ManifestToken) == "" || strings.TrimSpace(ack.ManifestHash) == "" {
		return ack, protocolError{"toolworker: malformed manifest response"}
	}
	return ack, nil
}

func (c client) registerExecutor(ctx context.Context, namespaces []string) (registerExecutorAck, error) {
	var wire struct {
		ExecutorToken          string `json:"executorToken"`
		ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/executors/register", escape(c.agentID)), map[string]any{"namespaces": namespaces}, &wire, ""); err != nil {
		return registerExecutorAck{}, err
	}
	expires, err := time.Parse(time.RFC3339, wire.ExecutorTokenExpiresAt)
	if strings.TrimSpace(wire.ExecutorToken) == "" || err != nil || !time.Now().Before(expires) {
		return registerExecutorAck{}, protocolError{"toolworker: malformed registration response"}
	}
	return registerExecutorAck{ExecutorToken: wire.ExecutorToken, ExecutorTokenExpiresAt: expires}, nil
}

func (c client) heartbeatExecutor(ctx context.Context, executorToken string) (heartbeatExecutorAck, error) {
	var wire HeartbeatExecutorAck
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/executors/heartbeat", escape(c.agentID)), map[string]any{"executorToken": executorToken}, &wire, ""); err != nil {
		return heartbeatExecutorAck{}, err
	}
	expires, err := time.Parse(time.RFC3339, wire.ExecutorTokenExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return heartbeatExecutorAck{}, protocolError{"toolworker: malformed heartbeat response"}
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
	body := map[string]any{"executorToken": executorToken, "namespaces": namespaces}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/claim", escape(c.agentID)), body, &wire, key); err != nil {
		return claimAck{}, err
	}
	var expiry string
	expiryErr := json.Unmarshal(wire.ClaimExpiresAt, &expiry)
	expires, parseErr := time.Parse(time.RFC3339, expiry)
	ack := claimAck{Kind: wire.Kind, OutcomeToken: wire.OutcomeToken, ClaimExpiresAt: expires, ClaimExpiryIsValid: expiryErr == nil && parseErr == nil && time.Now().Before(expires), ToolCall: wire.ToolCall}
	switch ack.Kind {
	case "none":
		if ack.OutcomeToken != "" || len(wire.ClaimExpiresAt) != 0 || ack.ToolCall.Namespace != "" || ack.ToolCall.Name != "" || ack.ToolCall.Version != "" || len(ack.ToolCall.Input) != 0 || ack.ToolCall.Subject.UserID != "" || len(ack.ToolCall.Context.SessionLabels) != 0 {
			return ack, protocolError{"toolworker: malformed empty claim response"}
		}
		return ack, nil
	case "claimed":
		if strings.TrimSpace(ack.OutcomeToken) == "" || strings.TrimSpace(ack.ToolCall.Namespace) == "" || strings.TrimSpace(ack.ToolCall.Name) == "" || strings.TrimSpace(ack.ToolCall.Version) == "" || len(ack.ToolCall.Input) == 0 || strings.TrimSpace(ack.ToolCall.Subject.UserID) == "" {
			return ack, protocolError{"toolworker: malformed claimed response"}
		}
		return ack, nil
	default:
		return ack, protocolError{"toolworker: malformed claim response"}
	}
}

func (c client) submitOutcome(ctx context.Context, outcomeToken string, outcome outcome, key string) error {
	var ack SubmitOutcomeAck
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/outcome", escape(c.agentID)), submitOutcomeRequest{OutcomeToken: outcomeToken, Outcome: outcome}, &ack, key); err != nil {
		return err
	}
	if ack.Recorded == nil {
		return protocolError{"toolworker: malformed outcome response: recorded must be boolean"}
	}
	return nil
}

func (c client) do(ctx context.Context, method string, path string, body any, result any, idempotencyKey string) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.token)
	if idempotencyKey != "" {
		req.Header.Set("idempotency-key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Code: errorCode(responseBody)}
	}
	if result == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	if err := decodeJSON(resp.Body, result); err != nil {
		return protocolError{fmt.Sprintf("toolworker: malformed successful response: %v", err)}
	}
	return nil
}

func decodeJSON(reader io.Reader, result any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(result); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("toolworker: trailing JSON response")
		}
		return fmt.Errorf("toolworker: trailing JSON response: %w", err)
	}
	return nil
}

func errorCode(body []byte) string {
	var envelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	return envelope.Code
}

func escape(segment string) string { return url.PathEscape(segment) }
