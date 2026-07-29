package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIssueUserAccessTokenUsesCanonicalServerRoute(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/agent/v1/agents/agent%2F1/server/users/user%2F1/access-tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.Header.Get("authorization") != "Bearer awi_tst_secret" {
			t.Fatalf("authorization = %q", r.Header.Get("authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accessToken": "access.jwt", "tokenType": "Bearer",
			"expiresAt": "2026-06-18T10:15:00.123456789Z", "future": true,
		})
	}))
	defer server.Close()

	token, err := New(Config{BaseURL: server.URL, AgentID: "agent/1", AgentAPIKey: "awi_tst_secret"}).IssueUserAccessToken(context.Background(), IssueUserAccessTokenRequest{UserID: "user/1", ProfileID: "profile_1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["profileId"] != "profile_1" {
		t.Fatalf("body = %#v", body)
	}
	if token.AccessToken != "access.jwt" || token.TokenType != "Bearer" || token.ExpiresAt.Format(time.RFC3339Nano) != "2026-06-18T10:15:00.123456789Z" {
		t.Fatalf("token = %+v", token)
	}
}

func TestIssueUserAccessTokenPreservesErrorsRetryAndSecretSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized.agent.agent_api_key"}}`))
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "secret-value"}).IssueUserAccessToken(context.Background(), IssueUserAccessTokenRequest{UserID: "user", ProfileID: "profile"})
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if apiErr.Method != http.MethodPost || apiErr.Path != "/agent/v1/agents/agent/server/users/user/access-tokens" || apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "unauthorized.agent.agent_api_key" {
		t.Fatalf("API error = %+v", apiErr)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("error leaked credential: %v", err)
	}
	if IsRetryable(err) || !IsRetryable(APIError{StatusCode: 429}) || !IsRetryable(APIError{StatusCode: 502}) || !IsRetryable(errors.New("network")) || IsRetryable(nil) {
		t.Fatal("retry classification changed")
	}
	if IsRetryable(fmt.Errorf("wrapped: %w", &APIError{StatusCode: http.StatusUnauthorized})) {
		t.Fatal("wrapped 401 should not be retryable")
	}
}

func TestIssueUserAccessTokenValidatesAndParsesResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		req  IssueUserAccessTokenRequest
	}{
		{"base URL", Config{AgentID: "a", AgentAPIKey: "k"}, IssueUserAccessTokenRequest{UserID: "u", ProfileID: "p"}},
		{"agent ID", Config{BaseURL: "https://example.com", AgentAPIKey: "k"}, IssueUserAccessTokenRequest{UserID: "u", ProfileID: "p"}},
		{"API key", Config{BaseURL: "https://example.com", AgentID: "a"}, IssueUserAccessTokenRequest{UserID: "u", ProfileID: "p"}},
		{"user ID", Config{BaseURL: "https://example.com", AgentID: "a", AgentAPIKey: "k"}, IssueUserAccessTokenRequest{ProfileID: "p"}},
		{"profile ID", Config{BaseURL: "https://example.com", AgentID: "a", AgentAPIKey: "k"}, IssueUserAccessTokenRequest{UserID: "u"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg).IssueUserAccessToken(context.Background(), tc.req); err == nil || !strings.HasPrefix(err.Error(), "agent: ") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	for _, response := range []string{
		`{"accessToken":"","tokenType":"Bearer","expiresAt":"2026-06-18T10:15:00Z"}`,
		`{"accessToken":"token","tokenType":"Basic","expiresAt":"2026-06-18T10:15:00Z"}`,
		`{"accessToken":"token","tokenType":"Bearer","expiresAt":"later"}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			if _, err := New(Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}).IssueUserAccessToken(context.Background(), IssueUserAccessTokenRequest{UserID: "user", ProfileID: "profile"}); err == nil {
				t.Fatal("expected malformed response error")
			}
		})
	}
}
