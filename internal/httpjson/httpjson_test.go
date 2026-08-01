package httpjson

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoOmitsBodyForNilInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != http.NoBody || r.Header.Get("Content-Type") != "" {
			t.Fatalf("body = %T, content-type = %q", r.Body, r.Header.Get("Content-Type"))
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	var result struct{ OK bool }
	if requestErr, err := Do(context.Background(), http.DefaultClient, http.MethodGet, server.URL, "/", "token", "", nil, &result); err != nil || requestErr != nil || !result.OK {
		t.Fatalf("requestErr = %v, err = %v, result = %#v", requestErr, err, result)
	}
}
