package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

type DiscoveredWorkspace struct {
	ID   string `json:"workspace_id"`
	Name string `json:"workspace_name"`
	URL  string `json:"url"`
}

type TemplateBatchOptions struct {
	HomeURL     string
	OutputRoot  string
	ProfileDir  string
	Overwrite   bool
	RetryFailed bool
	DryRun      bool
}

type TemplateWorkspaceResult struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	URL           string `json:"url"`
	Templates     int    `json:"templates"`
	Succeeded     int    `json:"succeeded"`
	Failed        int    `json:"failed"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

type TemplateBatchResult struct {
	SchemaVersion        string                    `json:"schema_version"`
	SourceSystem         string                    `json:"source_system"`
	ResourceType         string                    `json:"resource_type"`
	ExportedAt           string                    `json:"exported_at"`
	Checksum             string                    `json:"checksum"`
	Warnings             []string                  `json:"warnings"`
	WorkspaceCount       int                       `json:"workspace_count"`
	Succeeded            int                       `json:"succeeded"`
	Failed               int                       `json:"failed"`
	ValidationReportPath string                    `json:"validation_report_path,omitempty"`
	Validation           *TemplateValidationReport `json:"validation,omitempty"`
	Entries              []TemplateWorkspaceResult `json:"entries"`
}

// ExportAllTemplates logs in once, discovers every workspace on the Thoughts
// home page, and exports templates using the same authenticated session.
func ExportAllTemplates(opts TemplateBatchOptions) (TemplateBatchResult, error) {
	result := TemplateBatchResult{SchemaVersion: "thoughts-template-batch-export/v2", SourceSystem: "thoughts", ResourceType: "template", ExportedAt: time.Now().Format(time.RFC3339), Warnings: []string{}, Entries: []TemplateWorkspaceResult{}}
	if opts.HomeURL == "" {
		opts.HomeURL = "https://thoughts.teambition.com/"
	}
	if opts.OutputRoot == "" {
		opts.OutputRoot = "exports/templates-all"
	}
	if opts.ProfileDir == "" {
		opts.ProfileDir = defaultTemplateBrowserProfileDir()
	}
	workspaces, cookie, err := discoverWorkspacesWithLogin(opts.HomeURL, opts.ProfileDir)
	if err != nil {
		return result, err
	}
	discoveredCount := len(workspaces)
	storedWorkspaces, storedErr := storedTemplateWorkspaces(opts.OutputRoot)
	if storedErr != nil {
		result.Warnings = append(result.Warnings, "recover stored workspaces: "+storedErr.Error())
	}
	workspaces = deduplicateWorkspaces(append(workspaces, storedWorkspaces...))
	if len(workspaces) > discoveredCount {
		result.Warnings = append(result.Warnings, fmt.Sprintf("homepage exposed %d workspaces; recovered %d additional workspaces from existing manifests", discoveredCount, len(workspaces)-discoveredCount))
	}
	result.WorkspaceCount = len(workspaces)
	manifestPath := filepath.Join(opts.OutputRoot, "workspaces_manifest.json")
	if err := os.MkdirAll(opts.OutputRoot, 0755); err != nil {
		return result, err
	}
	for _, workspace := range workspaces {
		entry := TemplateWorkspaceResult{WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, URL: workspace.URL, Status: "running"}
		exportResult, exportErr := ExportTemplates(TemplateExportOptions{URL: workspace.URL, OutputRoot: opts.OutputRoot, Cookie: cookie, Overwrite: opts.Overwrite, RetryFailed: opts.RetryFailed, DryRun: opts.DryRun})
		entry.Templates, entry.Succeeded, entry.Failed = exportResult.Templates, exportResult.Succeeded, exportResult.Failed
		if exportErr != nil {
			entry.Status, entry.Error = "failed", exportErr.Error()
			result.Failed++
		} else if exportResult.Failed > 0 {
			entry.Status = "partial"
			result.Failed++
		} else {
			entry.Status = "success"
			result.Succeeded++
		}
		result.Entries = append(result.Entries, entry)
		entriesJSON, _ := json.Marshal(result.Entries)
		result.Checksum = sha256Hex(entriesJSON)
		if err := writeJSONFile(manifestPath, result, 0644); err != nil {
			return result, err
		}
	}
	report, validationErr := ValidateTemplateExport(opts.OutputRoot)
	if validationErr != nil {
		result.Warnings = append(result.Warnings, "validation report: "+validationErr.Error())
	} else {
		validationPath := filepath.Join(opts.OutputRoot, "validation_report.json")
		if err := writeJSONFile(validationPath, report, 0644); err != nil {
			result.Warnings = append(result.Warnings, "write validation report: "+err.Error())
		} else {
			result.ValidationReportPath = relativeExportPath(opts.OutputRoot, validationPath)
			result.Validation = &report
		}
	}
	entriesJSON, _ := json.Marshal(result.Entries)
	result.Checksum = sha256Hex(entriesJSON)
	if err := writeJSONFile(manifestPath, result, 0644); err != nil {
		return result, err
	}
	return result, nil
}

func discoverWorkspacesWithLogin(homeURL, profileDir string) ([]DiscoveredWorkspace, string, error) {
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return nil, "", err
	}
	cacheDir, err := os.MkdirTemp("", "chromedp-template-batch-cache")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(cacheDir)
	execPath := FindExecPath()
	if execPath == "" {
		return nil, "", errors.New("chrome path is not found")
	}
	procCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(procCtx, execPath, "--no-first-run", "--no-default-browser-check", "--disable-gpu", "--no-sandbox", "--user-data-dir="+profileDir, "--disk-cache-dir="+cacheDir, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	wsURL, err := ReadOutput(stderr, nil, nil)
	if err != nil {
		return nil, "", err
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), wsURL)
	defer allocCancel()
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()
	if err := chromedp.Run(taskCtx, chromedp.Navigate(homeURL)); err != nil {
		return nil, "", fmt.Errorf("navigate Thoughts home: %w", err)
	}
	cookie, err := WaitLoginReturnCookieString(taskCtx, "TB_ACCESS_TOKEN")
	if err != nil {
		return nil, "", err
	}
	deadline := time.Now().Add(30 * time.Second)
	var discovered []DiscoveredWorkspace
	stableRounds := 0
	for time.Now().Before(deadline) {
		var raw []DiscoveredWorkspace
		expression := `Array.from(document.querySelectorAll('a[href*="/workspaces/"]')).map(a => { const m = a.href.match(/\/workspaces\/([a-f0-9]+)/); return m ? {workspace_id:m[1], workspace_name:(a.innerText||'').split('\n').map(v=>v.trim()).filter(Boolean).slice(-2)[0]||'', url:'https://thoughts.teambition.com/workspaces/'+m[1]+'/overview'} : null }).filter(Boolean)`
		if err := chromedp.Run(taskCtx, chromedp.Evaluate(expression, &raw)); err == nil {
			before := len(discovered)
			discovered = deduplicateWorkspaces(append(discovered, raw...))
			if len(discovered) == before && len(discovered) > 0 {
				stableRounds++
			} else {
				stableRounds = 0
			}
			if stableRounds >= 6 {
				break
			}
		}
		scrollExpression := `(() => { window.scrollBy(0, Math.max(window.innerHeight * 0.8, 400)); for (const el of document.querySelectorAll('*')) { if (el.scrollHeight > el.clientHeight + 20) el.scrollTop = Math.min(el.scrollTop + Math.max(el.clientHeight * 0.8, 300), el.scrollHeight); } return true })()`
		var ignored bool
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(scrollExpression, &ignored))
		time.Sleep(500 * time.Millisecond)
	}
	if len(discovered) == 0 {
		return nil, "", errors.New("no workspace links found on Thoughts home page")
	}
	return discovered, cookie, nil
}

func storedTemplateWorkspaces(outputRoot string) ([]DiscoveredWorkspace, error) {
	workspaces := []DiscoveredWorkspace{}
	if _, err := os.Stat(outputRoot); errors.Is(err, os.ErrNotExist) {
		return workspaces, nil
	}
	err := filepath.Walk(outputRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || info.Name() != "templates_manifest.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest TemplateManifestDocument
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.WorkspaceID != "" {
			workspaces = append(workspaces, DiscoveredWorkspace{ID: manifest.WorkspaceID, Name: manifest.WorkspaceName, URL: "https://thoughts.teambition.com/workspaces/" + manifest.WorkspaceID + "/overview"})
		}
		return nil
	})
	return deduplicateWorkspaces(workspaces), err
}

func defaultTemplateBrowserProfileDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return filepath.Join(".thoughtsexport", "template-browser-profile")
	}
	return filepath.Join(configDir, "thoughtsexport", "template-browser-profile")
}

func deduplicateWorkspaces(input []DiscoveredWorkspace) []DiscoveredWorkspace {
	byID := map[string]DiscoveredWorkspace{}
	for _, workspace := range input {
		if workspace.ID == "" {
			continue
		}
		old, exists := byID[workspace.ID]
		if !exists || len(strings.TrimSpace(workspace.Name)) > len(strings.TrimSpace(old.Name)) {
			byID[workspace.ID] = workspace
		}
	}
	result := make([]DiscoveredWorkspace, 0, len(byID))
	for _, workspace := range byID {
		result = append(result, workspace)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
