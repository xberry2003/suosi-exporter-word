package filecollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"thoughtsexport/internal/tbinventory"
	"time"
)

type Result struct {
	Nodes             int
	Directories       int
	Files             int
	Pages             int
	Errors            int
	UnresolvedParents int
	UnknownKinds      int
}

type rawPage struct {
	Parent string
	Token  string
	Raw    string
}

func Discover(ctx context.Context, src PageSource, cfg Config) (Result, error) {
	if src == nil {
		return Result{}, errors.New("file discovery source is required")
	}
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return Result{}, errors.New("project id is required")
	}
	if rootParent(cfg.ProjectURL) == "" {
		return Result{}, errors.New("project file-library URL must contain /works/{rootId}")
	}
	if cfg.PageSize < 1 {
		cfg.PageSize = 100
	}
	root := filepath.Join(cfg.Output, "teambition-file-collector", cfg.ProjectID)
	for _, d := range []string{"entities", "raw", "checkpoints", filepath.Join("assets", "sha256")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			return Result{}, err
		}
	}
	if !cfg.Resume {
		_ = os.Remove(filepath.Join(root, "entities", "download_errors.jsonl"))
		for _, n := range []string{"project_file_nodes.jsonl", "project_file_versions.jsonl", "project_file_references.jsonl"} {
			if err := os.WriteFile(filepath.Join(root, "entities", n), nil, 0644); err != nil {
				return Result{}, err
			}
		}
		if err := os.WriteFile(filepath.Join(root, "download_errors.jsonl"), nil, 0644); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(filepath.Join(root, "errors.jsonl"), nil, 0644); err != nil {
			return Result{}, err
		}
		downloadCheckpoint, _ := json.MarshalIndent(map[string]any{"version": "1", "status": "unavailable", "reason": "binary download is disabled until discovery identity validation is accepted", "confirmed_external_ids": []string{}}, "", "  ")
		if err := atomicWrite(filepath.Join(root, "checkpoints", "file_downloads.json"), append(downloadCheckpoint, '\n')); err != nil {
			return Result{}, err
		}
	}
	seenNodes := map[string]bool{}
	seenParents := map[string]bool{}
	displayPaths := map[string]string{}
	if cfg.Resume {
		_ = loadIDs(filepath.Join(root, "entities", "project_file_nodes.jsonl"), seenNodes)
		for _, n := range readNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl")) {
			displayPaths[n.ExternalID] = n.DisplayPath
		}
	}
	rootID := rootParent(cfg.ProjectURL)
	queue := []string{rootID}
	known := map[string]bool{queue[0]: true}
	encountered := map[string]bool{queue[0]: true}
	displayPaths[rootID] = ""
	result := Result{}
	inaccessible := map[string]bool{}
	if !seenNodes[rootID] {
		rootNode := makeNode(cfg.ProjectID, rootID, "", "directory", nil, "", nil, "", nil, nil, "", "", false, nil, []string{"name", "order", "source_created_at", "source_updated_at", "creator_external_user_id", "modifier_external_user_id", "size", "mime_type", "version_external_id", "content_sha256", "local_asset_ref", "download_status"})
		rootNode.Root = true
		rootNode.Synthetic = true
		rootNode.Warnings = []string{"file-library root identity comes from the source project URL; root metadata was not returned by the listing response"}
		rootNode.Fingerprint = fingerprint(rootNode)
		if err := appendNodeJSONL(filepath.Join(root, "entities", "project_file_nodes.jsonl"), rootNode); err != nil {
			return result, err
		}
		seenNodes[rootID] = true
		result.Nodes++
		result.Directories++
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		token := ""
		pageTokens := map[string]bool{}
		for {
			if pageTokens[token] {
				return result, fmt.Errorf("pagination cycle for parent %s", parent)
			}
			pageTokens[token] = true
			page, status, err := src.ListFiles(ctx, cfg.ProjectID, parent, token, tbinventory.ListOptions{PageSize: cfg.PageSize})
			result.Pages++
			if err != nil {
				result.Errors++
				inaccessible[parent] = true
				if e := appendJSONL(filepath.Join(root, "errors.jsonl"), map[string]any{"external_id": parent, "operation": "discover", "http_status": status, "error": sanitizeError(err.Error()), "retryable": status == 429 || status >= 500}); e != nil {
					return result, e
				}
				if e := atomicWrite(filepath.Join(root, "checkpoints", "file_discovery.json"), checkpoint(cfg.ProjectID, parent, result, seenNodes)); e != nil {
					return result, e
				}
				break
			}
			if cfg.IncludeRaw {
				name := fmt.Sprintf("page-%s-%s.json", safe(parent), safe(tokenOrFirst(token)))
				if err := atomicWrite(filepath.Join(root, "raw", name), append(redactRaw(page.Diagnostics.RawResponse), '\n')); err != nil {
					return result, err
				}
			}
			rawRef := any(nil)
			if cfg.IncludeRaw {
				rawRef = filepath.ToSlash(filepath.Join("raw", fmt.Sprintf("page-%s-%s.json", safe(parent), safe(tokenOrFirst(token)))))
			}
			for _, f := range page.Collections {
				if f.ID == "" {
					continue
				}
				if encountered[f.ID] {
					return result, fmt.Errorf("duplicate project file external_id %s in discovery response", f.ID)
				}
				encountered[f.ID] = true
				seenParents[f.ID] = true
				known[f.ID] = true
				prefix := f.PrefixPath
				if prefix == "" {
					prefix = displayPaths[parent]
				}
				f.PrefixPath = prefix
				if seenNodes[f.ID] {
					if displayPaths[f.ID] == "" {
						displayPaths[f.ID] = joinDisplay(prefix, f.Title)
					}
				} else {
					n := nodeFromCollection(cfg.ProjectID, f, parent, rawRef)
					if err := appendNodeJSONL(filepath.Join(root, "entities", "project_file_nodes.jsonl"), n); err != nil {
						return result, err
					}
					seenNodes[f.ID] = true
					result.Nodes++
					result.Directories++
					displayPaths[f.ID] = n.DisplayPath
				}
				if !contains(queue, f.ID) {
					queue = append(queue, f.ID)
				}
			}
			for _, f := range page.Works {
				if f.ID == "" {
					continue
				}
				if encountered[f.ID] {
					return result, fmt.Errorf("duplicate project file external_id %s in discovery response", f.ID)
				}
				encountered[f.ID] = true
				if seenNodes[f.ID] {
					continue
				}
				if f.PrefixPath == "" {
					f.PrefixPath = displayPaths[parent]
				}
				n := nodeFromWork(cfg.ProjectID, f, parent, rawRef)
				if err := appendNodeJSONL(filepath.Join(root, "entities", "project_file_nodes.jsonl"), n); err != nil {
					return result, err
				}
				seenNodes[f.ID] = true
				result.Nodes++
				result.Files++
			}
			if err := atomicWrite(filepath.Join(root, "checkpoints", "file_discovery.json"), checkpoint(cfg.ProjectID, parent, result, seenNodes)); err != nil {
				return result, err
			}
			token = page.NextPageToken
			if token == "" {
				break
			}
		}
		if err := atomicWrite(filepath.Join(root, "checkpoints", "file_discovery.json"), checkpoint(cfg.ProjectID, parent, result, seenNodes)); err != nil {
			return result, err
		}
	}
	for _, n := range readNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl")) {
		if n.ParentExternalID != nil && *n.ParentExternalID != "" && !known[*n.ParentExternalID] && *n.ParentExternalID != rootParent(cfg.ProjectURL) {
			result.UnresolvedParents++
		}
	}
	result.UnknownKinds = 0
	if len(inaccessible) > 0 {
		nodes := readNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl"))
		for i := range nodes {
			if inaccessible[nodes[i].ExternalID] {
				nodes[i].Visibility = "partially_visible"
				nodes[i].Completeness = "partial"
				nodes[i].Warnings = appendUnique(nodes[i].Warnings, "child listing was inaccessible; descendants may be incomplete")
				nodes[i].Fingerprint = fingerprint(nodes[i])
			}
		}
		if err := rewriteNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl"), nodes); err != nil {
			return result, err
		}
	}
	actual := readNodes(filepath.Join(root, "entities", "project_file_nodes.jsonl"))
	result.Nodes, result.Directories, result.Files = len(actual), 0, 0
	for _, n := range actual {
		if n.NodeKind == "directory" {
			result.Directories++
		} else if n.NodeKind == "file" {
			result.Files++
		} else {
			result.UnknownKinds++
		}
	}
	_ = os.WriteFile(filepath.Join(root, "entities", "project_file_versions.jsonl"), nil, 0644)
	_ = os.WriteFile(filepath.Join(root, "entities", "project_file_references.jsonl"), nil, 0644)
	if err := validateNodes(actual); err != nil {
		return result, err
	}
	if err := writeManifest(root, cfg, result); err != nil {
		return result, err
	}
	if err := writeChecksums(root); err != nil {
		return result, err
	}
	return result, nil
}

