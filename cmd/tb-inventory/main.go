package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"thoughtsexport/internal/tbinventory"
	"thoughtsexport/internal/teambition/collector"
	"thoughtsexport/internal/teambition/taskprobe"
	"time"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }
func main() {
	code := run()
	if code != 0 {
		os.Exit(code)
	}
}
func run() int {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tb-inventory <inventory|summary|export|doctor|tasks probe>")
		return 1
	}
	switch os.Args[1] {
	case "inventory":
		return inventory(os.Args[2:])
	case "summary":
		return summary(os.Args[2:])
	case "export":
		return exportCmd(os.Args[2:])
	case "doctor":
		return doctor(os.Args[2:])
	case "tasks":
		if len(os.Args) < 3 || (os.Args[2] != "probe" && os.Args[2] != "collect") {
			fmt.Fprintln(os.Stderr, "usage: tb-inventory tasks <probe|collect>")
			return 1
		}
		if os.Args[2] == "collect" {
			return tasksCollect(os.Args[3:])
		}
		return tasksProbe(os.Args[3:])
	default:
		return 1
	}
}

func tasksProbe(args []string) int {
	fs := flag.NewFlagSet("tasks probe", flag.ContinueOnError)
	project := fs.String("project", "", "project ID or full Teambition project URL")
	task := fs.String("task", "", "task ID or full Teambition task URL")
	out := fs.String("output", "./exports", "local output root")
	resume := fs.Bool("resume", false, "reuse successful raw responses and downloaded files")
	if fs.Parse(args) != nil {
		return 1
	}
	in, err := taskprobe.ParseRef(*project, *task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tasks probe:", err)
		return 1
	}
	in.Output = *out
	in.Resume = *resume
	cfg := taskprobe.LoadConfig()
	if err = cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "tasks probe:", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := taskprobe.Run(ctx, taskprobe.NewClient(cfg), in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tasks probe failed: project=%s task=%s: %v\n", report.ProjectID, report.TaskID, err)
		return 1
	}
	failed := 0
	for _, r := range report.Results {
		if !r.Success {
			failed++
		}
	}
	fmt.Printf("task probe complete: project=%s task=%s interfaces=%d failed=%d output=%s\n", report.ProjectID, report.TaskID, len(report.Results), failed, filepath.Join(*out, "teambition", "task-probe", report.ProjectID, report.TaskID))
	if failed > 0 {
		return 2
	}
	return 0
}

