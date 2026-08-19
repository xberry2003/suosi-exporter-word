package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	server, err := NewServer(ServerConfig{DatabasePath: filepath.Join(root, "data", "jobs.sqlite"), ArtifactRoot: filepath.Join(root, "artifacts"), DataRoot: filepath.Join(root, "data"), Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestServerHealthModulesAndFrontend(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/api/health", "/api/modules", "/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", path, response.Code)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s did not include security headers", path)
		}
	}
}

func TestPreflightRejectsInvalidInput(t *testing.T) {
	server := newTestServer(t)
	body, _ := json.Marshal(CreateJobRequest{ModuleID: ModuleThoughts, Input: map[string]any{"url": "https://example.com/not-thoughts"}})
	request := httptest.NewRequest(http.MethodPost, "/api/preflight", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	var result PreflightResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("invalid Thoughts URL passed preflight")
	}
}

func TestJobArtifactFilesCanBeListedAndRead(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "result.txt"), []byte("result-data"), 0644); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "artifact-job", ModuleID: ModuleThoughts, ModuleName: "所思导出", Status: "succeeded", Stage: "finished", Message: "done", Input: json.RawMessage(`{}`), ArtifactPath: root, CreatedAt: time.Now().UTC()}
	if err := server.store.Create(job); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/jobs/artifact-job/files", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "nested/result.txt") {
		t.Fatalf("artifact listing missing file: %s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/jobs/artifact-job/files/nested/result.txt", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "result-data" {
		t.Fatalf("unexpected artifact response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCreateJobRequiresSuccessfulPreflight(t *testing.T) {
	server := newTestServer(t)
	body, _ := json.Marshal(CreateJobRequest{ModuleID: ModuleTBFiles, Input: map[string]any{"project_url": "invalid"}})
	request := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
}
