package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func memoryClient(t *testing.T, config Config) *Client {
	t.Helper()
	return mustClient(t, config)
}

func memoryBlock() AgentMemoryBlock {
	return AgentMemoryBlock{
		ID: "opaque/1", Text: " \n**Exact** \u00e9\n ", Version: 3,
		ManagedBy: MemoryActorAgent, CreatedBy: MemoryActorService, UpdatedBy: MemoryActorUser,
		CreatedAt: time.Date(2026, 9, 10, 10, 0, 0, 123456789, time.UTC),
		UpdatedAt: time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC),
	}
}

func TestMemoryWireContract(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			block := memoryBlock()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				path := "/prefix/agent/v1/agents/agent%2F1/server/users/user%2F1/memory"
				if method == http.MethodPatch || method == http.MethodDelete {
					path += "/opaque%2F1"
				}
				if r.Method != method || r.URL.EscapedPath() != path || r.Header.Get("Authorization") != "Bearer secret" {
					t.Errorf("request = %s %s %v", r.Method, r.URL.EscapedPath(), r.Header)
				}
				body, _ := io.ReadAll(r.Body)
				if method == http.MethodGet || method == http.MethodDelete {
					if len(body) != 0 || r.Header.Get("Content-Type") != "" {
						t.Errorf("bodyless request = %q %v", body, r.Header)
					}
				} else {
					var fields map[string]string
					if err := json.Unmarshal(body, &fields); err != nil || !reflect.DeepEqual(fields, map[string]string{"text": block.Text}) {
						t.Errorf("body = %s, error = %v", body, err)
					}
					if r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("content type = %q", r.Header.Get("Content-Type"))
					}
				}
				if method == http.MethodPatch || method == http.MethodDelete {
					if r.Header.Get("If-Match") != `"3"` || r.Header.Get("Idempotency-Key") != "" {
						t.Errorf("conditional headers = %v", r.Header)
					}
				} else if r.Header.Get("If-Match") != "" {
					t.Errorf("unexpected If-Match = %v", r.Header)
				}
				if method == http.MethodPost && r.Header.Get("Idempotency-Key") != "explicit.key:1" {
					t.Errorf("create key = %q", r.Header.Get("Idempotency-Key"))
				}
				switch method {
				case http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{"blocks": []AgentMemoryBlock{block}, "future": true})
				case http.MethodPost:
					block.ManagedBy = MemoryActorService
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"operation": "create", "block": block, "future": true})
				case http.MethodPatch:
					_ = json.NewEncoder(w).Encode(map[string]any{"operation": "update", "block": block})
				case http.MethodDelete:
					_ = json.NewEncoder(w).Encode(map[string]any{"operation": "delete", "id": block.ID, "version": block.Version, "future": true})
				}
			}))
			defer server.Close()
			c := memoryClient(t, Config{BaseURL: server.URL + "/prefix/", AgentID: "agent/1", AgentAPIKey: "secret"})
			ctx := context.Background()
			switch method {
			case http.MethodGet:
				result, err := c.ListMemory(ctx, ListMemoryParams{UserID: "user/1"})
				if err != nil || !reflect.DeepEqual(result.Blocks, []AgentMemoryBlock{block}) {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			case http.MethodPost:
				result, err := c.CreateMemory(ctx, CreateMemoryParams{UserID: "user/1", Text: block.Text, IdempotencyKey: "explicit.key:1"})
				if err != nil || result.Operation != "create" || result.IdempotencyKey != "explicit.key:1" || !reflect.DeepEqual(result.Block, block) {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			case http.MethodPatch:
				result, err := c.UpdateMemory(ctx, UpdateMemoryParams{UserID: "user/1", MemoryID: block.ID, Version: 3, Text: block.Text})
				if err != nil || result.Operation != "update" || !reflect.DeepEqual(result.Block, block) {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			case http.MethodDelete:
				result, err := c.DeleteMemory(ctx, DeleteMemoryParams{UserID: "user/1", MemoryID: block.ID, Version: 3})
				if err != nil || result.Operation != "delete" || result.ID != block.ID || result.Version != 3 {
					t.Fatalf("result = %+v, error = %v", result, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
		})
	}
}

func TestMemoryCreateManagementAndTextBoundaries(t *testing.T) {
	for _, manager := range []MemoryActor{MemoryActorUser, MemoryActorAgent, MemoryActorService} {
		t.Run(string(manager), func(t *testing.T) {
			text := strings.Repeat("\u00e9", 1024)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 2 || body["managedBy"] != string(manager) || body["text"] != text || !validIdempotencyKey(r.Header.Get("Idempotency-Key")) {
					t.Errorf("body = %v, headers = %v", body, r.Header)
				}
				block := memoryBlock()
				block.Text, block.ManagedBy, block.CreatedBy, block.UpdatedBy, block.Version = text, manager, MemoryActorService, MemoryActorService, 1
				_ = json.NewEncoder(w).Encode(CreateMemoryResult{Operation: "create", Block: block})
			}))
			defer server.Close()
			result, err := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"}).CreateMemory(context.Background(), CreateMemoryParams{UserID: "u", Text: text, ManagedBy: manager})
			if err != nil || result.Block.ManagedBy != manager || result.Block.CreatedBy != MemoryActorService || result.Block.Version != 1 || !validIdempotencyKey(result.IdempotencyKey) {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
		})
	}
}