func rewriteNodes(path string, nodes []Node) error {
	var b []byte
	for _, n := range nodes {
		line, err := marshalNodeEnvelope(n)
		if err != nil {
			return err
		}
		b = append(b, append(line, '\n')...)
	}
	return atomicWrite(path, b)
}

func appendNodeJSONL(path string, node Node) error {
	b, err := marshalNodeEnvelope(node)
	if err != nil {
		return err
	}
	old, _ := os.ReadFile(path)
	return atomicWrite(path, append(old, append(b, '\n')...))
}

func validateNodes(nodes []Node) error {
	byID := map[string]Node{}
	for _, n := range nodes {
		if n.ExternalID == "" {
			return errors.New("project file node missing external_id")
		}
		if _, exists := byID[n.ExternalID]; exists {
			return fmt.Errorf("duplicate project file external_id %s", n.ExternalID)
		}
		if n.ParentExternalID != nil && *n.ParentExternalID == n.ExternalID {
			return fmt.Errorf("project file node %s is its own parent", n.ExternalID)
		}
		if n.NodeKind == "directory" && (n.Size != nil || n.MIMEType != nil || n.SourceStorageKey != nil) {
			return fmt.Errorf("directory %s has file-only fields", n.ExternalID)
		}
		if n.Deleted {
			return fmt.Errorf("node %s marked deleted without a deletion source", n.ExternalID)
		}
		if fingerprint(n) != n.Fingerprint {
			return fmt.Errorf("node %s fingerprint mismatch", n.ExternalID)
		}
		byID[n.ExternalID] = n
	}
	for _, n := range nodes {
		if n.ParentExternalID != nil {
			if _, ok := byID[*n.ParentExternalID]; !ok {
				return fmt.Errorf("node %s has unresolved parent %s", n.ExternalID, *n.ParentExternalID)
			}
		}
	}
	for _, n := range nodes {
		seen := map[string]bool{n.ExternalID: true}
		cur := n
		for cur.ParentExternalID != nil {
			p := *cur.ParentExternalID
			if seen[p] {
				return fmt.Errorf("directory cycle involving %s", n.ExternalID)
			}
			seen[p] = true
			cur = byID[p]
		}
	}
	return nil
}

