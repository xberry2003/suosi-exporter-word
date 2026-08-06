package main

import (
	"bytes"
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

type projectRecord struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	ProjectURL  string `json:"projectUrl"`
	RootParent  string `json:"rootParentId"`
}

type projectList struct {
	Projects []projectRecord `json:"projects"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("tb-web-inventory", flag.ContinueOnError)
	projectsJSON := fs.String("projects-json", "./tb_discovered_projects.json", "JSON file containing discovered project file-library URLs; crawl results are embedded back into this file")
	output := fs.String("output", "", "supporting SQLite/CSV/JSONL output directory (default: <projects-json-dir>/teambition-inventory)")
	dbPath := fs.String("db", "", "SQLite checkpoint path")
	profileDir := fs.String("profile-dir", "", "persistent browser profile directory")
	pageSize := fs.Int("page-size", 100, "page size")
	includeArchived := fs.Bool("include-archived", false, "include archived files")
	loginTimeout := fs.Duration("login-timeout", 10*time.Minute, "time allowed for the single browser login")
	forceRefresh := fs.Bool("force-refresh", false, "recrawl projects already marked success or partial")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *pageSize < 1 {
		fmt.Fprintln(os.Stderr, "--page-size must be at least 1")
		return 1
	}

	inputPath, err := filepath.Abs(*projectsJSON)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --projects-json:", err)
		return 1
	}
	projects, err := loadProjects(inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load project URL list failed:", err)
		return 1
	}
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, "load project URL list failed: no valid project file-library URLs")
		return 1
	}
	if *output == "" {
		*output = filepath.Join(filepath.Dir(inputPath), "teambition-inventory")
	}
	if *dbPath == "" {
		*dbPath = filepath.Join(*output, "tb_inventory.sqlite")
	}
	if *profileDir == "" {
		*profileDir = filepath.Join(*output, "browser-profile")
	}

	db, err := tbinventory.OpenDB(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open checkpoint database failed:", err)
		return 1
	}
	defer db.Close()
	completed, err := completedProjectIDs(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read checkpoint failed:", err)
		return 1
	}
	pending := make([]tbinventory.Project, 0, len(projects))
	for _, project := range projects {
		if !*forceRefresh && completed[project.ID] {
			continue
		}
		pending = append(pending, project)
	}
	if len(pending) == 0 {
		if err := exportCheckpoint(db, *output, inputPath); err != nil {
			fmt.Fprintln(os.Stderr, "export checkpoint failed:", err)
			return 1
		}
		fmt.Printf("all %d projects are already complete; results=%s\n", len(projects), inputPath)
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fmt.Printf("opening one persistent browser for %d pending projects; log in once if prompted\n", len(pending))
	browser, _, err := tbweb.OpenBrowserSession(ctx, pending[0].URL, *profileDir, *loginTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open browser session failed:", err)
		return 1
	}
	defer browser.Close()

	runID := time.Now().UTC().Format("20060102T150405.000000000Z")
	configJSON, _ := json.Marshal(map[string]any{
		"mode": "browser-url-list", "projectsJson": inputPath, "includeArchived": *includeArchived,
		"pageSize": *pageSize, "forceRefresh": *forceRefresh, "persistentProfile": *profileDir,
	})
	_, _ = db.SQL.Exec(`INSERT INTO tb_crawl_runs(run_id,started_at,status,org_id,include_archived,config_json) VALUES(?,?,?,?,?,?)`,
		runID, time.Now().UTC().Format(time.RFC3339), "running", "browser-session", *includeArchived, string(configJSON))

	partialCount := 0
	failed := make([]tbinventory.Project, 0)
	for index, project := range pending {
		status, crawlErr := crawlOne(ctx, browser, db, runID, project, *pageSize, *includeArchived, *forceRefresh, index+1, len(pending), false)
		if status == "partial" {
			partialCount++
		}
		if crawlErr != nil {
			failed = append(failed, project)
		}
		if err := exportCheckpoint(db, *output, inputPath); err != nil {
			fmt.Fprintln(os.Stderr, "export checkpoint failed:", err)
			return 1
		}
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "crawl interrupted; completed checkpoints were retained:", ctx.Err())
			return 1
		}
	}

	remaining := make([]tbinventory.Project, 0)
	if len(failed) > 0 {
		fmt.Printf("first pass complete; retrying %d failed projects once\n", len(failed))
		for index, project := range failed {
			_, crawlErr := crawlOne(ctx, browser, db, runID, project, *pageSize, *includeArchived, true, index+1, len(failed), true)
			if crawlErr != nil {
				remaining = append(remaining, project)
			}
			if err := exportCheckpoint(db, *output, inputPath); err != nil {
				fmt.Fprintln(os.Stderr, "export retry checkpoint failed:", err)
				return 1
			}
		}
	}

	summary, err := tbinventory.BuildSummary(db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build final summary failed:", err)
		return 1
	}
	status := "success"
	if summary.SkippedFolderCount > 0 || len(remaining) > 0 {
		status = "partial"
	}
	_, _ = db.SQL.Exec(`UPDATE tb_crawl_runs SET finished_at=?,status=?,project_count=?,folder_count=?,file_count=?,total_size_bytes=?,error_count=? WHERE run_id=?`,
		time.Now().UTC().Format(time.RFC3339), status, summary.ProjectCount, summary.FolderCount, summary.FileCount, summary.TotalSizeBytes, summary.SkippedFolderCount+len(remaining), runID)
	if err := exportCheckpoint(db, *output, inputPath); err != nil {
		fmt.Fprintln(os.Stderr, "final export failed:", err)
		return 1
	}
	fmt.Printf("batch complete: listed=%d attempted=%d partial=%d failed=%d folders=%d files=%d skipped_folders=%d skipped_files_known=%t results=%s\n",
		len(projects), len(pending), partialCount, len(remaining), summary.FolderCount, summary.FileCount, summary.SkippedFolderCount, summary.SkippedFilesKnown, inputPath)
	if len(remaining) > 0 {
		return 2
	}
	return 0
}

func loadProjects(path string) ([]tbinventory.Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var list projectList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	projects := make([]tbinventory.Project, 0, len(list.Projects))
	seen := make(map[string]bool)
	for index, record := range list.Projects {
		ref, err := tbinventory.ParseProjectFilesURL(record.ProjectURL)
		if err != nil {
			return nil, fmt.Errorf("projects[%d] %q: %w", index, record.ProjectName, err)
		}
		if record.ProjectID != "" && record.ProjectID != ref.ProjectID {
			return nil, fmt.Errorf("projects[%d]: projectId %q does not match URL project %q", index, record.ProjectID, ref.ProjectID)
		}
		if record.RootParent != "" && record.RootParent != ref.ParentID {
			return nil, fmt.Errorf("projects[%d]: rootParentId %q does not match URL root %q", index, record.RootParent, ref.ParentID)
		}
		if seen[ref.ProjectID] {
			continue
		}
		seen[ref.ProjectID] = true
		projects = append(projects, tbinventory.Project{ID: ref.ProjectID, Name: record.ProjectName, URL: record.ProjectURL, RootParentID: ref.ParentID})
	}
	return projects, nil
}

func completedProjectIDs(db *tbinventory.DB) (map[string]bool, error) {
	completed := make(map[string]bool)
	rows, err := db.SQL.Query(`SELECT project_id FROM tb_projects WHERE crawl_status IN ('success','partial')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, err
		}
		completed[projectID] = true
	}
	return completed, rows.Err()
}