func TestMemoryECMAScriptWhitespace(t *testing.T) {
	// ECMAScript WhiteSpace and LineTerminator, as used by Cloud's String.trim().
	for _, r := range []rune{
		0x0009, 0x000a, 0x000b, 0x000c, 0x000d, 0x0020, 0x00a0, 0x1680,
		0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007,
		0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff,
	} {
		t.Run(fmt.Sprintf("blank U+%04X", r), func(t *testing.T) {
			if validMemoryText(string(r)) {
				t.Fatalf("U+%04X must be blank under ECMAScript trim", r)
			}
		})
	}
	for _, r := range []rune{0x0085, 0x180e, 0x200b} {
		t.Run(fmt.Sprintf("nonblank U+%04X", r), func(t *testing.T) {
			if !validMemoryText(string(r)) {
				t.Fatalf("U+%04X must be nonblank under ECMAScript trim", r)
			}
		})
	}
}

func TestMemoryUnicodeWhitespaceRequests(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"NEL", "\u0085", true},
		{"BOM", "\ufeff", false},
		{"surrounded NEL", "\ufeff \n\u0085\t\ufeff", true},
	} {
		for _, method := range []string{"POST", "PATCH"} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["text"] != tc.text {
						t.Errorf("text changed: %q, error = %v", body["text"], err)
					}
					encoded, _ := json.Marshal(memoryBlock())
					_, _ = io.WriteString(w, memoryResponse(method, string(encoded)))
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				err := callMemory(c, context.Background(), method, "u", "m", 3, tc.text)
				if tc.valid {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("Cloud-valid text rejected: %v, calls = %d", err, calls.Load())
					}
				} else if !errors.Is(err, ErrInvalidMemoryText) || IsRetryable(err) || calls.Load() != 0 {
					t.Fatalf("blank text must fail locally: %v, calls = %d", err, calls.Load())
				}
			})
		}
	}
}

func TestMemoryUnicodeWhitespaceResponses(t *testing.T) {
	for _, text := range []string{"\u0085", "\ufeff", "\ufeff \n\u0085\t\ufeff"} {
		for _, method := range []string{"GET", "POST", "PATCH"} {
			t.Run(fmt.Sprintf("%s %q", method, text), func(t *testing.T) {
				block := memoryBlock()
				block.Text = text
				encoded, _ := json.Marshal(block)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, memoryResponse(method, string(encoded)))
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				var err error
				if method == "GET" {
					var result ListMemoryResult
					result, err = c.ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
					if err == nil && (len(result.Blocks) != 1 || result.Blocks[0].Text != text) {
						t.Fatalf("response text changed: %+v", result)
					}
				} else {
					err = callMemory(c, context.Background(), method, "u", "m", 3, "valid request")
				}
				if text != "\ufeff" {
					if err != nil {
						t.Fatalf("Cloud-valid response rejected: %v", err)
					}
				} else {
					var apiErr Error
					if !errors.Is(err, errMalformedProtocol) || !errors.As(err, &apiErr) || apiErr.OutcomeUnknown != (method != "GET") || IsRetryable(err) {
						t.Fatalf("blank response error = %v", err)
					}
				}
			})
		}
	}
}

func TestMemoryValidationBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
	ctx := context.Background()
	for _, user := range []string{"", strings.Repeat("u", 257)} {
		for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
			t.Run(method+" user "+fmt.Sprint(len(user)), func(t *testing.T) {
				err := callMemory(c, ctx, method, user, "m", 1, "text")
				if !errors.Is(err, ErrInvalidUserID) || IsRetryable(err) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
	for _, text := range []string{"", " \t\r\n\u00a0", strings.Repeat("a", 2049), strings.Repeat("\u00e9", 1025), string([]byte{0xff})} {
		for _, method := range []string{"POST", "PATCH"} {
			t.Run(method+" text "+fmt.Sprint(len(text)), func(t *testing.T) {
				if err := callMemory(c, ctx, method, "u", "m", 1, text); !errors.Is(err, ErrInvalidMemoryText) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, version := range []int64{0, -1, MaxMemoryVersion + 1} {
			t.Run(fmt.Sprintf("%s version %d", method, version), func(t *testing.T) {
				if err := callMemory(c, ctx, method, "u", "m", version, "text"); !errors.Is(err, ErrInvalidMemoryVersion) {
					t.Fatalf("error = %v", err)
				}
			})
		}
		for _, id := range []string{"", strings.Repeat("m", 257)} {
			t.Run(method+" id "+fmt.Sprint(len(id)), func(t *testing.T) {
				if err := callMemory(c, ctx, method, "u", id, 1, "text"); !errors.Is(err, ErrInvalidMemoryID) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
	for _, key := range []string{"bad key", "bad\r\nkey", strings.Repeat("k", 257)} {
		t.Run("key "+fmt.Sprint(len(key)), func(t *testing.T) {
			_, err := c.CreateMemory(ctx, CreateMemoryParams{UserID: "u", Text: "text", IdempotencyKey: key})
			if !errors.Is(err, ErrInvalidIdempotencyKey) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("manager", func(t *testing.T) {
		_, err := c.CreateMemory(ctx, CreateMemoryParams{UserID: "u", Text: "text", ManagedBy: "server"})
		if !errors.Is(err, ErrInvalidMemoryManagedBy) {
			t.Fatalf("error = %v", err)
		}
	})
	var nilClient *Client
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		t.Run(method+" nil client", func(t *testing.T) {
			if err := callMemory(nilClient, ctx, method, "u", "m", 1, "text"); err == nil || IsRetryable(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs made %d requests", calls.Load())
	}
}

func TestMemoryRepresentableTextRequests(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"NUL", "\x00", false},
		{"embedded NUL", "before\x00after", false},
		{"invalid UTF8", "before\xffafter", false},
		{"UTF8 encoded surrogate", "\xed\xa0\x80", false},
		{"replacement character", "\ufffd", true},
		{"replacement byte limit", strings.Repeat("\ufffd", 682) + "ab", true},
		{"replacement over byte limit", strings.Repeat("\ufffd", 683), false},
		{"four byte limit", strings.Repeat("\U0001f600", 512), true},
		{"four byte over limit", strings.Repeat("\U0001f600", 512) + "a", false},
	} {
		for _, method := range []string{"POST", "PATCH"} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["text"] != tc.text {
						t.Errorf("text changed: %q, error = %v", body["text"], err)
					}
					encoded, _ := json.Marshal(memoryBlock())
					_, _ = io.WriteString(w, memoryResponse(method, string(encoded)))
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				err := callMemory(c, context.Background(), method, "u", "m", 3, tc.text)
				if tc.valid {
					if err != nil || calls.Load() != 1 {
						t.Fatalf("valid text rejected: %v, calls = %d", err, calls.Load())
					}
				} else if !errors.Is(err, ErrInvalidMemoryText) || IsRetryable(err) || calls.Load() != 0 {
					t.Fatalf("invalid text must fail locally: %v, calls = %d", err, calls.Load())
				}
			})
		}
	}
}

func TestMemoryRepresentableTextResponses(t *testing.T) {
	quote := func(text string) string {
		encoded, _ := json.Marshal(text)
		return string(encoded)
	}
	for _, tc := range []struct {
		name, wire, want string
		valid            bool
	}{
		{"NUL", `"\u0000"`, "", false},
		{"embedded NUL", `"before\u0000after"`, "", false},
		{"invalid UTF8", "\"before\xffafter\"", "", false},
		{"UTF8 encoded surrogate", "\"\xed\xa0\x80\"", "", false},
		{"replacement character", `"\ufffd"`, "\ufffd", true},
		{"replacement byte limit", quote(strings.Repeat("\ufffd", 682) + "ab"), strings.Repeat("\ufffd", 682) + "ab", true},
		{"replacement over byte limit", quote(strings.Repeat("\ufffd", 683)), "", false},
		{"four byte limit", quote(strings.Repeat("\U0001f600", 512)), strings.Repeat("\U0001f600", 512), true},
		{"four byte over limit", quote(strings.Repeat("\U0001f600", 512) + "a"), "", false},
		// Keep encoding/json's established handling of escaped UTF-16, not a custom decoder.
		{"lone high surrogate", `"\ud800"`, "\ufffd", true},
		{"lone low surrogate", `"\udc00"`, "\ufffd", true},
		{"surrogate pair", `"\ud83d\ude00"`, "\U0001f600", true},
		{"literal surrogate escape", `"\\ud800"`, `\ud800`, true},
	} {
		for _, method := range []string{"GET", "POST", "PATCH"} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				block := memoryBlock()
				block.Text = "TEXT_PLACEHOLDER"
				encoded, _ := json.Marshal(block)
				body := memoryResponse(method, strings.Replace(string(encoded), `"TEXT_PLACEHOLDER"`, tc.wire, 1))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				var text string
				var err error
				switch method {
				case "GET":
					var result ListMemoryResult
					result, err = c.ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
					if err == nil {
						if len(result.Blocks) != 1 {
							t.Fatalf("blocks = %+v", result.Blocks)
						}
						text = result.Blocks[0].Text
					}
				case "POST":
					var result CreateMemoryResult
					result, err = c.CreateMemory(context.Background(), CreateMemoryParams{UserID: "u", Text: "valid request"})
					text = result.Block.Text
				case "PATCH":
					var result UpdateMemoryResult
					result, err = c.UpdateMemory(context.Background(), UpdateMemoryParams{UserID: "u", MemoryID: "m", Version: 3, Text: "valid request"})
					text = result.Block.Text
				}
				if tc.valid {
					if err != nil || text != tc.want {
						t.Fatalf("text = %q, want %q, error = %v", text, tc.want, err)
					}
				} else {
					var apiErr Error
					if !errors.Is(err, errMalformedProtocol) || !errors.As(err, &apiErr) || apiErr.OutcomeUnknown != (method != "GET") || IsRetryable(err) {
						t.Fatalf("invalid response error = %v", err)
					}
				}
			})
		}
	}
}

func callMemory(c *Client, ctx context.Context, method, user, id string, version int64, text string) error {
	switch method {
	case http.MethodGet:
		_, err := c.ListMemory(ctx, ListMemoryParams{UserID: user})
		return err
	case http.MethodPost:
		_, err := c.CreateMemory(ctx, CreateMemoryParams{UserID: user, Text: text})
		return err
	case http.MethodPatch:
		_, err := c.UpdateMemory(ctx, UpdateMemoryParams{UserID: user, MemoryID: id, Version: version, Text: text})
		return err
	default:
		_, err := c.DeleteMemory(ctx, DeleteMemoryParams{UserID: user, MemoryID: id, Version: version})
		return err
	}
}

func TestMemoryResponseValidation(t *testing.T) {
	blockJSON, _ := json.Marshal(memoryBlock())
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		cases := map[string]string{"invalid JSON": "{", "null": "null", "missing": "{}", "wrong type": "[]"}
		if method == "GET" {
			cases["null blocks"] = `{"blocks":null}`
			cases["null block"] = `{"blocks":[null]}`
			cases["trailing JSON"] = `{"blocks":[]} {}`
		} else if method == "DELETE" {
			cases["wrong operation"] = `{"operation":"update","id":"m","version":3}`
			cases["missing id"] = `{"operation":"delete","version":3}`
			for _, version := range []string{"null", "0", "-1", "1.5", `"3"`, "9007199254740992"} {
				cases["version "+version] = `{"operation":"delete","id":"m","version":` + version + `}`
			}
		} else {
			operation := "create"
			if method == "PATCH" {
				operation = "update"
			}
			cases["wrong operation"] = `{"operation":"delete","block":` + string(blockJSON) + `}`
			cases["missing block"] = `{"operation":"` + operation + `"}`
			cases["null block"] = `{"operation":"` + operation + `","block":null}`
		}
		if method != "DELETE" {
			for _, field := range []string{"id", "text", "version", "managedBy", "createdBy", "updatedBy", "createdAt", "updatedAt"} {
				for _, value := range []string{"missing", "null"} {
					var block map[string]json.RawMessage
					_ = json.Unmarshal(blockJSON, &block)
					if value == "missing" {
						delete(block, field)
					} else {
						block[field] = json.RawMessage("null")
					}
					encoded, _ := json.Marshal(block)
					cases[field+" "+value] = memoryResponse(method, string(encoded))
				}
			}
			for _, tc := range []struct{ field, value string }{
				{"version", "0"}, {"version", "-1"}, {"version", "1.5"}, {"version", `"3"`}, {"version", "9007199254740992"},
				{"managedBy", `"server"`}, {"createdBy", `"other"`}, {"updatedBy", `"other"`},
				{"id", `""`}, {"text", `"  "`}, {"text", "42"}, {"createdAt", `"later"`}, {"updatedAt", `"2026-13-01T00:00:00Z"`},
			} {
				var block map[string]json.RawMessage
				_ = json.Unmarshal(blockJSON, &block)
				block[tc.field] = json.RawMessage(tc.value)
				encoded, _ := json.Marshal(block)
				cases[tc.field+" "+tc.value] = memoryResponse(method, string(encoded))
			}
		}
		for name, body := range cases {
			t.Run(method+" "+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				err := callMemory(c, context.Background(), method, "u", "m", 3, "text")
				if !errors.Is(err, errMalformedProtocol) || IsRetryable(err) {
					t.Fatalf("error = %T %v", err, err)
				}
				var apiErr Error
				if !errors.As(err, &apiErr) || apiErr.OutcomeUnknown != (method != "GET") {
					t.Fatalf("outcome = %+v", apiErr)
				}
			})
		}
	}
}

func memoryResponse(method, block string) string {
	if method == "GET" {
		return `{"blocks":[` + block + `],"future":true}`
	}
	operation := "create"
	if method == "PATCH" {
		operation = "update"
	}
	return `{"operation":"` + operation + `","block":` + block + `,"future":true}`
}

func TestMemoryTolerantResponsesAndMaxVersion(t *testing.T) {
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			block := memoryBlock()
			block.Version = MaxMemoryVersion
			encoded, _ := json.Marshal(block)
			body := memoryResponse(method, strings.TrimSuffix(string(encoded), "}")+`,"unknown":{"future":true}}`)
			if method == "DELETE" {
				body = `{"operation":"delete","id":"opaque/1","version":9007199254740991,"future":true}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (method == "PATCH" || method == "DELETE") && r.Header.Get("If-Match") != `"9007199254740991"` {
					t.Errorf("version header = %q", r.Header.Get("If-Match"))
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
			if err := callMemory(c, context.Background(), method, "u", "opaque/1", MaxMemoryVersion, block.Text); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("empty collection", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"blocks":[],"future":true}`) }))
		defer server.Close()
		result, err := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"}).ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
		if err != nil || result.Blocks == nil || len(result.Blocks) != 0 {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})
}

func TestMemoryCloudErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{428, "invalid.agent.memory.version_required"}, {400, "invalid.agent.memory.request"},
		{412, "conflict.agent.memory.version"}, {404, "not_found.agent.memory"}, {403, "forbidden.agent.memory.managed"},
		{409, "conflict.agent.memory.limit"}, {409, "conflict.agent.idempotency_key"}, {401, "unauthorized.agent.agent_api_key.invalid"},
	} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s nested=%t", tc.code, nested), func(t *testing.T) {
				block := memoryBlock()
				// JSON escaping can make a valid 2-KiB block exceed the legacy 4-KiB error cap.
				block.Text = strings.Repeat("<", 2048)
				details, _ := json.Marshal(map[string]any{"currentBlock": block, "limit": "totalTextBytes", "bound": 16384, "current": 16000, "requested": 17000, "future": true})
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(tc.status)
					var body any = map[string]any{"type": strings.Split(tc.code, ".")[0], "code": tc.code, "message": "Memory request rejected", "details": json.RawMessage(details), "future": true}
					if nested {
						body = map[string]any{"error": body}
					}
					_ = json.NewEncoder(w).Encode(body)
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "secret"})
				err := callMemory(c, context.Background(), "PATCH", "u", "m", 3, "text")
				var apiErr Error
				if !errors.As(err, &apiErr) || apiErr.Type != strings.Split(tc.code, ".")[0] || apiErr.Message != "Memory request rejected" || string(apiErr.Details) != string(details) || apiErr.OutcomeUnknown {
					t.Fatalf("error = %+v", apiErr)
				}
				if HTTPStatus(fmt.Errorf("wrapped: %w", err)) != tc.status || ErrorCode(err) != tc.code || IsRetryable(err) || calls.Load() != 1 || strings.Contains(err.Error(), "secret") {
					t.Fatalf("error = %v, calls = %d", err, calls.Load())
				}
				var conflict struct {
					CurrentBlock AgentMemoryBlock `json:"currentBlock"`
				}
				if err := json.Unmarshal(apiErr.Details, &conflict); err != nil || !reflect.DeepEqual(conflict.CurrentBlock, block) {
					t.Fatalf("details lost block: %v", err)
				}
			})
		}
	}
}

