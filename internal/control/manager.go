package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"thoughtsexport/internal/tbinventory"
	"thoughtsexport/internal/tbweb"
	"thoughtsexport/internal/teambition/collector"
	"thoughtsexport/internal/teambition/fileadapters"
	"thoughtsexport/internal/teambition/filecollector"
	"thoughtsexport/internal/teambition/taskprobe"
	"thoughtsexport/libs/logic"
)

type Manager struct {
	store        *Store
	artifactRoot string
	dataRoot     string
	workerSlots  chan struct{}
	thoughts     *thoughtsSession
	usage        *UsageTracker
	mu           sync.Mutex
	cancels      map[string]context.CancelFunc
	wg           sync.WaitGroup
}

func NewManager(store *Store, artifactRoot, dataRoot string, concurrency int, trackers ...*UsageTracker) *Manager {
	if concurrency < 1 {
		concurrency = 1
	}
	usage := NewUsageTracker(store, UsageConfigFromEnv())
	if len(trackers) > 0 && trackers[0] != nil {
		usage = trackers[0]
	}
	usage.RetryPending()
	return &Manager{store: store, artifactRoot: artifactRoot, dataRoot: dataRoot, workerSlots: make(chan struct{}, concurrency), thoughts: newThoughtsSession(dataRoot), usage: usage, cancels: map[string]context.CancelFunc{}}
}

func Modules() []ModuleInfo {
	return []ModuleInfo{
		{ID: ModuleThoughts, Name: "所思导出", Description: "导出单个知识库正文，可选包含模板、资源、链接和校验报告。", Capabilities: []string{"DOCX / HTML", "模板与资源", "断点与失败记录"}, Credential: "浏览器登录态"},
		{ID: ModuleTBFiles, Name: "TB 文件下载", Description: "发现项目文件目录并下载文件本体，生成校验和与可浏览目录镜像。", Capabilities: []string{"目录发现", "断点恢复", "SHA-256 校验"}, Credential: "浏览器登录态或 OpenAPI"},
		{ID: ModuleTBTasks, Name: "TB 任务采集", Description: "采集项目、阶段、用户、任务、任务关系和任务关联文件。", Capabilities: []string{"任务与阶段", "用户与关系", "关联文件"}, Credential: "MCP 环境变量"},
	}
}

func moduleByID(id string) (ModuleInfo, bool) {
	for _, module := range Modules() {
		if module.ID == id {
			return module, true
		}
	}
	return ModuleInfo{}, false
}

func (m *Manager) Preflight(moduleID string, input map[string]any) PreflightResult {
	checks := []Check{m.outputDirectoryCheck(input)}
	warnings := []string{}
	switch moduleID {
	case ModuleThoughts:
		checks = append(checks, checkThoughtsURL(textValue(input, "url")))
		format := defaultText(textValue(input, "format"), "docx")
		checks = append(checks, Check{Name: "format", OK: format == "docx" || format == "html", Message: "正文格式必须是 DOCX 或 HTML"})
		if logic.BrowserLoginCredentialsFromEnv().Configured() {
			warnings = append(warnings, "所思登录态会复用持久 Profile；过期时使用服务端账号自动续期。")
		} else {
			warnings = append(warnings, "所思登录态会复用持久 Profile；未配置自动登录账号时，过期后需要人工登录。")
		}
	case ModuleTBFiles:
		checks = append(checks, checkTBFilesURL(textValue(input, "project_url")))
		source := defaultText(textValue(input, "source"), "browser")
		checks = append(checks, Check{Name: "source", OK: source == "browser" || source == "sdk", Message: "数据来源必须是浏览器或 OpenAPI SDK"})
		checks = append(checks, numberCheck("concurrency", input, 2, 1, 8, "下载并发必须在 1 到 8 之间"))
		checks = append(checks, numberCheck("page_size", input, 100, 10, 200, "单页数量必须在 10 到 200 之间"))
		if source == "sdk" {
			cfg := tbinventory.LoadConfig()
			checks = append(checks, Check{Name: "credentials", OK: cfg.Validate() == nil, Message: "TB_APP_ID、TB_APP_SECRET、TB_ORG_ID 已配置"})
		} else if source == "browser" {
			checks = append(checks, Check{Name: "browser", OK: logic.FindExecPath() != "", Message: "已检测到 Chrome 或 Edge"})
			warnings = append(warnings, "浏览器模式会打开可见浏览器，并复用本机专用登录资料。")
		}
	case ModuleTBTasks:
		checks = append(checks, checkTaskProject(textValue(input, "project")))
		checks = append(checks, numberCheck("concurrency", input, 2, 1, 8, "采集并发必须在 1 到 8 之间"))
		if since := textValue(input, "since"); since != "" {
			_, err := time.Parse(time.RFC3339, since)
			checks = append(checks, Check{Name: "since", OK: err == nil, Message: "更新时间下限必须是 RFC3339 时间"})
		}
		cfg := taskprobe.LoadConfig()
		checks = append(checks, Check{Name: "credentials", OK: cfg.Validate() == nil, Message: "TEAMBITION_MCP_HOST 与 TEAMBITION_MCP_TOKEN 已配置"})
	default:
		checks = append(checks, Check{Name: "module", OK: false, Message: "未知模块"})
	}
	ok := true
	for _, check := range checks {
		if !check.OK {
			ok = false
		}
	}
	return PreflightResult{OK: ok, Checks: checks, Warnings: warnings}
}

