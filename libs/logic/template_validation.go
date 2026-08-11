package logic

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TemplateValidationReport struct {
	SchemaVersion             string   `json:"schema_version"`
	SourceSystem              string   `json:"source_system"`
	ExportRoot                string   `json:"export_root"`
	ValidatedAt               string   `json:"validated_at"`
	TemplateCount             int      `json:"template_count"`
	AssetCount                int      `json:"asset_count"`
	ImageCount                int      `json:"image_count"`
	AttachmentCount           int      `json:"attachment_count"`
	DownloadedAssetCount      int      `json:"downloaded_asset_count"`
	DownloadedAttachmentCount int      `json:"downloaded_attachment_count"`
	PendingAttachmentCount    int      `json:"pending_attachment_count"`
	LinkCount                 int      `json:"link_count"`
	MissingFileCount          int      `json:"missing_file_count"`
	HashMismatchCount         int      `json:"hash_mismatch_count"`
	FailedCount               int      `json:"failed_count"`
	MissingFiles              []string `json:"missing_files"`
	HashMismatches            []string `json:"hash_mismatches"`
	Failures                  []string `json:"failures"`
	Checksum                  string   `json:"checksum"`
}

func ValidateTemplateExport(outputRoot string) (TemplateValidationReport, error) {
	report := TemplateValidationReport{SchemaVersion: "thoughts-template-validation/v1", SourceSystem: "thoughts", ExportRoot: filepath.ToSlash(outputRoot), ValidatedAt: time.Now().Format(time.RFC3339), MissingFiles: []string{}, HashMismatches: []string{}, Failures: []string{}}
	err := filepath.Walk(outputRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || info.Name() != "templates_manifest.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var manifest TemplateManifestDocument
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		for _, entry := range manifest.Entries {
			report.TemplateCount++
			failed := entry.Status != "success" && entry.Status != "skipped"
			if entry.ContentStatus != "complete" || entry.HTMLStatus != "complete" || entry.DOCXStatus != "complete" || len(entry.Errors) > 0 {
				failed = true
			}
			checkValidationFile(outputRoot, entry.TemplateID, entry.SourcePath, "", &report, &failed)
			checkValidationFile(outputRoot, entry.TemplateID, entry.PreviewPath, entry.ContentSHA256, &report, &failed)
			checkValidationFile(outputRoot, entry.TemplateID, entry.HTMLPath, entry.HTMLSHA256, &report, &failed)
			checkValidationFile(outputRoot, entry.TemplateID, entry.DocxPath, entry.DOCXSHA256, &report, &failed)
			assets, err := readTemplateResourceManifest(outputRoot, entry.AssetsManifestPath)
			if err != nil {
				failed = true
				report.Failures = append(report.Failures, entry.TemplateID+": assets manifest: "+err.Error())
			} else {
				for _, asset := range assets.Entries {
					report.AssetCount++
					if asset.Kind == "image" {
						report.ImageCount++
					} else {
						report.AttachmentCount++
					}
					if asset.DownloadStatus == "downloaded" {
						report.DownloadedAssetCount++
						if asset.Kind == "attachment" {
							report.DownloadedAttachmentCount++
						}
						checkValidationFile(outputRoot, entry.TemplateID, asset.LocalAssetPath, asset.SHA256, &report, &failed)
					} else if asset.Kind == "attachment" {
						report.PendingAttachmentCount++
						failed = true
					}
				}
			}
			links, err := readTemplateResourceManifest(outputRoot, entry.LinksManifestPath)
			if err != nil {
				failed = true
				report.Failures = append(report.Failures, entry.TemplateID+": links manifest: "+err.Error())
			} else {
				report.LinkCount += len(links.Entries)
				htmlData, htmlErr := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(entry.HTMLPath)))
				if htmlErr != nil {
					failed = true
					report.Failures = append(report.Failures, entry.TemplateID+": read HTML for links: "+htmlErr.Error())
				} else {
					for _, link := range links.Entries {
						href := html.EscapeString(link.OriginalURL)
						if link.OriginalURL == "" || !strings.Contains(string(htmlData), `href="`+href+`"`) {
							failed = true
							report.Failures = append(report.Failures, entry.TemplateID+": link missing from HTML: "+link.OriginalURL)
						}
					}
				}
			}
			if failed {
				report.FailedCount++
				report.Failures = append(report.Failures, entry.TemplateID+": "+entry.Title)
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	checksumData, _ := json.Marshal(struct{ Templates, Assets, Downloaded, Pending, Links, Missing, Failed int }{report.TemplateCount, report.AssetCount, report.DownloadedAssetCount, report.PendingAttachmentCount, report.LinkCount, report.MissingFileCount, report.FailedCount})
	report.Checksum = sha256Hex(checksumData)
	return report, nil
}

func readTemplateResourceManifest(outputRoot, relativePath string) (TemplateResourceManifest, error) {
	var manifest TemplateResourceManifest
	data, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		return manifest, err
	}
	err = json.Unmarshal(data, &manifest)
	return manifest, err
}

func checkValidationFile(outputRoot, templateID, relativePath, expectedHash string, report *TemplateValidationReport, failed *bool) {
	if relativePath == "" {
		report.MissingFileCount++
		report.MissingFiles = append(report.MissingFiles, templateID+": path missing")
		*failed = true
		return
	}
	path := filepath.Join(outputRoot, filepath.FromSlash(relativePath))
	data, err := os.ReadFile(path)
	if err != nil {
		report.MissingFileCount++
		report.MissingFiles = append(report.MissingFiles, filepath.ToSlash(relativePath))
		*failed = true
		return
	}
	if expectedHash != "" && !strings.EqualFold(sha256Hex(data), expectedHash) {
		report.HashMismatchCount++
		report.HashMismatches = append(report.HashMismatches, filepath.ToSlash(relativePath))
		*failed = true
	}
}
