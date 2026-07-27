// Package runtimeauth provides deprecated compatibility with serviceauth.
//
// Deprecated: use github.com/aimount/aimount-go/serviceauth.
package runtimeauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aimount/aimount-go/serviceauth"
)

// Config configures Client.
// Deprecated: use serviceauth.Config.
type Config = serviceauth.Config

// Client issues user tokens.
// Deprecated: use serviceauth.Client.
type Client struct {
	config  Config
	service serviceauth.Client
}

// IssueUserTokenRequest identifies the user and execution profile.
// Deprecated: use serviceauth.IssueUserAccessTokenRequest.
type IssueUserTokenRequest struct {
	ProfileID string
	UserID    string
}

// UserToken is the legacy token result.
// Deprecated: use serviceauth.UserAccessToken.
type UserToken struct {
	RuntimeToken string
	TokenType    string
	ExpiresAt    time.Time
}

// APIError describes a failed API request.
// Deprecated: use serviceauth.APIError.
type APIError = serviceauth.APIError

// New creates a compatibility client.
// Deprecated: use serviceauth.New.
func New(config Config) Client {
	return Client{config: config, service: serviceauth.New(serviceauth.Config(config))}
}

func (c Client) validate(request IssueUserTokenRequest) error {
	checks := []struct{ value, message string }{
		{c.config.BaseURL, "runtimeauth: base url is required"},
		{c.config.AgentID, "runtimeauth: agent id is required"},
		{c.config.AgentAPIKey, "runtimeauth: agent api key is required"},
		{request.ProfileID, "runtimeauth: profile id is required"},
		{request.UserID, "runtimeauth: user id is required"},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.value) == "" {
			return errors.New(check.message)
		}
	}
	return nil
}

// IssueUserToken issues a token through canonical Agent Service API.
// Deprecated: use serviceauth.Client.IssueUserAccessToken.
func (c Client) IssueUserToken(ctx context.Context, request IssueUserTokenRequest) (UserToken, error) {
	if err := c.validate(request); err != nil {
		return UserToken{}, err
	}
	token, err := c.service.IssueUserAccessToken(ctx, serviceauth.IssueUserAccessTokenRequest{UserID: request.UserID, ProfileID: request.ProfileID})
	if err != nil {
		if strings.HasPrefix(err.Error(), "serviceauth: decode expiresAt: ") {
			return UserToken{}, fmt.Errorf("runtimeauth: decode expiresAt: %w", errors.Unwrap(err))
		}
		return UserToken{}, err
	}
	return UserToken{RuntimeToken: token.AccessToken, TokenType: token.TokenType, ExpiresAt: token.ExpiresAt}, nil
}

// IsRetryable reports whether an operation may succeed when retried.
// Deprecated: use serviceauth.IsRetryable.
func IsRetryable(err error) bool { return serviceauth.IsRetryable(err) }