func tasksCollect(args []string) int {
	fs := flag.NewFlagSet("tasks collect", flag.ContinueOnError)
	project := fs.String("project-id", "", "Teambition project ID")
	projectURL := fs.String("project-url", "", "Teambition project task-view URL")
	out := fs.String("output", "./exports", "collector output root")
	resume := fs.Bool("resume", false, "reuse checkpoint and existing records")
	raw := fs.Bool("include-raw", false, "save raw MCP responses")
	download := fs.Bool("download-assets", false, "download linked files")
	since := fs.String("since", "", "optional RFC3339 lower bound")
	concurrency := fs.Int("concurrency", 2, "bounded concurrency")
	if fs.Parse(args) != nil {
		return 1
	}
	id := strings.TrimSpace(*project)
	if id == "" && *projectURL != "" {
		parts := strings.Split(strings.Trim(*projectURL, "/"), "/")
		for i, p := range parts {
			if p == "project" && i+1 < len(parts) {
				id = parts[i+1]
				break
			}
		}
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "tasks collect: --project-id or --project-url is required")
		return 1
	}
	var sinceTime time.Time
	if *since != "" {
		var e error
		sinceTime, e = time.Parse(time.RFC3339, *since)
		if e != nil {
			fmt.Fprintln(os.Stderr, "tasks collect: invalid --since:", e)
			return 1
		}
	}
	cfg := taskprobe.LoadConfig()
	if e := cfg.Validate(); e != nil {
		fmt.Fprintln(os.Stderr, "tasks collect:", e)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	m, e := collector.New(taskprobe.NewClient(cfg), collector.Config{ProjectID: id, ProjectURL: *projectURL, Output: *out, Resume: *resume, IncludeRaw: *raw, DownloadAssets: *download, Since: sinceTime, Concurrency: *concurrency}).Run(ctx)
	if e != nil {
		fmt.Fprintln(os.Stderr, "tasks collect failed:", e)
		return 1
	}
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
	if m.Status == "partial" {
		return 2
	}
	return 0
}
func inventory(args []string) int {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	var ids, projectURLs stringsFlag
	var pf, out, dbPath, logLevel, parentID string
	var discover, archived, resume, force bool
	var pageSize, concurrency int
	fs.Var(&ids, "project-id", "Teambition project ID (repeatable)")
	fs.Var(&projectURLs, "project-url", "Teambition project files URL (repeatable)")
	fs.StringVar(&parentID, "parent-id", "", "starting collection ID; only valid for one project")
	fs.StringVar(&pf, "projects-file", "", "project IDs file")
	fs.BoolVar(&discover, "discover-projects", false, "discover joined projects")
	fs.BoolVar(&archived, "include-archived", false, "include archived files")
	fs.IntVar(&pageSize, "page-size", 100, "API page size")
	fs.IntVar(&concurrency, "concurrency", 4, "project workers")
	fs.BoolVar(&resume, "resume", false, "skip successful projects")
	fs.BoolVar(&force, "force-refresh", false, "ignore resume state")
	fs.StringVar(&out, "output", "./output/teambition", "output directory")
	fs.StringVar(&dbPath, "db", "", "SQLite path")
	fs.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	if fs.Parse(args) != nil {
		return 1
	}
	logger, ok := newLogger(logLevel)
	if !ok {
		fmt.Fprintln(os.Stderr, "invalid --log-level; use debug, info, warn or error")
		return 1
	}
	if pf != "" {
		v, e := readProjectFile(pf)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		ids = append(ids, v...)
	}
	cfg := tbinventory.LoadConfig()
	if e := cfg.Validate(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := tbinventory.NewSDKClient(cfg)
	var projects []tbinventory.Project
	for _, id := range dedupe(ids) {
		projects = append(projects, tbinventory.Project{ID: id, URL: "https://www.teambition.com/project/" + id})
	}
	for _, raw := range projectURLs {
		ref, err := tbinventory.ParseProjectFilesURL(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --project-url %q: %v\n", raw, err)
			return 1
		}
		projects = mergeProjects(projects, []tbinventory.Project{{ID: ref.ProjectID, URL: raw, RootParentID: ref.ParentID}})
	}
	if discover {
		p, e := client.ListProjects(ctx, pageSize, cfg.OperatorID)
		if e != nil {
			logger.Warn("project discovery failed; continuing explicit projects", "error", e)
		} else {
			projects = mergeProjects(projects, p)
		}
	}
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, tbinventory.ErrNoProjects)
		return 1
	}
	if parentID != "" {
		if len(projects) != 1 {
			fmt.Fprintln(os.Stderr, "--parent-id requires exactly one project")
			return 1
		}
		projects[0].RootParentID = parentID
	}
	if dbPath == "" {
		dbPath = filepath.Join(out, "tb_inventory.sqlite")
	}
	db, e := tbinventory.OpenDB(dbPath)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer db.Close()
	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	configJSON, _ := json.Marshal(map[string]any{"includeArchived": archived, "pageSize": pageSize, "concurrency": concurrency, "resume": resume, "forceRefresh": force, "logLevel": logLevel, "projects": projects})
	_, _ = db.SQL.Exec(`INSERT INTO tb_crawl_runs(run_id,started_at,status,org_id,include_archived,config_json) VALUES(?,?,?,?,?,?)`, runID, time.Now().UTC().Format(time.RFC3339), "running", cfg.OrgID, archived, string(configJSON))
	c := &tbinventory.Crawler{Files: client, DB: db, Options: tbinventory.CrawlOptions{IncludeArchived: archived, PageSize: pageSize, Retries: 4, Resume: resume, ForceRefresh: force}, RunID: runID}
	if concurrency < 1 {
		concurrency = 1
	}
	jobs := make(chan tbinventory.Project)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	crawlAttempts := 0
	authFailures := 0
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if resume && !force && projectSucceeded(db, p.ID) {
					continue
				}
				if e := c.CrawlProject(ctx, p); e != nil {
					mu.Lock()
					crawlAttempts++
					failures++
					var apiErr *tbinventory.APIError
					if errors.As(e, &apiErr) && isAuthError(apiErr) {
						authFailures++
					}
					mu.Unlock()
					logger.Error("project crawl failed", "project_id", p.ID, "error", e)
				} else {
					mu.Lock()
					crawlAttempts++
					mu.Unlock()
				}
			}
		}()
	}
