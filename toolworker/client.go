package toolworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type client struct {
	baseURL string
	agentID string
	token   string
	http    *http.Client
}

type publishManifestRequest struct {
	IfMatchManifestToken *string      `json:"ifMatchManifestToken"`
	Tools                []Definition `json:"tools"`
}

type PublishManifestAck struct {
	ManifestToken string `json:"manifestToken"`
	ManifestHash  string `json:"manifestHash"`
}

type RegisterExecutorAck struct {
	ExecutorToken          string `json:"executorToken"`
	ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
}

type ClaimAck struct {
	Kind           string      `json:"kind"`
	OutcomeToken   string      `json:"outcomeToken"`
	ClaimExpiresAt string      `json:"claimExpiresAt"`
	ToolCall       ClaimedCall `json:"toolCall"`
}

type ClaimedCall struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Input     map[string]any `json:"input"`
	Subject   Subject        `json:"subject"`
}

type submitOutcomeRequest struct {
	OutcomeToken string  `json:"outcomeToken"`
	Outcome      Outcome `json:"outcome"`
}

type SubmitOutcomeAck struct {
	Recorded bool `json:"recorded"`
}

type HeartbeatExecutorAck struct {
	ExecutorTokenExpiresAt string `json:"executorTokenExpiresAt"`
}

func (c client) publishManifest(ctx context.Context, namespace string, definitions []Definition) (PublishManifestAck, error) {
	var ack PublishManifestAck
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/agent/v1/agents/%s/tool/namespaces/%s/manifest", escape(c.agentID), escape(namespace)), publishManifestRequest{Tools: definitions}, &ack, "")
	return ack, err
}

func (c client) registerExecutor(ctx context.Context, namespaces []string) (RegisterExecutorAck, error) {
	var ack RegisterExecutorAck
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/executors/register", escape(c.agentID)), map[string]any{"namespaces": namespaces}, &ack, "")
	return ack, err
}

func (c client) heartbeatExecutor(ctx context.Context, executorToken string) (HeartbeatExecutorAck, error) {
	var ack HeartbeatExecutorAck
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/executors/heartbeat", escape(c.agentID)), map[string]any{"executorToken": executorToken}, &ack, "")
	return ack, err
}

func (c client) claim(ctx context.Context, executorToken string, namespaces []string, key string) (ClaimAck, error) {
	var ack ClaimAck
	body := map[string]any{"executorToken": executorToken}
	if len(namespaces) > 0 {
		body["namespaces"] = namespaces
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/claim", escape(c.agentID)), body, &ack, key)
	return ack, err
}

func (c client) submitOutcome(ctx context.Context, outcomeToken string, outcome Outcome, key string) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/agent/v1/agents/%s/tool/server/outcome", escape(c.agentID)), submitOutcomeRequest{OutcomeToken: outcomeToken, Outcome: outcome}, nil, key)
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
	httpClient := c.http
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
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
	return json.NewDecoder(resp.Body).Decode(result)
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
