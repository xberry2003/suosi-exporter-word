package taskprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Input struct {
	ProjectID, TaskID, Output string
	Resume                    bool
}
type Coverage struct {
	Category             string   `json:"category"`
	Command              string   `json:"command"`
	HTTPStatus           int      `json:"httpStatus"`
	Success              bool     `json:"success"`
	ReturnedCount        int      `json:"returnedCount"`
	ExpectedCount        any      `json:"expectedCount,omitempty"`
	KeyFields            []string `json:"keyFields,omitempty"`
	Downloadable         *bool    `json:"downloadable,omitempty"`
	FailureReason        string   `json:"failureReason,omitempty"`
	BrowserLoginRequired bool     `json:"browserLoginRequired"`
	Classification       string   `json:"classification"`
	Resumed              bool     `json:"resumed,omitempty"`
}
type Report struct {
	ProjectID, TaskID string
	GeneratedAt       string     `json:"generatedAt"`
	Results           []Coverage `json:"results"`
	Downloads         []Download `json:"downloads,omitempty"`
}
type Download struct {
	ResourceID    string `json:"resourceId"`
	OriginalName  string `json:"originalName"`
	LocalName     string `json:"localName"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	Success       bool   `json:"success"`
	FailureReason string `json:"failureReason,omitempty"`
}

func ParseRef(projectRaw, taskRaw string) (Input, error) {
	p := strings.TrimSpace(projectRaw)
	t := strings.TrimSpace(taskRaw)
	if strings.Contains(p, "://") {
		u, e := url.Parse(p)
		if e != nil {
			return Input{}, e
		}
		seg := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(seg) >= 2 && seg[0] == "project" {
			p = seg[1]
		}
	}
	if strings.Contains(t, "://") {
		u, e := url.Parse(t)
		if e != nil {
			return Input{}, e
		}
		seg := strings.Split(strings.Trim(u.Path, "/"), "/")
		for i := range seg {
			if seg[i] == "task" && i+1 < len(seg) {
				t = seg[i+1]
				break
			}
		}
		if strings.Contains(t, "://") {
			return Input{}, fmt.Errorf("task URL must contain /task/{taskId}")
		}
	}
	if !validID(p) || !validID(t) {
		return Input{}, fmt.Errorf("invalid project or task ID")
	}
	return Input{ProjectID: p, TaskID: t}, nil
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,}$`)

func validID(s string) bool { return idRe.MatchString(s) }

