package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"thoughtsexport/internal/tbinventory"
	"thoughtsexport/internal/tbweb"
	"time"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tb-web-inventory <doctor|inventory|summary|export>")
		return 1
	}
	switch os.Args[1] {
	case "doctor":
		return doctor(os.Args[2:])
	case "inventory":
		return inventory(os.Args[2:])
	case "summary":
		return summary(os.Args[2:])
	case "export":
		return exportCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		return 1
	}
}

func doctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	projectURL := fs.String("project-url", "", "full Teambition project files URL")
	parentID := fs.String("parent-id", "", "override starting collection ID")
	pageSize := fs.Int("page-size", 50, "page size")
	loginTimeout := fs.Duration("login-timeout", 10*time.Minute, "time allowed for browser login")
	rawResponse := fs.Bool("raw-response", false, "print raw metadata responses")
	if fs.Parse(args) != nil {
		return 1
	}
	ref, ok := parseURL(*projectURL, *parentID)
	if !ok {
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fmt.Println("Opening a temporary browser. Complete Teambition login there; this can take up to", *loginTimeout)
	session, err := tbweb.AcquireSession(ctx, *projectURL, *loginTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor failed:", err)
		return 1
	}
	client := tbweb.NewClient(session.CookieHeader, session.Referer)
	page, status, err := client.ListFiles(ctx, ref.ProjectID, ref.ParentID, "", tbinventory.ListOptions{PageSize: *pageSize})
	printDiagnostics(ref, page, status, *rawResponse)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor failed:", err)
		return 1
	}
	fmt.Printf("doctor ok: project=%s parent=%s folders=%d files=%d (metadata only)\n", ref.ProjectID, valueOrRoot(ref.ParentID), len(page.Collections), len(page.Works))
	return 0
}

func inventory(args []string) int {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	projectURL := fs.String("project-url", "", "full Teambition project files URL")
	parentID := fs.String("parent-id", "", "override starting collection ID")
	output := fs.String("output", "./output/teambition-web", "output directory")
	dbPath := fs.String("db", "", "SQLite path")
	pageSize := fs.Int("page-size", 100, "page size")
	includeArchived := fs.Bool("include-archived", false, "include archived files")
	loginTimeout := fs.Duration("login-timeout", 10*time.Minute, "time allowed for browser login")
	forceRefresh := fs.Bool("force-refresh", false, "refresh even when previous data exists")
	if fs.Parse(args) != nil {
		return 1
	}
	ref, ok := parseURL(*projectURL, *parentID)
	if !ok {
		return 1
	}
	if *pageSize < 1 {
		fmt.Fprintln(os.Stderr, "--page-size must be at least 1")
		return 1
	}
	if *dbPath == "" {
		*dbPath = filepath.Join(*output, "tb_inventory.sqlite")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fmt.Println("Opening a temporary browser. Complete Teambition login there; this can take up to", *loginTimeout)
	session, err := tbweb.AcquireSession(ctx, *projectURL, *loginTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inventory failed:", err)
		return 1
	}
	db, err := tbinventory.OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inventory failed:", err)
		return 1
	}
	defer db.Close()

	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	project := tbinventory.Project{ID: ref.ProjectID, URL: *projectURL, RootParentID: ref.ParentID}
	configJSON, _ := json.Marshal(map[string]any{
		"mode": "browser-session", "project": project, "includeArchived": *includeArchived,
		"pageSize": *pageSize, "forceRefresh": *forceRefresh,
	})
	_, _ = db.SQL.Exec(`INSERT INTO tb_crawl_runs(run_id,started_at,status,org_id,include_archived,config_json) VALUES(?,?,?,?,?,?)`,
		runID, time.Now().UTC().Format(time.RFC3339), "running", "browser-session", *includeArchived, string(configJSON))

	crawler := &tbinventory.Crawler{
		Files: tbweb.NewClient(session.CookieHeader, session.Referer), DB: db, RunID: runID,
		Options: tbinventory.CrawlOptions{IncludeArchived: *includeArchived, SkipForbiddenFolders: true, PageSize: *pageSize, Retries: 4, ForceRefresh: *forceRefresh},
	}
	crawlErr := crawler.CrawlProject(ctx, project)
	var partialErr *tbinventory.PartialCrawlError
	isPartial := errors.As(crawlErr, &partialErr)
	exportErr := tbinventory.ExportAll(db, *output)
	summaryValue, summaryErr := tbinventory.BuildSummary(db)
	status := "success"
	errorCount := 0
	if isPartial {
		status = "partial"
		errorCount = partialErr.SkippedFolders
	} else if crawlErr != nil || exportErr != nil || summaryErr != nil {
		status = "failed"
		errorCount = 1
	}
	_, _ = db.SQL.Exec(`UPDATE tb_crawl_runs SET finished_at=?,status=?,project_count=?,folder_count=?,file_count=?,total_size_bytes=?,error_count=? WHERE run_id=?`,
		time.Now().UTC().Format(time.RFC3339), status, summaryValue.ProjectCount, summaryValue.FolderCount, summaryValue.FileCount, summaryValue.TotalSizeBytes, errorCount, runID)
	if crawlErr != nil && !isPartial {
		fmt.Fprintln(os.Stderr, "inventory failed:", crawlErr)
		return 1
	}
	if exportErr != nil {
		fmt.Fprintln(os.Stderr, "export failed:", exportErr)
		return 1
	}
	if summaryErr != nil {
		fmt.Fprintln(os.Stderr, "summary failed:", summaryErr)
		return 1
	}
	if isPartial {
		fmt.Printf("inventory partial: skipped_forbidden_folders=%d; details=%s\n", partialErr.SkippedFolders, filepath.Join(*output, "tb_errors.csv"))
	}
	fmt.Printf("projects=%d folders=%d files=%d bytes=%d status=%s output=%s\n", summaryValue.ProjectCount, summaryValue.FolderCount, summaryValue.FileCount, summaryValue.TotalSizeBytes, status, *output)
	return 0
}

