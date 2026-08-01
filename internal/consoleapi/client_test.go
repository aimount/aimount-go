package consoleapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewValidatesConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
		want   string
	}{
		{name: "base URL", config: Config{Token: "secret"}, want: "base URL is required"},
		{name: "invalid base URL", config: Config{BaseURL: "://", Token: "secret"}, want: "invalid base URL"},
		{name: "unsupported scheme", config: Config{BaseURL: "ftp://api.aimount.dev", Token: "secret"}, want: "invalid base URL"},
		{name: "query", config: Config{BaseURL: "https://api.aimount.dev?x=1", Token: "secret"}, want: "invalid base URL"},
		{name: "token", config: Config{BaseURL: "https://api.aimount.dev"}, want: "token is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestClientToleratesUnknownResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["status"] != nil {
			t.Fatalf("body = %#v, err = %v", body, err)
		}
		json.NewEncoder(w).Encode(map[string]any{"slug": "support", "name": "Support", "status": "active", "future": true})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := client.UpsertAgent(context.Background(), "org_1", "support", "Support")
	if err != nil || agent.Slug != "support" {
		t.Fatalf("UpsertAgent() = %#v, %v", agent, err)
	}
}

func TestClientRejectsMissingRequiredResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateAgentInstructionVersion(context.Background(), "org", "agent", "instruction"); err == nil {
		t.Fatal("expected malformed response error")
	}
}

func TestClientReturnsSafeTypedError(t *testing.T) {
	token := "do-not-print"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"type": "unauthorized", "code": "unauthorized.console_api.token", "message": token})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetPrincipal(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unauthorized.console_api.token") || strings.Contains(err.Error(), token) {
		t.Fatalf("error exposed token: %v", err)
	}
}