func TestMemoryCreateRetainsKeyAfterLostResponse(t *testing.T) {
	for _, explicit := range []string{"", "application-create-1"} {
		t.Run("explicit="+explicit, func(t *testing.T) {
			var calls atomic.Int32
			var keys []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Idempotency-Key"))
				if calls.Add(1) == 1 {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				_ = json.NewEncoder(w).Encode(CreateMemoryResult{Operation: "create", Block: memoryBlock()})
			}))
			defer server.Close()
			c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
			params := CreateMemoryParams{UserID: "u", Text: "text", IdempotencyKey: explicit}
			result, err := c.CreateMemory(context.Background(), params)
			var apiErr Error
			if !errors.As(err, &apiErr) || !apiErr.OutcomeUnknown || !validIdempotencyKey(apiErr.IdempotencyKey) || apiErr.IdempotencyKey != result.IdempotencyKey || calls.Load() != 1 {
				t.Fatalf("result = %+v, error = %+v, calls = %d", result, apiErr, calls.Load())
			}
			params.IdempotencyKey = apiErr.IdempotencyKey
			result, err = c.CreateMemory(context.Background(), params)
			if err != nil || result.IdempotencyKey != params.IdempotencyKey || calls.Load() != 2 || keys[0] != keys[1] || (explicit != "" && keys[0] != explicit) {
				t.Fatalf("result = %+v, error = %v, keys = %v", result, err, keys)
			}
			// A different logical create must not reuse the prior generated key.
			if explicit == "" {
				params.IdempotencyKey = ""
				result, err = c.CreateMemory(context.Background(), params)
				if err != nil || result.IdempotencyKey == keys[0] {
					t.Fatalf("new create reused key: %+v, %v", result, err)
				}
			}
		})
	}
}

