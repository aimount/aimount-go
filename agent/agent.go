// Package agent provides backend-only access to the Agent Server API.
// AgentAPIKey credentials must never be exposed to end-user clients.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aimount/aimount-go/internal/httpjson"
)

type Config struct {
	BaseURL     string
	AgentID     string
	AgentAPIKey string
	HTTPClient  *http.Client
}

type Client struct{ config Config }

type IssueUserAccessTokenRequest struct {
	UserID    string
	ProfileID string
}

type UserAccessToken struct {
	AccessToken string
	TokenType   string
	ExpiresAt   time.Time
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

func New(config Config) Client { return Client{config: config} }

func (c Client) IssueUserAccessToken(ctx context.Context, request IssueUserAccessTokenRequest) (UserAccessToken, error) {
	if strings.TrimSpace(c.config.BaseURL) == "" {
		return UserAccessToken{}, errors.New("agent: base url is required")
	}
	if strings.TrimSpace(c.config.AgentID) == "" {
		return UserAccessToken{}, errors.New("agent: agent id is required")
	}
	if strings.TrimSpace(c.config.AgentAPIKey) == "" {
		return UserAccessToken{}, errors.New("agent: agent api key is required")
	}
	if strings.TrimSpace(request.UserID) == "" {
		return UserAccessToken{}, errors.New("agent: user id is required")
	}
	if strings.TrimSpace(request.ProfileID) == "" {
		return UserAccessToken{}, errors.New("agent: profile id is required")
	}

	path := fmt.Sprintf("/agent/v1/agents/%s/server/users/%s/access-tokens", url.PathEscape(c.config.AgentID), url.PathEscape(request.UserID))
	body := struct {
		ProfileID string `json:"profileId"`
	}{request.ProfileID}
	httpClient := c.config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	var wire struct {
		AccessToken string `json:"accessToken"`
		TokenType   string `json:"tokenType"`
		ExpiresAt   string `json:"expiresAt"`
	}
	requestErr, err := httpjson.Do(ctx, httpClient, http.MethodPost, c.config.BaseURL, path, c.config.AgentAPIKey, "", body, &wire)
	if err != nil {
		return UserAccessToken{}, err
	}
	if requestErr != nil {
		return UserAccessToken{}, APIError{Method: http.MethodPost, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	if strings.TrimSpace(wire.AccessToken) == "" {
		return UserAccessToken{}, errors.New("agent: accessToken is required")
	}
	if wire.TokenType != "Bearer" {
		return UserAccessToken{}, errors.New("agent: tokenType must be Bearer")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil {
		return UserAccessToken{}, fmt.Errorf("agent: decode expiresAt: %w", err)
	}
	return UserAccessToken{AccessToken: wire.AccessToken, TokenType: wire.TokenType, ExpiresAt: expiresAt}, nil
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if errors.As(err, &apiErr) {
		return httpjson.RetryableStatus(apiErr.StatusCode)
	}
	var apiErrPointer *APIError
	if errors.As(err, &apiErrPointer) {
		return apiErrPointer != nil && httpjson.RetryableStatus(apiErrPointer.StatusCode)
	}
	return true
}
