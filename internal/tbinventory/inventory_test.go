package tbinventory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ptr(v int64) *int64 { return &v }

type fakeSource struct {
	pages     map[string]Page
	calls     map[string]int
	failCount int
	always    error
}

func (f *fakeSource) ListFiles(_ context.Context, pid, parent, token string, _ ListOptions) (Page, int, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	k := parent + "|" + token
	f.calls[k]++
	if f.always != nil {
		return Page{}, 503, f.always
	}
	if f.failCount > 0 {
		f.failCount--
		return Page{}, 429, errors.New("limited")
	}
	return f.pages[k], 200, nil
}
func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, e := OpenDB(filepath.Join(t.TempDir(), "test.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
func TestURLParsing(t *testing.T) {
	cases := []struct {
		url, p, w string
		ok        bool
	}{{"https://www.teambition.com/project/p1/works/w1", "p1", "w1", true}, {"https://teambition.com/project/p1/works/c1/work/w2/?x=1#z", "p1", "w2", true}, {"https://example.com/project/p/works/w", "", "", false}}
	for _, c := range cases {
		p, w, ok := ParseWorkURL(c.url)
		if p != c.p || w != c.w || ok != c.ok {
			t.Fatalf("ParseWorkURL(%q)=(%q,%q,%v)", c.url, p, w, ok)
		}
	}
}
func TestProjectFilesURLParsing(t *testing.T) {
	cases := []struct {
		raw, project, parent string
		ok                   bool
	}{
		{"https://www.teambition.com/project/p1/works/root1", "p1", "root1", true},
		{"https://teambition.com/project/p2/works/c2/work/w2/?x=1#f", "p2", "c2", true},
		{"https://www.teambition.com/project/p3/works", "p3", "", true},
		{"https://example.com/project/p/works/c", "", "", false},
	}
	for _, c := range cases {
		ref, err := ParseProjectFilesURL(c.raw)
		if (err == nil) != c.ok || ref.ProjectID != c.project || ref.ParentID != c.parent {
			t.Fatalf("ParseProjectFilesURL(%q)=%+v,%v", c.raw, ref, err)
		}
	}
}
func TestCrawlerStartsAtConfiguredRootCollection(t *testing.T) {
	db := openTestDB(t)
	src := &fakeSource{pages: map[string]Page{"root|": {Collections: []Collection{{ID: "child", ParentID: "root", Title: "child"}}, Works: []Work{{ID: "w", ParentID: "root", FileName: "file.txt", FileSize: ptr(1)}}}, "child|": {}}}
	c := Crawler{Files: src, DB: db, Options: CrawlOptions{PageSize: 50}, RunID: "root"}
	if err := c.CrawlProject(context.Background(), Project{ID: "p", RootParentID: "root"}); err != nil {
		t.Fatal(err)
	}
	if src.calls["root|"] != 1 || src.calls["|"] != 0 {
		t.Fatalf("unexpected calls: %+v", src.calls)
	}
	var n int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_works`).Scan(&n)
	if n != 1 {
		t.Fatalf("works=%d", n)
	}
}

type forbiddenFolderSource struct{}

func (forbiddenFolderSource) ListFiles(_ context.Context, _, parent, _ string, _ ListOptions) (Page, int, error) {
	switch parent {
	case "root":
		return Page{Collections: []Collection{{ID: "denied", ParentID: "root"}, {ID: "allowed", ParentID: "root"}}}, 200, nil
	case "denied":
		return Page{}, 403, errors.New("permission denied")
	case "allowed":
		return Page{Works: []Work{{ID: "work", ParentID: "allowed", FileName: "ok.txt", FileSize: ptr(1)}}}, 200, nil
	default:
		return Page{}, 200, nil
	}
}

func TestCrawlerSkipsForbiddenFolderAndMarksPartial(t *testing.T) {
	db := openTestDB(t)
	c := Crawler{
		Files: forbiddenFolderSource{}, DB: db, RunID: "partial",
		Options: CrawlOptions{PageSize: 50, Retries: 0, SkipForbiddenFolders: true},
	}
	err := c.CrawlProject(context.Background(), Project{ID: "p", RootParentID: "root"})
	var partial *PartialCrawlError
	if !errors.As(err, &partial) || partial.SkippedFolders != 1 {
		t.Fatalf("expected one skipped folder, got %v", err)
	}
	var files, errorsCount int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_works`).Scan(&files)
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_crawl_errors`).Scan(&errorsCount)
	if files != 1 || errorsCount != 1 {
		t.Fatalf("files=%d errors=%d", files, errorsCount)
	}
	var status string
	_ = db.SQL.QueryRow(`SELECT crawl_status FROM tb_projects WHERE project_id='p'`).Scan(&status)
	if status != "partial" {
		t.Fatalf("crawl status=%q", status)
	}
}
func TestSDKBusinessErrorAndRawDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Tenant-Type"); got != "organization" {
			t.Errorf("X-Tenant-Type = %q, want organization", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":403,"errorMessage":"file permission denied","requestId":"req-1","result":{"collections":[],"works":[]}}`))
	}))
	defer server.Close()
	c := NewSDKClient(Config{AppID: "app", AppSecret: "secret", OrgID: "org", APIBase: server.URL})
	page, status, err := c.ListFiles(context.Background(), "p", "root", "", ListOptions{PageSize: 50})
	if err == nil {
		t.Fatal("expected business error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type %T", err)
	}
	if status != 200 || apiErr.BusinessCode == nil || *apiErr.BusinessCode != 403 || apiErr.RequestID != "req-1" {
		t.Fatalf("status=%d error=%+v", status, apiErr)
	}
	if page.Diagnostics.RawResponse == "" || page.Diagnostics.ErrorMessage != "file permission denied" {
		t.Fatalf("diagnostics=%+v", page.Diagnostics)
	}
}
func TestBusinessSuccessCodes(t *testing.T) {
	for _, code := range []float32{0, 200} {
		v := code
		if err := businessError(200, &v, "", ""); err != nil {
			t.Fatalf("code %.0f: %v", code, err)
		}
	}
}
func TestRecursivePaginationDedupAndSummary(t *testing.T) {
	db := openTestDB(t)
	src := &fakeSource{pages: map[string]Page{"|": {Collections: []Collection{{ID: "a", Title: "中文目录", PrefixPath: "/中文目录"}, {ID: "a"}}, Works: []Work{{ID: "root", FileName: "根.txt", FileSize: ptr(10)}}, NextPageToken: "n"}, "|n": {Collections: []Collection{{ID: "b", ParentID: "a", Title: "二层"}}, Works: []Work{{ID: "root", FileName: "duplicate.txt", FileSize: ptr(99)}}}, "a|": {Collections: []Collection{{ID: "b", ParentID: "a", Title: "二层"}}, Works: []Work{{ID: "w2", ParentID: "a", PrefixPath: "/中文目录", FileName: "报告.PDF", FileSize: ptr(20), Archived: true}}}, "b|": {Works: []Work{{ID: "missing", ParentID: "b", FileName: "未知.bin"}}}}}
	c := Crawler{Files: src, DB: db, Options: CrawlOptions{PageSize: 50, Retries: 0, RetryDelay: time.Nanosecond}, RunID: "r"}
	if e := c.CrawlProject(context.Background(), Project{ID: "p", Name: "项目"}); e != nil {
		t.Fatal(e)
	}
	s, e := BuildSummary(db)
	if e != nil {
		t.Fatal(e)
	}
	if s.ProjectCount != 1 || s.FolderCount != 2 || s.FileCount != 3 || s.TotalSizeBytes != 30 || s.MissingSizeCount != 1 || s.ArchivedFileCount != 1 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	var count int
	if e = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_works WHERE work_id='root'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("duplicate work inserted count=%d err=%v", count, e)
	}
	if len(s.LargestFiles) != 2 || s.LargestFiles[0].FileSizeBytes != 20 {
		t.Fatalf("largest files: %+v", s.LargestFiles)
	}
}
func TestRetryAndPersistentFailure(t *testing.T) {
	db := openTestDB(t)
	src := &fakeSource{pages: map[string]Page{"|": {}}, failCount: 1}
	c := Crawler{Files: src, DB: db, Options: CrawlOptions{Retries: 2, RetryDelay: time.Nanosecond}, RunID: "retry"}
	if e := c.CrawlProject(context.Background(), Project{ID: "ok"}); e != nil {
		t.Fatal(e)
	}
	if src.calls["|"] != 2 {
		t.Fatalf("calls=%d", src.calls["|"])
	}
	bad := &fakeSource{always: errors.New("down")}
	c.Files = bad
	c.RunID = "bad"
	if e := c.CrawlProject(context.Background(), Project{ID: "bad"}); e == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_crawl_errors WHERE run_id='bad'`).Scan(&n)
	if n != 1 {
		t.Fatalf("errors=%d", n)
	}
}

func TestFailureThenResumeKeepsCommittedRows(t *testing.T) {
	db := openTestDB(t)
	first := &fakeSource{pages: map[string]Page{"|": {Works: []Work{{ID: "first", FileName: "first.txt", FileSize: ptr(1)}}, NextPageToken: "next"}}, always: nil}
	// Fail only the second page after the first page has already been committed.
	typeSwitch := &resumeSource{first: first.pages["|"], fail: true}
	c := Crawler{Files: typeSwitch, DB: db, Options: CrawlOptions{Retries: 0, RetryDelay: time.Nanosecond}, RunID: "one"}
	if e := c.CrawlProject(context.Background(), Project{ID: "p"}); e == nil {
		t.Fatal("expected interrupted crawl")
	}
	var n int
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_works`).Scan(&n)
	if n != 1 {
		t.Fatalf("committed rows=%d", n)
	}
	typeSwitch.fail = false
	c.RunID = "two"
	if e := c.CrawlProject(context.Background(), Project{ID: "p"}); e != nil {
		t.Fatal(e)
	}
	_ = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_works`).Scan(&n)
	if n != 2 {
		t.Fatalf("resumed rows=%d", n)
	}
}

type resumeSource struct {
	first Page
	fail  bool
}

func (r *resumeSource) ListFiles(_ context.Context, _, _, token string, _ ListOptions) (Page, int, error) {
	if token == "" {
		return r.first, 200, nil
	}
	if r.fail {
		return Page{}, 503, errors.New("interrupted")
	}
	return Page{Works: []Work{{ID: "second", FileName: "second.txt", FileSize: ptr(2)}}}, 200, nil
}
func TestUpsertAndImportExport(t *testing.T) {
	db := openTestDB(t)
	if e := db.UpsertProject(Project{ID: "p", Name: "old"}, "success", ""); e != nil {
		t.Fatal(e)
	}
	if e := db.UpsertProject(Project{ID: "p", Name: "new"}, "success", ""); e != nil {
		t.Fatal(e)
	}
	w := Work{ID: "w", ParentID: "c", PrefixPath: "/中文", FileName: "文件.docx", MIMEType: "application/test", FileSize: ptr(123), SourcePageURL: WorkURL("p", "c", "w")}
	if e := db.UpsertWork("p", w); e != nil {
		t.Fatal(e)
	}
	w.FileSize = ptr(456)
	if e := db.UpsertWork("p", w); e != nil {
		t.Fatal(e)
	}
	var count int
	var size int64
	_ = db.SQL.QueryRow(`SELECT COUNT(*),file_size_bytes FROM tb_works`).Scan(&count, &size)
	if count != 1 || size != 456 {
		t.Fatalf("count=%d size=%d", count, size)
	}
	out := t.TempDir()
	if e := ExportAll(db, out); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(filepath.Join(out, "import_resources.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		t.Fatal("empty import")
	}
	var got ImportResource
	if e = json.Unmarshal(s.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if got.SourceSystem != "teambition" || got.ResourceType != "work" || got.SourceKey != "w" || got.SourceProjectKey != "p" || got.Status != "pending" {
		t.Fatalf("bad import: %+v", got)
	}
	for _, name := range []string{"tb_works.jsonl", "tb_works.csv", "tb_projects.jsonl", "tb_folders.jsonl", "tb_summary.json", "tb_summary.md", "tb_errors.csv"} {
		if _, e = os.Stat(filepath.Join(out, name)); e != nil {
			t.Errorf("missing %s: %v", name, e)
		}
	}
}
