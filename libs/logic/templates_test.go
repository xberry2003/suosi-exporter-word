package logic

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceIDFromURL(t *testing.T) {
	id, err := workspaceIDFromURL("https://thoughts.teambition.com/workspaces/65c049fa6e8a7600180f2cca/overview")
	if err != nil {
		t.Fatal(err)
	}
	if id != "65c049fa6e8a7600180f2cca" {
		t.Fatalf("unexpected workspace ID %q", id)
	}
	if _, err := workspaceIDFromURL("https://thoughts.teambition.com/overview"); err == nil {
		t.Fatal("expected invalid workspace URL to fail")
	}
}

func TestDecodeTemplatesAcceptsKnownResponseShapes(t *testing.T) {
	for name, payload := range map[string]string{
		"array":  `[{"_id":"t1","title":"Template","relatedTemplateId":"n1"}]`,
		"result": `{"result":[{"_id":"t1","title":"Template","relatedTemplateId":"n1"}]}`,
		"data":   `{"data":[{"_id":"t1","title":"Template","relatedTemplateId":"n1"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			templates, err := decodeTemplates([]byte(payload))
			if err != nil {
				t.Fatal(err)
			}
			if len(templates) != 1 || templates[0].ID != "t1" || templates[0].RelatedTemplateID != "n1" {
				t.Fatalf("unexpected templates: %#v", templates)
			}
		})
	}
}

func TestTemplateMetadataIsPromoted(t *testing.T) {
	templates, err := decodeTemplates([]byte(`[{"_id":"t1","title":"Template","relatedTemplateId":"c1","type":"workspace","boundType":"workspace","_boundId":"w1","pos":42.5,"created":"2025-01-01T00:00:00Z","updated":"2025-01-02T00:00:00Z","_ownerId":"u1","isDeleted":true}]`))
	if err != nil {
		t.Fatal(err)
	}
	got := templates[0]
	if got.ID != "t1" || got.RelatedTemplateID != "c1" || got.BoundID != "w1" || got.Position != 42.5 || got.SourceOwnerID != "u1" || !got.Deleted {
		t.Fatalf("metadata was not decoded: %#v", got)
	}
}

func TestTemplateDirectoryNameSeparatesDuplicateTitles(t *testing.T) {
	a := templateDirectoryName(Template{ID: "one", Title: "same"})
	b := templateDirectoryName(Template{ID: "two", Title: "same"})
	if a == b {
		t.Fatalf("duplicate template titles produced the same directory: %q", a)
	}
}

func TestExportTemplatesForWorkspaceWithNoTemplates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "templates")
	result, err := exportTemplatesForWorkspace(nil, TemplateExportOptions{}, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Templates != 0 || result.Failed != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "templates_manifest.json")); err != nil {
		t.Fatalf("manifest was not written: %v", err)
	}
}

func TestFailedTemplateIsNotRetriedUnlessRequested(t *testing.T) {
	template := Template{ID: "t1", Title: "Template", RelatedTemplateID: "n1"}
	old := TemplateManifestEntry{SchemaVersion: templateSchemaVersion, TemplateID: "t1", Status: "failed", Errors: []string{"previous"}}
	entry := exportOneTemplate(nil, TemplateExportOptions{}, t.TempDir(), template, map[string]TemplateManifestEntry{"t1": old})
	if entry.Status != "failed" || len(entry.Errors) != 1 || entry.Errors[0] != "previous" {
		t.Fatalf("failed entry was unexpectedly retried: %#v", entry)
	}
}

func TestTemplateContentIDFallsBackToTemplateID(t *testing.T) {
	if got := templateContentID(Template{ID: "t1"}); got != "t1" {
		t.Fatalf("unexpected fallback content ID %q", got)
	}
	if got := templateContentID(Template{ID: "t1", RelatedTemplateID: "source"}); got != "source" {
		t.Fatalf("related template ID was not preferred: %q", got)
	}
	if got := templateContentID(Template{ID: "t1", RelatedTemplateID: "related", ContentID: "content"}); got != "content" {
		t.Fatalf("explicit content ID was not preferred: %q", got)
	}
}

func TestExtractTemplateResourcesSeparatesAssetsAndLinks(t *testing.T) {
	sections := []TemplateSection{
		{ID: "image-block", Type: "image", Raw: `{"data":{"downloadUrl":"https://cdn/image.png","fileKey":"image-key","fileName":"image.png","mimeType":"image/png","fileSize":12}}`},
		{ID: "attachment-block", Type: "attachment", Raw: `{"data":{"downloadUrl":"https://cdn/file.docx","fileKey":"file-key","fileName":"file.docx","mimeType":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","fileSize":34}}`},
		{ID: "link-block", Type: "paragraph", Raw: `{"text":"Example","entities":[{"type":"LINK","offset":0,"length":7,"data":{"url":"https://example.com"}}]}`},
	}
	assets, links := extractTemplateResources(sections, "w1", "t1")
	if len(assets) != 2 || len(links) != 1 {
		t.Fatalf("unexpected resources: assets=%#v links=%#v", assets, links)
	}
	if assets[0].WorkspaceID != "w1" || links[0].TemplateID != "t1" {
		t.Fatalf("resource ownership missing")
	}
}

func TestSignedURLExpiryAndResourceLocation(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800}`))
	url := "https://tcs.teambition.net/storage/key/file.png?Signature=x." + claims + ".y"
	if got := signedURLExpiry(url); got != "2100-01-01T00:00:00Z" {
		t.Fatalf("unexpected expiry %q", got)
	}
	sections := []TemplateSection{{ID: "b1", Type: "paragraph", Raw: `{"text":"Anchor","entities":[{"type":"LINK","offset":0,"length":6,"data":{"url":"https://example.com/a"}}]}`}}
	_, links := extractTemplateResources(sections, "w", "t")
	if len(links) != 1 || links[0].SourceBlockID != "b1" || links[0].BlockIndex != 0 || links[0].BlockNumber != 1 || links[0].AnchorText != "Anchor" {
		t.Fatalf("unexpected link location: %#v", links)
	}
}

func TestRenderTemplateHTMLUsesLocalAssetAndPreservesLink(t *testing.T) {
	sections := []TemplateSection{{ID: "img", Type: "image", Raw: `{"data":{"downloadUrl":"https://cdn/image.png","fileKey":"img-key","fileName":"image.png","mimeType":"image/png"}}`}}
	assets, links := extractTemplateResources(sections, "w", "t")
	assets[0].DownloadStatus = "downloaded"
	assets[0].LocalAssetPath = "templates/t/assets/img-key__image.png"
	links = append(links, TemplateResourceEntry{SourceBlockID: "img", OriginalURL: "https://example.com/?a=1&b=2", AnchorText: "Example"})
	out := string(renderTemplateHTMLWithResources("T", sections, assets, links, filepath.Join("export", "templates", "t"), "export"))
	if !strings.Contains(out, `src="assets/img-key__image.png"`) || !strings.Contains(out, `href="https://example.com/?a=1&amp;b=2"`) {
		t.Fatalf("asset/link not preserved in HTML: %s", out)
	}
}

func TestDownloadTemplateAssetsWritesStableFileAndHash(t *testing.T) {
	payload := []byte("image-content")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	localDir := filepath.Join(root, "templates", "t")
	assets := []TemplateResourceEntry{{StorageKey: "stable-key", OriginalFileName: "image.png", SourceURL: server.URL + "/image", Kind: "image"}}
	got, warnings := downloadTemplateAssets(nil, TemplateExportOptions{OutputRoot: root}, localDir, assets)
	if len(warnings) != 0 || got[0].DownloadStatus != "downloaded" || got[0].SHA256 != sha256Hex(payload) || got[0].MIMEType != "image/png" {
		t.Fatalf("unexpected download result: %#v warnings=%#v", got, warnings)
	}
	if got[0].LocalAssetPath != "templates/t/assets/stable-key__image.png" {
		t.Fatalf("unexpected local path %q", got[0].LocalAssetPath)
	}
	if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(got[0].LocalAssetPath))); err != nil || string(data) != string(payload) {
		t.Fatalf("downloaded file mismatch: %q, %v", data, err)
	}
}

func TestSaveTemplateManifestWritesVersionedEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "templates_manifest.json")
	entries := map[string]TemplateManifestEntry{"t1": {SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts", WorkspaceID: "w1", WorkspaceName: "Workspace", TemplateID: "t1", LocalDir: "templates/t1"}}
	if err := saveTemplateManifest(path, entries); err != nil {
		t.Fatal(err)
	}
	var document TemplateManifestDocument
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != templateSchemaVersion || document.ResourceType != "template" || document.Checksum == "" || len(document.Entries) != 1 {
		t.Fatalf("unexpected manifest: %#v", document)
	}
}

func TestRenderTemplateHTMLAndDOCX(t *testing.T) {
	preview := []byte(`[
  {"_id":"s2","type":"paragraph","pos":2,"content":{"type":"paragraph","raw":"{\"text\":\"Body & text\",\"inlineStyleRanges\":[{\"style\":\"BOLD\",\"offset\":0,\"length\":4}]}"}},
  {"_id":"s1","type":"header-one","pos":1,"content":{"type":"header-one","raw":"{\"text\":\"Heading\"}"}}
]`)
	sections, err := decodeTemplateSections(preview)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[0].ID != "s1" {
		t.Fatalf("sections were not decoded and sorted: %#v", sections)
	}
	htmlOutput := string(renderTemplateHTML("Template", sections))
	if strings.Contains(htmlOutput, "<body><h1>Template</h1>") {
		t.Fatalf("HTML must not inject the template name as a body heading: %s", htmlOutput)
	}
	if !strings.Contains(htmlOutput, "<h1>Heading</h1>") || !strings.Contains(htmlOutput, "<strong>Body</strong> &amp; text") {
		t.Fatalf("unexpected HTML: %s", htmlOutput)
	}
	docxPath := filepath.Join(t.TempDir(), "template.docx")
	if err := writeTemplateDOCX(docxPath, "Template", sections); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(docxPath)
	if err != nil {
		t.Fatalf("generated DOCX is not a valid ZIP package: %v", err)
	}
	defer reader.Close()
	var foundDocument bool
	for _, file := range reader.File {
		if file.Name == "word/document.xml" {
			foundDocument = true
		}
	}
	if !foundDocument {
		t.Fatal("generated DOCX has no word/document.xml")
	}
}

func TestRenderTemplateHTMLDropsFirstMatchingHeading(t *testing.T) {
	sections, err := decodeTemplateSections([]byte(`[{"type":"header-one","pos":1,"content":{"raw":"{\"text\":\"Template\"}"}},{"type":"header-one","pos":2,"content":{"raw":"{\"text\":\"Content\"}"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	output := string(renderTemplateHTML("Template", sections))
	if strings.Contains(output, "<h1>Template</h1>") || !strings.Contains(output, "<h1>Content</h1>") {
		t.Fatalf("unexpected heading normalization: %s", output)
	}
}

func TestRelativeExportPathUsesPOSIXSeparators(t *testing.T) {
	got := relativeExportPath(filepath.Join("root", "export"), filepath.Join("root", "export", "templates", "t1", "template.html"))
	if got != "templates/t1/template.html" {
		t.Fatalf("unexpected path %q", got)
	}
}

func TestDeduplicateWorkspacesUsesStableSourceID(t *testing.T) {
	got := deduplicateWorkspaces([]DiscoveredWorkspace{{ID: "w1", Name: "A", URL: "one"}, {ID: "w1", Name: "Workspace A", URL: "two"}, {ID: "w2", Name: "B", URL: "three"}})
	if len(got) != 2 {
		t.Fatalf("unexpected workspaces: %#v", got)
	}
	for _, workspace := range got {
		if workspace.ID == "w1" && workspace.Name != "Workspace A" {
			t.Fatalf("did not retain richer metadata: %#v", workspace)
		}
	}
}

func TestStoredTemplateWorkspacesRecoversZeroTemplateWorkspace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "org", "workspace", "templates", "templates_manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	document := TemplateManifestDocument{SchemaVersion: templateSchemaVersion, WorkspaceID: "w1", WorkspaceName: "Empty workspace", Entries: []TemplateManifestEntry{}}
	if err := writeJSONFile(path, document, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := storedTemplateWorkspaces(root)
	if err != nil || len(got) != 1 || got[0].ID != "w1" || got[0].Name != "Empty workspace" {
		t.Fatalf("unexpected recovered workspaces: %#v, %v", got, err)
	}
}
