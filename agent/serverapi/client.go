// Package serverapi provides backend-only access to the Agent Server API.
// AgentAPIKey credentials must never be exposed to end-user clients.
package serverapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aimount/aimount-go/agent"
	"github.com/aimount/aimount-go/internal/httpjson"
)

type Config struct {
	BaseURL     string
	AgentID     string
	AgentAPIKey string
	HTTPClient  *http.Client
}

type Client struct{ client client }

type IssueClientAPIAccessTokenParams struct {
	UserID    string
	ProfileID string
}

func New(config Config) (*Client, error) {
	c, err := newClient(config)
	if err != nil {
		return nil, err
	}
	return &Client{client: c}, nil
}

func (c *Client) IssueClientAPIAccessToken(ctx context.Context, params IssueClientAPIAccessTokenParams) (agent.AgentUserAccessToken, error) {
	if c == nil {
		return agent.AgentUserAccessToken{}, errors.New("serverapi: client is required")
	}
	if strings.TrimSpace(params.UserID) == "" {
		return agent.AgentUserAccessToken{}, errors.New("serverapi: user id is required")
	}
	if strings.TrimSpace(params.ProfileID) == "" {
		return agent.AgentUserAccessToken{}, errors.New("serverapi: profile id is required")
	}

	path := fmt.Sprintf("/agent/v1/agents/%s/server/users/%s/access-tokens", url.PathEscape(c.client.agentID), url.PathEscape(params.UserID))
	body := struct {
		ProfileID string `json:"profileId"`
	}{params.ProfileID}
	var wire struct {
		AccessToken string
		TokenType   string `json:"tokenType"`
		ExpiresAt   string `json:"expiresAt"`
	}
	requestErr, err := httpjson.Do(ctx, c.client.http, http.MethodPost, c.client.baseURL, path, c.client.token, "", body, &wire)
	if err != nil {
		var decodeErr httpjson.DecodeError
		if errors.As(err, &decodeErr) {
			return agent.AgentUserAccessToken{}, protocolError{fmt.Sprintf("serverapi: malformed access token response: %v", err)}
		}
		return agent.AgentUserAccessToken{}, err
	}
	if requestErr != nil {
		return agent.AgentUserAccessToken{}, Error{Method: http.MethodPost, Path: path, StatusCode: requestErr.StatusCode, Code: requestErr.Code}
	}
	if strings.TrimSpace(wire.AccessToken) == "" {
		return agent.AgentUserAccessToken{}, protocolError{"serverapi: malformed access token response"}
	}
	if wire.TokenType != "Bearer" {
		return agent.AgentUserAccessToken{}, protocolError{"serverapi: malformed access token response"}
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil {
		return agent.AgentUserAccessToken{}, protocolError{fmt.Sprintf("serverapi: malformed access token response: %v", err)}
	}
	return agent.AgentUserAccessToken{AccessToken: wire.AccessToken, TokenType: wire.TokenType, ExpiresAt: expiresAt}, nil
}

type Error struct {
	Method     string
	Path       string
	StatusCode int
	Code       string
}

func asError(err error) (Error, bool) {
	var value Error
	if errors.As(err, &value) {
		return value, true
	}
	var pointer *Error
	if errors.As(err, &pointer) && pointer != nil {
		return *pointer, true
	}
	return Error{}, false
}

func (e Error) Error() string {
	if e.Code != "" {
		return e.Method + " " + e.Path + " failed: " + e.Code
	}
	return e.Method + " " + e.Path + " failed"
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errMalformedProtocol) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if apiErr, ok := asError(err); ok {
		return httpjson.RetryableStatus(apiErr.StatusCode)
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func ErrorCode(err error) string {
	if apiErr, ok := asError(err); ok {
		return apiErr.Code
	}
	return ""
}

func HTTPStatus(err error) int {
	if apiErr, ok := asError(err); ok {
		return apiErr.StatusCode
	}
	return 0
}