func (m *Manager) Submit(moduleID string, input map[string]any, owners ...JobOwner) (Job, error) {
	module, ok := moduleByID(moduleID)
	if !ok {
		return Job{}, errors.New("unknown module")
	}
	preflight := m.Preflight(moduleID, input)
	if !preflight.OK {
		return Job{}, errors.New("preflight failed")
	}
	id, err := newJobID()
	if err != nil {
		return Job{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Job{}, err
	}
	owner := JobOwner{}
	if len(owners) > 0 {
		owner = owners[0]
	}
	artifactPath := filepath.Join(m.artifactRoot, module.ID, fmt.Sprintf("user-%d", owner.ID), id)
	job := Job{ID: id, ModuleID: module.ID, ModuleName: module.Name, Status: "queued", Stage: "queued", Message: "任务已进入队列", Input: raw, ArtifactPath: artifactPath, OwnerID: owner.ID, OwnerName: owner.Name, CreatedAt: time.Now().UTC()}
	if err := m.store.Create(job); err != nil {
		return Job{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[job.ID] = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go m.execute(ctx, cancel, job, input)
	return job, nil
}

func (m *Manager) Cancel(id string) error {
	changed, err := m.store.MarkCancelling(id)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("job is not cancellable")
	}
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels {
		cancels = append(cancels, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return m.usage.Wait(ctx)
}

func (m *Manager) execute(ctx context.Context, cancel context.CancelFunc, job Job, input map[string]any) {
	defer m.wg.Done()
	select {
	case m.workerSlots <- struct{}{}:
	case <-ctx.Done():
		_ = m.store.Finish(job.ID, "cancelled", "cancelled", "任务已取消", nil, ctx.Err())
		m.cleanupCancel(job.ID, cancel)
		return
	}
	defer func() { <-m.workerSlots }()
	defer m.cleanupCancel(job.ID, cancel)
	if ctx.Err() != nil {
		_ = m.store.Finish(job.ID, "cancelled", "cancelled", "任务已取消", nil, ctx.Err())
		return
	}
	_ = os.MkdirAll(job.ArtifactPath, 0755)
	_ = m.store.Start(job.ID, "starting", "正在准备采集环境")
	result, err := m.run(ctx, job, input)
	if errors.Is(ctx.Err(), context.Canceled) {
		_ = m.store.Finish(job.ID, "cancelled", "cancelled", "任务已取消", result, ctx.Err())
		return
	}
	if err != nil {
		_ = m.store.Finish(job.ID, "failed", "failed", "任务执行失败", result, err)
		return
	}
	status := "succeeded"
	message := "任务执行完成"
	if value, ok := result["status"].(string); ok && value == "partial" {
		status, message = "partial", "任务完成，但存在部分失败"
	}
	if finishErr := m.store.Finish(job.ID, status, "finished", message, result, nil); finishErr == nil && status == "succeeded" {
		m.usage.RecordSuccessfulJob(Job{ID: job.ID, ModuleID: job.ModuleID, Status: status, ArtifactPath: job.ArtifactPath, OwnerID: job.OwnerID, OwnerName: job.OwnerName}, result)
	}
}

func (m *Manager) run(ctx context.Context, job Job, input map[string]any) (map[string]any, error) {
	switch job.ModuleID {
	case ModuleThoughts:
		progress := func(stage, message string) { _ = m.store.Progress(job.ID, stage, message) }
		progress("starting", "正在准备所思导出")
		cookie, err := m.thoughts.Acquire(ctx, textValue(input, "url"), progress)
		if err != nil {
			return map[string]any{"status": "failed", "output": job.ArtifactPath}, err
		}
		err = logic.ExportWorkspace(logic.ExportOptions{Context: ctx, Cookie: cookie, LoginTimeout: 5 * time.Minute, ProfileDir: filepath.Join(m.dataRoot, "browser-profiles", "thoughts"), Progress: progress, URL: textValue(input, "url"), OutputRoot: job.ArtifactPath, LogRoot: filepath.Join(job.ArtifactPath, "logs"), Format: defaultText(textValue(input, "format"), "docx"), IncludeTemplates: boolValue(input, "include_templates"), Overwrite: boolValue(input, "overwrite"), RetryFailed: boolValue(input, "retry_failed"), DryRun: boolValue(input, "dry_run")})
		return map[string]any{"status": "succeeded", "output": job.ArtifactPath, "dry_run": boolValue(input, "dry_run")}, err
	case ModuleTBFiles:
		return m.runTBFiles(ctx, job, input)
	case ModuleTBTasks:
		return m.runTBTasks(ctx, job, input)
	default:
		return nil, errors.New("unknown module")
	}
}

func (m *Manager) runTBFiles(ctx context.Context, job Job, input map[string]any) (map[string]any, error) {
	projectURL := textValue(input, "project_url")
	ref, err := tbinventory.ParseProjectFilesURL(projectURL)
	if err != nil {
		return nil, err
	}
	cfg := filecollector.Config{ProjectID: ref.ProjectID, ProjectURL: projectURL, Output: job.ArtifactPath, Resume: boolValue(input, "resume"), IncludeRaw: boolValue(input, "include_raw"), PageSize: intValue(input, "page_size", 100), MaxFileSize: int64Value(input, "max_file_size"), Concurrency: intValue(input, "concurrency", 2), RetryFailedDownloads: boolValue(input, "retry_failed")}
	type fileSource interface {
		filecollector.PageSource
		filecollector.DownloadSource
	}
	var source fileSource
	downloadClient := http.DefaultClient
	var browser *tbweb.BrowserSession
	if defaultText(textValue(input, "source"), "browser") == "sdk" {
		sdkConfig := tbinventory.LoadConfig()
		if err := sdkConfig.Validate(); err != nil {
			return nil, err
		}
		source = fileadapters.SDK{Client: tbinventory.NewSDKClient(sdkConfig)}
	} else {
		_ = m.store.Progress(job.ID, "authentication", "正在打开浏览器并等待 Teambition 登录")
		profile := filepath.Join(m.dataRoot, "browser-profiles", "tb-files")
		var session tbweb.Session
		browser, session, err = tbweb.OpenBrowserSession(ctx, projectURL, profile, 10*time.Minute)
		if err != nil {
			return nil, err
		}
		defer browser.Close()
		client := tbweb.NewClient(session.CookieHeader, session.Referer)
		client.HTTP.Timeout = 15 * time.Minute
		source = fileadapters.Browser{Client: client}
		downloadClient = client.HTTP
	}
	_ = m.store.Progress(job.ID, "discovering", "正在发现项目目录和文件")
	discovered, err := filecollector.Discover(ctx, source, cfg)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"project_id": ref.ProjectID, "nodes": discovered.Nodes, "directories": discovered.Directories, "files": discovered.Files, "pages": discovered.Pages, "errors": discovered.Errors, "output": filepath.Join(job.ArtifactPath, "teambition-file-collector", ref.ProjectID)}
	if boolValue(input, "download_assets") {
		_ = m.store.Progress(job.ID, "downloading", fmt.Sprintf("目录发现完成，共 %d 个文件，正在下载", discovered.Files))
		downloaded, downloadErr := filecollector.Download(ctx, source, downloadClient, cfg)
		result["downloaded"] = downloaded.Downloaded
		result["skipped"] = downloaded.Skipped
		result["failed"] = downloaded.Failed
		result["bytes"] = downloaded.Bytes
		if downloadErr != nil {
			return result, downloadErr
		}
		if downloaded.Failed > 0 {
			result["status"] = "partial"
		}
	}
	if discovered.Errors > 0 || discovered.UnresolvedParents > 0 {
		result["status"] = "partial"
	} else if result["status"] == nil {
		result["status"] = "succeeded"
	}
	return result, nil
}

func (m *Manager) runTBTasks(ctx context.Context, job Job, input map[string]any) (map[string]any, error) {
	projectID, projectURL, err := taskProjectRef(textValue(input, "project"))
	if err != nil {
		return nil, err
	}
	var since time.Time
	if value := textValue(input, "since"); value != "" {
		since, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, fmt.Errorf("invalid since time: %w", err)
		}
	}
	cfg := taskprobe.LoadConfig()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	_ = m.store.Progress(job.ID, "collecting", "正在采集项目任务、阶段、用户和关联文件")
	manifest, err := collector.New(taskprobe.NewClient(cfg), collector.Config{ProjectID: projectID, ProjectURL: projectURL, Output: job.ArtifactPath, Resume: boolValue(input, "resume"), IncludeRaw: boolValue(input, "include_raw"), DownloadAssets: boolValue(input, "download_assets"), Since: since, Concurrency: intValue(input, "concurrency", 2)}).Run(ctx)
	result := map[string]any{"status": manifest.Status, "project_id": projectID, "counts": manifest.Counts, "coverage": manifest.Coverage, "warnings": manifest.Warnings, "output": filepath.Join(job.ArtifactPath, "teambition-collector", projectID)}
	if err == nil && manifest.Status == "failed" {
		err = errors.New("task collector validation failed")
	}
	return result, err
}

func (m *Manager) cleanupCancel(id string, cancel context.CancelFunc) {
	cancel()
	m.mu.Lock()
	delete(m.cancels, id)
	m.mu.Unlock()
}

func checkThoughtsURL(raw string) Check {
	u, err := url.Parse(raw)
	ok := err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "thoughts.teambition.com") && strings.Contains(u.Path, "/workspaces/")
	return Check{Name: "url", OK: ok, Message: "所思地址必须是 thoughts.teambition.com/workspaces/..."}
}

func checkTBFilesURL(raw string) Check {
	_, err := tbinventory.ParseProjectFilesURL(raw)
	return Check{Name: "url", OK: err == nil, Message: "TB 文件地址必须包含 /project/{id}/works/{rootId}"}
}

func checkTaskProject(raw string) Check {
	_, _, err := taskProjectRef(raw)
	return Check{Name: "project", OK: err == nil, Message: "请输入有效的项目 ID 或 Teambition 项目地址"}
}

func taskProjectRef(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", errors.New("project is required")
	}
	if !strings.Contains(value, "://") {
		if len(value) < 6 {
			return "", "", errors.New("invalid project id")
		}
		return value, "", nil
	}
	u, err := url.Parse(value)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "teambition.com") {
		return "", "", errors.New("invalid Teambition project URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		if part == "project" && i+1 < len(parts) && len(parts[i+1]) >= 6 {
			return parts[i+1], value, nil
		}
	}
	return "", "", errors.New("project URL must contain /project/{projectId}")
}

