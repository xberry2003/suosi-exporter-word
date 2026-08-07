package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"thoughtsexport/internal/teambition/taskprobe"
)

func TestCollectorFixturePaginationAndResume(t *testing.T) {
	var callsMu sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var req map[string]any
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Fatal("invalid request")
		}
		id, _ := req["id"].(float64)
		method, _ := req["method"].(string)
		params, _ := req["params"].(map[string]any)
		if method == "initialize" {
			writeRPC(w, int(id), map[string]any{})
			return
		}
		name, _ := params["name"].(string)
		callsMu.Lock()
		calls[name]++
		callsMu.Unlock()
		args, _ := params["arguments"].(map[string]any)
		var payload any
		switch name {
		case "SearchProjectTasksV3":
			if args["pageToken"] == "p2" {
				payload = map[string]any{"items": []any{map[string]any{"id": "task-2"}}}
			} else {
				payload = map[string]any{"items": []any{map[string]any{"id": "task-1", "updated": "2026-08-06T00:00:00Z"}}, "nextPageToken": "p2"}
			}
		case "QueryProjectsV3":
			payload = map[string]any{"items": []any{map[string]any{"id": "project-1", "name": "Fixture"}}}
		case "SearchTaskGroupsV3":
			payload = map[string]any{"items": []any{map[string]any{"id": "group-1", "title": "Backlog"}}}
		case "ListProjectMembersV3":
			payload = map[string]any{"items": []any{map[string]any{"id": "member-1", "userId": "user-1", "roleIds": []any{"role-1"}}}}
		case "PostV3MemberQuery":
			payload = map[string]any{"code": 200, "result": []any{map[string]any{"userId": "user-1", "name": "Alice", "profile": map[string]any{"email": "alice@example.test", "employeeNumber": "E-1"}, "isDisabled": false, "isResigned": false}}}
		case "QueryTaskV3":
			payload = []any{map[string]any{"id": args["taskId"], "content": "fixture", "projectId": "project-1", "tasklistId": "group-1", "isDone": false, "involveMembers": []any{}}}
		case "GetTaskLinksV3":
			payload = map[string]any{"items": []any{map[string]any{"id": "link-1", "linkedType": "work", "linkedId": "work-1"}}}
		case "QueryTaskTfs":
			payload = map[string]any{"items": []any{}}
		case "BatchGetFileDetails":
			body, _ := args["requestBody"].(map[string]any)
			var files []any
			for _, id := range body["resourceIds"].([]any) {
				files = append(files, map[string]any{"resourceId": id, "fileName": "unsafe:name?.txt", "fileSize": 12, "mimeType": "text/plain"})
			}
			payload = map[string]any{"items": files}
		default:
			payload = map[string]any{"items": []any{}}
		}
		writeRPC(w, int(id), payload)
	}))
	defer server.Close()
	root := t.TempDir()
	cfg := taskprobe.Config{Host: server.URL, Token: "fixture", HTTP: server.Client(), Retries: 0}
	c := New(taskprobe.NewClient(cfg), Config{ProjectID: "project-1", ProjectURL: "https://www.teambition.com/project/project-1/tasks/view/view-1", Output: root, IncludeRaw: true, Concurrency: 1})
	m, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Counts["tasks"] != 2 {
		t.Fatalf("unexpected counts: %#v", m.Counts)
	}
	if _, ok := m.Counts["comments"]; ok {
		t.Fatalf("comments must not be emitted: %#v", m.Counts)
	}
	if _, ok := m.Counts["activities"]; ok {
		t.Fatalf("activities must not be emitted: %#v", m.Counts)
	}
	if m.Coverage["comments"] != "unavailable" || m.Coverage["activities"] != "unavailable" {
		t.Fatalf("excluded engagement coverage is ambiguous: %#v", m.Coverage)
	}
	callsMu.Lock()
	activityCalls := calls["ListTaskActivitiesV3"] + calls["GetTaskTracesV3"]
	callsMu.Unlock()
	if activityCalls != 0 {
		t.Fatalf("comments or activities were requested %d times", activityCalls)
	}
	for _, name := range []string{"comments.jsonl", "activities.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, "teambition-collector", "project-1", "entities", name)); !os.IsNotExist(err) {
			t.Fatalf("excluded entity file %s should not be created", name)
		}
	}
	assertRawRefsExist(t, filepath.Join(root, "teambition-collector", "project-1"))
	users := readLines(t, filepath.Join(root, "teambition-collector", "project-1", "entities", "users.jsonl"))
	if len(users) != 1 || !strings.Contains(users[0], `"external_id":"user-1"`) || !strings.Contains(users[0], `"display_name":"Alice"`) {
		t.Fatalf("user profile was not joined by userId: %v", users)
	}
	lines := readLines(t, filepath.Join(root, "teambition-collector", "project-1", "entities", "tasks.jsonl"))
	if len(lines) != 2 {
		t.Fatalf("tasks lines=%d", len(lines))
	}
	if !strings.Contains(lines[0], "\"assignee_external_user_ids\":[]") {
		t.Fatal("missing empty assignee array")
	}
	c2 := New(taskprobe.NewClient(cfg), Config{ProjectID: "project-1", Output: root, Resume: true, IncludeRaw: true, Concurrency: 1})
	m2, err := c2.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m2.Counts["tasks"] != 2 || len(readLines(t, filepath.Join(root, "teambition-collector", "project-1", "entities", "tasks.jsonl"))) != 2 {
		t.Fatalf("resume duplicated tasks: %#v", m2.Counts)
	}
	if m2.ProjectURL != "https://www.teambition.com/project/project-1/tasks/view/view-1" {
		t.Fatalf("resume did not preserve project URL: %q", m2.ProjectURL)
	}
}