func summary(args []string) int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	dbPath := fs.String("db", "./output/teambition-web/tb_inventory.sqlite", "SQLite path")
	if fs.Parse(args) != nil {
		return 1
	}
	db, err := tbinventory.OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	value, err := tbinventory.BuildSummary(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(data))
	return 0
}

func exportCmd(args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	dbPath := fs.String("db", "./output/teambition-web/tb_inventory.sqlite", "SQLite path")
	output := fs.String("output", "./output/teambition-web", "output directory")
	if fs.Parse(args) != nil {
		return 1
	}
	db, err := tbinventory.OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	if err := tbinventory.ExportAll(db, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func parseURL(raw, parentOverride string) (tbinventory.ProjectFilesRef, bool) {
	if raw == "" {
		fmt.Fprintln(os.Stderr, "--project-url is required")
		return tbinventory.ProjectFilesRef{}, false
	}
	ref, err := tbinventory.ParseProjectFilesURL(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --project-url:", err)
		return tbinventory.ProjectFilesRef{}, false
	}
	if parentOverride != "" {
		ref.ParentID = parentOverride
	}
	return ref, true
}

func printDiagnostics(ref tbinventory.ProjectFilesRef, page tbinventory.Page, status int, raw bool) {
	code := "<absent>"
	if page.Diagnostics.BusinessCode != nil {
		code = fmt.Sprintf("%.0f", *page.Diagnostics.BusinessCode)
	}
	fmt.Printf("doctor response: http_status=%d business_code=%s request_id=%q project=%s parent=%s next_page=%q folders=%d files=%d\n",
		status, code, page.Diagnostics.RequestID, ref.ProjectID, valueOrRoot(ref.ParentID), page.NextPageToken, len(page.Collections), len(page.Works))
	if page.Diagnostics.ErrorMessage != "" {
		fmt.Println("doctor error_message:", page.Diagnostics.ErrorMessage)
	}
	if raw {
		fmt.Println("doctor raw_response:")
		fmt.Println(page.Diagnostics.RawResponse)
	}
}

func valueOrRoot(value string) string {
	if value == "" {
		return "<project-root>"
	}
	return value
}