dispatch:
	for _, p := range projects {
		select {
		case jobs <- p:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if e := tbinventory.ExportAll(db, out); e != nil {
		fmt.Fprintln(os.Stderr, e)
		failures++
	}
	s, _ := tbinventory.BuildSummary(db)
	status := "success"
	if failures > 0 {
		status = "partial"
	}
	_, _ = db.SQL.Exec(`UPDATE tb_crawl_runs SET finished_at=?,status=?,project_count=?,folder_count=?,file_count=?,total_size_bytes=?,error_count=? WHERE run_id=?`, time.Now().UTC().Format(time.RFC3339), status, s.ProjectCount, s.FolderCount, s.FileCount, s.TotalSizeBytes, failures, runID)
	fmt.Printf("projects=%d folders=%d files=%d bytes=%d status=%s\n", s.ProjectCount, s.FolderCount, s.FileCount, s.TotalSizeBytes, status)
	if authFailures > 0 && authFailures == crawlAttempts {
		return 1
	}
	if failures > 0 {
		return 2
	}
	return 0
}
func summary(args []string) int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	path := fs.String("db", "./output/teambition/tb_inventory.sqlite", "SQLite path")
	if fs.Parse(args) != nil {
		return 1
	}
	db, e := tbinventory.OpenDB(*path)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer db.Close()
	s, e := tbinventory.BuildSummary(db)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(b))
	return 0
}
func exportCmd(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	path := fs.String("db", "./output/teambition/tb_inventory.sqlite", "SQLite path")
	out := fs.String("output", "./output/teambition", "output directory")
	if fs.Parse(args) != nil {
		return 1
	}
	db, e := tbinventory.OpenDB(*path)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer db.Close()
	if e = tbinventory.ExportAll(db, *out); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	return 0
}
func doctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	pid := fs.String("project-id", "", "optional project")
	projectURL := fs.String("project-url", "", "full Teambition project files URL")
	parentID := fs.String("parent-id", "", "starting collection ID")
	rawResponse := fs.Bool("raw-response", false, "print the raw Teambition JSON response")
	page := fs.Int("page-size", 50, "page size")
	if fs.Parse(args) != nil {
		return 1
	}
	cfg := tbinventory.LoadConfig()
	if e := cfg.Validate(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := tbinventory.NewSDKClient(cfg)
	if *projectURL != "" {
		ref, err := tbinventory.ParseProjectFilesURL(*projectURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "doctor failed:", err)
			return 1
		}
		if *pid != "" && *pid != ref.ProjectID {
			fmt.Fprintln(os.Stderr, "doctor failed: --project-id does not match --project-url")
			return 1
		}
		*pid = ref.ProjectID
		if *parentID == "" {
			*parentID = ref.ParentID
		}
	}
	if *pid == "" && *parentID != "" {
		fmt.Fprintln(os.Stderr, "doctor failed: --parent-id requires --project-id or --project-url")
		return 1
	}
	if *pid != "" {
		p, status, e := c.ListFiles(ctx, *pid, *parentID, "", tbinventory.ListOptions{PageSize: *page})
		printDiagnostics(*pid, *parentID, p, status, *rawResponse)
		if e != nil {
			fmt.Fprintf(os.Stderr, "doctor failed status=%d: %v\n", status, e)
			return 1
		}
		fmt.Printf("doctor ok: project=%s parent=%s folders=%d files=%d (metadata only)\n", *pid, valueOrRoot(*parentID), len(p.Collections), len(p.Works))
		return 0
	}
	p, e := c.ListProjects(ctx, *page, cfg.OperatorID)
	if e != nil {
		fmt.Fprintln(os.Stderr, "doctor failed:", e)
		return 1
	}
	fmt.Printf("doctor ok: org=%s readable_projects=%d (metadata only)\n", cfg.OrgID, len(p))
	return 0
}
func readProjectFile(path string) ([]string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		v := strings.TrimSpace(s.Text())
		if v != "" && !strings.HasPrefix(v, "#") {
			out = append(out, v)
		}
	}
	return out, s.Err()
}
func dedupe(v []string) []string {
	m := map[string]bool{}
	var out []string
	for _, x := range v {
		x = strings.TrimSpace(x)
		if x != "" && !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	return out
}
func mergeProjects(a, b []tbinventory.Project) []tbinventory.Project {
	m := map[string]int{}
	for i, p := range a {
		m[p.ID] = i
	}
	for _, p := range b {
		if i, ok := m[p.ID]; ok {
			if a[i].RootParentID != "" && p.RootParentID == "" {
				p.RootParentID = a[i].RootParentID
			}
			if strings.Contains(a[i].URL, "/works") && !strings.Contains(p.URL, "/works") {
				p.URL = a[i].URL
			}
			a[i] = p
		} else {
			m[p.ID] = len(a)
			a = append(a, p)
		}
	}
	return a
}
func isAuthError(e *tbinventory.APIError) bool {
	if e.Status == 401 || e.Status == 403 {
		return true
	}
	return e.BusinessCode != nil && (*e.BusinessCode == 401 || *e.BusinessCode == 403)
}
func valueOrRoot(v string) string {
	if v == "" {
		return "<project-root>"
	}
	return v
}
func printDiagnostics(projectID, parentID string, p tbinventory.Page, status int, raw bool) {
	code := "<absent>"
	if p.Diagnostics.BusinessCode != nil {
		code = fmt.Sprintf("%.0f", *p.Diagnostics.BusinessCode)
	}
	fmt.Printf("doctor response: http_status=%d business_code=%s request_id=%q project=%s parent=%s next_page_token=%q folders=%d files=%d\n", status, code, p.Diagnostics.RequestID, projectID, valueOrRoot(parentID), p.NextPageToken, len(p.Collections), len(p.Works))
	if p.Diagnostics.ErrorMessage != "" {
		fmt.Printf("doctor error_message: %s\n", p.Diagnostics.ErrorMessage)
	}
	if raw {
		body := p.Diagnostics.RawResponse
		if body == "" {
			body = "<empty>"
		}
		fmt.Printf("doctor raw_response:\n%s\n", body)
	}
}
func projectSucceeded(db *tbinventory.DB, pid string) bool {
	var s string
	e := db.SQL.QueryRow(`SELECT crawl_status FROM tb_projects WHERE project_id=?`, pid).Scan(&s)
	return e == nil && s == "success"
}
func newLogger(level string) (*slog.Logger, bool) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil, false
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), true
}
