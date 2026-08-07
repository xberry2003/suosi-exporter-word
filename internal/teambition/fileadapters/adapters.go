package fileadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"thoughtsexport/internal/tbinventory"
	"thoughtsexport/internal/tbweb"
	"thoughtsexport/internal/teambition/filecollector"
)

type Browser struct{ Client *tbweb.Client }

func (b Browser) ListFiles(ctx context.Context, projectID, parentID, pageToken string, opts tbinventory.ListOptions) (tbinventory.Page, int, error) {
	return b.Client.ListFiles(ctx, projectID, parentID, pageToken, opts)
}

func (b Browser) ResolveDownload(ctx context.Context, projectID, nodeID, _ string) (filecollector.DownloadDescriptor, int, error) {
	if b.Client == nil || b.Client.HTTP == nil {
		return filecollector.DownloadDescriptor{}, 0, errors.New("browser file source is not configured")
	}
	base := strings.TrimRight(b.Client.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/works/"+url.PathEscape(nodeID), nil)
	if err != nil {
		return filecollector.DownloadDescriptor{}, 0, err
	}
	setBrowserHeaders(req, b.Client)
	resp, err := b.Client.HTTP.Do(req)
	if err != nil {
		return filecollector.DownloadDescriptor{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return filecollector.DownloadDescriptor{}, resp.StatusCode, fmt.Errorf("Teambition file-detail HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return filecollector.DownloadDescriptor{}, resp.StatusCode, err
	}
	var detail struct {
		ID          string `json:"_id"`
		ProjectID   string `json:"_projectId"`
		FileType    string `json:"fileType"`
		FileSize    *int64 `json:"fileSize"`
		FileKey     string `json:"fileKey"`
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return filecollector.DownloadDescriptor{}, resp.StatusCode, errors.New("Teambition file-detail response was not valid JSON")
	}
	if detail.ID != nodeID || detail.ProjectID != projectID {
		return filecollector.DownloadDescriptor{}, resp.StatusCode, errors.New("Teambition file-detail identity did not match the requested project file")
	}
	if err := validateDownloadURL(detail.DownloadURL); err != nil {
		return filecollector.DownloadDescriptor{}, resp.StatusCode, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/octet-stream, */*")
	headers.Set("Cookie", b.Client.CookieHeader)
	headers.Set("Referer", b.Client.Referer)
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	return filecollector.DownloadDescriptor{URL: detail.DownloadURL, ExpectedSize: detail.FileSize, MIMEType: detail.FileType, SourceStorageKey: detail.FileKey, Headers: headers}, resp.StatusCode, nil
}

func setBrowserHeaders(req *http.Request, client *tbweb.Client) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Cookie", client.CookieHeader)
	req.Header.Set("Referer", client.Referer)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
}

func validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("Teambition file detail returned an invalid download URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "teambition.com" && !strings.HasSuffix(host, ".teambition.com") && host != "teambition.net" && !strings.HasSuffix(host, ".teambition.net") {
		return errors.New("Teambition file detail returned a download URL outside the allowed source domains")
	}
	return nil
}

type SDK struct{ Client *tbinventory.SDKClient }

func (s SDK) ListFiles(ctx context.Context, projectID, parentID, pageToken string, opts tbinventory.ListOptions) (tbinventory.Page, int, error) {
	return s.Client.ListFiles(ctx, projectID, parentID, pageToken, opts)
}

func (s SDK) ResolveDownload(ctx context.Context, _, nodeID, _ string) (filecollector.DownloadDescriptor, int, error) {
	if s.Client == nil {
		return filecollector.DownloadDescriptor{}, 0, errors.New("SDK file source is not configured")
	}
	detail, status, err := s.Client.ResolveFileDownload(ctx, nodeID)
	if err != nil {
		return filecollector.DownloadDescriptor{}, status, err
	}
	if err := validateDownloadURL(detail.URL); err != nil {
		return filecollector.DownloadDescriptor{}, status, err
	}
	return filecollector.DownloadDescriptor{URL: detail.URL, ExpectedSize: detail.Size, MIMEType: detail.MIMEType}, status, nil
}
