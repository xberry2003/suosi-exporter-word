package tbinventory

import (
	"bytes"
	"context"
	"fmt"
	openapi "github.com/teambition/openapi-sdk-golang"
	"io"
	"net/http"
	"strings"
)

type SDKClient struct {
	client            *openapi.APIClient
	orgID, operatorID string
}

func NewSDKClient(cfg Config) *SDKClient {
	c := openapi.NewConfiguration(cfg.AppID, cfg.AppSecret)
	c.BasePath = cfg.APIBase
	c.AddDefaultHeader("X-Tenant-Type", "organization")
	return &SDKClient{client: openapi.NewAPIClient(c), orgID: cfg.OrgID, operatorID: cfg.OperatorID}
}
func (s *SDKClient) ListFiles(ctx context.Context, pid, parent, token string, o ListOptions) (Page, int, error) {
	if o.PageSize < 1 {
		o.PageSize = 100
	}
	r := s.client.FileAPI.ListFilesV3(ctx).XTenantId(s.orgID).ProjectId(pid).DisplayPrefixPath(true).IncludeArchived(o.IncludeArchived).PageSize(int32(o.PageSize))
	if parent != "" {
		r = r.ParentId(parent)
	}
	if token != "" {
		r = r.PageToken(token)
	}
	resp, httpResp, err := r.Execute()
	status := httpStatus(httpResp)
	raw := responseBody(httpResp)
	p := Page{Diagnostics: ResponseDiagnostics{HTTPStatus: status, RawResponse: raw}}
	if err != nil {
		return p, status, sdkError(err, httpResp, raw)
	}
	if resp == nil {
		return p, status, &APIError{Status: status, Message: "Teambition returned an empty response"}
	}
	p.Diagnostics.BusinessCode = resp.Code
	p.Diagnostics.ErrorMessage = resp.GetErrorMessage()
	p.Diagnostics.RequestID = resp.GetRequestId()
	if businessErr := businessError(status, resp.Code, resp.GetErrorMessage(), resp.GetRequestId()); businessErr != nil {
		return p, status, businessErr
	}
	if resp.Result == nil {
		return p, status, &APIError{Status: status, BusinessCode: resp.Code, RequestID: resp.GetRequestId(), Message: "Teambition response has no result object"}
	}
	p.NextPageToken = resp.Result.GetNextPageToken()
	for _, f := range resp.Result.Collections {
		p.Collections = append(p.Collections, Collection{ID: f.GetId(), ParentID: f.GetParentId(), Title: f.GetTitle(), PrefixPath: f.GetPrefixPath(), CreatorID: f.GetCreatorId(), Archived: f.GetIsArchived(), Created: f.GetCreated(), Updated: f.GetUpdated()})
	}
	for _, w := range resp.Result.Works {
		var size *int64
		if w.FileSize != nil {
			v := int64(*w.FileSize)
			if v >= 0 {
				size = &v
			}
		}
		p.Works = append(p.Works, Work{ID: w.GetId(), ParentID: w.GetParentId(), PrefixPath: w.GetPrefixPath(), FileName: w.GetFileName(), MIMEType: w.GetFileType(), FileSize: size, Archived: w.GetIsArchived(), CreatorID: w.GetCreatorId(), Created: w.GetCreated(), Updated: w.GetUpdated(), SourcePageURL: WorkURL(pid, w.GetParentId(), w.GetId())})
	}
	return p, status, nil
}
func (s *SDKClient) ListProjects(ctx context.Context, pageSize int, operatorID string) ([]Project, error) {
	if pageSize < 1 {
		pageSize = 100
	}
	var ids []string
	token := ""
	op := operatorID
	if op == "" {
		op = s.operatorID
	}
	for {
		r := s.client.ProjectAPI.ListUserProjectsV3(ctx).XTenantId(s.orgID).PageSize(int32(pageSize))
		if token != "" {
			r = r.PageToken(token)
		}
		if op != "" {
			r = r.XOperatorId(op)
		}
		resp, h, err := r.Execute()
		if err != nil {
			return nil, sdkErr(err, h)
		}
		if resp == nil {
			return nil, &APIError{Status: httpStatus(h), Message: "Teambition returned an empty project-list response"}
		}
		if e := businessError(httpStatus(h), resp.Code, resp.GetErrorMessage(), resp.GetRequestId()); e != nil {
			return nil, e
		}
		ids = append(ids, resp.Result...)
		token = resp.GetNextPageToken()
		if token == "" {
			break
		}
	}
	ids = unique(ids)
	if len(ids) == 0 {
		return nil, nil
	}
	var out []Project
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		token = ""
		for {
			q := s.client.ProjectAPI.QueryProjectsV3(ctx).XTenantId(s.orgID).ProjectIds(strings.Join(ids[start:end], ",")).PageSize(int32(pageSize))
			if op != "" {
				q = q.XOperatorId(op)
			}
			if token != "" {
				q = q.PageToken(token)
			}
			resp, h, err := q.Execute()
			if err != nil {
				return nil, sdkErr(err, h)
			}
			if resp == nil {
				return nil, &APIError{Status: httpStatus(h), Message: "Teambition returned an empty project-query response"}
			}
			if e := businessError(httpStatus(h), resp.Code, resp.GetErrorMessage(), resp.GetRequestId()); e != nil {
				return nil, e
			}
			for _, p := range resp.Result {
				out = append(out, Project{ID: p.GetId(), Name: p.GetName(), URL: "https://www.teambition.com/project/" + p.GetId(), Archived: p.GetIsArchived()})
			}
			token = resp.GetNextPageToken()
			if token == "" {
				break
			}
		}
	}
	return mergeProjectDetails(out), nil
}
func mergeProjectDetails(in []Project) []Project {
	m := map[string]bool{}
	var out []Project
	for _, p := range in {
		if p.ID != "" && !m[p.ID] {
			m[p.ID] = true
			out = append(out, p)
		}
	}
	return out
}
func unique(in []string) []string {
	m := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !m[v] {
			m[v] = true
			out = append(out, v)
		}
	}
	return out
}
func sdkErr(err error, r *http.Response) error {
	return sdkError(err, r, responseBody(r))
}
func sdkError(err error, r *http.Response, raw string) error {
	status := httpStatus(r)
	msg := err.Error()
	if raw != "" {
		msg = raw
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &APIError{Status: status, Message: msg}
}
func httpStatus(r *http.Response) int {
	if r == nil {
		return 0
	}
	return r.StatusCode
}
func responseBody(r *http.Response) string {
	if r == nil || r.Body == nil {
		return ""
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	if len(b) > 8192 {
		b = b[:8192]
	}
	return string(b)
}
func businessError(status int, code *float32, message, requestID string) error {
	msg := strings.TrimSpace(message)
	failedCode := code != nil && *code != 0 && *code != 200
	if msg == "" && !failedCode {
		return nil
	}
	if code != nil {
		if msg == "" {
			msg = fmt.Sprintf("Teambition business error code %.0f", *code)
		} else {
			msg = fmt.Sprintf("Teambition business error code %.0f: %s", *code, msg)
		}
	}
	if requestID != "" {
		msg += " (requestId=" + requestID + ")"
	}
	return &APIError{Status: status, BusinessCode: code, RequestID: requestID, Message: msg}

}