func crawlOne(ctx context.Context, browser *tbweb.BrowserSession, db *tbinventory.DB, runID string, project tbinventory.Project, pageSize int, includeArchived, forceRefresh bool, index, total int, retry bool) (string, error) {
	label := "crawl"
	if retry {
		label = "retry"
	}
	fmt.Printf("%s project %d/%d id=%s name=%s url=%s\n", label, index, total, project.ID, project.Name, project.URL)
	session, err := browser.Navigate(ctx, project.URL)
	if err != nil {
		_ = db.UpsertProject(project, "failed", err.Error())
		_ = db.AddError(runID, project.ID, project.RootParentID, "navigation", project.ID, "Navigate", 0, err.Error(), 0)
		fmt.Fprintf(os.Stderr, "%s failed id=%s: %v\n", label, project.ID, err)
		return "failed", err
	}
	_, _ = db.SQL.Exec(`DELETE FROM tb_crawl_errors WHERE run_id=? AND project_id=?`, runID, project.ID)
	crawler := &tbinventory.Crawler{
		Files: tbweb.NewClient(session.CookieHeader, session.Referer), DB: db, RunID: runID,
		Options: tbinventory.CrawlOptions{IncludeArchived: includeArchived, SkipForbiddenFolders: true, PageSize: pageSize, Retries: 4, ForceRefresh: forceRefresh},
	}
	err = crawler.CrawlProject(ctx, project)
	var partialErr *tbinventory.PartialCrawlError
	if errors.As(err, &partialErr) {
		fmt.Printf("%s partial id=%s skipped_forbidden_folders=%d\n", label, project.ID, partialErr.SkippedFolders)
		return "partial", nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed id=%s: %v\n", label, project.ID, err)
		return "failed", err
	}
	fmt.Printf("%s success id=%s\n", label, project.ID)
	return "success", nil
}

func exportCheckpoint(db *tbinventory.DB, output, projectsJSON string) error {
	if err := tbinventory.ExportAll(db, output); err != nil {
		return err
	}
	summary, err := os.ReadFile(filepath.Join(output, "tb_summary.json"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(projectsJSON), "tb_summary.json"), summary, 0644); err != nil {
		return err
	}
	inventory, err := os.ReadFile(filepath.Join(output, "tb_inventory.json"))
	if err != nil {
		return err
	}
	original, err := os.ReadFile(projectsJSON)
	if err != nil {
		return err
	}
	original = bytes.TrimPrefix(original, []byte{0xEF, 0xBB, 0xBF})
	var root map[string]json.RawMessage
	if err := json.Unmarshal(original, &root); err != nil {
		return err
	}
	if !json.Valid(inventory) {
		return errors.New("generated tb_inventory.json is invalid")
	}
	root["crawl"] = json.RawMessage(inventory)
	merged, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projectsJSON, append(merged, '\n'), 0644)
}