type memoryRoundTripper func(*http.Request) (*http.Response, error)

func (f memoryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMemoryUnknownOutcomesAndReconciliation(t *testing.T) {
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, failure := range []string{"network", "canceled", "deadline", "truncated", "502", "408", "429", "403"} {
			t.Run(method+" "+failure, func(t *testing.T) {
				var calls atomic.Int32
				cause := error(net.UnknownNetworkError("lost response"))
				if failure == "canceled" {
					cause = context.Canceled
				}
				if failure == "deadline" {
					cause = context.DeadlineExceeded
				}
				transport := memoryRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					if r.Method == "GET" {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"blocks":[]}`))}, nil
					}
					switch failure {
					case "network", "canceled", "deadline":
						return nil, cause
					case "truncated":
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"operation":`))}, nil
					default:
						var status int
						_, _ = fmt.Sscan(failure, &status)
						return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"type":"error","code":"test.error","message":"request failed","details":{}}`))}, nil
					}
				})
				c := memoryClient(t, Config{BaseURL: "https://example.test", AgentID: "a", AgentAPIKey: "k", HTTPClient: &http.Client{Transport: transport}})
				err := callMemory(c, context.Background(), method, "u", "m", 3, "text")
				var apiErr Error
				unknown := failure != "429" && failure != "403"
				if !errors.As(err, &apiErr) || apiErr.OutcomeUnknown != unknown || IsRetryable(err) != (failure == "429") || calls.Load() != 1 {
					t.Fatalf("error = %+v, retryable = %t, calls = %d", apiErr, IsRetryable(err), calls.Load())
				}
				if unknown && !strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("error hides uncertainty: %v", err)
				}
				if failure == "network" || failure == "canceled" || failure == "deadline" {
					if !errors.Is(err, cause) {
						t.Fatalf("cause lost: %v", err)
					}
				}
				if IsRetryable(fmt.Errorf("wrapped: %w", &Error{OutcomeUnknown: true, StatusCode: 502, Method: method})) {
					t.Fatal("wrapped uncertain error is retryable")
				}
				result, readErr := c.ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
				if readErr != nil || len(result.Blocks) != 0 || calls.Load() != 2 {
					t.Fatalf("reconciliation = %+v, %v", result, readErr)
				}
			})
		}
	}
}

func TestMemoryWritesDoNotFollowRedirects(t *testing.T) {
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%s %d", method, status), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(status)
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				err := callMemory(c, context.Background(), method, "u", "m", 1, "text")
				if err == nil || IsRetryable(err) || calls.Load() != 1 {
					t.Fatalf("error = %v, calls = %d", err, calls.Load())
				}
			})
		}
	}
}

func TestMemoryReadNetworkErrorsRemainRetryable(t *testing.T) {
	for _, status := range []int{0, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			cause := net.UnknownNetworkError("connection lost")
			c := memoryClient(t, Config{BaseURL: "https://example.test", AgentID: "a", AgentAPIKey: "k", HTTPClient: &http.Client{Transport: memoryRoundTripper(func(*http.Request) (*http.Response, error) {
				if status == 0 {
					return nil, cause
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"code":"unavailable"}`))}, nil
			})}})
			err := callMemory(c, context.Background(), "GET", "u", "", 0, "")
			var apiErr Error
			if !errors.As(err, &apiErr) || apiErr.OutcomeUnknown || !IsRetryable(err) || (status == 0 && !errors.Is(err, cause)) {
				t.Fatalf("read error = %+v, retryable = %t", apiErr, IsRetryable(err))
			}
		})
	}
}

