package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"thoughtsexport/internal/teambition/taskprobe"
	"time"
)

type Config struct {
	ProjectID, ProjectURL, Output      string
	Resume, IncludeRaw, DownloadAssets bool
	Since                              time.Time
	Concurrency                        int
}
type Collector struct {
	client   *taskprobe.Client
	cfg      Config
	root     string
	mu       sync.Mutex
	counts   map[string]int
	errors   []map[string]any
	seen     map[string]bool
	done     []string
	warnings []string
}
type Manifest struct {
	SchemaVersion     string            `json:"schema_version"`
	SourceSystem      string            `json:"source_system"`
	CollectorName     string            `json:"collector_name"`
	CollectorVersion  string            `json:"collector_version"`
	RunID             string            `json:"run_id"`
	Mode              string            `json:"mode"`
	ProjectExternalID string            `json:"project_external_id"`
	ProjectURL        string            `json:"project_url"`
	StartedAt         string            `json:"started_at"`
	FinishedAt        string            `json:"finished_at"`
	Status            string            `json:"status"`
	SourceTimezone    string            `json:"source_timezone"`
	Counts            map[string]int    `json:"counts"`
	Coverage          map[string]string `json:"coverage"`
	Warnings          []string          `json:"warnings"`
	Resume            map[string]any    `json:"resume"`
}

