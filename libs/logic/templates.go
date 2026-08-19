package logic

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const templateSchemaVersion = "thoughts-template-export/v2"

// TemplateExportOptions controls the standalone template export operation.
// Both DOCX and editable HTML are always exported.
type TemplateExportOptions struct {
	URL              string
	OutputRoot       string
	Cookie           string
	TemplateID       string
	IncludeRaw       bool
	Overwrite        bool
	RetryFailed      bool
	DryRun           bool
	WorkspaceID      string
	WorkspaceName    string
	OrganizationName string
}

type TemplateManifestEntry struct {
	SchemaVersion        string   `json:"schema_version"`
	SourceSystem         string   `json:"source_system"`
	WorkspaceID          string   `json:"workspace_id"`
	WorkspaceName        string   `json:"workspace_name"`
	TemplateID           string   `json:"template_id"`
	RelatedTemplateID    *string  `json:"related_template_id"`
	ContentID            string   `json:"content_id"`
	Title                string   `json:"title"`
	Summary              []string `json:"summary"`
	Type                 string   `json:"type,omitempty"`
	BoundType            string   `json:"bound_type,omitempty"`
	BoundID              string   `json:"bound_id,omitempty"`
	Position             float64  `json:"position,omitempty"`
	CreatedAt            string   `json:"created_at,omitempty"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
	SourceOwnerID        string   `json:"source_owner_id,omitempty"`
	Deleted              bool     `json:"deleted"`
	LocalDir             string   `json:"local_dir"`
	DocxPath             string   `json:"docx_path,omitempty"`
	HTMLPath             string   `json:"html_path,omitempty"`
	SourcePath           string   `json:"source_path,omitempty"`
	PreviewPath          string   `json:"preview_path,omitempty"`
	Status               string   `json:"status"`
	ContentStatus        string   `json:"content_status"`
	HTMLStatus           string   `json:"html_status"`
	DOCXStatus           string   `json:"docx_status"`
	ContentSHA256        string   `json:"content_sha256,omitempty"`
	HTMLSHA256           string   `json:"html_sha256,omitempty"`
	DOCXSHA256           string   `json:"docx_sha256,omitempty"`
	AssetCount           int      `json:"asset_count"`
	ImageCount           int      `json:"image_count"`
	AttachmentCount      int      `json:"attachment_count"`
	DownloadedAssetCount int      `json:"downloaded_asset_count"`
	PendingAssetCount    int      `json:"pending_asset_count"`
	LinkCount            int      `json:"link_count"`
	AssetsManifestPath   string   `json:"assets_manifest_path,omitempty"`
	LinksManifestPath    string   `json:"links_manifest_path,omitempty"`
	Warnings             []string `json:"warnings"`
	Errors               []string `json:"errors"`
	ExportedAt           string   `json:"exported_at"`
}

type TemplateResourceEntry struct {
	SchemaVersion      string `json:"schema_version"`
	SourceSystem       string `json:"source_system"`
	ResourceType       string `json:"resource_type"`
	WorkspaceID        string `json:"workspace_id"`
	TemplateID         string `json:"template_id"`
	SourceBlockID      string `json:"source_block_id"`
	BlockIndex         int    `json:"block_index"`
	BlockNumber        int    `json:"block_number"`
	Kind               string `json:"kind"`
	SourceURL          string `json:"source_url,omitempty"`
	OriginalURL        string `json:"original_url,omitempty"`
	AnchorText         string `json:"anchor_text,omitempty"`
	StorageKey         string `json:"storage_key,omitempty"`
	OriginalFileName   string `json:"original_file_name,omitempty"`
	MIMEType           string `json:"mime_type,omitempty"`
	Size               int64  `json:"size,omitempty"`
	SignatureExpiresAt string `json:"signature_expires_at,omitempty"`
	LocalAssetPath     string `json:"local_asset_path,omitempty"`
	SHA256             string `json:"sha256,omitempty"`
	DownloadStatus     string `json:"download_status,omitempty"`
	Error              string `json:"error,omitempty"`
}

type TemplateResourceManifest struct {
	SchemaVersion string                  `json:"schema_version"`
	SourceSystem  string                  `json:"source_system"`
	ResourceType  string                  `json:"resource_type"`
	WorkspaceID   string                  `json:"workspace_id"`
	TemplateID    string                  `json:"template_id"`
	ExportedAt    string                  `json:"exported_at"`
	Checksum      string                  `json:"checksum"`
	Warnings      []string                `json:"warnings"`
	Entries       []TemplateResourceEntry `json:"entries"`
}

type TemplateManifestDocument struct {
	SchemaVersion string                  `json:"schema_version"`
	SourceSystem  string                  `json:"source_system"`
	ResourceType  string                  `json:"resource_type"`
	WorkspaceID   string                  `json:"workspace_id"`
	WorkspaceName string                  `json:"workspace_name"`
	ExportedAt    string                  `json:"exported_at"`
	Checksum      string                  `json:"checksum"`
	Warnings      []string                `json:"warnings"`
	Entries       []TemplateManifestEntry `json:"entries"`
}

type TemplateExportResult struct {
	Templates int                     `json:"templates"`
	Succeeded int                     `json:"succeeded"`
	Failed    int                     `json:"failed"`
	Entries   []TemplateManifestEntry `json:"entries"`
}

// ExportTemplates discovers and exports all templates bound to one workspace.
// It is independent from ExportWorkspace and can be called by another Go
// program without invoking the CLI.
func ExportTemplates(opts TemplateExportOptions) (TemplateExportResult, error) {
	var result TemplateExportResult
	if strings.TrimSpace(opts.URL) == "" {
		return result, errors.New("url is required")
	}
	workspaceID, err := workspaceIDFromURL(opts.URL)
	if err != nil {
		return result, err
	}
	if opts.OutputRoot == "" {
		opts.OutputRoot = "exports"
	}
	cookie := opts.Cookie
	if cookie == "" {
		cookie, err = GetLoginCookieString(opts.URL, "TB_ACCESS_TOKEN")
		if err != nil {
			return result, err
		}
	}
	req := NewRequest(cookie, workspaceID)
	workspace, err := req.GetWorkspace(workspaceID)
	if err != nil {
		return result, err
	}
	templates, err := req.GetTemplates(workspaceID)
	if err != nil {
		return result, err
	}
	if opts.TemplateID != "" {
		templates = filterTemplates(templates, opts.TemplateID)
		if len(templates) == 0 {
			return result, fmt.Errorf("template not found: %s", opts.TemplateID)
		}
	}
	root := filepath.Join(opts.OutputRoot, sanitizeName(workspace.Organization.Name), sanitizeName(workspace.Name), "templates")
	opts.WorkspaceID, opts.WorkspaceName, opts.OrganizationName = workspace.ID, workspace.Name, workspace.Organization.Name
	return exportTemplatesForWorkspace(req, opts, root, templates)
}

func exportTemplatesForWorkspace(req *Request, opts TemplateExportOptions, root string, templates []Template) (TemplateExportResult, error) {
	var result TemplateExportResult
	result.Templates = len(templates)
	if err := os.MkdirAll(root, 0755); err != nil {
		return result, err
	}
	manifestPath := filepath.Join(root, "templates_manifest.json")
	manifest := map[string]TemplateManifestEntry{}
	if err := loadTemplateManifest(manifestPath, manifest); err != nil {
		return result, err
	}
	if len(templates) == 0 {
		return result, saveTemplateManifest(manifestPath, manifest)
	}
	for _, template := range templates {
		entry := exportOneTemplate(req, opts, root, template, manifest)
		manifest[templateKey(template)] = entry
		result.Entries = append(result.Entries, entry)
		if entry.Status == "success" || entry.Status == "skipped" {
			result.Succeeded++
		} else {
			result.Failed++
		}
		if err := saveTemplateManifest(manifestPath, manifest); err != nil {
			return result, err
		}
	}
	return result, nil
}

func exportOneTemplate(req *Request, opts TemplateExportOptions, root string, template Template, previous map[string]TemplateManifestEntry) TemplateManifestEntry {
	localDir := filepath.Join(root, templateDirectoryName(template))
	old, hasOld := previous[templateKey(template)]
	entry := TemplateManifestEntry{
		SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts",
		WorkspaceID: opts.WorkspaceID, WorkspaceName: opts.WorkspaceName,
		TemplateID:        template.ID,
		RelatedTemplateID: optionalString(template.RelatedTemplateID),
		ContentID:         templateContentID(template),
		Title:             template.Title,
		Summary:           nonNilStrings(template.Summary), Type: firstNonEmptyValue(template.Type, "workspace"), BoundType: firstNonEmptyValue(template.BoundType, "workspace"),
		BoundID: firstNonEmptyValue(template.BoundID, opts.WorkspaceID), Position: template.Position, CreatedAt: template.CreatedAt,
		UpdatedAt: template.UpdatedAt, SourceOwnerID: template.SourceOwnerID, Deleted: template.Deleted,
		LocalDir: relativeExportPath(opts.OutputRoot, localDir),
		Status:   "pending", Warnings: []string{}, Errors: []string{},
		ExportedAt: time.Now().Format(time.RFC3339),
	}
	if hasOld && !opts.Overwrite && old.SchemaVersion == templateSchemaVersion && old.Status == "failed" && !opts.RetryFailed {
		return old
	}
	if template.ID == "" {
		entry.Status = "failed"
		entry.Errors = []string{"template has no _id"}
		return entry
	}
	if opts.DryRun && !opts.IncludeRaw {
		entry.Status = "skipped"
		return entry
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		entry.Status = "failed"
		entry.Errors = []string{err.Error()}
		return entry
	}
	sourcePath := filepath.Join(localDir, "template_source.json")
	entry.SourcePath = relativeExportPath(opts.OutputRoot, sourcePath)
	if err := os.WriteFile(sourcePath, template.Raw, 0600); err != nil {
		entry.Status = "failed"
		entry.ContentStatus = "unavailable"
		entry.HTMLStatus, entry.DOCXStatus = "not_generated", "not_generated"
		entry.Errors = []string{"source: " + err.Error()}
		return entry
	}
	preview, err := req.GetTemplatePreview(entry.ContentID)
	if err != nil {
		entry.Status = "failed"
		entry.ContentStatus = "failed"
		entry.HTMLStatus, entry.DOCXStatus = "not_generated", "not_generated"
		entry.Errors = []string{"preview: " + err.Error()}
		return entry
	}
	previewPath := filepath.Join(localDir, "template_preview.json")
	entry.PreviewPath = relativeExportPath(opts.OutputRoot, previewPath)
	if err := os.WriteFile(previewPath, preview, 0600); err != nil {
		entry.Status = "failed"
		entry.ContentStatus = "unavailable"
		entry.HTMLStatus, entry.DOCXStatus = "not_generated", "not_generated"
		entry.Errors = []string{"preview source: " + err.Error()}
		return entry
	}
	if opts.DryRun {
		entry.Status = "skipped"
		return entry
	}
	base := sanitizeName(template.Title)
	docx := filepath.Join(localDir, base+".docx")
	html := filepath.Join(localDir, base+".html")
	sections, err := decodeTemplateSections(preview)
	if err != nil {
		entry.Status = "failed"
		entry.ContentStatus = "failed"
		entry.HTMLStatus, entry.DOCXStatus = "not_generated", "not_generated"
		entry.Errors = []string{"preview structure: " + err.Error()}
		return entry
	}
	entry.ContentSHA256 = sha256Hex(preview)
	contentUnchanged := hasOld && !opts.Overwrite && old.SchemaVersion == entry.SchemaVersion && (old.Status == "success" || old.Status == "skipped") && old.UpdatedAt == entry.UpdatedAt && old.ContentSHA256 == entry.ContentSHA256
	regenerate := opts.Overwrite || !contentUnchanged
	assets, links := extractTemplateResources(sections, opts.WorkspaceID, template.ID)
	assets, assetWarnings := downloadTemplateAssets(req, opts, localDir, assets)
	entry.Warnings = append(entry.Warnings, assetWarnings...)
	entry.AssetCount, entry.LinkCount = len(assets), len(links)
	for _, asset := range assets {
		if asset.Kind == "image" {
			entry.ImageCount++
		} else {
			entry.AttachmentCount++
		}
		if asset.DownloadStatus == "downloaded" {
			entry.DownloadedAssetCount++
		} else {
			entry.PendingAssetCount++
		}
	}
	if entry.PendingAssetCount > 0 {
		entry.Errors = append(entry.Errors, fmt.Sprintf("%d template assets were not downloaded", entry.PendingAssetCount))
	}
	assetsPath, linksPath := filepath.Join(localDir, "assets_manifest.json"), filepath.Join(localDir, "links_manifest.json")
	entry.AssetsManifestPath, entry.LinksManifestPath = relativeExportPath(opts.OutputRoot, assetsPath), relativeExportPath(opts.OutputRoot, linksPath)
	if err := writeTemplateResourceManifest(assetsPath, "asset", opts.WorkspaceID, template.ID, assets); err != nil {
		entry.Warnings = append(entry.Warnings, "assets manifest: "+err.Error())
		entry.Errors = append(entry.Errors, "assets manifest: "+err.Error())
	}
	if err := writeTemplateResourceManifest(linksPath, "link", opts.WorkspaceID, template.ID, links); err != nil {
		entry.Warnings = append(entry.Warnings, "links manifest: "+err.Error())
		entry.Errors = append(entry.Errors, "links manifest: "+err.Error())
	}
	if len(sections) == 0 {
		entry.ContentStatus = "empty"
		entry.Warnings = append(entry.Warnings, "template preview has no sections")
	} else {
		entry.ContentStatus = "complete"
	}
	if !fileExists(html) || regenerate {
		htmlBytes := renderTemplateHTMLWithResources(template.Title, sections, assets, links, localDir, opts.OutputRoot)
		if err := os.WriteFile(html, htmlBytes, 0644); err != nil {
			entry.Errors = append(entry.Errors, "html: "+err.Error())
			entry.HTMLStatus = "failed"
		} else {
			entry.HTMLSHA256 = sha256Hex(htmlBytes)
		}
	}
	entry.HTMLPath = relativeExportPath(opts.OutputRoot, html)
	if fileExists(html) && entry.HTMLSHA256 == "" {
		if b, e := os.ReadFile(html); e == nil {
			entry.HTMLSHA256 = sha256Hex(b)
		}
	}
	if !fileExists(docx) || regenerate {
		if err := writeTemplateDOCX(docx, template.Title, sections); err != nil {
			entry.Errors = append(entry.Errors, "docx: "+err.Error())
			entry.DOCXStatus = "failed"
		} else if b, e := os.ReadFile(docx); e == nil {
			entry.DOCXSHA256 = sha256Hex(b)
		}
	}
	entry.DocxPath = relativeExportPath(opts.OutputRoot, docx)
	if fileExists(docx) && entry.DOCXSHA256 == "" {
		if b, e := os.ReadFile(docx); e == nil {
			entry.DOCXSHA256 = sha256Hex(b)
		}
	}
	if entry.HTMLStatus == "" {
		if entry.HTMLSHA256 == "" {
			entry.HTMLStatus = "missing"
		} else {
			entry.HTMLStatus = "complete"
		}
	}
	if entry.DOCXStatus == "" {
		if entry.DOCXSHA256 == "" {
			entry.DOCXStatus = "missing"
		} else {
			entry.DOCXStatus = "complete"
		}
	}
	if len(entry.Errors) == 0 {
		if contentUnchanged {
			entry.Status = "skipped"
		} else {
			entry.Status = "success"
		}
	} else {
		entry.Status = "failed"
	}
	return entry
}

func relativeExportPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	return filepath.ToSlash(rel)
}
func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return fmt.Sprintf("%x", sum[:]) }
func writeJSONFile(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}
func writeTemplateResourceManifest(path, resourceType, workspaceID, templateID string, entries []TemplateResourceEntry) error {
	entriesJSON, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	document := TemplateResourceManifest{SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts", ResourceType: resourceType, WorkspaceID: workspaceID, TemplateID: templateID, ExportedAt: time.Now().Format(time.RFC3339), Checksum: sha256Hex(entriesJSON), Warnings: []string{}, Entries: entries}
	return writeJSONFile(path, document, 0644)
}

func downloadTemplateAssets(req *Request, opts TemplateExportOptions, localDir string, assets []TemplateResourceEntry) ([]TemplateResourceEntry, []string) {
	warnings := []string{}
	if len(assets) == 0 {
		return assets, warnings
	}
	assetsDir := filepath.Join(localDir, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		for index := range assets {
			assets[index].DownloadStatus, assets[index].Error = "pending", err.Error()
		}
		return assets, []string{"create assets directory: " + err.Error()}
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	for index := range assets {
		asset := &assets[index]
		stableKey := asset.StorageKey
		if stableKey == "" {
			stableKey = sha256Hex([]byte(asset.SourceURL))[:16]
			asset.StorageKey = stableKey
		}
		fileName := sanitizeName(asset.OriginalFileName)
		if fileName == "" {
			fileName = "asset"
		}
		localPath := filepath.Join(assetsDir, sanitizeName(stableKey)+"__"+fileName)
		asset.LocalAssetPath = relativeExportPath(opts.OutputRoot, localPath)
		if fileExists(localPath) {
			if data, err := os.ReadFile(localPath); err == nil && (asset.Size == 0 || int64(len(data)) == asset.Size) {
				asset.Size, asset.SHA256, asset.DownloadStatus, asset.Error = int64(len(data)), sha256Hex(data), "downloaded", ""
				continue
			}
		}
		if asset.SourceURL == "" {
			asset.DownloadStatus, asset.Error = "pending", "download URL is empty"
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		httpRequest, err := http.NewRequest(http.MethodGet, asset.SourceURL, nil)
		if err != nil {
			asset.DownloadStatus, asset.Error = "pending", err.Error()
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		httpRequest.Header.Set("User-Agent", "Mozilla/5.0")
		if req != nil && req.cookie != "" {
			httpRequest.Header.Set("Cookie", req.cookie)
		}
		response, err := client.Do(httpRequest)
		if err != nil {
			asset.DownloadStatus, asset.Error = "pending", err.Error()
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			asset.DownloadStatus, asset.Error = "pending", fmt.Sprintf("download returned HTTP %d", response.StatusCode)
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		tempPath := localPath + ".part"
		file, createErr := os.Create(tempPath)
		if createErr != nil {
			_ = response.Body.Close()
			asset.DownloadStatus, asset.Error = "pending", createErr.Error()
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		_, copyErr := io.Copy(file, response.Body)
		closeErr := file.Close()
		_ = response.Body.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tempPath)
			asset.DownloadStatus, asset.Error = "pending", firstNonEmptyValue(errorString(copyErr), errorString(closeErr))
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		if err := os.Rename(tempPath, localPath); err != nil {
			_ = os.Remove(tempPath)
			asset.DownloadStatus, asset.Error = "pending", err.Error()
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		data, err := os.ReadFile(localPath)
		if err != nil {
			asset.DownloadStatus, asset.Error = "pending", err.Error()
			warnings = append(warnings, asset.OriginalFileName+": "+asset.Error)
			continue
		}
		asset.Size, asset.SHA256, asset.DownloadStatus, asset.Error = int64(len(data)), sha256Hex(data), "downloaded", ""
		if asset.MIMEType == "" {
			if parsed, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err == nil {
				asset.MIMEType = parsed
			}
		}
	}
	return assets, warnings
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func workspaceIDFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid workspace URL: %w", err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := range parts {
		if parts[i] == "workspaces" && i+1 < len(parts) && parts[i+1] != "" {
			return parts[i+1], nil
		}
	}
	return "", fmt.Errorf("workspace ID not found in URL: %s", raw)
}

func templateKey(template Template) string {
	if template.ID != "" {
		return template.ID
	}
	return template.Title
}

func templateDirectoryName(template Template) string {
	name := sanitizeName(template.Title)
	if template.ID == "" {
		return name
	}
	return name + "__" + sanitizeName(template.ID)
}

func templateContentID(template Template) string {
	if template.ContentID != "" {
		return template.ContentID
	}
	if template.SourceContentID != "" {
		return template.SourceContentID
	}
	if template.RelatedTemplateID != "" {
		return template.RelatedTemplateID
	}
	return template.ID
}

func filterTemplates(templates []Template, templateID string) []Template {
	filtered := make([]Template, 0, 1)
	for _, template := range templates {
		if template.ID == templateID {
			filtered = append(filtered, template)
		}
	}
	return filtered
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadTemplateManifest(path string, out map[string]TemplateManifestEntry) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []TemplateManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		var document TemplateManifestDocument
		if envelopeErr := json.Unmarshal(data, &document); envelopeErr != nil {
			return err
		}
		entries = document.Entries
	}
	for _, entry := range entries {
		out[entry.TemplateID] = entry
	}
	return nil
}

func saveTemplateManifest(path string, entries map[string]TemplateManifestEntry) error {
	list := make([]TemplateManifestEntry, 0, len(entries))
	for _, entry := range entries {
		entry.Summary = nonNilStrings(entry.Summary)
		entry.Warnings = nonNilStrings(entry.Warnings)
		entry.Errors = nonNilStrings(entry.Errors)
		list = append(list, entry)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LocalDir < list[j].LocalDir })
	entriesJSON, err := json.Marshal(list)
	if err != nil {
		return err
	}
	document := TemplateManifestDocument{SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts", ResourceType: "template", ExportedAt: time.Now().Format(time.RFC3339), Checksum: sha256Hex(entriesJSON), Warnings: []string{}, Entries: list}
	if len(list) > 0 {
		document.WorkspaceID, document.WorkspaceName = list[0].WorkspaceID, list[0].WorkspaceName
	}
	return writeJSONFile(path, document, 0644)
}

func logTemplateResult(logger *log.Logger, result TemplateExportResult) {
	if logger != nil {
		logger.Printf("templates complete: total=%d succeeded=%d failed=%d", result.Templates, result.Succeeded, result.Failed)
	}
}
