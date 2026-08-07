package filecollector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"thoughtsexport/internal/tbinventory"
)

type fixtureSource struct {
	pages  map[string][]tbinventory.Page
	errors map[string]error
	calls  []string
}

func (f *fixtureSource) ListFiles(_ context.Context, _, parent, token string, _ tbinventory.ListOptions) (tbinventory.Page, int, error) {
	f.calls = append(f.calls, parent+"|"+token)
	if e := f.errors[parent+"|"+token]; e != nil {
		return tbinventory.Page{}, 403, e
	}
	p := f.pages[parent+"|"+token]
	if len(p) == 0 {
		return tbinventory.Page{}, 200, nil
	}
	return p[0], 200, nil
}

func TestDiscoverPaginationDeepTreeAndRawRedaction(t *testing.T) {
	s := &fixtureSource{pages: map[string][]tbinventory.Page{
		"root|":   {{Collections: []tbinventory.Collection{{ID: "a", ParentID: "root", Title: "A"}, {ID: "empty", ParentID: "root", Title: "Empty"}}, Works: []tbinventory.Work{{ID: "same-1", ParentID: "root", FileName: "same.txt", FileSize: ptr(0)}, {ID: "unsafe", ParentID: "root", FileName: "../unsafe.txt"}}, NextPageToken: "p2", Diagnostics: tbinventory.ResponseDiagnostics{RawResponse: `{"downloadUrl":"https://cdn.test/x?X-Amz-Signature=secret","ok":true}`}}},
		"root|p2": {{Works: []tbinventory.Work{{ID: "same-2", ParentID: "root", FileName: "same.txt"}}}},
		"a|":      {{Collections: []tbinventory.Collection{{ID: "b", ParentID: "a", Title: "B"}}}},
		"b|":      {{Works: []tbinventory.Work{{ID: "deep", ParentID: "b", FileName: "zero"}}}},
		"empty|":  {{}},
	}}
	out := t.TempDir()
	r, e := Discover(context.Background(), s, Config{ProjectID: "p", ProjectURL: "https://www.teambition.com/project/p/works/root", Output: out, IncludeRaw: true, PageSize: 1})
	if e != nil {
		t.Fatal(e)
	}
	if r.Nodes != 8 || r.Directories != 4 || r.Files != 4 || r.Pages != 5 {
		t.Fatalf("unexpected result: %+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(out, "teambition-file-collector", "p", "raw", "page-root-first.json"))
	if strings.Contains(string(b), "secret") || !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("raw URL was not redacted: %s", b)
	}
	lines := readNodeLines(t, filepath.Join(out, "teambition-file-collector", "p", "entities", "project_file_nodes.jsonl"))
	if lines[0].ExternalID != "root" || lines[0].ParentExternalID != nil {
		t.Fatal("file-library root was not emitted as a source node")
	}
	if !lines[0].Root || !lines[0].Synthetic {
		t.Fatal("file-library root was not marked synthetic")
	}
	rawNodes, _ := os.ReadFile(filepath.Join(out, "teambition-file-collector", "p", "entities", "project_file_nodes.jsonl"))
	var envelope NodeEnvelope
	if json.Unmarshal([]byte(strings.Split(string(rawNodes), "\n")[0]), &envelope) != nil || envelope.SchemaVersion != "1.1" || envelope.EntityType != "project_file_node" {
		t.Fatal("project file node was not written as a 1.1 envelope")
	}
	if lines[0].ExternalID == "" {
		t.Fatal("missing id")
	}
	if lines[0].Fingerprint == "" {
		t.Fatal("missing fingerprint")
	}
	ids := map[string]bool{}
	paths := map[string]string{}
	for _, n := range lines {
		if ids[n.ExternalID] {
			t.Fatalf("duplicate %s", n.ExternalID)
		}
		ids[n.ExternalID] = true
		paths[n.ExternalID] = n.DisplayPath
	}
	if !ids["same-1"] || !ids["same-2"] {
		t.Fatal("same-name/source-id fixture was not preserved")
	}
	if paths["deep"] != "A/B/zero" {
		t.Fatalf("deep display path=%q", paths["deep"])
	}
}

func TestDiscoverResumeDoesNotDuplicateNodes(t *testing.T) {
	s := &fixtureSource{pages: map[string][]tbinventory.Page{"root|": {{Works: []tbinventory.Work{{ID: "w1", ParentID: "root", FileName: "one"}}}}}}
	out := t.TempDir()
	cfg := Config{ProjectID: "p", ProjectURL: "https://www.teambition.com/project/p/works/root", Output: out, Resume: false}
	if _, e := Discover(context.Background(), s, cfg); e != nil {
		t.Fatal(e)
	}
	cfg.Resume = true
	if _, e := Discover(context.Background(), s, cfg); e != nil {
		t.Fatal(e)
	}
	if n := len(readNodeLines(t, filepath.Join(out, "teambition-file-collector", "p", "entities", "project_file_nodes.jsonl"))); n != 2 {
		t.Fatalf("resume duplicated %d nodes", n)
	}
}

func TestDiscoverRecordsForbiddenParentAndContinues(t *testing.T) {
	s := &fixtureSource{pages: map[string][]tbinventory.Page{"root|": {{Collections: []tbinventory.Collection{{ID: "denied", ParentID: "root", Title: "denied"}, {ID: "ok", ParentID: "root", Title: "ok"}}}}, "denied|": nil, "ok|": {{Works: []tbinventory.Work{{ID: "w", ParentID: "ok", FileName: "ok"}}}}}, errors: map[string]error{"denied|": errors.New("permission denied")}}
	out := t.TempDir()
	r, e := Discover(context.Background(), s, Config{ProjectID: "p", ProjectURL: "https://www.teambition.com/project/p/works/root", Output: out})
	if e != nil {
		t.Fatal(e)
	}
	if r.Errors != 1 || r.Files != 1 {
		t.Fatalf("unexpected %+v", r)
	}
	root := filepath.Join(out, "teambition-file-collector", "p")
	discoveryErrors, _ := os.ReadFile(filepath.Join(root, "errors.jsonl"))
	downloadErrors, _ := os.ReadFile(filepath.Join(root, "download_errors.jsonl"))
	if len(strings.TrimSpace(string(discoveryErrors))) == 0 || len(strings.TrimSpace(string(downloadErrors))) != 0 {
		t.Fatal("discovery and download errors were mixed")
	}
	nodes := readNodeLines(t, filepath.Join(root, "entities", "project_file_nodes.jsonl"))
	for _, n := range nodes {
		if n.ExternalID == "denied" && n.Visibility != "partially_visible" {
			t.Fatalf("denied folder visibility=%s", n.Visibility)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(root, "manifest.json"))
	if !strings.Contains(string(manifest), `"status": "partial"`) || !strings.Contains(string(manifest), `"nodes": "partial"`) {
		t.Fatalf("403 discovery did not keep manifest partial: %s", manifest)
	}
}

func TestMIMETypeNormalizationPreservesSourceValue(t *testing.T) {
	n := nodeFromWork("p", tbinventory.Work{ID: "w", ParentID: "root", FileName: "sheet.xlsx", MIMEType: "xlsx"}, "root", nil)
	if n.SourceMIMEType != "xlsx" || n.MIMEType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("source=%v normalized=%v", n.SourceMIMEType, n.MIMEType)
	}
}

func TestBrowseComponentRejectsPathTraversalAndReservedNames(t *testing.T) {
	if value := browseComponent(`../CON`, "fallback"); strings.Contains(value, "/") || strings.Contains(value, `\`) || value == "CON" {
		t.Fatalf("unsafe browse component %q", value)
	}
	if value := browseComponent("NUL", "fallback"); value != "_NUL" {
		t.Fatalf("reserved browse component %q", value)
	}
}

func TestFingerprintExcludesRawReference(t *testing.T) {
	a := Node{ExternalID: "w", ProjectExternalID: "p", NodeKind: "file", Name: "a"}
	b := a
	a.RawRef = "raw/a"
	b.RawRef = "raw/b"
	if fingerprint(a) != fingerprint(b) {
		t.Fatal("raw ref changed fingerprint")
	}
}

func TestRenameAndMoveKeepExternalIDButChangeFingerprint(t *testing.T) {
	a := nodeFromWork("p", tbinventory.Work{ID: "stable", ParentID: "a", FileName: "old.txt"}, "a", nil)
	b := nodeFromWork("p", tbinventory.Work{ID: "stable", ParentID: "b", FileName: "new.txt"}, "b", nil)
	if a.ExternalID != b.ExternalID || a.Fingerprint == b.Fingerprint {
		t.Fatalf("identity/fingerprint mismatch: %#v %#v", a, b)
	}
}

func TestResumeDoesNotInferDeletionFromAbsence(t *testing.T) {
	s := &fixtureSource{pages: map[string][]tbinventory.Page{"root|": {{Works: []tbinventory.Work{{ID: "one", ParentID: "root", FileName: "one"}, {ID: "two", ParentID: "root", FileName: "two"}}}}}}
	out := t.TempDir()
	cfg := Config{ProjectID: "p", ProjectURL: "https://www.teambition.com/project/p/works/root", Output: out}
	if _, e := Discover(context.Background(), s, cfg); e != nil {
		t.Fatal(e)
	}
	s.pages["root|"] = []tbinventory.Page{{Works: []tbinventory.Work{{ID: "one", ParentID: "root", FileName: "one"}}}}
	cfg.Resume = true
	if _, e := Discover(context.Background(), s, cfg); e != nil {
		t.Fatal(e)
	}
	nodes := readNodeLines(t, filepath.Join(out, "teambition-file-collector", "p", "entities", "project_file_nodes.jsonl"))
	found := false
	for _, n := range nodes {
		if n.ExternalID == "two" {
			found = true
			if n.Deleted {
				t.Fatal("absent node was marked deleted")
			}
		}
	}
	if !found {
		t.Fatal("absent node was discarded")
	}
}

func ptr(v int64) *int64 { return &v }
func readNodeLines(t *testing.T, path string) []Node {
	t.Helper()
	out := readNodes(path)
	if len(out) == 0 {
		t.Fatal("invalid or empty node jsonl")
	}
	return out
}
