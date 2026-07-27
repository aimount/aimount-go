package serviceauth

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
	BaseURL     string
	AgentID     string
	AgentAPIKey string
	HTTPClient  *http.Client
}

type Client struct {
	config Config
}

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
		return UserAccessToken{}, errors.New("serviceauth: base url is required")
	}
	if strings.TrimSpace(c.config.AgentID) == "" {
		return UserAccessToken{}, errors.New("serviceauth: agent id is required")
	}
	if strings.TrimSpace(c.config.AgentAPIKey) == "" {
		return UserAccessToken{}, errors.New("serviceauth: agent api key is required")
	}
	if strings.TrimSpace(request.UserID) == "" {
		return UserAccessToken{}, errors.New("serviceauth: user id is required")
	}
	if strings.TrimSpace(request.ProfileID) == "" {
		return UserAccessToken{}, errors.New("serviceauth: profile id is required")
	}

	path := fmt.Sprintf("/agent/v1/agents/%s/service/users/%s/access-tokens", url.PathEscape(c.config.AgentID), url.PathEscape(request.UserID))
	body, err := json.Marshal(struct {
		ProfileID string `json:"profileId"`
	}{request.ProfileID})
	if err != nil {
		return UserAccessToken{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return UserAccessToken{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+c.config.AgentAPIKey)
	httpClient := c.config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return UserAccessToken{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return UserAccessToken{}, APIError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Code: errorCode(responseBody)}
	}
	var wire struct {
		AccessToken string `json:"accessToken"`
		TokenType   string `json:"tokenType"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return UserAccessToken{}, err
	}
	if strings.TrimSpace(wire.AccessToken) == "" {
		return UserAccessToken{}, errors.New("serviceauth: accessToken is required")
	}
	if wire.TokenType != "Bearer" {
		return UserAccessToken{}, errors.New("serviceauth: tokenType must be Bearer")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	if err != nil {
		return UserAccessToken{}, fmt.Errorf("serviceauth: decode expiresAt: %w", err)
	}
	return UserAccessToken{AccessToken: wire.AccessToken, TokenType: wire.TokenType, ExpiresAt: expiresAt}, nil
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var apiErr APIError
	if errors.As(err, &apiErr) {
		return retryableStatus(apiErr.StatusCode)
	}
	var apiErrPointer *APIError
	if errors.As(err, &apiErrPointer) {
		if apiErrPointer == nil {
			return false
		}
		return retryableStatus(apiErrPointer.StatusCode)
	}
	return true
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func errorCode(body []byte) string {
	var envelope struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if envelope.Error != nil {
		return envelope.Error.Code
	}
	return envelope.Code
}