func New(c *taskprobe.Client, cfg Config) *Collector {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	return &Collector{client: c, cfg: cfg, counts: map[string]int{}, seen: map[string]bool{}}
}
func (c *Collector) Run(ctx context.Context) (Manifest, error) {
	if c.cfg.ProjectID == "" {
		return Manifest{}, fmt.Errorf("project id is required")
	}
	c.root = filepath.Join(c.cfg.Output, "teambition-collector", c.cfg.ProjectID)
	for _, d := range []string{"entities", "raw", "assets", "checkpoints"} {
		if e := os.MkdirAll(filepath.Join(c.root, d), 0755); e != nil {
			return Manifest{}, e
		}
	}
	if c.cfg.Resume && c.cfg.ProjectURL == "" {
		var previous Manifest
		if b, err := os.ReadFile(filepath.Join(c.root, "manifest.json")); err == nil && json.Unmarshal(b, &previous) == nil {
			c.cfg.ProjectURL = previous.ProjectURL
		}
	}
	started := time.Now().UTC()
	runID := started.Format("20060102T150405.000000000Z")
	m := Manifest{"1.0", "teambition", "tb-collector", "1", runID, "full", c.cfg.ProjectID, c.cfg.ProjectURL, started.Format(time.RFC3339), "", "running", "Asia/Shanghai", map[string]int{}, map[string]string{}, nil, map[string]any{"resumed": c.cfg.Resume, "checkpoint_version": "1"}}
	if !c.cfg.Resume {
		_ = os.Remove(filepath.Join(c.root, "errors.jsonl"))
	}
	_ = c.writeManifest(m)
	c.ensureEntityFiles()
	c.collectProjectContext(ctx)
	tasks, err := c.listTasks(ctx)
	if err != nil {
		c.addError("list_tasks", "project", c.cfg.ProjectID, err, false)
		m.Status = "failed"
		m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		m.Counts = c.counts
		_ = c.writeManifest(m)
		return m, err
	}
	for _, t := range tasks {
		if e := c.collectTask(ctx, t); e != nil {
			c.addError("fetch_task", "task", fmt.Sprint(t["id"]), e, false)
		}
	}
	m.Counts = c.actualCounts()
	validationWarnings, validationFailed := c.validateEntities()
	m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	m.Status = "succeeded"
	if validationFailed {
		m.Status = "failed"
	} else if len(c.errors) > 0 || len(validationWarnings) > 0 {
		m.Status = "partial"
	}
	m.Coverage = map[string]string{"project": "complete", "task_groups": "complete", "users": "complete", "tags": "unavailable", "tasks": "complete", "task_priorities": "complete", "task_relations": "partial", "comments": "complete", "activities": "complete", "attachments": "partial"}
	for _, warning := range c.warnings {
		if strings.HasPrefix(warning, "stage metadata unavailable") {
			m.Coverage["task_groups"] = "partial"
		}
		if strings.HasPrefix(warning, "user profile unavailable") || strings.HasPrefix(warning, "user profile lookup failed") {
			m.Coverage["users"] = "partial"
		}
	}
	m.Warnings = append(m.Warnings, c.warnings...)
	m.Warnings = append(m.Warnings, validationWarnings...)
	if len(c.errors) > 0 {
		m.Warnings = append(m.Warnings, "one or more source calls failed; see errors.jsonl")
	}
	_ = c.writeManifest(m)
	_ = c.writeChecksums()
	return m, nil
}
func (c *Collector) listTasks(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	token := ""
	for {
		args := map[string]any{"projectId": c.cfg.ProjectID, "pageSize": 100, "includeArchived": true}
		if token != "" {
			args["pageToken"] = token
		}
		raw, _, e := c.client.Call(ctx, "SearchProjectTasksV3", args)
		if e != nil {
			return nil, e
		}
		data := unwrap(raw)
		arr := arrayFrom(data)
		for _, x := range arr {
			if t, ok := x.(map[string]any); ok {
				if !c.cfg.Since.IsZero() {
					if s, _ := t["updated"].(string); s != "" {
						if tm, e := time.Parse(time.RFC3339, s); e == nil && tm.Before(c.cfg.Since) {
							continue
						}
					}
				}
				if id, _ := t["id"].(string); id != "" {
					out = append(out, t)
				}
			}
		}
		token = pageToken(data)
		if token == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"]) })
	return out, nil
}
func (c *Collector) collectTask(ctx context.Context, summary map[string]any) error {
	id, _ := summary["id"].(string)
	if id == "" {
		return fmt.Errorf("task id missing")
	}
	if c.cfg.Resume && (c.seen[id] || c.entityExists("tasks", id)) {
		return nil
	}
	c.seen[id] = true
	detail, _, e := c.client.Call(ctx, "QueryTaskV3", map[string]any{"taskId": id})
	if e != nil {
		return e
	}
	d := unwrap(detail)
	source := firstObject(d)
	if source == nil {
		return fmt.Errorf("task detail %s returned no object", id)
	}
	if statusRaw, _, statusErr := c.client.Call(ctx, "QueryTaskTfs", map[string]any{"taskId": id}); statusErr == nil {
		statusID, _ := source["tfsId"].(string)
		for _, x := range arrayFrom(unwrap(statusRaw)) {
			if v, ok := x.(map[string]any); ok && v["id"] == statusID {
				source["tfsName"] = v["name"]
				break
			}
		}
	} else {
		c.addError("QueryTaskTfs", "task", id, statusErr, false)
	}
	env := c.envelope("task", id, source, normalizeTask(source))
	if e = c.writeEntity("tasks", id, env); e != nil {
		return e
	}
	c.writeRaw("tasks", id, d)
	c.counts["tasks"]++
	c.ensureStageGroup(source)
	if parent, _ := source["parentTaskId"].(string); parent != "" {
		rid := "parent:" + parent + ":" + id
		relation := map[string]any{"source_task_external_id": id, "target_task_external_id": parent, "relation_type": "parent", "target_source_url": nil, "target_visibility": "visible"}
		c.writeRaw("task_relations", rid, mustJSON(relation))
		_ = c.writeEntity("task_relations", rid, c.envelope("task_relation", rid, source, relation))
		c.counts["task_relations"]++
	}
	for _, spec := range []struct {
		name, entity string
		args         map[string]any
	}{{"ListTaskActivitiesV3", "activities", map[string]any{"taskId": id, "pageSize": 100}}, {"GetTaskLinksV3", "task_relations", map[string]any{"taskId": id}}, {"GetTaskDependenciesV3", "task_relations", map[string]any{"taskId": id, "pageSize": 100}}, {"GetTaskTracesV3", "activities", map[string]any{"taskId": id, "pageSize": 100}}} {
		token := ""
		for {
			args := map[string]any{}
			for k, v := range spec.args {
				args[k] = v
			}
			if token != "" {
				args["pageToken"] = token
			}
			next, er := c.collectTaskSource(ctx, id, spec.name, spec.entity, args)
			if er != nil {
				c.addError(spec.name, "task", id, er, false)
				break
			}
			token = next
			if token == "" {
				break
			}
		}
	}
	c.done = append(c.done, id)
	_ = c.writeCheckpoint()
	return nil
}

func (c *Collector) collectTaskSource(ctx context.Context, taskID, name, entity string, args map[string]any) (string, error) {
	raw, _, err := c.client.Call(ctx, name, args)
	if err != nil {
		return "", err
	}
	data := unwrap(raw)
	c.writeRaw(entity, taskID+"-"+name, data)
	var resourceIDs []string
	if name == "GetTaskLinksV3" {
		resourceIDs = append(resourceIDs, workResourceIDs(data)...)
	}
	if name == "ListTaskActivitiesV3" {
		resourceIDs = append(resourceIDs, activityResourceIDs(taskID, data)...)
	}
	for i, x := range arrayFrom(data) {
		obj, ok := x.(map[string]any)
		if !ok {
			continue
		}
		eid, _ := obj["id"].(string)
		if eid == "" {
			eid = fmt.Sprintf("%s-%s-%d", taskID, name, i)
		}
		outEntity, outData := entity, any(obj)
		if name == "GetTaskLinksV3" {
			if typ, _ := obj["linkedType"].(string); typ != "task" {
				continue
			}
			outEntity, outData = "task_relations", normalizeRelation(taskID, obj)
		}
		if name == "GetTaskDependenciesV3" {
			outEntity, outData = "task_relations", normalizeDependency(taskID, obj)
		}
		if name == "ListTaskActivitiesV3" {
			if action, _ := obj["action"].(string); strings.Contains(strings.ToLower(action), "comment") {
				outEntity, outData = "comments", normalizeComment(taskID, obj)
			} else {
				outData = normalizeActivity(taskID, obj)
			}
		}
		c.writeRaw(outEntity, eid, mustJSON(obj))
		if e := c.writeEntity(outEntity, eid, c.envelope(outEntity, eid, obj, outData)); e == nil {
			c.counts[outEntity]++
		}
	}
	if len(resourceIDs) > 0 {
		c.collectAttachments(ctx, taskID, resourceIDs)
	}
	return pageToken(data), nil
}

func (c *Collector) collectProjectContext(ctx context.Context) {
	specs := []struct {
		name, entity string
		args         map[string]any
	}{
		{"QueryProjectsV3", "projects", map[string]any{"projectIds": c.cfg.ProjectID, "pageSize": 10}},
		// Task lists are containers, not Kanban columns. Preserve their raw response
		// but create task groups exclusively from stages.
		{"SearchTaskGroupsV3", "", map[string]any{"projectId": c.cfg.ProjectID, "pageSize": 100}},
		{"SearchStagesV3", "task_groups", map[string]any{"projectId": c.cfg.ProjectID, "pageSize": 100}},
	}
	for _, spec := range specs {
		raw, _, e := c.client.Call(ctx, spec.name, spec.args)
		if e != nil {
			c.addError(spec.name, "project", c.cfg.ProjectID, e, false)
			continue
		}
		data := unwrap(raw)
		rawKind := spec.entity
		if rawKind == "" {
			rawKind = "task_lists"
		}
		c.writeRaw(rawKind, spec.name, data)
		if spec.entity == "" {
			continue
		}
		for i, x := range arrayFrom(data) {
			obj, ok := x.(map[string]any)
			if !ok {
				continue
			}
			id, _ := obj["id"].(string)
			if id == "" {
				id = fmt.Sprintf("%s-%d", spec.entity, i)
			}
			if c.cfg.Resume && c.entityExists(spec.entity, id) {
				continue
			}
			normalized := obj
			if spec.entity == "projects" {
				normalized = normalizeProject(obj)
			}
			if spec.entity == "task_groups" {
				normalized = normalizeTaskGroup(obj)
				c.writeRaw("task_groups", id, mustJSON(obj))
			}
			if spec.entity == "users" {
				normalized = normalizeUser(obj)
			}
			c.writeRaw(spec.entity, id, mustJSON(obj))
			if e := c.writeEntity(spec.entity, id, c.envelope(spec.entity, id, obj, normalized)); e == nil {
				c.counts[spec.entity]++
			}
		}
	}
	c.collectProjectMembers(ctx)
}

func (c *Collector) collectProjectMembers(ctx context.Context) {
	members := map[string]map[string]any{}
	var userIDs []string
	var allMembers []any
	token := ""
	for {
		args := map[string]any{"projectId": c.cfg.ProjectID, "pageSize": 100}
		if token != "" {
			args["pageToken"] = token
		}
		raw, _, err := c.client.Call(ctx, "ListProjectMembersV3", args)
		if err != nil {
			c.addError("ListProjectMembersV3", "project", c.cfg.ProjectID, err, false)
			return
		}
		data := unwrap(raw)
		allMembers = append(allMembers, arrayFrom(data)...)
		token = pageToken(data)
		if token == "" {
			break
		}
	}
	c.writeRaw("users", "ListProjectMembersV3", mustJSON(allMembers))
	for _, x := range allMembers {
		member, ok := x.(map[string]any)
		if !ok {
			continue
		}
		userID, _ := member["userId"].(string)
		if userID == "" {
			userID, _ = member["id"].(string)
		}
		if userID != "" && members[userID] == nil {
			members[userID] = member
			userIDs = append(userIDs, userID)
		}
	}
	if len(userIDs) == 0 {
		return
	}

	profileByID := map[string]map[string]any{}
	profileRaw, _, profileErr := c.client.Call(ctx, "PostV3MemberQuery", map[string]any{
		"userIds":   strings.Join(userIDs, ","),
		"isDisable": "all",
	})
	if profileErr != nil {
		c.warnings = append(c.warnings, "user profile lookup failed: "+profileErr.Error())
	} else {
		profileData := unwrap(profileRaw)
		c.writeRaw("users", "PostV3MemberQuery", profileData)
		for _, x := range arrayFrom(profileData) {
			profile, ok := x.(map[string]any)
			if !ok {
				continue
			}
			userID, _ := profile["userId"].(string)
			if userID != "" {
				profileByID[userID] = profile
				c.writeRaw("users", userID, mustJSON(profile))
			}
		}
	}

	for _, userID := range userIDs {
		member := members[userID]
		profile := profileByID[userID]
		source := mergeUserSource(member, profile)
		source["userId"] = userID
		source["projectMemberId"] = member["id"]
		source["projectRoleIds"] = member["roleIds"]
		if profile == nil {
			source["profileLookupStatus"] = "not_found"
			c.warnings = append(c.warnings, "user profile unavailable: "+userID)
		} else {
			source["profileLookupStatus"] = "matched"
		}
		if c.cfg.Resume && c.entityExists("users", userID) {
			continue
		}
		c.writeRaw("users", userID, mustJSON(source))
		if e := c.writeEntity("users", userID, c.envelope("user", userID, source, normalizeUser(source))); e == nil {
			c.counts["users"]++
		}
	}
}

// ensureStageGroup creates a clearly marked fallback when stage metadata is not
// available through SearchStagesV3. It never derives a name from a task title.
func (c *Collector) ensureStageGroup(task map[string]any) {
	id, _ := task["stageId"].(string)
	if id == "" || c.entityExists("task_groups", id) {
		return
	}
	v := map[string]any{
		"id": id, "name": nil, "pos": nil, "isArchived": nil,
		"derived": true, "name_source": "unresolved", "unresolved_name": true,
	}
	c.writeRaw("task_groups", id, mustJSON(v))
	data := normalizeTaskGroup(v)
	data["derived"] = true
	data["name_source"] = "unresolved"
	data["unresolved_name"] = true
	if c.writeEntity("task_groups", id, c.envelope("task_group", id, v, data)) == nil {
		c.counts["task_groups"]++
		c.warnings = append(c.warnings, "stage metadata unavailable; derived stage group: "+id)
	}
}

func (c *Collector) collectAttachments(ctx context.Context, taskID string, resourceIDs []string) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	raw, _, err := c.client.Call(ctx, "BatchGetFileDetails", map[string]any{"requestBody": map[string]any{"resourceIds": ids, "needSign": c.cfg.DownloadAssets, "expireAfterSeconds": 600}})
	if err != nil {
		c.addError("batch_file_details", "task", taskID, err, false)
		return
	}
	data := unwrap(raw)
	c.writeRaw("attachments", taskID+"-BatchGetFileDetails", data)
	for _, x := range arrayFrom(data) {
		v, ok := x.(map[string]any)
		if !ok {
			continue
		}
		id, _ := v["resourceId"].(string)
		if id == "" {
			id, _ = v["id"].(string)
		}
		if id == "" {
			continue
		}
		name, _ := v["fileName"].(string)
		if name == "" {
			name = id
		}
		var local any
		download := map[string]any{"url": nil, "expires_at": nil, "is_ephemeral": true}
		if c.cfg.DownloadAssets {
			if u, _ := v["downloadUrl"].(string); u != "" {
				dest := filepath.Join(c.root, "assets", taskID, safeFilename(name))
				size, hash, e := c.client.DownloadURL(ctx, u, dest)
				if e != nil {
					c.addError("download_attachment", "attachment", id, e, true)
				} else {
					v["downloadedSize"] = size
					v["checksum"] = hash
					local = filepath.ToSlash(filepath.Join("assets", taskID, safeFilename(name)))
				}
			}
		}
		transferStatus := "pending_download_url"
		retry := map[string]any{"recommended_action": "request a short-lived download URL with BatchGetFileDetails needSign=true", "retryable": true}
		if local != nil {
			transferStatus = "downloaded"
			retry = nil
		}
		obj := map[string]any{"name": name, "size": v["fileSize"], "mime_type": v["mimeType"], "storage_key": id, "checksum": v["checksum"], "scope": "task", "task_external_id": taskID, "comment_external_id": nil, "parent_folder_external_id": nil, "is_folder": false, "download": download, "local_asset_ref": local, "transfer_status": transferStatus, "retry": retry}
		c.writeRaw("attachments", id, mustJSON(v))
		_ = c.writeEntity("attachments", id, c.envelope("attachment", id, v, obj))
		c.counts["attachments"]++
	}
}
func workResourceIDs(raw []byte) []string {
	var out []string
	for _, x := range arrayFrom(raw) {
		if v, ok := x.(map[string]any); ok {
			typ, _ := v["linkedType"].(string)
			id, _ := v["linkedId"].(string)
			if typ == "work" && id != "" {
				out = append(out, "work:"+id)
			}
		}
	}
	return out
}
func activityResourceIDs(taskID string, raw []byte) []string {
	var out []string
	for _, x := range arrayFrom(raw) {
		v, ok := x.(map[string]any)
		if !ok {
			continue
		}
		activity, _ := v["id"].(string)
		collectFileIDs(v, func(file string) {
			if activity != "" {
				out = append(out, "task:"+taskID+"/activity:"+activity+"/file:"+file)
			}
		})
	}
	return out
}
func collectFileIDs(v map[string]any, add func(string)) {
	for key, val := range v {
		switch key {
		case "fileId", "fileID":
			if s, ok := val.(string); ok {
				add(s)
			}
		case "fileIds", "fileIDs":
			if a, ok := val.([]any); ok {
				for _, x := range a {
					if s, ok := x.(string); ok {
						add(s)
					}
				}
			}
		}
		if child, ok := val.(map[string]any); ok {
			collectFileIDs(child, add)
		}
	}
}
func collectUserIDs(v map[string]any, add func(string)) {
	for key, val := range v {
		switch key {
		case "userId", "userID", "mentionedUserId":
			if s, ok := val.(string); ok {
				add(s)
			}
		case "userIds", "userIDs", "mentionedUserIds":
			if a, ok := val.([]any); ok {
				for _, x := range a {
					if s, ok := x.(string); ok {
						add(s)
					}
				}
			}
		}
		if child, ok := val.(map[string]any); ok {
			collectUserIDs(child, add)
		}
	}
}
func activityPayload(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if s, ok := v.(string); ok {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
		return map[string]any{"parse_status": "unavailable", "raw_text": s}
	}
	return map[string]any{}
}
func arrayValue(v any) []any {
	if values, ok := v.([]any); ok {
		return values
	}
	return []any{}
}
func stageNameSource(v map[string]any, name any) string {
	if source, ok := v["name_source"].(string); ok && source != "" {
		return source
	}
	if name != nil {
		return "SearchStagesV3"
	}
	return "unresolved"
}
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
func safeFilename(name string) string {
	name = regexp.MustCompile("[<>:\"/\\\\|?*\\x00-\\x1f]").ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.TrimRight(name, " .")
	if name == "" {
		return "unnamed"
	}
	return name
}