func TestMemoryCanceledBeforeDispatchIsNotUnknown(t *testing.T) {
	var calls atomic.Int32
	c := memoryClient(t, Config{BaseURL: "https://example.test", AgentID: "a", AgentAPIKey: "k", HTTPClient: &http.Client{Transport: memoryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, r.Context().Err()
	})}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			err := callMemory(c, ctx, method, "u", "m", 1, "text")
			var apiErr Error
			if !errors.Is(err, context.Canceled) || IsRetryable(err) || (errors.As(err, &apiErr) && apiErr.OutcomeUnknown) || calls.Load() != 0 {
				t.Fatalf("pre-dispatch error = %v, calls = %d", err, calls.Load())
			}
		})
	}
}

func TestMemoryAppliedConditionalWriteLosesResponse(t *testing.T) {
	for _, method := range []string{"PATCH", "DELETE"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s partial=%t", method, partial), func(t *testing.T) {
				var writes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" {
						blocks := []AgentMemoryBlock{}
						if method == "PATCH" {
							block := memoryBlock()
							block.Text, block.Version = "saved", 4
							blocks = append(blocks, block)
						}
						_ = json.NewEncoder(w).Encode(ListMemoryResult{Blocks: blocks})
						return
					}
					writes.Add(1)
					if partial {
						w.Header().Set("Content-Length", "1000")
						_, _ = io.WriteString(w, `{"operation":`)
						w.(http.Flusher).Flush()
					}
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
				}))
				defer server.Close()
				c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
				err := callMemory(c, context.Background(), method, "u", "opaque/1", 3, "saved")
				var apiErr Error
				if !errors.As(err, &apiErr) || !apiErr.OutcomeUnknown || IsRetryable(err) || writes.Load() != 1 {
					t.Fatalf("error = %+v, writes = %d", apiErr, writes.Load())
				}
				current, err := c.ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
				if err != nil || writes.Load() != 1 {
					t.Fatalf("GET error = %v, writes = %d", err, writes.Load())
				}
				if method == "DELETE" && len(current.Blocks) != 0 {
					t.Fatalf("deleted block visible: %+v", current)
				}
				if method == "PATCH" && (len(current.Blocks) != 1 || current.Blocks[0].Text != "saved" || current.Blocks[0].Version != 4) {
					t.Fatalf("update not visible: %+v", current)
				}
			})
		}
	}
}

