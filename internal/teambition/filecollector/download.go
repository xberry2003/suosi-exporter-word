package filecollector

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DownloadResult struct {
	Downloaded, Skipped, Failed, PermissionDenied, TooLarge int
	Bytes                                                   int64
}
type DownloadRecord struct {
	ExternalID       string `json:"external_id"`
	Status           string `json:"status"`
	ContentSHA256    any    `json:"content_sha256"`
	LocalAssetRef    any    `json:"local_asset_ref"`
	Size             any    `json:"size"`
	SourceStorageKey any    `json:"source_storage_key,omitempty"`
	Attempts         int    `json:"attempts"`
	UpdatedAt        string `json:"updated_at"`
}
type downloadFailure struct {
	Status     int
	RetryAfter time.Duration
	Message    string
}

func (e *downloadFailure) Error() string { return e.Message }

func Download(ctx context.Context, src DownloadSource, httpClient *http.Client, cfg Config) (DownloadResult, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	root := filepath.Join(cfg.Output, "teambition-file-collector", cfg.ProjectID)
	nodesPath := filepath.Join(root, "entities", "project_file_nodes.jsonl")
	nodes := readNodes(nodesPath)
	if len(nodes) == 0 {
		return DownloadResult{}, errors.New("discovery nodes are required before download")
	}
	for i := range nodes {
		upgradeNodeContract(&nodes[i])
	}
	records := loadDownloadRecords(filepath.Join(root, "checkpoints", "file_downloads.json"))
	if err := os.MkdirAll(filepath.Join(root, "assets", "sha256"), 0755); err != nil {
		return DownloadResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(root, "assets", ".partial"), 0700); err != nil {
		return DownloadResult{}, err
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	result := DownloadResult{}
	var persistErr error
	for worker := 0; worker < cfg.Concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				n := nodes[index]
				previous := records[n.ExternalID]
				if previous.Status == "downloaded" && verifiedAsset(root, previous) {
					mu.Lock()
					result.Skipped++
					nodes[index].DownloadStatus = "downloaded"
					nodes[index].ContentSHA256 = previous.ContentSHA256
					nodes[index].LocalAssetRef = previous.LocalAssetRef
					nodes[index].Size = previous.Size
					nodes[index].SourceStorageKey = previous.SourceStorageKey
					markDownloadComplete(&nodes[index])
					nodes[index].Fingerprint = fingerprint(nodes[index])
					mu.Unlock()
					continue
				}
				if previous.Status == "failed" && !cfg.RetryFailedDownloads {
					mu.Lock()
					result.Skipped++
					mu.Unlock()
					continue
				}
				var rec DownloadRecord
				var kind string
				var err error
				if src == nil {
					rec = DownloadRecord{ExternalID: n.ExternalID, Status: "failed", Attempts: 0, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
					kind = "failed"
					err = errors.New("download source is unavailable; existing verified assets can be upgraded offline, but missing assets require sdk or browser source")
				} else {
					rec, kind, err = downloadNode(ctx, src, httpClient, root, cfg, n)
				}
				mu.Lock()
				records[n.ExternalID] = rec
				switch kind {
				case "downloaded":
					result.Downloaded++
					if sz, ok := toInt64(rec.Size); ok {
						result.Bytes += sz
					}
					nodes[index].DownloadStatus = "downloaded"
					nodes[index].ContentSHA256 = rec.ContentSHA256
					nodes[index].LocalAssetRef = rec.LocalAssetRef
					nodes[index].Size = rec.Size
					nodes[index].SourceStorageKey = rec.SourceStorageKey
					markDownloadComplete(&nodes[index])
					nodes[index].Fingerprint = fingerprint(nodes[index])
				case "permission":
					result.PermissionDenied++
					result.Failed++
					nodes[index].DownloadStatus = "failed"
				case "too_large":
					result.TooLarge++
					result.Failed++
					nodes[index].DownloadStatus = "failed"
				default:
					result.Failed++
					nodes[index].DownloadStatus = "failed"
				}
				if err != nil {
					if e := appendJSONL(filepath.Join(root, "download_errors.jsonl"), map[string]any{"external_id": n.ExternalID, "operation": "download", "status": kind, "error": sanitizeError(err.Error()), "retryable": kind == "transient", "attempts": rec.Attempts, "occurred_at": time.Now().UTC().Format(time.RFC3339)}); e != nil && persistErr == nil {
						persistErr = e
					}
				}
				if e := writeDownloadCheckpoint(filepath.Join(root, "checkpoints", "file_downloads.json"), cfg.ProjectID, records); e != nil && persistErr == nil {
					persistErr = e
				}
				if e := rewriteNodes(nodesPath, nodes); e != nil && persistErr == nil {
					persistErr = e
				}
				mu.Unlock()
			}
		}()
	}
