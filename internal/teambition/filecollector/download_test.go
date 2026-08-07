package filecollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type downloadSourceFunc func(context.Context, string, string, string) (DownloadDescriptor, int, error)

func (f downloadSourceFunc) ResolveDownload(ctx context.Context, projectID, nodeID, versionID string) (DownloadDescriptor, int, error) {
	return f(ctx, projectID, nodeID, versionID)
}

func TestDownloadZeroByteContentDedupResumeAndSignedURLRedaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	out := t.TempDir()
	writeDownloadFixture(t, out, fileNode("one", ptr(0)), fileNode("two", ptr(0)))
	source := downloadSourceFunc(func(_ context.Context, _, _, _ string) (DownloadDescriptor, int, error) {
		return DownloadDescriptor{URL: server.URL + "/asset?X-Amz-Signature=top-secret", ExpectedSize: ptr(0)}, 200, nil
	})
	cfg := Config{ProjectID: "p", Output: out, Concurrency: 2}
	result, err := Download(context.Background(), source, server.Client(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded != 2 || result.Failed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	root := filepath.Join(out, "teambition-file-collector", "p")
	for _, name := range []string{"one.bin", "two.bin"} {
		if _, err := os.Stat(filepath.Join(root, "view", name)); err != nil {
			t.Fatalf("browse view %s: %v", name, err)
		}
	}
	checksums, _ := os.ReadFile(filepath.Join(root, "checksums.sha256"))
	if !strings.Contains(string(checksums), "entities/project_file_nodes.jsonl") || strings.Contains(string(checksums), ".partial") {
		t.Fatalf("invalid checksums manifest: %s", checksums)
	}
	assets, err := filepath.Glob(filepath.Join(root, "assets", "sha256", "*", "*"))
	if err != nil || len(assets) != 1 {
		t.Fatalf("content dedup assets=%v err=%v", assets, err)
	}
	assertNoTextInTree(t, root, "top-secret")

	result, err = Download(context.Background(), source, server.Client(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 2 || result.Downloaded != 0 {
		t.Fatalf("resume result: %+v", result)
	}
	result, err = Download(context.Background(), nil, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 2 || result.Downloaded != 0 || result.Failed != 0 {
		t.Fatalf("offline upgrade result: %+v", result)
	}
	for _, node := range readNodeLines(t, filepath.Join(root, "entities", "project_file_nodes.jsonl")) {
		if node.NodeKind == "file" && (node.DownloadStatus != "downloaded" || node.ContentSHA256 == nil || node.LocalAssetRef == nil) {
			t.Fatalf("incomplete downloaded node: %+v", node)
		}
	}
}

func TestBrowseViewPreservesSameNameDifferentID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.URL.Path == "/two" {
			_, _ = io.WriteString(w, "two")
			return
		}
		_, _ = io.WriteString(w, "one")
	}))
	defer server.Close()

	out := t.TempDir()
	one := fileNode("one", ptr(3))
	two := fileNode("two", ptr(3))
	one.Name = "same.txt"
	two.Name = "same.txt"
	one.Fingerprint = fingerprint(one)
	two.Fingerprint = fingerprint(two)
	writeDownloadFixture(t, out, one, two)
	source := downloadSourceFunc(func(_ context.Context, _, nodeID, _ string) (DownloadDescriptor, int, error) {
		return DownloadDescriptor{URL: server.URL + "/" + nodeID, ExpectedSize: ptr(3)}, 200, nil
	})
	if _, err := Download(context.Background(), source, server.Client(), Config{ProjectID: "p", Output: out, Concurrency: 2}); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(out, "teambition-file-collector", "p")
	data, err := os.ReadFile(filepath.Join(root, "view", "view_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []browseViewEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, entry := range entries {
		paths[entry.ViewPath] = true
		if _, err := os.Stat(filepath.Join(root, "view", filepath.FromSlash(entry.ViewPath))); err != nil {
			t.Fatalf("manifest points to missing view file %s: %v", entry.ViewPath, err)
		}
	}
	if len(paths) != 2 || !paths["same.txt"] || !paths["same [two].txt"] {
		t.Fatalf("same-name view paths were not preserved: %+v", entries)
	}
}

func TestDownloadRejectsHTMLSizeMismatchAndOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "abc")
		}
	}))
	defer server.Close()

	out := t.TempDir()
	writeDownloadFixture(t, out, fileNode("html", nil), fileNode("mismatch", ptr(5)), fileNode("large", ptr(100)))
	var resolveCalls atomic.Int32
	source := downloadSourceFunc(func(_ context.Context, _, nodeID, _ string) (DownloadDescriptor, int, error) {
		resolveCalls.Add(1)
		return DownloadDescriptor{URL: server.URL + "/" + nodeID}, 200, nil
	})
	result, err := Download(context.Background(), source, server.Client(), Config{ProjectID: "p", Output: out, MaxFileSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 3 || result.TooLarge != 1 || resolveCalls.Load() != 2 {
		t.Fatalf("unexpected result=%+v resolveCalls=%d", result, resolveCalls.Load())
	}
	errorsJSONL, _ := os.ReadFile(filepath.Join(out, "teambition-file-collector", "p", "download_errors.jsonl"))
	text := string(errorsJSONL)
	if !strings.Contains(text, "HTML page") || !strings.Contains(text, "size mismatch") || !strings.Contains(text, "exceeds max-file-size") {
		t.Fatalf("missing classified errors: %s", text)
	}
}

func TestDownloadRetries429AndDoesNotRetry403(t *testing.T) {
	var transientCalls atomic.Int32
	var forbiddenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forbidden" {
			forbiddenCalls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if transientCalls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	out := t.TempDir()
	writeDownloadFixture(t, out, fileNode("transient", ptr(2)), fileNode("forbidden", nil))
	source := downloadSourceFunc(func(_ context.Context, _, nodeID, _ string) (DownloadDescriptor, int, error) {
		return DownloadDescriptor{URL: server.URL + "/" + nodeID}, 200, nil
	})
	result, err := Download(context.Background(), source, server.Client(), Config{ProjectID: "p", Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded != 1 || result.PermissionDenied != 1 || transientCalls.Load() != 2 || forbiddenCalls.Load() != 1 {
		t.Fatalf("result=%+v transient=%d forbidden=%d", result, transientCalls.Load(), forbiddenCalls.Load())
	}
}

func TestDownloadInterruptedStreamThenResume(t *testing.T) {
	out := t.TempDir()
	writeDownloadFixture(t, out, fileNode("interrupted", ptr(6)))
	source := downloadSourceFunc(func(_ context.Context, _, _, _ string) (DownloadDescriptor, int, error) {
		return DownloadDescriptor{URL: "https://signed.example.test/file?signature=secret"}, 200, nil
	})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: &failingBody{data: []byte("abc")}}, nil
	})}
	result, err := Download(context.Background(), source, client, Config{ProjectID: "p", Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 {
		t.Fatalf("first result: %+v", result)
	}
	partials, _ := filepath.Glob(filepath.Join(out, "teambition-file-collector", "p", "assets", ".partial", "*"))
	if len(partials) != 0 {
		t.Fatalf("partial files were retained: %v", partials)
	}

	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: io.NopCloser(bytes.NewReader([]byte("abcdef")))}, nil
	})
	result, err = Download(context.Background(), source, client, Config{ProjectID: "p", Output: out, RetryFailedDownloads: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded != 1 || result.Failed != 0 {
		t.Fatalf("resume result: %+v", result)
	}
	assertNoTextInTree(t, filepath.Join(out, "teambition-file-collector", "p"), "signed.example.test")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct {
	data []byte
	done bool
}

func (b *failingBody) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		return copy(p, b.data), nil
	}
	return 0, errors.New("fixture stream interrupted")
}
func (b *failingBody) Close() error { return nil }

func fileNode(id string, size *int64) Node {
	n := makeNode("p", id, "root", "file", id+".bin", "", nil, "user", nil, "application/octet-stream", "", "", false, nil, []string{"content_sha256", "local_asset_ref", "download_status"})
	if size != nil {
		n.Size = *size
	}
	n.Fingerprint = fingerprint(n)
	return n
}

func writeDownloadFixture(t *testing.T, output string, files ...Node) {
	t.Helper()
	root := filepath.Join(output, "teambition-file-collector", "p")
	if err := os.MkdirAll(filepath.Join(root, "entities"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "checkpoints"), 0755); err != nil {
		t.Fatal(err)
	}
	rootNode := makeNode("p", "root", "", "directory", nil, "", nil, "", nil, nil, "", "", false, nil, nil)
	rootNode.Fingerprint = fingerprint(rootNode)
	if err := rewriteNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl"), append([]Node{rootNode}, files...)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "download_errors.jsonl"), nil, 0644); err != nil {
		t.Fatal(err)
	}
}

func assertNoTextInTree(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), needle) {
			t.Fatalf("secret leaked to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
