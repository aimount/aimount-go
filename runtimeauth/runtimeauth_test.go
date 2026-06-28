package runtimeauth

import (
	"context"
	"encoding/json"
	"errors"
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
		_ = json.NewEncoder(w).Encode(map[string]string{
			"runtimeToken": "runtime.jwt",
			"tokenType":    "Bearer",
			"expiresAt":    expiresAt,
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, AgentID: "agent/1", ServerAPIKey: "awi_tst_secret"})
	token, err := client.IssueUserToken(context.Background(), IssueUserTokenRequest{ProfileID: "profile_1", UserID: "user_1"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if method != http.MethodPost || path != "/agent/v1/agents/agent/1/runtime/tokens" {
		t.Fatalf("unexpected request %s %s", method, path)
	}
	if auth != "Bearer awi_tst_secret" {
		t.Fatalf("unexpected auth header: %q", auth)
	}
	if body["profileId"] != "profile_1" || body["userId"] != "user_1" {
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
		{name: "base url", config: Config{AgentID: "agent", ServerAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "agent id", config: Config{BaseURL: server.URL, ServerAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "server api key", config: Config{BaseURL: server.URL, AgentID: "agent"}, request: IssueUserTokenRequest{ProfileID: "profile", UserID: "user"}},
		{name: "profile id", config: Config{BaseURL: server.URL, AgentID: "agent", ServerAPIKey: "key"}, request: IssueUserTokenRequest{UserID: "user"}},
		{name: "user id", config: Config{BaseURL: server.URL, AgentID: "agent", ServerAPIKey: "key"}, request: IssueUserTokenRequest{ProfileID: "profile"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.config).IssueUserToken(context.Background(), tt.request)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if called {
		t.Fatal("server should not be called for invalid inputs")
	}
}

func TestIssueUserTokenAPIErrorAndRetryability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized.runtime.v2.server_api_key"}`))
	}))
	defer server.Close()

	_, err := New(Config{BaseURL: server.URL, AgentID: "agent", ServerAPIKey: "awi_tst_secret"}).IssueUserToken(context.Background(), IssueUserTokenRequest{ProfileID: "profile", UserID: "user"})
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T %[1]v", err)
	}
	if apiErr.Method != http.MethodPost || apiErr.Path != "/agent/v1/agents/agent/runtime/tokens" || apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "unauthorized.runtime.v2.server_api_key" {
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
}
