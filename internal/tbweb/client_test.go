package tbweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"thoughtsexport/internal/tbinventory"
)

func TestListFilesCombinesCollectionsAndWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("_projectId") != "project-1" || r.URL.Query().Get("_parentId") != "folder-1" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/collections":
			_, _ = w.Write([]byte(`[{"_id":"folder-2","_parentId":"folder-1","title":"Child"}]`))
		case "/api/works":
			_, _ = w.Write([]byte(`[{"_id":"work-1","_parentId":"folder-1","fileName":"report.pdf","fileType":"application/pdf","fileSize":12}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient("session=value", "https://www.teambition.com/project/project-1/works/folder-1")
	client.BaseURL = server.URL
	page, status, err := client.ListFiles(context.Background(), "project-1", "folder-1", "", tbinventory.ListOptions{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || len(page.Collections) != 1 || len(page.Works) != 1 {
		t.Fatalf("unexpected result: status=%d collections=%d works=%d", status, len(page.Collections), len(page.Works))
	}
	if page.NextPageToken != "2" {
		t.Fatalf("next token = %q, want 2", page.NextPageToken)
	}
}

func TestListFilesRejectsBusinessErrorAtHTTP200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":403,"errorMessage":"permission denied"}`))
	}))
	defer server.Close()

	client := NewClient("session=value", "https://www.teambition.com/")
	client.BaseURL = server.URL
	page, status, err := client.ListFiles(context.Background(), "project-1", "folder-1", "", tbinventory.ListOptions{PageSize: 50})
	if err == nil {
		t.Fatal("expected business error")
	}
	var apiErr *tbinventory.APIError
	if !errors.As(err, &apiErr) || apiErr.BusinessCode == nil || *apiErr.BusinessCode != 403 {
		t.Fatalf("unexpected error: %#v", err)
	}
	if status != http.StatusOK || page.Diagnostics.ErrorMessage != "permission denied" {
		t.Fatalf("unexpected diagnostics: status=%d diag=%+v", status, page.Diagnostics)
	}
}