func directoryWritable(path string) bool {
	if err := os.MkdirAll(path, 0755); err != nil {
		return false
	}
	file, err := os.CreateTemp(path, ".write-check-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

func (m *Manager) outputRoot(input map[string]any) (string, bool, error) {
	raw := textValue(input, "output_dir")
	if raw == "" {
		return filepath.Clean(m.artifactRoot), false, nil
	}
	if !filepath.IsAbs(raw) {
		return "", true, errors.New("output directory must be an absolute path")
	}
	return filepath.Clean(raw), true, nil
}

func (m *Manager) outputDirectoryCheck(input map[string]any) Check {
	if !directoryWritable(m.artifactRoot) {
		return Check{Name: "output", OK: false, Message: "服务器归档目录无法创建或写入"}
	}
	return Check{Name: "output", OK: true, Message: "任务结果将保存到服务器归档目录"}
}

func newJobID() (string, error) {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(random[:]), nil
}

func textValue(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func defaultText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func boolValue(input map[string]any, key string) bool {
	value, _ := input[key].(bool)
	return value
}

func intValue(input map[string]any, key string, fallback int) int {
	value, ok := input[key].(float64)
	if !ok || value < 1 {
		return fallback
	}
	return int(value)
}

func int64Value(input map[string]any, key string) int64 {
	value, _ := input[key].(float64)
	if value < 0 {
		return 0
	}
	return int64(value)
}

func numberCheck(name string, input map[string]any, fallback, minimum, maximum int, message string) Check {
	value := intValue(input, name, fallback)
	return Check{Name: name, OK: value >= minimum && value <= maximum, Message: message}
}