func Run(ctx context.Context, c *Client, in Input) (Report, error) {
	root := filepath.Join(in.Output, "teambition", "task-probe", in.ProjectID, in.TaskID)
	rawDir := filepath.Join(root, "raw")
	if err := os.MkdirAll(rawDir, 0755); err != nil {
		return Report{}, err
	}
	state := filepath.Join(root, "state.json")
	rep := Report{ProjectID: in.ProjectID, TaskID: in.TaskID, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Results: []Coverage{}}
	responses := map[string]json.RawMessage{}
	call := func(name string, args any, cat string) (json.RawMessage, error) {
		path := rawPath(rawDir, name, args)
		raw, status, e, resumed := json.RawMessage(nil), 0, error(nil), false
		if in.Resume {
			if saved, readErr := os.ReadFile(path); readErr == nil {
				raw, status, resumed = saved, 200, true
			}
		}
		if !resumed {
			raw, status, e = c.Call(ctx, name, args)
		}
		if e == nil {
			raw = unwrap(raw)
			e = businessError(raw)
			if e == nil {
				responses[name] = raw
			}
		}
		cov := Coverage{Category: cat, Command: name, HTTPStatus: status, Success: e == nil, ExpectedCount: "unknown", BrowserLoginRequired: false, Classification: classify(status, e), Resumed: resumed}
		if e != nil {
			cov.FailureReason = e.Error()
		} else {
			cov.ReturnedCount = countItems(raw)
			cov.KeyFields = presentFields(raw, []string{"id", "taskId", "content", "projectId", "created", "updated"})
			_ = os.WriteFile(path, append(raw, '\n'), 0644)
		}
		rep.Results = append(rep.Results, cov)
		_ = writeReport(root, rep)
		_ = os.WriteFile(state, []byte(fmt.Sprintf("{\"last\":%q,\"updated\":%q}\n", name, time.Now().UTC().Format(time.RFC3339))), 0644)
		return raw, e
	}
	if _, e := call("QueryTaskV3", map[string]any{"taskId": in.TaskID}, "task detail"); e != nil {
		return rep, e
	}
	for _, spec := range []struct {
		name, cat string
		args      map[string]any
	}{
		{"GetTaskLinksV3", "links", map[string]any{"taskId": in.TaskID}},
		{"GetTaskDependenciesV3", "dependencies", map[string]any{"taskId": in.TaskID, "pageSize": 100}},
		{"SearchTaskGroupsV3", "task groups", map[string]any{"projectId": in.ProjectID, "pageSize": 100}},
		{"SearchProjectCustomFiledsV3", "custom fields", map[string]any{"projectId": in.ProjectID, "scope": "kanbanCardAdd", "pageSize": 100}},
		{"ListProjectMembersV3", "project members", map[string]any{"projectId": in.ProjectID, "pageSize": 100}},
		{"QueryProjectsV3", "project", map[string]any{"projectIds": in.ProjectID, "pageSize": 10}},
		{"QueryTaskTfs", "task status", map[string]any{"taskId": in.TaskID}},
		{"SearchTaskflowsV3", "task workflows", map[string]any{"projectId": in.ProjectID, "pageSize": 100}},
		{"SearchTaskflowStatusesV3", "workflow statuses", map[string]any{"projectId": in.ProjectID, "pageSize": 100}},
		{"GetScenarioFieldsV3", "task types", map[string]any{"projectId": in.ProjectID, "objectTypes": "task", "pageSize": 100}},
		{"SearchStagesV3", "board stages", map[string]any{"projectId": in.ProjectID, "pageSize": 100}},
	} {
		raw, e := call(spec.name, spec.args, spec.cat)
		if e == nil {
			for hasPageToken(raw) {
				tok := nextPageToken(raw)
				if tok == "" {
					break
				}
				spec.args["pageToken"] = tok
				raw, e = call(spec.name, spec.args, spec.cat)
				if e != nil {
					break
				}
			}
		}
	}
	if linkRaw := responses["GetTaskLinksV3"]; len(linkRaw) > 0 {
		ids := workResourceIDs(linkRaw)
		if len(ids) > 0 {
			if detail, e := call("BatchGetFileDetails", map[string]any{"requestBody": map[string]any{"resourceIds": ids, "needSign": true, "expireAfterSeconds": 600}}, "file metadata/download permission"); e == nil {
				rep.Downloads = downloadFiles(ctx, c, detail, filepath.Join(root, "downloads"))
				downloadable := len(rep.Downloads) > 0
				for _, d := range rep.Downloads {
					downloadable = downloadable && d.Success
				}
				for i := range rep.Results {
					if rep.Results[i].Command == "BatchGetFileDetails" {
						rep.Results[i].Downloadable = &downloadable
					}
				}
			}
		}
	}
	if taskRaw := responses["QueryTaskV3"]; len(taskRaw) > 0 && strings.Contains(string(taskRaw), `"noteRenderMode":"rtf"`) {
		_, _ = call("RenderTaskRtfV3", map[string]any{"rtfFields": in.TaskID + ":note"}, "rich text note")
	}
	_ = writeReport(root, rep)
	return rep, nil
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

func businessError(raw []byte) error {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	code := 0
	if n, ok := v["code"].(float64); ok {
		code = int(n)
	}
	msg, _ := v["errorMessage"].(string)
	if code != 0 && code != 200 {
		return fmt.Errorf("Teambition business code %d: %s", code, msg)
	}
	if msg != "" {
		return fmt.Errorf("Teambition business error: %s", msg)
	}
	return nil
}
func countItems(raw []byte) int {
	var x any
	if json.Unmarshal(raw, &x) != nil {
		return 0
	}
	if x == nil {
		return 0
	}
	switch v := x.(type) {
	case []any:
		return len(v)
	case map[string]any:
		for _, k := range []string{"result", "items", "data", "list"} {
			if a, ok := v[k].([]any); ok {
				return len(a)
			}
		}
	}
	return 1
}
func presentFields(raw []byte, fields []string) []string {
	var x any
	_ = json.Unmarshal(raw, &x)
	b, _ := json.Marshal(x)
	s := string(b)
	var out []string
	for _, f := range fields {
		if strings.Contains(s, "\""+f+"\"") {
			out = append(out, f)
		}
	}
	return out
}
func hasPageToken(raw []byte) bool { return nextPageToken(raw) != "" }
func nextPageToken(raw []byte) string {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	for _, k := range []string{"nextPageToken", "pageToken", "nextToken"} {
		if s, ok := v[k].(string); ok && s != "" {
			return s
		}
	}
	if r, ok := v["result"].(map[string]any); ok {
		for _, k := range []string{"nextPageToken", "pageToken", "nextToken"} {
			if s, ok := r[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
func safe(s string) string { return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(s) }
func rawPath(dir, name string, args any) string {
	b, _ := json.Marshal(args)
	h := sha256.Sum256(b)
	return filepath.Join(dir, fmt.Sprintf("%s-%s.json", safe(name), hex.EncodeToString(h[:4])))
}
func classify(status int, err error) string {
	if err == nil {
		return "A"
	}
	msg := err.Error()
	if status == 401 || status == 403 || strings.Contains(msg, "code 401") || strings.Contains(msg, "code 403") {
		return "B"
	}
	if status == 404 {
		return "D"
	}
	return "C"
}
func writeReport(root string, r Report) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "coverage-report.json"), append(b, '\n'), 0644); err != nil {
		return err
	}
	var md strings.Builder
	md.WriteString("# Teambition task probe\n\n")
	for _, x := range r.Results {
		fmt.Fprintf(&md, "- %s: %s (HTTP %d, returned %d)\n", x.Category, map[bool]string{true: "success", false: "failed"}[x.Success], x.HTTPStatus, x.ReturnedCount)
		if x.FailureReason != "" {
			fmt.Fprintf(&md, "  - failure: %s\n", x.FailureReason)
		}
	}
	return os.WriteFile(filepath.Join(root, "coverage-report.md"), []byte(md.String()), 0644)
}
func FileSHA256(path string) (int64, string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return 0, "", e
	}
	h := sha256.Sum256(b)
	return int64(len(b)), hex.EncodeToString(h[:]), nil
}

func workResourceIDs(raw []byte) []string {
	var a []map[string]any
	if json.Unmarshal(raw, &a) != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range a {
		typ, _ := v["linkedType"].(string)
		id, _ := v["linkedId"].(string)
		if typ == "work" && id != "" && !seen[id] {
			seen[id] = true
			out = append(out, "work:"+id)
		}
	}
	return out
}
func downloadFiles(ctx context.Context, c *Client, raw []byte, dir string) []Download {
	var items []map[string]any
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	_ = os.MkdirAll(dir, 0755)
	var out []Download
	for _, v := range items {
		d := Download{}
		d.ResourceID, _ = v["resourceId"].(string)
		d.OriginalName, _ = v["fileName"].(string)
		if d.OriginalName == "" {
			d.OriginalName = d.ResourceID
		}
		d.LocalName = safeFilename(d.OriginalName)
		u, _ := v["downloadUrl"].(string)
		if u == "" {
			d.FailureReason = "download URL not returned"
			out = append(out, d)
			continue
		}
		size, hash, err := download(ctx, c, u, filepath.Join(dir, d.LocalName))
		if err != nil {
			d.FailureReason = err.Error()
		} else {
			d.Size, d.SHA256, d.Success = size, hash, true
		}
		out = append(out, d)
	}
	return out
}
func safeFilename(name string) string {
	name = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`).ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.TrimRight(name, " .")
	if name == "" {
		return "unnamed"
	}
	return name
}
func download(ctx context.Context, c *Client, rawURL, path string) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	} else {
		_ = os.Remove(tmp)
	}
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