func writeManifest(root string, cfg Config, result Result) error {
	now := time.Now().UTC()
	incrementalCoverage := "empty"
	warnings := []string{}
	if cfg.Since != "" {
		incrementalCoverage = "unavailable"
		warnings = append(warnings, "--since was recorded but not applied because the listing source exposes no confirmed updated-time filter")
	}
	partial := result.Errors > 0 || result.UnresolvedParents > 0
	if result.Errors > 0 {
		warnings = append(warnings, fmt.Sprintf("%d discovery operation(s) failed; inaccessible subtrees are not treated as empty", result.Errors))
	}
	b, _ := json.MarshalIndent(map[string]any{
		"schema_version": "1.1", "source_system": "teambition", "collector_name": "tb-file-collector", "collector_version": "1.1.0",
		"run_id": now.Format("20060102T150405.000000000Z") + "-" + cfg.ProjectID,
		"mode":   "discover", "project_external_id": cfg.ProjectID, "project_url": redactURL(cfg.ProjectURL), "since": nilIfEmpty(cfg.Since),
		"started_at": now.Format(time.RFC3339Nano), "finished_at": now.Format(time.RFC3339Nano),
		"status":     map[bool]string{true: "partial", false: "succeeded"}[partial],
		"counts":     map[string]int{"project_file_nodes": result.Nodes, "directories": result.Directories, "files": result.Files, "links": 0, "online_documents": 0, "downloaded": 0, "verified": 0, "download_failed": 0, "permission_denied": 0, "too_large": 0, "pages": result.Pages, "errors": result.Errors},
		"coverage":   map[string]string{"nodes": map[bool]string{true: "partial", false: "complete"}[result.Errors > 0], "parent_relationships": map[bool]string{true: "partial", false: "complete"}[result.UnresolvedParents > 0], "versions": "unavailable", "references": "unavailable", "downloads": "unavailable", "incremental_filter": incrementalCoverage},
		"warnings":   warnings,
		"resume":     map[string]any{"enabled": cfg.Resume, "discovery_checkpoint": "checkpoints/file_discovery.json", "download_checkpoint": "checkpoints/file_downloads.json"},
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	return atomicWrite(filepath.Join(root, "manifest.json"), append(b, '\n'))
}

func writeChecksums(root string) error {
	checksums := filepath.Join(root, "checksums.sha256")
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || path == checksums || strings.Contains(filepath.ToSlash(path), "/.partial/") || strings.HasSuffix(path, ".partial") {
			return nil
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines = append(lines, hash+"  "+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(lines)
	return atomicWrite(checksums, []byte(strings.Join(lines, "\n")+"\n"))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func nodeFromCollection(pid string, f tbinventory.Collection, parent string, rawRef any) Node {
	n := makeNode(pid, f.ID, parent, "directory", f.Title, f.PrefixPath, nil, f.CreatorID, nil, nil, f.Created, f.Updated, f.Archived, rawRef, []string{"order", "modifier_external_user_id", "size", "mime_type", "version_external_id", "content_sha256", "local_asset_ref", "download_status"})
	if strings.TrimSpace(f.Title) == "" {
		n.MissingFields = appendUnique(n.MissingFields, "name")
		n.Completeness = "partial"
		n.Warnings = append(n.Warnings, "source directory name is empty; manual review may be required")
		n.Fingerprint = fingerprint(n)
	}
	return n
}
func nodeFromWork(pid string, f tbinventory.Work, parent string, rawRef any) Node {
	missing := []string{"order", "modifier_external_user_id", "source_storage_key", "version_external_id", "content_sha256", "local_asset_ref", "download_status"}
	if f.CreatorID == "" {
		missing = append(missing, "creator_external_user_id")
	}
	var size any
	if f.FileSize != nil {
		size = *f.FileSize
	}
	sourceMime := f.MIMEType
	normalizedMime := mimeTypeFromSource(sourceMime, f.FileName)
	n := makeNode(pid, f.ID, parent, "file", f.FileName, f.PrefixPath, nil, f.CreatorID, size, normalizedMime, f.Created, f.Updated, f.Archived, rawRef, missing)
	n.SourceMIMEType = nilIfEmpty(sourceMime)
	if normalizedMime == nil {
		n.MissingFields = appendUnique(n.MissingFields, "mime_type")
		n.Completeness = "partial"
	}
	n.Fingerprint = fingerprint(n)
	return n
}

func mimeTypeFromSource(source, name string) any {
	if source != "" {
		if strings.Contains(source, "/") {
			return source
		}
		if value := mime.TypeByExtension("." + strings.ToLower(source)); value != "" {
			return value
		}
	}
	if value := mime.TypeByExtension(filepath.Ext(name)); value != "" {
		return value
	}
	return nil
}
func makeNode(pid, id, parent, kind string, name any, prefix string, order any, creator string, size, mime any, created, updated string, archived bool, rawRef any, missing []string) Node {
	var p *string
	if parent != "" {
		p = &parent
	}
	if creator == "" {
		missing = appendUnique(missing, "creator_external_user_id")
	}
	completeness := "complete"
	if len(missing) > 0 {
		completeness = "partial"
	}
	n := Node{ExternalID: id, ProjectExternalID: pid, ParentExternalID: p, NodeKind: kind, Name: name, DisplayPath: joinDisplay(prefix, name), Order: order, SourceCreatedAt: nilIfEmpty(created), SourceUpdatedAt: nilIfEmpty(updated), Size: size, MIMEType: mime, SourceStorageKey: nil, DownloadStatus: "not_requested", Visibility: "visible", Completeness: completeness, MissingFields: sortedUnique(missing), Warnings: []string{}, Archived: archived, Deleted: false}
	if kind == "directory" {
		n.SourceStorageKey = nil
	}
	if creator != "" {
		n.CreatorExternalUserID = creator
	}
	n.Fingerprint = fingerprint(n)
	n.RawRef = rawRef
	return n
}

func rootParent(u string) string {
	if parsed, err := url.Parse(u); err == nil {
		u = parsed.Path
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) > 3 && parts[2] == "works" {
		return parts[3]
	}
	return ""
}
func joinDisplay(prefix string, name any) string {
	s, _ := name.(string)
	p := strings.Trim(prefix, "/")
	if p == "" {
		return s
	}
	if s == "" {
		return p
	}
	return p + "/" + s
}
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func appendUnique(a []string, v string) []string {
	for _, x := range a {
		if x == v {
			return a
		}
	}
	return append(a, v)
}
func sortedUnique(a []string) []string {
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	out := make([]string, 0, len(m))
	for x := range m {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}
func tokenOrFirst(s string) string {
	if s == "" {
		return "first"
	}
	return s
}
func safe(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "empty"
	}
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("<>:\"/\\|?*\x00", r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func sanitizeError(s string) string { return redactURL(s) }
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err == nil && u.Scheme != "" && u.Host != "" {
		if u.RawQuery != "" || u.Fragment != "" {
			u.RawQuery = "[REDACTED]"
			u.Fragment = ""
		}
		return u.String()
	}
	for _, marker := range []string{"downloadUrl", "thumbnailUrl", "token", "cookie", "authorization", "signature"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(marker)) {
			return "[REDACTED]"
		}
	}
	return s
}
func redactRaw(s string) []byte {
	if strings.HasPrefix(s, "collections:\n") {
		parts := strings.SplitN(strings.TrimPrefix(s, "collections:\n"), "\nworks:\n", 2)
		if len(parts) == 2 {
			var collections, works any
			if json.Unmarshal([]byte(parts[0]), &collections) == nil && json.Unmarshal([]byte(parts[1]), &works) == nil {
				b, _ := json.Marshal(map[string]any{"collections": redactValue(collections), "works": redactValue(works)})
				return b
			}
		}
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return []byte(redactURL(s))
	}
	b, _ := json.Marshal(redactValue(v))
	return b
}
func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			lower := strings.ToLower(k)
			switch {
			case strings.Contains(lower, "cookie"), strings.Contains(lower, "token"), strings.Contains(lower, "authorization"), strings.Contains(lower, "signature"), strings.Contains(lower, "secret"):
				out[k] = "[REDACTED]"
			case strings.Contains(lower, "url"):
				if text, ok := value.(string); ok {
					out[k] = redactURL(text)
				} else {
					out[k] = redactValue(value)
				}
			default:
				out[k] = redactValue(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redactValue(x[i])
		}
		return out
	default:
		return v
	}
}
func fingerprint(n Node) string {
	n.Fingerprint = ""
	n.RawRef = nil
	// Binary transfer state is deliberately separate from source metadata.
	n.ContentSHA256 = nil
	n.LocalAssetRef = nil
	n.DownloadStatus = ""
	transferFields := map[string]bool{"content_sha256": true, "local_asset_ref": true, "download_status": true}
	missing := make([]string, 0, len(n.MissingFields))
	for _, field := range n.MissingFields {
		if !transferFields[field] {
			missing = append(missing, field)
		}
	}
	n.MissingFields = missing
	if len(missing) == 0 && n.Completeness == "partial" {
		n.Completeness = "complete"
	}
	b, _ := json.Marshal(n)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func appendJSONL(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	old, _ := os.ReadFile(path)
	return atomicWrite(path, append(old, append(b, '\n')...))
}
func atomicWrite(path string, b []byte) error {
	tmp := path + ".partial"
	if e := os.WriteFile(tmp, b, 0644); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func loadIDs(path string, ids map[string]bool) error {
	for _, n := range readNodes(path) {
		ids[n.ExternalID] = true
	}
	return nil
}
func readNodes(path string) []Node {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil
	}
	var out []Node
	for _, line := range strings.Split(string(b), "\n") {
		var n Node
		var env NodeEnvelope
		if json.Unmarshal([]byte(line), &env) == nil && env.EntityType == "project_file_node" && env.ExternalID != "" {
			n = env.Data
			n.ExternalID = env.ExternalID
			n.ProjectExternalID = env.ProjectExternalID
			n.SourceCreatedAt = env.SourceCreatedAt
			n.SourceUpdatedAt = env.SourceUpdatedAt
			n.Visibility = env.Visibility
			n.Completeness = env.Completeness
			n.MissingFields = env.MissingFields
			n.Warnings = env.Warnings
			n.Fingerprint = env.Fingerprint
			n.RawRef = env.RawRef
			out = append(out, n)
			continue
		}
		if json.Unmarshal([]byte(line), &n) == nil && n.ExternalID != "" {
			out = append(out, n)
		}
	}
	return out
}
func checkpoint(pid, parent string, r Result, ids map[string]bool) []byte {
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.MarshalIndent(map[string]any{"version": "1", "project_external_id": pid, "last_parent_external_id": parent, "result": r, "confirmed_external_ids": keys, "updated_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	return append(b, '\n')
}