queue:
	for i, n := range nodes {
		if n.NodeKind != "file" {
			continue
		}
		if cfg.DownloadExternalID != "" && n.ExternalID != cfg.DownloadExternalID {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if persistErr != nil {
		return result, fmt.Errorf("persist download state: %w", persistErr)
	}
	if err := rewriteNodes(nodesPath, nodes); err != nil {
		return result, fmt.Errorf("rewrite upgraded node contract: %w", err)
	}
	if err := reconcileDownloadErrors(filepath.Join(root, "download_errors.jsonl"), records); err != nil {
		return result, fmt.Errorf("reconcile download errors: %w", err)
	}
	if err := validateDownloadedNodes(root, nodes); err != nil {
		return result, err
	}
	if err := updateDownloadManifest(root, cfg, result, nodes); err != nil {
		return result, err
	}
	if err := writeBrowseView(root, nodes); err != nil {
		return result, fmt.Errorf("write browse view: %w", err)
	}
	if err := writeChecksums(root); err != nil {
		return result, err
	}
	return result, nil
}

func upgradeNodeContract(node *Node) {
	if node.ParentExternalID == nil && node.NodeKind == "directory" {
		node.Root = true
		node.Synthetic = true
	}
	if node.NodeKind == "file" && node.SourceMIMEType == nil {
		source := stringValue(node.MIMEType)
		node.SourceMIMEType = nilIfEmpty(source)
		node.MIMEType = mimeTypeFromSource(source, stringValue(node.Name))
		if node.MIMEType == nil {
			node.MissingFields = appendUnique(node.MissingFields, "mime_type")
			node.Completeness = "partial"
		}
	}
	node.Fingerprint = fingerprint(*node)
}

func reconcileDownloadErrors(path string, records map[string]DownloadRecord) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	failed := map[string]bool{}
	resolved := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		var record struct {
			ExternalID string `json:"external_id"`
			Status     string `json:"status"`
		}
		if json.Unmarshal([]byte(line), &record) != nil || record.ExternalID == "" {
			continue
		}
		if record.Status == "resolved" {
			resolved[record.ExternalID] = true
		} else {
			failed[record.ExternalID] = true
		}
	}
	for externalID := range failed {
		if resolved[externalID] || records[externalID].Status != "downloaded" {
			continue
		}
		if err := appendJSONL(path, map[string]any{
			"external_id": externalID,
			"operation":   "download",
			"status":      "resolved",
			"retryable":   false,
			"occurred_at": time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	return nil
}

func markDownloadComplete(node *Node) {
	remove := map[string]bool{"content_sha256": true, "local_asset_ref": true, "download_status": true}
	if node.SourceStorageKey != nil {
		remove["source_storage_key"] = true
	}
	kept := node.MissingFields[:0]
	for _, field := range node.MissingFields {
		if !remove[field] {
			kept = append(kept, field)
		}
	}
	node.MissingFields = kept
	if len(kept) == 0 {
		node.Completeness = "complete"
	}
}

func downloadNode(ctx context.Context, src DownloadSource, client *http.Client, root string, cfg Config, node Node) (DownloadRecord, string, error) {
	rec := DownloadRecord{ExternalID: node.ExternalID, Status: "failed", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	expected, _ := toInt64(node.Size)
	if cfg.MaxFileSize > 0 && expected > cfg.MaxFileSize {
		return rec, "too_large", fmt.Errorf("declared size %d exceeds max-file-size", expected)
	}
	var last error
	kind := "failed"
	for attempt := 1; attempt <= 4; attempt++ {
		rec.Attempts = attempt
		desc, status, err := src.ResolveDownload(ctx, cfg.ProjectID, node.ExternalID, stringValue(node.VersionExternalID))
		if err != nil {
			last = err
			if status == 401 || status == 403 {
				kind = "permission"
				break
			}
			if !retryableDownload(status) {
				break
			}
			kind = "transient"
			waitRetry(ctx, retryDelay(attempt, 0))
			continue
		}
		if desc.ExpectedSize != nil {
			expected = *desc.ExpectedSize
		}
		if cfg.MaxFileSize > 0 && expected > cfg.MaxFileSize {
			return rec, "too_large", fmt.Errorf("source size %d exceeds max-file-size", expected)
		}
		size, hash, ref, statusCode, retryAfter, e := streamAsset(ctx, client, root, desc, expected, cfg.MaxFileSize)
		if e == nil {
			rec.Status = "downloaded"
			rec.ContentSHA256 = hash
			rec.LocalAssetRef = ref
			rec.Size = size
			rec.SourceStorageKey = desc.SourceStorageKey
			rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			return rec, "downloaded", nil
		}
		last = e
		if statusCode == 401 || statusCode == 403 {
			kind = "permission"
			break
		}
		if statusCode == 413 {
			kind = "too_large"
			break
		}
		if !retryableDownload(statusCode) {
			break
		}
		kind = "transient"
		waitRetry(ctx, retryDelay(attempt, retryAfter))
	}
	return rec, kind, last
}

func streamAsset(ctx context.Context, client *http.Client, root string, desc DownloadDescriptor, expected, max int64) (int64, string, string, int, time.Duration, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, desc.URL, nil)
	if e != nil {
		return 0, "", "", 0, 0, e
	}
	for key, values := range desc.Headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, "", "", 0, 0, e
	}
	defer resp.Body.Close()
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", "", resp.StatusCode, retryAfter, &downloadFailure{Status: resp.StatusCode, RetryAfter: retryAfter, Message: fmt.Sprintf("download HTTP %d", resp.StatusCode)}
	}
	if max > 0 && resp.ContentLength > max {
		return 0, "", "", 413, 0, fmt.Errorf("content length %d exceeds max-file-size", resp.ContentLength)
	}
	reader := bufio.NewReader(resp.Body)
	prefix, _ := reader.Peek(512)
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	trimmed := strings.ToLower(strings.TrimSpace(string(prefix)))
	if strings.Contains(contentType, "text/html") || strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") {
		return 0, "", "", 200, 0, errors.New("download returned an HTML page instead of file content")
	}
	partialDir := filepath.Join(root, "assets", ".partial")
	f, e := os.CreateTemp(partialDir, "asset-*.partial")
	if e != nil {
		return 0, "", "", 0, 0, e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	h := sha256.New()
	limit := io.Reader(reader)
	if max > 0 {
		limit = io.LimitReader(reader, max+1)
	}
	size, copyErr := io.Copy(io.MultiWriter(f, h), limit)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return 0, "", "", 0, 0, copyErr
	}
	if syncErr != nil {
		return 0, "", "", 0, 0, syncErr
	}
	if closeErr != nil {
		return 0, "", "", 0, 0, closeErr
	}
	if max > 0 && size > max {
		return 0, "", "", 413, 0, fmt.Errorf("download exceeded max-file-size")
	}
	if expected >= 0 && expected != size {
		return 0, "", "", 200, 0, fmt.Errorf("size mismatch: expected %d, received %d", expected, size)
	}
	hash := hex.EncodeToString(h.Sum(nil))
	rel := filepath.ToSlash(filepath.Join("assets", "sha256", hash[:2], hash))
	dest := filepath.Join(root, filepath.FromSlash(rel))
	if e := os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return 0, "", "", 0, 0, e
	}
	if _, e = os.Stat(dest); e == nil {
		if ok := verifyFile(dest, size, hash); !ok {
			return 0, "", "", 0, 0, errors.New("existing content-addressed asset failed verification")
		}
		return size, hash, rel, 200, 0, nil
	}
	if !os.IsNotExist(e) {
		return 0, "", "", 0, 0, e
	}
	if e = os.Rename(tmp, dest); e != nil {
		// Concurrent equal-content downloads can both pass Stat. The winner's
		// asset is acceptable if it verifies after the losing rename.
		if verifyFile(dest, size, hash) {
			return size, hash, rel, 200, 0, nil
		}
		return 0, "", "", 0, 0, e
	}
	return size, hash, rel, 200, 0, nil
}

