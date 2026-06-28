package runtimeauth

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
)

type Config struct {
	BaseURL      string
	AgentID      string
	ServerAPIKey string
	HTTPClient   *http.Client
}

type Client struct {
	baseURL      string
	agentID      string
	serverAPIKey string
	http         *http.Client
}

type IssueUserTokenRequest struct {
	ProfileID string
	UserID    string
}

type UserToken struct {
	RuntimeToken string
	TokenType    string
	ExpiresAt    time.Time
}

type issueUserTokenRequest struct {
	ProfileID string `json:"profileId"`
	UserID    string `json:"userId"`
}

type issueUserTokenResponse struct {
	RuntimeToken string `json:"runtimeToken"`
	TokenType    string `json:"tokenType"`
	ExpiresAt    string `json:"expiresAt"`
}

func New(config Config) Client {
	return Client{baseURL: config.BaseURL, agentID: config.AgentID, serverAPIKey: config.ServerAPIKey, http: config.HTTPClient}
}

func (c Client) IssueUserToken(ctx context.Context, request IssueUserTokenRequest) (UserToken, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return UserToken{}, errors.New("runtimeauth: base url is required")
	}
	if strings.TrimSpace(c.agentID) == "" {
		return UserToken{}, errors.New("runtimeauth: agent id is required")
	}
	if strings.TrimSpace(c.serverAPIKey) == "" {
		return UserToken{}, errors.New("runtimeauth: server api key is required")
	}
	if strings.TrimSpace(request.ProfileID) == "" {
		return UserToken{}, errors.New("runtimeauth: profile id is required")
	}
	if strings.TrimSpace(request.UserID) == "" {
		return UserToken{}, errors.New("runtimeauth: user id is required")
	}

	path := fmt.Sprintf("/agent/v1/agents/%s/runtime/tokens", escape(c.agentID))
	var response issueUserTokenResponse
	err := c.do(ctx, http.MethodPost, path, issueUserTokenRequest{ProfileID: request.ProfileID, UserID: request.UserID}, &response)
	if err != nil {
		return UserToken{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, response.ExpiresAt)
	if err != nil {
		expiresAt, err = time.Parse(time.RFC3339Nano, response.ExpiresAt)
	}
	if err != nil {
		return UserToken{}, fmt.Errorf("runtimeauth: decode expiresAt: %w", err)
	}
	return UserToken{RuntimeToken: response.RuntimeToken, TokenType: response.TokenType, ExpiresAt: expiresAt}, nil
}

func (c Client) do(ctx context.Context, method string, path string, body any, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.serverAPIKey)
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
	return json.NewDecoder(resp.Body).Decode(result)
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Code       string
}

func (e APIError) Error() string {
	if e.Code != "" {
		return e.Method + " " + e.Path + " failed: " + e.Code
	}
	return e.Method + " " + e.Path + " failed"
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
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
