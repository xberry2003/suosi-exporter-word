package filecollector

import (
	"context"
	"encoding/json"
	"net/http"
	"thoughtsexport/internal/tbinventory"
	"time"
)

type Config struct {
	ProjectID            string
	ProjectURL           string
	Output               string
	Resume               bool
	IncludeRaw           bool
	PageSize             int
	Since                string
	MaxFileSize          int64
	Concurrency          int
	RetryFailedDownloads bool
	DownloadExternalID   string
}

type DownloadDescriptor struct {
	URL              string
	ExpectedSize     *int64
	MIMEType         string
	SourceStorageKey any
	Headers          http.Header
}

type DownloadSource interface {
	ResolveDownload(ctx context.Context, projectID, nodeExternalID, versionExternalID string) (DownloadDescriptor, int, error)
}

type Node struct {
	ExternalID             string   `json:"external_id"`
	ProjectExternalID      string   `json:"project_external_id"`
	ParentExternalID       *string  `json:"parent_external_id"`
	NodeKind               string   `json:"node_kind"`
	Name                   any      `json:"name"`
	DisplayPath            string   `json:"display_path"`
	Order                  any      `json:"order"`
	SourceCreatedAt        any      `json:"source_created_at"`
	SourceUpdatedAt        any      `json:"source_updated_at"`
	CreatorExternalUserID  any      `json:"creator_external_user_id"`
	ModifierExternalUserID any      `json:"modifier_external_user_id"`
	Size                   any      `json:"size"`
	MIMEType               any      `json:"mime_type"`
	SourceMIMEType         any      `json:"source_mime_type"`
	SourceStorageKey       any      `json:"source_storage_key"`
	VersionExternalID      any      `json:"version_external_id"`
	ContentSHA256          any      `json:"content_sha256"`
	LocalAssetRef          any      `json:"local_asset_ref"`
	DownloadStatus         string   `json:"download_status"`
	Visibility             string   `json:"visibility"`
	Completeness           string   `json:"completeness"`
	MissingFields          []string `json:"missing_fields"`
	Warnings               []string `json:"warnings"`
	Fingerprint            string   `json:"fingerprint"`
	RawRef                 any      `json:"raw_ref"`
	Archived               bool     `json:"archived"`
	Deleted                bool     `json:"deleted"`
	Root                   bool     `json:"root"`
	Synthetic              bool     `json:"synthetic"`
}

type NodeEnvelope struct {
	SchemaVersion     string   `json:"schema_version"`
	EntityType        string   `json:"entity_type"`
	SourceSystem      string   `json:"source_system"`
	ExternalID        string   `json:"external_id"`
	ProjectExternalID string   `json:"project_external_id"`
	SourceURL         any      `json:"source_url"`
	SourceCreatedAt   any      `json:"source_created_at"`
	SourceUpdatedAt   any      `json:"source_updated_at"`
	FetchedAt         string   `json:"fetched_at"`
	Visibility        string   `json:"visibility"`
	Completeness      string   `json:"completeness"`
	MissingFields     []string `json:"missing_fields"`
	Warnings          []string `json:"warnings"`
	Fingerprint       string   `json:"fingerprint"`
	RawRef            any      `json:"raw_ref"`
	Data              Node     `json:"data"`
}

func newNodeEnvelope(node Node) NodeEnvelope {
	return NodeEnvelope{
		SchemaVersion: "1.1", EntityType: "project_file_node", SourceSystem: "teambition",
		ExternalID: node.ExternalID, ProjectExternalID: node.ProjectExternalID,
		SourceCreatedAt: node.SourceCreatedAt, SourceUpdatedAt: node.SourceUpdatedAt,
		FetchedAt: time.Now().UTC().Format(time.RFC3339), Visibility: node.Visibility,
		Completeness: node.Completeness, MissingFields: node.MissingFields, Warnings: node.Warnings,
		Fingerprint: node.Fingerprint, RawRef: node.RawRef, Data: node,
	}
}

func marshalNodeEnvelope(node Node) ([]byte, error) { return json.Marshal(newNodeEnvelope(node)) }

type Version struct {
	ExternalID        string   `json:"external_id"`
	ProjectExternalID string   `json:"project_external_id"`
	NodeExternalID    string   `json:"node_external_id"`
	RawRef            any      `json:"raw_ref"`
	Completeness      string   `json:"completeness"`
	Warnings          []string `json:"warnings"`
}

type Reference struct {
	ExternalID        string   `json:"external_id"`
	ProjectExternalID string   `json:"project_external_id"`
	NodeExternalID    any      `json:"node_external_id"`
	ReferenceKind     string   `json:"reference_kind"`
	SourceStorageKey  any      `json:"source_storage_key"`
	RawRef            any      `json:"raw_ref"`
	Completeness      string   `json:"completeness"`
	Warnings          []string `json:"warnings"`
}

type PageSource interface {
	ListFiles(ctx context.Context, projectID, parentID, pageToken string, opts tbinventory.ListOptions) (tbinventory.Page, int, error)
}
