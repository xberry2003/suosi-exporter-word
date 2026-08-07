package fileadapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"thoughtsexport/internal/tbweb"
)

func TestBrowserResolveDownloadUsesVerifiedDetailResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/works/work-1" || r.Header.Get("Cookie") != "TB_ACCESS_TOKEN=fixture" || r.Header.Get("Referer") != "https://www.teambition.com/project/p/works/root" {
			t.Fatalf("unexpected request path=%s cookie=%q referer=%q", r.URL.Path, r.Header.Get("Cookie"), r.Header.Get("Referer"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"_id":"work-1","_projectId":"p","fileType":"text/plain","fileSize":4,"downloadUrl":"https://tcs.teambition.net/storage/key?Signature=fixture"}`))
	}))
	defer server.Close()
	client := tbweb.NewClient("TB_ACCESS_TOKEN=fixture", "https://www.teambition.com/project/p/works/root")
	client.BaseURL = server.URL
	desc, status, err := (Browser{Client: client}).ResolveDownload(context.Background(), "p", "work-1", "")
	if err != nil || status != 200 || desc.URL == "" || desc.ExpectedSize == nil || *desc.ExpectedSize != 4 {
		t.Fatalf("desc=%+v status=%d err=%v", desc, status, err)
	}
}

func TestValidateDownloadURLRejectsUntrustedHost(t *testing.T) {
	if err := validateDownloadURL("https://evil.example/file?Signature=secret"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}