func verifiedAsset(root string, r DownloadRecord) bool {
	hash, _ := r.ContentSHA256.(string)
	ref, _ := r.LocalAssetRef.(string)
	size, ok := toInt64(r.Size)
	return ok && len(hash) == 64 && ref != "" && verifyFile(filepath.Join(root, filepath.FromSlash(ref)), size, hash)
}
func verifyFile(path string, size int64, want string) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return e == nil && n == size && hex.EncodeToString(h.Sum(nil)) == want
}

func validateDownloadedNodes(root string, nodes []Node) error {
	for _, node := range nodes {
		if node.DownloadStatus != "downloaded" {
			continue
		}
		hash, hashOK := node.ContentSHA256.(string)
		ref, refOK := node.LocalAssetRef.(string)
		size, sizeOK := toInt64(node.Size)
		if !hashOK || len(hash) != 64 || !refOK || ref == "" || !sizeOK {
			return fmt.Errorf("downloaded node %s has incomplete asset metadata", node.ExternalID)
		}
		cleanRef := filepath.Clean(filepath.FromSlash(ref))
		if filepath.IsAbs(cleanRef) || cleanRef == ".." || strings.HasPrefix(cleanRef, ".."+string(filepath.Separator)) {
			return fmt.Errorf("downloaded node %s has an unsafe local asset reference", node.ExternalID)
		}
		if !verifyFile(filepath.Join(root, cleanRef), size, hash) {
			return fmt.Errorf("downloaded node %s failed final size or SHA-256 verification", node.ExternalID)
		}
	}
	return validateNodes(nodes)
}

