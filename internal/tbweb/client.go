package tbweb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"thoughtsexport/internal/tbinventory"
	"time"
)

const defaultBaseURL = "https://www.teambition.com"

type Client struct {
	HTTP         *http.Client
	BaseURL      string
	CookieHeader string
	Referer      string
}

func NewClient(cookieHeader, referer string) *Client {
	return &Client{
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		BaseURL:      defaultBaseURL,
		CookieHeader: cookieHeader,
		Referer:      referer,
	}
}

type collectionDTO struct {
	ID        string `json:"_id"`
	ParentID  string `json:"_parentId"`
	CreatorID string `json:"_creatorId"`
	Title     string `json:"title"`
	Created   string `json:"created"`
	Updated   string `json:"updated"`
	Archived  bool   `json:"isArchived"`
}

type workDTO struct {
	ID        string `json:"_id"`
	ParentID  string `json:"_parentId"`
	CreatorID string `json:"_creatorId"`
	FileName  string `json:"fileName"`
	FileType  string `json:"fileType"`
	FileSize  *int64 `json:"fileSize"`
	Created   string `json:"created"`
	Updated   string `json:"updated"`
	Archived  bool   `json:"isArchived"`
}

func (c *Client) ListFiles(ctx context.Context, projectID, parentID, pageToken string, opts tbinventory.ListOptions) (tbinventory.Page, int, error) {
	pageNumber := 1
	if pageToken != "" {
		parsed, err := strconv.Atoi(pageToken)
		if err != nil || parsed < 1 {
			return tbinventory.Page{}, 0, fmt.Errorf("invalid page token %q", pageToken)
		}
		pageNumber = parsed
	}
	pageSize := opts.PageSize
	if pageSize < 1 {
		pageSize = 100
	}

	var collections []collectionDTO
	collectionStatus, collectionRaw, collectionDiag, err := c.getArray(ctx, "/api/collections", projectID, parentID, pageNumber, pageSize, &collections)
	if err != nil {
		return tbinventory.Page{Diagnostics: collectionDiag}, collectionStatus, err
	}
	var works []workDTO
	workStatus, workRaw, workDiag, err := c.getArray(ctx, "/api/works", projectID, parentID, pageNumber, pageSize, &works)
	if err != nil {
		workDiag.RawResponse = joinRaw(collectionRaw, workRaw)
		return tbinventory.Page{Diagnostics: workDiag}, workStatus, err
	}

	result := tbinventory.Page{Diagnostics: workDiag}
	result.Diagnostics.HTTPStatus = workStatus
	result.Diagnostics.RawResponse = joinRaw(collectionRaw, workRaw)
	for _, item := range collections {
		if item.Archived && !opts.IncludeArchived {
			continue
		}
		result.Collections = append(result.Collections, tbinventory.Collection{
			ID: item.ID, ParentID: item.ParentID, Title: item.Title, CreatorID: item.CreatorID,
			Archived: item.Archived, Created: item.Created, Updated: item.Updated,
		})
	}
	for _, item := range works {
		if item.Archived && !opts.IncludeArchived {
			continue
		}
		result.Works = append(result.Works, tbinventory.Work{
			ID: item.ID, ParentID: item.ParentID, FileName: item.FileName, MIMEType: item.FileType,
			CreatorID: item.CreatorID, FileSize: item.FileSize, Archived: item.Archived,
			Created: item.Created, Updated: item.Updated,
		})
	}
	if len(collections) == pageSize || len(works) == pageSize {
		result.NextPageToken = strconv.Itoa(pageNumber + 1)
	}
	return result, workStatus, nil
}

func (c *Client) getArray(ctx context.Context, path, projectID, parentID string, page, pageSize int, target any) (int, string, tbinventory.ResponseDiagnostics, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	u, err := url.Parse(base + path)
	if err != nil {
		return 0, "", tbinventory.ResponseDiagnostics{}, err
	}
	q := u.Query()
	q.Set("_projectId", projectID)
	q.Set("_parentId", parentID)
	q.Set("order", "updatedDesc")
	q.Set("count", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", tbinventory.ResponseDiagnostics{}, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cookie", c.CookieHeader)
	req.Header.Set("Origin", defaultBaseURL)
	req.Header.Set("Referer", c.Referer)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120 Safari/537.36")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, "", tbinventory.ResponseDiagnostics{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, "", tbinventory.ResponseDiagnostics{HTTPStatus: resp.StatusCode}, err
	}
	raw := string(body)
	diag := tbinventory.ResponseDiagnostics{
		HTTPStatus:  resp.StatusCode,
		RequestID:   firstHeader(resp.Header, "X-Request-Id", "X-Request-ID", "Request-Id"),
		RawResponse: raw,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		readBusinessError(body, &diag)
		return resp.StatusCode, raw, diag, apiError(diag, resp.Status)
	}
	if err := json.Unmarshal(body, target); err != nil {
		readBusinessError(body, &diag)
		if diag.BusinessCode != nil || diag.ErrorMessage != "" {
			return resp.StatusCode, raw, diag, apiError(diag, "Teambition business error")
		}
		return resp.StatusCode, raw, diag, fmt.Errorf("unexpected %s response: %w", path, err)
	}
	return resp.StatusCode, raw, diag, nil
}

func readBusinessError(body []byte, diag *tbinventory.ResponseDiagnostics) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return
	}
	for _, key := range []string{"code", "status", "statusCode"} {
		if raw, ok := object[key]; ok {
			var number float32
			if json.Unmarshal(raw, &number) == nil {
				diag.BusinessCode = &number
				break
			}
		}
	}
	for _, key := range []string{"errorMessage", "message", "error_description", "error"} {
		if raw, ok := object[key]; ok {
			var message string
			if json.Unmarshal(raw, &message) == nil && message != "" {
				diag.ErrorMessage = message
				break
			}
		}
	}
}

func apiError(diag tbinventory.ResponseDiagnostics, fallback string) error {
	message := diag.ErrorMessage
	if message == "" {
		message = fallback
	}
	return &tbinventory.APIError{Status: diag.HTTPStatus, BusinessCode: diag.BusinessCode, RequestID: diag.RequestID, Message: message}
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func joinRaw(collections, works string) string {
	return "collections:\n" + collections + "\nworks:\n" + works
}
