package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpDoesNotRequireToken(t *testing.T) {
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"help"}, stdout, stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aimount agents instruction set") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestMissingTokenFailsBeforeRequest(t *testing.T) {
	t.Setenv("AIMOUNT_TOKEN", "")
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"auth", "whoami"}, stdout, stderr); code == 0 {
		t.Fatal("expected failure")
	}
	if !strings.Contains(stderr.String(), "AIMOUNT_TOKEN is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCommands(t *testing.T) {
	token := "secret-token"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("missing authorization")
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("content-type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /console/v1/me":
			json.NewEncoder(w).Encode(map[string]any{"tokenId": "ptok_1", "ownerType": "org", "ownerId": "org_1", "orgIds": []string{"org_1"}, "scopes": []string{"agent:read", "agent:write"}})
		case "GET /console/v1/orgs/org_1/agents":
			json.NewEncoder(w).Encode([]map[string]any{{"id": "agent_1", "orgId": "org_1", "slug": "support", "name": "Support", "status": "active", "createdAt": "2026-07-31T00:00:00Z"}})
		case "PUT /console/v1/orgs/org_1/agents/new-agent":
			json.NewEncoder(w).Encode(map[string]any{"id": "agent_2", "orgId": "org_1", "slug": "new-agent", "name": "New Agent", "status": "active", "createdAt": "2026-07-31T00:00:00Z"})
		case "POST /console/v1/orgs/org_1/agents/support/instruction/versions":
			json.NewEncoder(w).Encode(map[string]string{"id": "aiv_1"})
		case "POST /console/v1/orgs/org_1/agents/support/instruction/release":
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("AIMOUNT_TOKEN", token)
	t.Setenv("AIMOUNT_API_URL", server.URL)

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "whoami", args: []string{"auth", "whoami"}, want: "org_1"},
		{name: "list", args: []string{"agents", "list"}, want: "support\tSupport\tactive"},
		{name: "set", args: []string{"agents", "set", "new-agent", "--name", "New Agent"}, want: "new-agent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			if code := Run(context.Background(), test.args, stdout, stderr); code != 0 {
				t.Fatalf("code = %d, stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.want)
			}
		})
	}

	instruction := filepath.Join(t.TempDir(), "instruction.md")
	if err := os.WriteFile(instruction, []byte("Help users."), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"agents", "instruction", "set", "support", "--file", instruction}, stdout, stderr); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aiv_1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	sets := 0
	for _, request := range requests {
		if request == "PUT /console/v1/orgs/org_1/agents/new-agent" {
			sets++
		}
	}
	if sets != 1 {
		t.Fatalf("agent set requests = %d, all requests = %#v", sets, requests)
	}
}

func TestOrganizationResolution(t *testing.T) {
	for _, test := range []struct {
		name    string
		orgs    []string
		args    []string
		wantErr string
	}{
		{name: "explicit", orgs: []string{"org_a", "org_b"}, args: []string{"--org", "org_b", "agents", "list"}},
		{name: "none", wantErr: "no accessible organizations"},
		{name: "many", orgs: []string{"org_a", "org_b"}, wantErr: "provide --org"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AIMOUNT_TOKEN", "token")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				if r.URL.Path == "/console/v1/me" {
					json.NewEncoder(w).Encode(map[string]any{"tokenId": "p", "ownerType": "org", "ownerId": "o", "orgIds": test.orgs, "scopes": []string{}})
					return
				}
				json.NewEncoder(w).Encode([]any{})
			}))
			defer server.Close()
			t.Setenv("AIMOUNT_API_URL", server.URL)
			args := test.args
			if args == nil {
				args = []string{"agents", "list"}
			}
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			code := Run(context.Background(), args, stdout, stderr)
			if test.wantErr == "" && code != 0 {
				t.Fatalf("code = %d, stderr = %s", code, stderr.String())
			}
			if test.wantErr != "" && (code == 0 || !strings.Contains(stderr.String(), test.wantErr)) {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
		})
	}
}

func TestInstructionValidationAndPartialFailure(t *testing.T) {
	t.Setenv("AIMOUNT_TOKEN", "token")
	empty := filepath.Join(t.TempDir(), "empty.md")
	os.WriteFile(empty, []byte("  \n"), 0o600)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"--org", "org_1", "agents", "instruction", "set", "support", "--file", empty}, stdout, stderr); code == 0 || !strings.Contains(stderr.String(), "instruction file is empty") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	file := filepath.Join(t.TempDir(), "instruction.md")
	os.WriteFile(file, []byte("Help."), 0o600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/versions") {
			json.NewEncoder(w).Encode(map[string]string{"id": "aiv_partial"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"code": "internal.release"})
	}))
	defer server.Close()
	t.Setenv("AIMOUNT_API_URL", server.URL)
	stdout, stderr = new(bytes.Buffer), new(bytes.Buffer)
	code := Run(context.Background(), []string{"--org", "org_1", "agents", "instruction", "set", "support", "--file", file}, stdout, stderr)
	if code == 0 || !strings.Contains(stderr.String(), "aiv_partial") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestErrorsDoNotExposeToken(t *testing.T) {
	token := "never-print-this"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": token})
	}))
	defer server.Close()
	t.Setenv("AIMOUNT_TOKEN", token)
	t.Setenv("AIMOUNT_API_URL", server.URL)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"auth", "whoami"}, stdout, stderr); code == 0 || strings.Contains(stderr.String(), token) {
		t.Fatalf("code/output = %d %q", code, stderr.String())
	}
}

func TestHelpAndInvalidInputStayOffline(t *testing.T) {
	t.Setenv("AIMOUNT_TOKEN", "")
	for _, args := range [][]string{
		{"agents", "--help"},
		{"agents", "set", "support", "--help"},
	} {
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		if code := Run(context.Background(), args, stdout, stderr); code != 0 || !strings.Contains(stdout.String(), "Usage:") {
			t.Fatalf("args = %#v, code = %d, stdout = %q, stderr = %q", args, code, stdout.String(), stderr.String())
		}
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	code := Run(context.Background(), []string{"agents", "set", "support", "--name", "Support", "extra"}, stdout, stderr)
	if code != 2 || strings.Contains(stderr.String(), "AIMOUNT_TOKEN") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestEmptyAgentListHasNoOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	}))
	defer server.Close()
	t.Setenv("AIMOUNT_TOKEN", "token")
	t.Setenv("AIMOUNT_API_URL", server.URL)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	if code := Run(context.Background(), []string{"--org", " org_1 ", "agents", "list"}, stdout, stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}
