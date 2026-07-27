package runtimeauth

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

func TestIssueUserTokenSendsRequestAndDecodesResponse(t *testing.T) {
	expiresAt := "2026-06-18T10:15:00.000Z"
	var method, path, auth string
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("authorization")
		if ct := r.Header.Get("content-type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("unexpected content-type: %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if r.URL.EscapedPath() == "/agent/v1/agents/agent%2F1/runtime/tokens" {
			t.Fatal("runtimeauth called deprecated runtime token route")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken": "runtime.jwt",
			"tokenType":   "Bearer",
			"expiresAt":   expiresAt,
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, AgentID: "agent/1", AgentAPIKey: "awi_tst_secret"})
	token, err := client.IssueUserToken(context.Background(), IssueUserTokenRequest{ProfileID: "profile_1", UserID: "user_1"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if method != http.MethodPost || path != "/agent/v1/agents/agent/1/service/users/user_1/access-tokens" {
		t.Fatalf("unexpected request %s %s", method, path)
	}
	if auth != "Bearer awi_tst_secret" {
		t.Fatalf("unexpected auth header: %q", auth)
	}
	if len(body) != 1 || body["profileId"] != "profile_1" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if token.RuntimeToken != "runtime.jwt" || token.TokenType != "Bearer" {
		t.Fatalf("unexpected token: %+v", token)
	}
	if token.ExpiresAt.Format(time.RFC3339Nano) != "2026-06-18T10:15:00Z" {
		t.Fatalf("unexpected expiry: %s", token.ExpiresAt.Format(time.RFC3339Nano))
	}
}

func TestIssueUserTokenValidatesRequiredFields(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	tests := []struct {
		name    string
		config  Config
		request IssueUserTokenRequest
	}{
		{name: "base url", config: Config{AgentID: "agent", AgentAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "agent id", config: Config{BaseURL: server.URL, AgentAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "agent api key", config: Config{BaseURL: server.URL, AgentID: "agent"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "profile id", config: Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}, request: IssueUserTokenRequest{UserID: "user"}},
		{name: "user id", config: Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.config).IssueUserToken(context.Background(), tt.request)
			if err == nil || err.Error() != "runtimeauth: "+tt.name+" is required" {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if called {
		t.Fatal("server should not be called for invalid inputs")
	}
}

func TestIssueUserTokenPreservesExpiresAtDecodeErrorPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"token","tokenType":"Bearer","expiresAt":"later"}`))
	}))
	defer server.Close()
	_, err := New(Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}).IssueUserToken(context.Background(), IssueUserTokenRequest{UserID: "user", ProfileID: "profile"})
	if err == nil || !strings.HasPrefix(err.Error(), "runtimeauth: decode expiresAt: ") {
		t.Fatalf("error = %v", err)
	}
	var parseErr *time.ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected wrapped time.ParseError, got %T %[1]v", err)
	}
}

func TestIssueUserTokenAPIErrorAndRetryability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized.agent.agent_api_key"}`))
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "awi_tst_secret"}).IssueUserToken(context.Background(), IssueUserTokenRequest{ProfileID: "profile", UserID: "user"})
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T %[1]v", err)
	}
	if apiErr.Method != http.MethodPost || apiErr.Path != "/agent/v1/agents/agent/service/users/user/access-tokens" || apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "unauthorized.agent.agent_api_key" {
		t.Fatalf("unexpected api error: %+v", apiErr)
	}
	if strings.Contains(apiErr.Error(), "awi_tst_secret") {
		t.Fatalf("error leaked token: %s", apiErr.Error())
	}
	if IsRetryable(apiErr) {
		t.Fatal("401 should not be retryable")
	}
	if !IsRetryable(APIError{StatusCode: http.StatusTooManyRequests}) {
		t.Fatal("429 should be retryable")
	}
	if !IsRetryable(APIError{StatusCode: http.StatusBadGateway}) {
		t.Fatal("5xx should be retryable")
	}
	if !IsRetryable(errors.New("network")) {
		t.Fatal("transport errors should be retryable")
	}
	for _, err := range []error{
		&APIError{StatusCode: http.StatusUnauthorized},
		fmt.Errorf("wrapped: %w", &APIError{StatusCode: http.StatusUnauthorized}),
	} {
		if IsRetryable(err) {
			t.Fatalf("pointer API error should not be retryable: %v", err)
		}
	}
	if !IsRetryable(fmt.Errorf("wrapped: %w", &APIError{StatusCode: http.StatusBadGateway})) {
		t.Fatal("wrapped pointer 5xx should be retryable")
	}
	var typedNil *APIError
	if IsRetryable(typedNil) {
		t.Fatal("typed-nil API error should not be retryable")
	}
}
