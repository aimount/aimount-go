package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateSessionUsesCanonicalRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/agent/v1/agents/agent%2F1/server/users/user%2F1/sessions" || r.Header.Get("authorization") != "Bearer secret" || r.Header.Get("idempotency-key") != "create_1" {
			t.Fatalf("request = %s %s %#v", r.Method, r.URL.EscapedPath(), r.Header)
		}
		var body struct {
			Title       string            `json:"title"`
			Labels      map[string]string `json:"labels"`
			Instruction string            `json:"instruction"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title != "Required" || body.Labels["client"] != "acme" || body.Instruction != "private" {
			t.Fatalf("body = %#v, error = %v", body, err)
		}
		_, _ = w.Write([]byte(createSessionResponse("session_1")))
	}))
	defer server.Close()

	result, err := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent/1", AgentAPIKey: "secret"}).CreateSession(context.Background(), CreateSessionParams{UserID: "user/1", IdempotencyKey: "create_1", Title: stringPtr("Required"), Labels: map[string]string{"client": "acme"}, Instruction: stringPtr("private")})
	if err != nil || result.SessionID != "session_1" || result.CommandID != "cmd_1" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestArchiveAndRestoreUseCanonicalRoutes(t *testing.T) {
	for _, transition := range []string{"archive", "restore"} {
		t.Run(transition, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/agent/v1/agents/agent%2F1/server/users/user%2F1/sessions/session%2F1/"+transition || r.Header.Get("idempotency-key") != transition+"_1" {
					t.Fatalf("request = %s %s %#v", r.Method, r.URL.EscapedPath(), r.Header)
				}
				_, _ = w.Write([]byte(`{"commandId":"cmd_1","status":"accepted"}`))
			}))
			defer server.Close()
			client := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent/1", AgentAPIKey: "secret"})
			if transition == "archive" {
				result, err := client.ArchiveSession(context.Background(), ArchiveSessionParams{UserID: "user/1", SessionID: "session/1", IdempotencyKey: transition + "_1"})
				if err != nil || result.CommandID != "cmd_1" {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			} else {
				result, err := client.RestoreSession(context.Background(), RestoreSessionParams{UserID: "user/1", SessionID: "session/1", IdempotencyKey: transition + "_1"})
				if err != nil || result.CommandID != "cmd_1" {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			}
		})
	}
}

func TestDeleteSessionUsesCanonicalRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/agent/v1/agents/agent%2F1/server/users/user%2F1/sessions/session%2F1" || r.Header.Get("authorization") != "Bearer secret" || r.Header.Get("idempotency-key") != "" {
			t.Fatalf("request = %s %s %#v", r.Method, r.URL.EscapedPath(), r.Header)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent/1", AgentAPIKey: "secret"}).DeleteSession(context.Background(), DeleteSessionParams{UserID: "user/1", SessionID: "session/1"}); err != nil {
		t.Fatal(err)
	}
}

func TestServerSessionOperationsRejectMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  string
		operation func(*Client) error
	}{
		{"invalid create JSON", `{`, func(client *Client) error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "create"})
			return err
		}},
		{"inconsistent create session", strings.Replace(createSessionResponse("session_1"), `"sessionId":"session_1","commandId"`, `"sessionId":"different_session","commandId"`, 1), func(client *Client) error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "create"})
			return err
		}},
		{"empty command ID", `{"commandId":"","status":"accepted"}`, func(client *Client) error {
			_, err := client.ArchiveSession(context.Background(), ArchiveSessionParams{UserID: "user", SessionID: "session", IdempotencyKey: "archive"})
			return err
		}},
		{"invalid command status", `{"commandId":"cmd","status":"done"}`, func(client *Client) error {
			_, err := client.ArchiveSession(context.Background(), ArchiveSessionParams{UserID: "user", SessionID: "session", IdempotencyKey: "archive"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }))
			defer server.Close()
			client := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"})
			err := tc.operation(client)
			if !errors.Is(err, errMalformedProtocol) || IsRetryable(err) {
				t.Fatalf("error = %T %v", err, err)
			}
		})
	}
}

func TestServerSessionOperationsPreserveStructuredErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"conflict.agent.session_managed"}`))
	}))
	defer server.Close()

	_, err := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}).ArchiveSession(context.Background(), ArchiveSessionParams{UserID: "user", SessionID: "session", IdempotencyKey: "archive"})
	if ErrorCode(err) != "conflict.agent.session_managed" || HTTPStatus(err) != http.StatusConflict {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestServerSessionOperationsValidateRequiredFields(t *testing.T) {
	client := mustClient(t, Config{BaseURL: "https://example.test", AgentID: "agent", AgentAPIKey: "key"})
	if _, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user"}); !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Fatalf("create error = %v", err)
	}
	if _, err := client.ArchiveSession(context.Background(), ArchiveSessionParams{UserID: "user", IdempotencyKey: "archive"}); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("archive error = %v", err)
	}
	if _, err := client.RestoreSession(context.Background(), RestoreSessionParams{UserID: "user", IdempotencyKey: "restore"}); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("restore error = %v", err)
	}
	if err := client.DeleteSession(context.Background(), DeleteSessionParams{UserID: "user"}); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("delete error = %v", err)
	}
}