func updateDownloadManifest(root string, cfg Config, result DownloadResult, nodes []Node) error {
	path := filepath.Join(root, "manifest.json")
	manifest := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}
	now := time.Now().UTC()
	manifest["schema_version"] = "1.1"
	manifest["source_system"] = "teambition"
	manifest["collector_name"] = "tb-file-collector"
	manifest["collector_version"] = "1.1.0"
	if _, ok := manifest["run_id"].(string); !ok {
		manifest["run_id"] = now.Format("20060102T150405.000000000Z") + "-" + cfg.ProjectID
	}
	if _, ok := manifest["started_at"].(string); !ok {
		manifest["started_at"] = now.Format(time.RFC3339Nano)
	}
	manifest["project_external_id"] = cfg.ProjectID
	manifest["project_url"] = redactURL(cfg.ProjectURL)
	manifest["resume"] = map[string]any{"enabled": cfg.Resume, "discovery_checkpoint": "checkpoints/file_discovery.json", "download_checkpoint": "checkpoints/file_downloads.json"}
	if _, ok := manifest["warnings"].([]any); !ok {
		if _, ok := manifest["warnings"].([]string); !ok {
			manifest["warnings"] = []string{}
		}
	}
	counts, _ := manifest["counts"].(map[string]any)
	if counts == nil {
		counts = map[string]any{}
	}
	stateDownloaded := 0
	stateFailed := 0
	var stateBytes int64
	requestedFiles := 0
	discoveryPartial := false
	if data, err := os.ReadFile(filepath.Join(root, "errors.jsonl")); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		discoveryPartial = true
	}
	for _, node := range nodes {
		if node.NodeKind != "file" || (cfg.DownloadExternalID != "" && node.ExternalID != cfg.DownloadExternalID) {
			continue
		}
		requestedFiles++
		switch node.DownloadStatus {
		case "downloaded":
			stateDownloaded++
			if size, ok := toInt64(node.Size); ok {
				stateBytes += size
			}
		case "failed":
			stateFailed++
		}
	}
	counts["downloaded"] = stateDownloaded
	counts["verified"] = stateDownloaded
	counts["download_skipped"] = result.Skipped
	counts["download_failed"] = stateFailed
	counts["download_permission_denied"] = result.PermissionDenied
	counts["download_too_large"] = result.TooLarge
	counts["permission_denied"] = result.PermissionDenied
	counts["too_large"] = result.TooLarge
	counts["downloaded_bytes"] = stateBytes
	manifest["counts"] = counts
	coverage, _ := manifest["coverage"].(map[string]any)
	if coverage == nil {
		coverage = map[string]any{}
	}
	downloadCoverage := "complete"
	if requestedFiles == 0 {
		downloadCoverage = "empty"
	} else if cfg.DownloadExternalID != "" || stateFailed > 0 {
		downloadCoverage = "partial"
	}
	coverage["downloads"] = downloadCoverage
	manifest["coverage"] = coverage
	manifest["mode"] = "download"
	manifest["status"] = map[bool]string{true: "partial", false: "succeeded"}[stateFailed > 0 || discoveryPartial]
	manifest["download_selection_external_id"] = nilIfEmpty(cfg.DownloadExternalID)
	manifest["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	manifest["finished_at"] = now.Format(time.RFC3339Nano)
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}
func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		return int64(x), x == float64(int64(x))
	case json.Number:
		n, e := x.Int64()
		return n, e == nil
	case nil:
		return -1, false
	}
	return -1, false
}
func stringValue(v any) string { s, _ := v.(string); return s }
func retryableDownload(status int) bool {
	return status == 0 || status == 408 || status == 429 || status >= 500
}
func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := time.Duration(1<<uint(attempt-1)) * 250 * time.Millisecond
	jitter := time.Duration(time.Now().UnixNano() % int64(base/2+1))
	return base + jitter
}
func waitRetry(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(v); e == nil {
		return time.Until(t)
	}
	return 0
}
func loadDownloadRecords(path string) map[string]DownloadRecord {
	out := map[string]DownloadRecord{}
	b, e := os.ReadFile(path)
	if e != nil {
		return out
	}
	var wrapper struct {
		Files map[string]DownloadRecord `json:"files"`
	}
	if json.Unmarshal(b, &wrapper) == nil && wrapper.Files != nil {
		return wrapper.Files
	}
	return out
}
func writeDownloadCheckpoint(path, pid string, records map[string]DownloadRecord) error {
	b, _ := json.MarshalIndent(map[string]any{"version": "1", "project_external_id": pid, "files": records, "updated_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	return atomicWrite(path, append(b, '\n'))
}