func TestMemoryCreateFailureAlwaysExposesSentKey(t *testing.T) {
	for _, status := range []int{200, 400, 409, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var key string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key = r.Header.Get("Idempotency-Key")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"type":"conflict","code":"conflict.agent.idempotency_key","message":"Different creation","details":{}}`)
			}))
			defer server.Close()
			c := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"})
			result, err := c.CreateMemory(context.Background(), CreateMemoryParams{UserID: "u", Text: "text"})
			var apiErr Error
			if !errors.As(err, &apiErr) || !validIdempotencyKey(key) || result.IdempotencyKey != key || apiErr.IdempotencyKey != key || apiErr.OutcomeUnknown != (status == 200 || status == 500) {
				t.Fatalf("result = %+v, error = %+v, sent key = %q", result, apiErr, key)
			}
		})
	}
}

func TestMemoryListPreservesWireOrderAndNormalizesTimes(t *testing.T) {
	first, second := memoryBlock(), memoryBlock()
	first.ID, second.ID = "opaque:a", "opaque:b"
	first.CreatedAt = first.CreatedAt.In(time.FixedZone("wire", 3600))
	first.UpdatedAt = first.UpdatedAt.In(time.FixedZone("wire", 3600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ListMemoryResult{Blocks: []AgentMemoryBlock{first, second}})
	}))
	defer server.Close()
	result, err := memoryClient(t, Config{BaseURL: server.URL, AgentID: "a", AgentAPIKey: "k"}).ListMemory(context.Background(), ListMemoryParams{UserID: "u"})
	if err != nil || len(result.Blocks) != 2 || result.Blocks[0].ID != first.ID || result.Blocks[1].ID != second.ID || !result.Blocks[0].CreatedAt.Equal(first.CreatedAt) || result.Blocks[0].CreatedAt.Location() != time.UTC || result.Blocks[0].UpdatedAt.Location() != time.UTC {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}