func (c *Collector) ensureEntityFiles() {
	for _, name := range []string{"projects", "task_groups", "users", "tags", "tasks", "task_relations", "comments", "activities", "attachments"} {
		p := filepath.Join(c.root, "entities", name+".jsonl")
		if !c.cfg.Resume {
			_ = os.WriteFile(p, nil, 0644)
			continue
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			f, _ := os.Create(p)
			if f != nil {
				_ = f.Close()
			}
		}
	}
}
func (c *Collector) writeCheckpoint() error {
	sort.Strings(c.done)
	b, _ := json.MarshalIndent(map[string]any{"version": "1", "project_external_id": c.cfg.ProjectID, "completed_task_external_ids": c.done, "updated_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	return atomicWrite(filepath.Join(c.root, "checkpoints", "checkpoint.json"), append(b, '\n'))
}
func (c *Collector) actualCounts() map[string]int {
	out := map[string]int{}
	for _, name := range []string{"projects", "task_groups", "users", "tags", "tasks", "task_relations", "comments", "activities", "attachments"} {
		b, _ := os.ReadFile(filepath.Join(c.root, "entities", name+".jsonl"))
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		out[name] = n
	}
	if b, _ := os.ReadFile(filepath.Join(c.root, "errors.jsonl")); len(b) > 0 {
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		out["errors"] = n
	} else {
		out["errors"] = 0
	}
	return out
}

func (c *Collector) validateEntities() ([]string, bool) {
	warnings := []string{}
	fatal := false
	ids := map[string]map[string]bool{}
	for _, name := range []string{"projects", "task_groups", "users", "tags", "tasks", "task_relations", "comments", "activities", "attachments"} {
		ids[name] = map[string]bool{}
		path := filepath.Join(c.root, "entities", name+".jsonl")
		b, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var v map[string]any
			if json.Unmarshal([]byte(line), &v) != nil {
				warnings = append(warnings, name+" contains invalid JSON")
				fatal = true
				continue
			}
			id, _ := v["external_id"].(string)
			if id == "" {
				warnings = append(warnings, name+" contains a record without external_id")
				fatal = true
				continue
			}
			if ids[name][id] {
				warnings = append(warnings, name+" duplicate external_id: "+id)
				fatal = true
			}
			ids[name][id] = true
		}
	}
	taskLines := readEntityMaps(filepath.Join(c.root, "entities", "tasks.jsonl"))
	for _, task := range taskLines {
		data, _ := task["data"].(map[string]any)
		if group, _ := data["task_group_external_id"].(string); group != "" && !ids["task_groups"][group] {
			warnings = append(warnings, "task group reference missing: "+group)
		}
		if user, _ := data["creator_external_user_id"].(string); user != "" && !ids["users"][user] {
			warnings = append(warnings, "creator reference missing: "+user)
		}
	}
	for _, name := range []string{"comments", "activities", "attachments"} {
		for _, v := range readEntityMaps(filepath.Join(c.root, "entities", name+".jsonl")) {
			data, _ := v["data"].(map[string]any)
			taskID, _ := data["task_external_id"].(string)
			if taskID == "" {
				taskID, _ = data["task_external_id"].(string)
			}
			if taskID != "" && !ids["tasks"][taskID] {
				warnings = append(warnings, name+" task reference missing: "+taskID)
			}
		}
	}
	for _, relation := range readEntityMaps(filepath.Join(c.root, "entities", "task_relations.jsonl")) {
		data, _ := relation["data"].(map[string]any)
		target, _ := data["target_task_external_id"].(string)
		if target != "" && !ids["tasks"][target] {
			warnings = append(warnings, "unresolved relation target: "+target)
		}
	}
	return uniqueStrings(warnings), fatal
}
func readEntityMaps(path string) []map[string]any {
	b, _ := os.ReadFile(path)
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
func (c *Collector) entityExists(kind, id string) bool {
	b, err := os.ReadFile(filepath.Join(c.root, "entities", kind+".jsonl"))
	return err == nil && strings.Contains(string(b), "\"external_id\":\""+id+"\"")
}
func normalizeProject(v map[string]any) map[string]any {
	return map[string]any{"name": v["name"], "description": v["description"], "status": v["status"], "organization_external_id": v["organizationId"], "owner_external_user_id": v["ownerId"], "member_external_user_ids": v["memberIds"], "source_timezone": "Asia/Shanghai"}
}
func normalizeTaskGroup(v map[string]any) map[string]any {
	name := first(v, "name", "title")
	return map[string]any{"name": name, "order": first(v, "pos", "order"), "color": v["color"], "archived": first(v, "isArchived", "archived"), "derived": first(v, "derived"), "name_source": stageNameSource(v, name), "unresolved_name": name == nil}
}
func normalizeUser(v map[string]any) map[string]any {
	profile, _ := v["profile"].(map[string]any)
	name := first(v, "name", "displayName", "nickName")
	if name == nil {
		name = first(profile, "name", "displayName", "nickName")
	}
	email := first(v, "email")
	if email == nil {
		email = profile["email"]
	}
	employee := first(v, "employeeNumber", "employeeNo")
	if employee == nil {
		employee = first(profile, "employeeNumber", "employeeNo")
	}
	status := "available"
	if !hasText(name) && !hasText(email) && !hasText(employee) {
		status = "unavailable"
	}
	identitySource := "ListProjectMembersV3"
	if v["profileLookupStatus"] != nil || v["profile"] != nil {
		identitySource = "PostV3MemberQuery"
	}
	return map[string]any{
		"display_name":               name,
		"email":                      email,
		"employee_number":            employee,
		"avatar_url":                 first(v, "avatarUrl", "avatar", "avatarURL"),
		"active":                     first(v, "active", "isActive"),
		"is_disabled":                first(v, "isDisabled"),
		"is_resigned":                first(v, "isResigned"),
		"identity_mapping_status":    status,
		"identity_source":            identitySource,
		"profile_lookup_status":      first(v, "profileLookupStatus"),
		"project_member_external_id": first(v, "projectMemberId"),
		"project_role_external_ids":  first(v, "projectRoleIds"),
	}
}

func mergeUserSource(member, profile map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range member {
		out[k] = v
	}
	for k, v := range profile {
		out[k] = v
	}
	return out
}

func hasText(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}
func normalizeTask(v map[string]any) map[string]any {
	done, _ := v["isDone"].(bool)
	category := "open"
	if done {
		category = "done"
	}
	format := first(v, "noteRenderMode", "renderMode")
	if format == nil {
		format = "plain_text"
	}
	status := map[string]any{"external_id": v["tfsId"], "name": v["tfsName"], "category": category, "is_done": done}
	priorityName := priorityDisplayName(v)
	priority := map[string]any{"external_id": v["priority"], "name": nil, "rank": v["priority"], "source": "QueryTaskV3", "display_name_status": "unavailable"}
	if priorityName != "" {
		priority["name"] = priorityName
		priority["display_name_status"] = "available"
	}
	return map[string]any{"title": first(v, "content", "title"), "task_group_external_id": v["stageId"], "task_list_external_id": first(v, "tasklistId", "taskGroupId"), "order": v["pos"], "status": status, "description": map[string]any{"format": format, "text": v["note"], "html": nil, "document": nil}, "priority": priority, "start_at": v["startDate"], "due_at": v["dueDate"], "completed_at": v["accomplishTime"], "creator_external_user_id": v["creatorId"], "assignee_external_user_ids": manyOrOne(v["executorIds"], v["executorId"]), "participant_external_user_ids": arrayOrEmpty(v["involveMembers"]), "tag_external_ids": arrayOrEmpty(v["tagIds"]), "parent_external_task_id": v["parentTaskId"], "custom_fields": arrayOrEmpty(v["customfields"]), "archived": v["isArchived"], "deleted": false}
}

func priorityDisplayName(v map[string]any) string {
	name, _ := v["priorityName"].(string)
	return strings.TrimSpace(name)
}

func normalizeRelation(taskID string, v map[string]any) map[string]any {
	target, _ := v["linkedId"].(string)
	return map[string]any{"source_task_external_id": taskID, "target_task_external_id": target, "relation_type": "related", "target_source_url": nil, "target_visibility": "visible"}
}
func normalizeDependency(taskID string, v map[string]any) map[string]any {
	from, _ := v["fromId"].(string)
	to, _ := v["toId"].(string)
	kind, _ := v["kind"].(string)
	target := to
	relation := "blocks"
	if from != taskID {
		target = from
		relation = "blocked_by"
	}
	if target == "" {
		target, _ = v["taskId"].(string)
		relation = "unknown"
	}
	return map[string]any{"source_task_external_id": taskID, "target_task_external_id": target, "relation_type": relation, "target_source_url": nil, "target_visibility": "visible", "source_dependency_kind": kind}
}
func normalizeComment(taskID string, v map[string]any) map[string]any {
	text := v["message"]
	mentions := []string{}
	attachments := []string{}
	parseStatus := "available"
	if raw, ok := v["content"].(string); ok && raw != "" {
		var inner map[string]any
		if json.Unmarshal([]byte(raw), &inner) == nil {
			if comment, ok := inner["comment"]; ok {
				text = comment
			}
			collectUserIDs(inner, func(id string) { mentions = append(mentions, id) })
			collectFileIDs(inner, func(id string) { attachments = append(attachments, id) })
			for _, item := range arrayValue(inner["attachments"]) {
				if attachment, ok := item.(map[string]any); ok {
					if id, ok := attachment["id"].(string); ok && id != "" {
						attachments = append(attachments, id)
					}
				}
			}
		} else {
			parseStatus = "unavailable"
		}
	}
	return map[string]any{"task_external_id": taskID, "author_external_user_id": v["creatorId"], "content": map[string]any{"format": "plain_text", "text": text, "html": nil, "document": nil}, "mentioned_external_user_ids": uniqueStrings(mentions), "attachment_external_ids": uniqueStrings(attachments), "reply_to_external_comment_id": nil, "deleted": false, "content_parse_status": parseStatus}
}
func normalizeActivity(taskID string, v map[string]any) map[string]any {
	payload := activityPayload(v["content"])
	return map[string]any{"task_external_id": taskID, "activity_type": v["action"], "actor_external_user_id": v["creatorId"], "occurred_at": first(v, "createTime", "updateTime"), "summary": v["message"], "payload": payload}
}
func first(v map[string]any, keys ...string) any {
	for _, k := range keys {
		if x, ok := v[k]; ok && x != nil {
			return x
		}
	}
	return nil
}
func oneOrEmpty(v any) []any {
	if v == nil {
		return []any{}
	}
	return []any{v}
}
func manyOrOne(many, one any) []any {
	if a, ok := many.([]any); ok {
		return a
	}
	return oneOrEmpty(one)
}
func arrayOrEmpty(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}
func (c *Collector) envelope(typ, id string, source map[string]any, data any) map[string]any {
	typ = singularEntityType(typ)
	b, _ := json.Marshal(data)
	h := sha256.Sum256(b)
	var sourceURL any
	if c.cfg.ProjectURL != "" {
		sourceURL = c.cfg.ProjectURL
		if typ == "task" {
			sourceURL = strings.TrimRight(c.cfg.ProjectURL, "/") + "/task/" + id
		}
	}
	completeness := "complete"
	missing := []string{}
	warnings := []string{}
	if m, ok := data.(map[string]any); ok {
		if m["identity_mapping_status"] == "unavailable" {
			completeness = "partial"
			missing = append(missing, "display_name", "email", "employee_number")
			warnings = append(warnings, "user identity fields unavailable from ListProjectMembersV3")
		}
		if m["content_parse_status"] == "unavailable" {
			completeness = "partial"
			missing = append(missing, "content.text")
			warnings = append(warnings, "comment content JSON could not be parsed")
		}
	}
	var rawRef any
	if c.cfg.IncludeRaw {
		rawRef = filepath.ToSlash(filepath.Join("raw", rawEntityDir(typ), rawFilename(id)))
	}
	return map[string]any{"schema_version": "1.0", "entity_type": typ, "source_system": "teambition", "external_id": id, "project_external_id": c.cfg.ProjectID, "source_url": sourceURL, "source_created_at": source["created"], "source_updated_at": source["updated"], "fetched_at": time.Now().UTC().Format(time.RFC3339), "visibility": "visible", "completeness": completeness, "missing_fields": missing, "warnings": warnings, "fingerprint": "sha256:" + hex.EncodeToString(h[:]), "raw_ref": rawRef, "data": data}
}
func rawEntityDir(typ string) string {
	if dir, ok := map[string]string{"project": "projects", "task_group": "task_groups", "user": "users", "tag": "tags", "task": "tasks", "task_relation": "task_relations", "comment": "comments", "activity": "activities", "attachment": "attachments"}[typ]; ok {
		return dir
	}
	return typ
}
func singularEntityType(v string) string {
	if singular, ok := map[string]string{"projects": "project", "task_groups": "task_group", "users": "user", "tags": "tag", "tasks": "task", "task_relations": "task_relation", "comments": "comment", "activities": "activity", "attachments": "attachment"}[v]; ok {
		return singular
	}
	return v
}
func (c *Collector) writeEntity(kind, id string, v map[string]any) error {
	p := filepath.Join(c.root, "entities", kind+".jsonl")
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	old, _ := os.ReadFile(p)
	return atomicWrite(p, append(old, append(b, '\n')...))
}
func (c *Collector) writeRaw(kind, id string, v json.RawMessage) {
	if !c.cfg.IncludeRaw {
		return
	}
	p := filepath.Join(c.root, "raw", kind)
	_ = os.MkdirAll(p, 0755)
	_ = atomicWrite(filepath.Join(p, rawFilename(id)), append(v, '\n'))
}

func rawFilename(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = "unnamed"
	}
	return safeFilename(url.PathEscape(id)) + ".json"
}
func (c *Collector) addError(stage, typ, id string, e error, retry bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, map[string]any{"run_id": "", "stage": stage, "entity_type": typ, "external_id": id, "code": "source_error", "retryable": retry, "message": e.Error(), "occurred_at": time.Now().UTC().Format(time.RFC3339)})
	p := filepath.Join(c.root, "errors.jsonl")
	b, _ := json.Marshal(c.errors[len(c.errors)-1])
	old, _ := os.ReadFile(p)
	_ = atomicWrite(p, append(old, append(b, '\n')...))
}
func (c *Collector) writeManifest(m Manifest) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return atomicWrite(filepath.Join(c.root, "manifest.json"), append(b, '\n'))
}
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (c *Collector) writeChecksums() error {
	var paths []string
	_ = filepath.Walk(c.root, func(p string, i os.FileInfo, e error) error {
		if e == nil && !i.IsDir() && filepath.Base(p) != "checksums.sha256" {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	f, e := os.Create(filepath.Join(c.root, "checksums.sha256"))
	if e != nil {
		return e
	}
	defer f.Close()
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		rel, _ := filepath.Rel(c.root, p)
		fmt.Fprintf(f, "%s  %s\n", hex.EncodeToString(h[:]), filepath.ToSlash(rel))
	}
	return nil
}
func unwrap(raw json.RawMessage) json.RawMessage {
	var o struct {
		Result  json.RawMessage `json:"result"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &o) == nil && len(o.Content) > 0 {
		var x json.RawMessage
		if json.Unmarshal([]byte(o.Content[0].Text), &x) == nil {
			return unwrap(x)
		}
	}
	if len(o.Result) > 0 {
		return unwrap(o.Result)
	}
	return raw
}
func arrayFrom(raw []byte) []any {
	var x any
	if json.Unmarshal(raw, &x) != nil || x == nil {
		return nil
	}
	if a, ok := x.([]any); ok {
		return a
	}
	if m, ok := x.(map[string]any); ok {
		for _, k := range []string{"result", "items", "data", "list"} {
			if a, ok := m[k].([]any); ok {
				return a
			}
		}
	}
	return nil
}
func firstObject(raw []byte) map[string]any {
	for _, x := range arrayFrom(raw) {
		if m, ok := x.(map[string]any); ok {
			return m
		}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		return m
	}
	return nil
}
func pageToken(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"nextPageToken", "pageToken", "nextToken"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