func TestSingularEntityTypeIsIdempotent(t *testing.T) {
	for _, typ := range []string{"task_relation", "attachment", "task", "project"} {
		if got := singularEntityType(typ); got != typ {
			t.Fatalf("singularEntityType(%q) = %q", typ, got)
		}
	}
}

func TestAuditNormalizationRequirements(t *testing.T) {
	task := normalizeTask(map[string]any{"stageId": "stage-1", "tasklistId": "list-1"})
	if task["task_group_external_id"] != "stage-1" || task["task_list_external_id"] != "list-1" {
		t.Fatalf("stage and task-list meanings were mixed: %#v", task)
	}
	comment := normalizeComment("task-1", map[string]any{"content": `{"comment":"restored body","attachments":[{"id":"file-1"}]}`})
	content := comment["content"].(map[string]any)
	if content["text"] != "restored body" || comment["content_parse_status"] != "available" {
		t.Fatalf("comment content was not restored: %#v", comment)
	}
	if ids := comment["attachment_external_ids"].([]string); len(ids) != 1 || ids[0] != "file-1" {
		t.Fatalf("comment attachments missing: %#v", comment)
	}
	activity := normalizeActivity("task-1", map[string]any{"content": `{"field":"value"}`})
	if payload := activity["payload"].(map[string]any); payload["field"] != "value" {
		t.Fatalf("activity payload was not decoded: %#v", activity)
	}
	user := normalizeUser(map[string]any{"id": "member-1", "userId": "user-1"})
	if user["identity_mapping_status"] != "unavailable" {
		t.Fatalf("identity gap not explicit: %#v", user)
	}
}

func TestPriorityNormalizationDoesNotInventDisplayNames(t *testing.T) {
	withoutName := normalizeTask(map[string]any{"priority": float64(0)})
	priority := withoutName["priority"].(map[string]any)
	if priority["external_id"] != float64(0) || priority["name"] != nil || priority["display_name_status"] != "unavailable" {
		t.Fatalf("unexpected unresolved priority: %#v", priority)
	}
	withName := normalizeTask(map[string]any{"priority": float64(0), "priorityName": "一般任务-老实习生"})
	priority = withName["priority"].(map[string]any)
	if priority["name"] != "一般任务-老实习生" || priority["display_name_status"] != "available" {
		t.Fatalf("priority name was not preserved: %#v", priority)
	}
	if priorityDisplayName(map[string]any{"priorityName": nil}) != "" {
		t.Fatal("nil priority name was treated as a display name")
	}
}

func TestRawFilenameEscapesPathSeparators(t *testing.T) {
	got := rawFilename("task:task-1/activity:activity-1/file:file/inner:1")
	if strings.ContainsAny(strings.TrimSuffix(got, ".json"), `/\`) {
		t.Fatalf("raw filename still contains path separator: %q", got)
	}
	if !strings.HasSuffix(got, ".json") {
		t.Fatalf("raw filename missing suffix: %q", got)
	}
}

func writeRPC(w http.ResponseWriter, id int, payload any) {
	b, _ := json.Marshal(payload)
	out := map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func assertRawRefsExist(t *testing.T, root string) {
	t.Helper()
	entityDir := filepath.Join(root, "entities")
	entries, err := os.ReadDir(entityDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		for _, line := range readLines(t, filepath.Join(entityDir, entry.Name())) {
			var env map[string]any
			if err := json.Unmarshal([]byte(line), &env); err != nil {
				t.Fatal(err)
			}
			ref, _ := env["raw_ref"].(string)
			if ref == "" {
				t.Fatalf("%s missing raw_ref: %s", entry.Name(), line)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
				t.Fatalf("%s raw_ref does not exist: %s (%v)", entry.Name(), ref, err)
			}
		}
	}
}