func TestCreateSessionOmitsNilOptionalStringsAndSendsEmptyStrings(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if _, ok := body["title"]; ok {
				t.Fatalf("title was sent: %#v", body)
			}
			if _, ok := body["instruction"]; ok {
				t.Fatalf("instruction was sent: %#v", body)
			}
		} else if body["title"] != "" || body["instruction"] != "" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(createSessionResponse("session_1")))
	}))
	defer server.Close()

	client := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"})
	if _, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "nil"}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if _, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "empty", Title: &empty, Instruction: &empty}); err != nil {
		t.Fatal(err)
	}
}

func TestServerSessionOperationsMatchCloudValidation(t *testing.T) {
	labels := make(map[string]string, 17)
	for i := 0; i < 17; i++ {
		labels[string(rune('a'+i))] = "value"
	}
	client := mustClient(t, Config{BaseURL: "https://example.test", AgentID: "agent", AgentAPIKey: "key"})
	tests := []struct {
		name string
		want error
		run  func() error
	}{
		{"empty user ID", ErrInvalidUserID, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{IdempotencyKey: "key"})
			return err
		}},
		{"long user ID", ErrInvalidUserID, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: strings.Repeat("u", 257), IdempotencyKey: "key"})
			return err
		}},
		{"long session ID", ErrInvalidSessionID, func() error {
			return client.DeleteSession(context.Background(), DeleteSessionParams{UserID: "user", SessionID: strings.Repeat("s", 257)})
		}},
		{"invalid idempotency key", ErrInvalidIdempotencyKey, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "bad key"})
			return err
		}},
		{"long idempotency key", ErrInvalidIdempotencyKey, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: strings.Repeat("k", 257)})
			return err
		}},
		{"long title", ErrInvalidSessionTitle, func() error {
			title := strings.Repeat("t", 4097)
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Title: &title})
			return err
		}},
		{"long instruction", ErrInvalidSessionInstruction, func() error {
			instruction := strings.Repeat("i", 32001)
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Instruction: &instruction})
			return err
		}},
		{"too many labels", ErrInvalidSessionLabels, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Labels: labels})
			return err
		}},
		{"empty label key", ErrInvalidSessionLabels, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Labels: map[string]string{"": "value"}})
			return err
		}},
		{"long label key", ErrInvalidSessionLabels, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Labels: map[string]string{strings.Repeat("k", 65): "value"}})
			return err
		}},
		{"long label value", ErrInvalidSessionLabels, func() error {
			_, err := client.CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Labels: map[string]string{"key": strings.Repeat("v", 257)}})
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCreateSessionUsesJavaScriptStringLengths(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(createSessionResponse("session_1")))
	}))
	defer server.Close()

	title := strings.Repeat("é", 4096)
	_, err := mustClient(t, Config{BaseURL: server.URL, AgentID: "agent", AgentAPIKey: "key"}).CreateSession(context.Background(), CreateSessionParams{UserID: "user", IdempotencyKey: "key", Title: &title})
	if err != nil {
		t.Fatalf("valid Unicode title rejected: %v", err)
	}
}

func createSessionResponse(executionSessionID string) string {
	return `{"session":{"id":"session_1","agentId":"agent_1","userId":"user_1","title":null,"labels":{},"managedBy":"server","status":"active","archivedAt":null,"createdAt":"2026-07-20T12:00:00Z","updatedAt":"2026-07-20T12:00:00Z"},"execution":{"sessionId":"` + executionSessionID + `","executionToken":"exec_1","status":"idle","createdAt":"2026-07-20T12:00:00Z","updatedAt":"2026-07-20T12:00:00Z"},"lastEventId":"event_1","capabilities":{"sendText":{"available":true},"stop":{"available":false,"reason":"execution_not_active"}},"items":{"entries":[],"continuityToken":"continuity_1","olderCursor":null,"newerCursor":null,"hasOlder":false,"hasNewer":false},"pendingInput":{"entries":[]},"ack":{"sessionId":"session_1","commandId":"cmd_1","status":"accepted"},"future":{"enabled":true}}`
}

func stringPtr(value string) *string { return &value }
