package logic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureTeambitionBrowserSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ensure" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"authenticated":true,"reused":true}`))
	}))
	defer server.Close()
	t.Setenv("TEAMBITION_AUTH_URL", server.URL)
	if err := EnsureTeambitionBrowserSession(context.Background(), "https://thoughts.teambition.com/workspaces/test/overview"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureTeambitionBrowserSessionRejectsRemoteEndpoint(t *testing.T) {
	t.Setenv("TEAMBITION_AUTH_URL", "https://example.com")
	err := EnsureTeambitionBrowserSession(context.Background(), "https://thoughts.teambition.com")
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}
