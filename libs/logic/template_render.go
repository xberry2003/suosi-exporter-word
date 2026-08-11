package logic

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type TemplateSection struct {
	ID   string  `json:"_id"`
	Type string  `json:"type"`
	Raw  string  `json:"raw"`
	Pos  float64 `json:"pos,omitempty"`
}

func (s *TemplateSection) UnmarshalJSON(data []byte) error {
	type sectionAlias TemplateSection
	var decoded struct {
		sectionAlias
		Content *struct {
			Raw  string `json:"raw"`
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = TemplateSection(decoded.sectionAlias)
	if decoded.Content != nil {
		if s.Raw == "" {
			s.Raw = decoded.Content.Raw
		}
		if s.Type == "" {
			s.Type = decoded.Content.Type
		}
	}
	return nil
}

type documentBlock struct {
	Kind string
	Text string
}

func decodeTemplateSections(preview json.RawMessage) ([]TemplateSection, error) {
	var sections []TemplateSection
	if len(preview) == 0 {
		return nil, fmt.Errorf("template preview is empty")
	}
	if preview[0] == '[' {
		if err := json.Unmarshal(preview, &sections); err != nil {
			return nil, err
		}
	} else {
		var envelope struct {
			Sections []TemplateSection `json:"sections"`
			Result   []TemplateSection `json:"result"`
		}
		if err := json.Unmarshal(preview, &envelope); err != nil {
			return nil, err
		}
		sections = envelope.Sections
		if sections == nil {
			sections = envelope.Result
		}
	}
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].Pos < sections[j].Pos })
	return sections, nil
}

func renderTemplateHTML(title string, sections []TemplateSection) []byte {
	return renderTemplateHTMLWithResources(title, sections, nil, nil, "", "")
}

func renderTemplateHTMLWithResources(title string, sections []TemplateSection, assets, links []TemplateResourceEntry, localDir, outputRoot string) []byte {
	assetsByBlock := map[string]TemplateResourceEntry{}
	linksByBlock := map[string][]TemplateResourceEntry{}
	for _, asset := range assets {
		assetsByBlock[asset.SourceBlockID] = asset
	}
	for _, link := range links {
		linksByBlock[link.SourceBlockID] = append(linksByBlock[link.SourceBlockID], link)
	}
	var body strings.Builder
	skippedMatchingTitle := false
	for _, section := range sections {
		if !skippedMatchingTitle && sectionTag(section.Type) == "h1" && strings.TrimSpace(sectionPlainText(section)) == strings.TrimSpace(title) {
			skippedMatchingTitle = true
			continue
		}
		rendered := ""
		if asset, ok := assetsByBlock[section.ID]; ok {
			rendered = renderTemplateAssetHTML(asset, localDir, outputRoot)
		} else {
			rendered = renderSectionHTML(section)
		}
		for _, link := range linksByBlock[section.ID] {
			href := html.EscapeString(link.OriginalURL)
			if !strings.Contains(rendered, `href="`+href+`"`) {
				rendered += `<p><a href="` + href + `">` + html.EscapeString(firstNonEmptyValue(link.AnchorText, link.OriginalURL)) + `</a></p>`
			}
		}
		body.WriteString(rendered)
	}
	return []byte("<!doctype html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title>" +
		"<style>body{font-family:Arial,'Microsoft YaHei',sans-serif;max-width:960px;margin:32px auto;line-height:1.7;padding:0 24px}img{max-width:100%;height:auto}table{border-collapse:collapse;width:100%}td,th{border:1px solid #ccc;padding:6px}pre{white-space:pre-wrap}</style>" +
		"</head><body>" + body.String() + "</body></html>")
}

func renderTemplateAssetHTML(asset TemplateResourceEntry, localDir, outputRoot string) string {
	if asset.DownloadStatus != "downloaded" || asset.LocalAssetPath == "" {
		return `<p data-asset-status="pending">` + html.EscapeString(asset.OriginalFileName) + `</p>`
	}
	absolutePath := filepath.Join(outputRoot, filepath.FromSlash(asset.LocalAssetPath))
	relativePath, err := filepath.Rel(localDir, absolutePath)
	if err != nil {
		relativePath = filepath.Join("assets", filepath.Base(absolutePath))
	}
	href := html.EscapeString(filepath.ToSlash(relativePath))
	name := html.EscapeString(asset.OriginalFileName)
	if asset.Kind == "image" {
		return `<figure><img src="` + href + `" alt="` + name + `"></figure>`
	}
	return `<p><a href="` + href + `" data-storage-key="` + html.EscapeString(asset.StorageKey) + `">` + name + `</a></p>`
}

func sectionPlainText(section TemplateSection) string {
	value, ok := decodeRawValue(section.Raw)
	if !ok {
		return stripHTML(section.Raw)
	}
	return richPlainText(value)
}

func extractTemplateResources(sections []TemplateSection, workspaceID, templateID string) ([]TemplateResourceEntry, []TemplateResourceEntry) {
	assets := map[string]TemplateResourceEntry{}
	links := map[string]TemplateResourceEntry{}
	for index, section := range sections {
		value, ok := decodeRawValue(section.Raw)
		if !ok {
			continue
		}
		if asset, found := templateAssetFromSection(section, index, value, workspaceID, templateID); found {
			key := firstNonEmptyValue(asset.StorageKey, asset.SourceURL, fmt.Sprintf("%s:%d", section.ID, index))
			assets[key] = asset
			continue
		}
		collectTemplateLinks(value, section.ID, index, workspaceID, templateID, links)
	}
	assetList, linkList := make([]TemplateResourceEntry, 0, len(assets)), make([]TemplateResourceEntry, 0, len(links))
	for _, asset := range assets {
		assetList = append(assetList, asset)
	}
	for _, link := range links {
		linkList = append(linkList, link)
	}
	sort.Slice(assetList, func(i, j int) bool { return resourceSortKey(assetList[i]) < resourceSortKey(assetList[j]) })
	sort.Slice(linkList, func(i, j int) bool { return resourceSortKey(linkList[i]) < resourceSortKey(linkList[j]) })
	return assetList, linkList
}

func resourceSortKey(entry TemplateResourceEntry) string {
	return fmt.Sprintf("%08d:%s:%s", entry.BlockIndex, entry.StorageKey, entry.OriginalURL)
}

func templateAssetFromSection(section TemplateSection, index int, value any, workspaceID, templateID string) (TemplateResourceEntry, bool) {
	root, ok := value.(map[string]any)
	if !ok {
		return TemplateResourceEntry{}, false
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		data = root
	}
	blockType := strings.ToLower(section.Type)
	dataType := strings.ToLower(stringValue(data, "type"))
	downloadURL := firstString(data, "downloadUrl", "src")
	storageKey := firstString(data, "fileKey", "storageKey")
	looksLikeAsset := blockType == "image" || blockType == "attachment" || (blockType == "atomic" && (dataType == "attachment" || storageKey != ""))
	if !looksLikeAsset || (downloadURL == "" && storageKey == "") {
		return TemplateResourceEntry{}, false
	}
	mimeType := firstString(data, "mimeType", "contentType")
	fileCategory := strings.ToLower(stringValue(data, "fileCategory"))
	kind := "attachment"
	if blockType == "image" || fileCategory == "image" || strings.HasPrefix(mimeType, "image/") {
		kind = "image"
	}
	fileName := firstString(data, "fileName", "name", "title")
	if parsed, err := url.Parse(downloadURL); err == nil {
		if storageKey == "" {
			storageKey = storageKeyFromURL(parsed)
		}
		if fileName == "" {
			fileName = parsed.Query().Get("download")
		}
	}
	if fileName == "" {
		fileName = firstNonEmptyValue(storageKey, fmt.Sprintf("asset-%d", index+1))
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(fileName))
	}
	return TemplateResourceEntry{
		SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts", ResourceType: "asset",
		WorkspaceID: workspaceID, TemplateID: templateID, SourceBlockID: section.ID,
		BlockIndex: index, BlockNumber: index + 1, Kind: kind, SourceURL: downloadURL, OriginalURL: downloadURL,
		AnchorText: fileName, StorageKey: storageKey, OriginalFileName: fileName, MIMEType: mimeType,
		Size: int64Value(data, "fileSize"), SignatureExpiresAt: signedURLExpiry(downloadURL), DownloadStatus: "pending",
	}, true
}

func collectTemplateLinks(value any, blockID string, index int, workspaceID, templateID string, links map[string]TemplateResourceEntry) {
	switch current := value.(type) {
	case []any:
		for _, child := range current {
			collectTemplateLinks(child, blockID, index, workspaceID, templateID, links)
		}
	case map[string]any:
		textValue, _ := current["text"].(string)
		if entities, ok := current["entities"].([]any); ok {
			for _, rawEntity := range entities {
				entity, ok := rawEntity.(map[string]any)
				if !ok || !strings.EqualFold(stringValue(entity, "type"), "LINK") {
					continue
				}
				data, _ := entity["data"].(map[string]any)
				originalURL := firstString(data, "url", "href")
				if originalURL == "" {
					continue
				}
				offset, _ := numberValue(entity, "offset")
				length, _ := numberValue(entity, "length")
				addTemplateLink(links, originalURL, runeSlice(textValue, offset, length), blockID, index, workspaceID, templateID)
			}
		}
		kind := strings.ToLower(stringValue(current, "type"))
		data, _ := current["data"].(map[string]any)
		dataType := strings.ToLower(stringValue(data, "type"))
		if kind == "link" || kind == "a" || kind == "bookmark" || dataType == "bookmark" {
			originalURL := firstNonEmptyValue(firstString(current, "url", "href"), firstString(data, "url", "href", "origin"))
			anchor := firstNonEmptyValue(textValue, firstString(current, "title", "name"), firstString(data, "title", "name", "origin"), originalURL)
			addTemplateLink(links, originalURL, anchor, blockID, index, workspaceID, templateID)
		}
		for _, sourceURL := range templateURLPattern.FindAllString(textValue, -1) {
			addTemplateLink(links, sourceURL, sourceURL, blockID, index, workspaceID, templateID)
		}
		for key, child := range current {
			if key == "entities" || key == "text" {
				continue
			}
			collectTemplateLinks(child, blockID, index, workspaceID, templateID, links)
		}
	case string:
		for _, sourceURL := range templateURLPattern.FindAllString(current, -1) {
			addTemplateLink(links, sourceURL, sourceURL, blockID, index, workspaceID, templateID)
		}
	}
}

func addTemplateLink(links map[string]TemplateResourceEntry, originalURL, anchor, blockID string, index int, workspaceID, templateID string) {
	if originalURL == "" {
		return
	}
	key := fmt.Sprintf("%s:%d:%s:%s", blockID, index, originalURL, anchor)
	links[key] = TemplateResourceEntry{SchemaVersion: templateSchemaVersion, SourceSystem: "thoughts", ResourceType: "link", WorkspaceID: workspaceID, TemplateID: templateID, SourceBlockID: blockID, BlockIndex: index, BlockNumber: index + 1, Kind: "link", SourceURL: originalURL, OriginalURL: originalURL, AnchorText: firstNonEmptyValue(anchor, originalURL)}
}

func runeSlice(value string, offset, length int) string {
	runes := []rune(value)
	if offset < 0 || offset >= len(runes) || length <= 0 {
		return ""
	}
	end := offset + length
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end])
}

func int64Value(value map[string]any, key string) int64 {
	number, ok := value[key].(float64)
	if !ok {
		return 0
	}
	return int64(number)
}

func storageKeyFromURL(parsed *url.URL) string {
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, part := range parts {
		if part == "storage" && index+1 < len(parts) {
			return strings.TrimSuffix(parts[index+1], filepath.Ext(parts[index+1]))
		}
	}
	return ""
}

func signedURLExpiry(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	token := parsed.Query().Get("Signature")
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return ""
	}
	return time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)
}

var templateURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func renderSectionHTML(section TemplateSection) string {
	value, ok := decodeRawValue(section.Raw)
	if !ok {
		return wrapHTML(sectionTag(section.Type), html.EscapeString(section.Raw))
	}
	rendered := renderRichValue(value)
	if strings.TrimSpace(rendered) == "" {
		return wrapHTML(sectionTag(section.Type), "")
	}
	return wrapHTML(sectionTag(section.Type), rendered)
}

func decodeRawValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, true
	}
	var value any
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Unmarshal([]byte(trimmed), &value) == nil {
		return value, true
	}
	return nil, false
}

func renderRichValue(value any) string {
	switch current := value.(type) {
	case []any:
		var out strings.Builder
		for _, child := range current {
			out.WriteString(renderRichValue(child))
		}
		return out.String()
	case map[string]any:
		if text, ok := current["text"].(string); ok {
			rendered := renderDraftText(text, current)
			if kind := stringValue(current, "type"); kind != "" {
				return wrapHTML(richTag(kind), rendered)
			}
			return rendered
		}
		kind := stringValue(current, "type")
		tag := richTag(kind)
		var content strings.Builder
		for _, key := range []string{"children", "nodes", "content", "document", "value"} {
			if children, ok := current[key]; ok {
				content.WriteString(renderRichValue(children))
				break
			}
		}
		if tag == "img" {
			src := firstString(current, "url", "src", "downloadUrl")
			alt := firstString(current, "alt", "title", "name")
			return "<img src=\"" + html.EscapeString(src) + "\" alt=\"" + html.EscapeString(alt) + "\">"
		}
		if tag == "a" {
			href := firstString(current, "url", "href")
			return "<a href=\"" + html.EscapeString(href) + "\">" + content.String() + "</a>"
		}
		if kind == "" {
			return content.String()
		}
		return wrapHTML(tag, content.String())
	case string:
		return html.EscapeString(current)
	default:
		return ""
	}
}

func renderTextLeaf(text string, attrs map[string]any) string {
	result := html.EscapeString(text)
	if boolValue(attrs, "code") {
		result = "<code>" + result + "</code>"
	}
	if boolValue(attrs, "bold") || boolValue(attrs, "strong") {
		result = "<strong>" + result + "</strong>"
	}
	if boolValue(attrs, "italic") || boolValue(attrs, "emphasis") {
		result = "<em>" + result + "</em>"
	}
	if boolValue(attrs, "underline") {
		result = "<u>" + result + "</u>"
	}
	if boolValue(attrs, "strikethrough") || boolValue(attrs, "deleted") {
		result = "<s>" + result + "</s>"
	}
	return result
}

func renderDraftText(text string, value map[string]any) string {
	ranges, _ := value["inlineStyleRanges"].([]any)
	entities, _ := value["entities"].([]any)
	if len(ranges) == 0 && len(entities) == 0 {
		return renderTextLeaf(text, value)
	}
	type styleRange struct {
		start, end int
		style      string
	}
	parsed := make([]styleRange, 0, len(ranges))
	for _, item := range ranges {
		rangeValue, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start, startOK := numberValue(rangeValue, "offset")
		length, lengthOK := numberValue(rangeValue, "length")
		if !startOK || !lengthOK || length <= 0 {
			continue
		}
		parsed = append(parsed, styleRange{start: start, end: start + length, style: strings.ToUpper(stringValue(rangeValue, "style"))})
	}
	type linkRange struct {
		start, end int
		href       string
	}
	linkRanges := make([]linkRange, 0, len(entities))
	for _, item := range entities {
		entity, ok := item.(map[string]any)
		if !ok || !strings.EqualFold(stringValue(entity, "type"), "LINK") {
			continue
		}
		data, _ := entity["data"].(map[string]any)
		href := firstString(data, "url", "href")
		start, startOK := numberValue(entity, "offset")
		length, lengthOK := numberValue(entity, "length")
		if href != "" && startOK && lengthOK && length > 0 {
			linkRanges = append(linkRanges, linkRange{start: start, end: start + length, href: href})
		}
	}
	runes := []rune(text)
	var result strings.Builder
	for index, char := range runes {
		piece := html.EscapeString(string(char))
		for _, current := range parsed {
			if index < current.start || index >= current.end {
				continue
			}
			switch {
			case strings.Contains(current.style, "BOLD"):
				piece = "<strong>" + piece + "</strong>"
			case strings.Contains(current.style, "ITALIC"):
				piece = "<em>" + piece + "</em>"
			case strings.Contains(current.style, "UNDERLINE"):
				piece = "<u>" + piece + "</u>"
			case strings.HasPrefix(current.style, "COLOR_"):
				color := strings.TrimPrefix(current.style, "COLOR_")
				piece = "<span style=\"color:" + html.EscapeString(color) + "\">" + piece + "</span>"
			}
		}
		for _, current := range linkRanges {
			if index >= current.start && index < current.end {
				piece = `<a href="` + html.EscapeString(current.href) + `">` + piece + `</a>`
			}
		}
		result.WriteString(piece)
	}
	output := result.String()
	for _, tag := range []string{"strong", "em", "u"} {
		output = strings.ReplaceAll(output, "</"+tag+"><"+tag+">", "")
	}
	for _, current := range linkRanges {
		href := html.EscapeString(current.href)
		output = strings.ReplaceAll(output, `</a><a href="`+href+`">`, "")
	}
	return output
}

func richTag(kind string) string {
	kind = strings.ToLower(strings.ReplaceAll(kind, "_", "-"))
	switch kind {
	case "heading-one", "header-one", "heading-1", "h1", "title":
		return "h1"
	case "heading-two", "header-two", "heading-2", "h2":
		return "h2"
	case "heading-three", "header-three", "heading-3", "h3":
		return "h3"
	case "heading-four", "heading-4", "h4":
		return "h4"
	case "bulleted-list", "bullet-list", "unordered-list", "ul":
		return "ul"
	case "numbered-list", "ordered-list", "ol":
		return "ol"
	case "list-item", "li":
		return "li"
	case "unordered-list-item", "ordered-list-item":
		return "li"
	case "unordered-list-wrapper":
		return "ul"
	case "ordered-list-wrapper":
		return "ol"
	case "block-quote", "blockquote", "quote":
		return "blockquote"
	case "code-block", "pre":
		return "pre"
	case "link", "a":
		return "a"
	case "image", "img":
		return "img"
	case "table", "tbody", "tr", "td", "th":
		return kind
	case "paragraph", "p", "":
		return "p"
	default:
		return "div"
	}
}

func sectionTag(kind string) string {
	return richTag(kind)
}

func wrapHTML(tag, content string) string {
	if tag == "img" {
		return content
	}
	return "<" + tag + ">" + content + "</" + tag + ">"
}

func templateDocumentBlocks(title string, sections []TemplateSection) []documentBlock {
	blocks := []documentBlock{{Kind: "h1", Text: title}}
	for _, section := range sections {
		value, ok := decodeRawValue(section.Raw)
		if !ok {
			blocks = append(blocks, documentBlock{Kind: sectionTag(section.Type), Text: stripHTML(section.Raw)})
			continue
		}
		appendRichBlocks(&blocks, value, sectionTag(section.Type))
	}
	return blocks
}

func appendRichBlocks(blocks *[]documentBlock, value any, inherited string) {
	switch current := value.(type) {
	case []any:
		for _, child := range current {
			appendRichBlocks(blocks, child, inherited)
		}
	case map[string]any:
		if text, ok := current["text"].(string); ok {
			if strings.TrimSpace(text) != "" {
				kind := inherited
				if currentType := stringValue(current, "type"); currentType != "" {
					kind = richTag(currentType)
				}
				if kind == "li" {
					text = "• " + text
				}
				*blocks = append(*blocks, documentBlock{Kind: kind, Text: text})
			}
			return
		}
		kind := richTag(stringValue(current, "type"))
		if kind == "p" && inherited != "" {
			kind = inherited
		}
		text := richPlainText(current)
		if strings.TrimSpace(text) != "" && hasBlockType(current) {
			if kind == "li" {
				text = "• " + text
			}
			*blocks = append(*blocks, documentBlock{Kind: kind, Text: text})
			return
		}
		for _, key := range []string{"children", "nodes", "content", "document", "value"} {
			if children, ok := current[key]; ok {
				appendRichBlocks(blocks, children, kind)
				return
			}
		}
	case string:
		if strings.TrimSpace(current) != "" {
			*blocks = append(*blocks, documentBlock{Kind: inherited, Text: current})
		}
	}
}

func richPlainText(value any) string {
	switch current := value.(type) {
	case []any:
		parts := make([]string, 0, len(current))
		for _, child := range current {
			parts = append(parts, richPlainText(child))
		}
		return strings.Join(parts, "")
	case map[string]any:
		if text, ok := current["text"].(string); ok {
			return text
		}
		if richTag(stringValue(current, "type")) == "img" {
			return firstString(current, "alt", "title", "name", "url", "src")
		}
		for _, key := range []string{"children", "nodes", "content", "document", "value"} {
			if child, ok := current[key]; ok {
				return richPlainText(child)
			}
		}
	}
	return ""
}

func writeTemplateDOCX(path, title string, sections []TemplateSection) error {
	var document strings.Builder
	document.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, block := range templateDocumentBlocks(title, sections) {
		document.WriteString(docxParagraph(block))
	}
	document.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`)
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   document.String(),
	}
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buffer.Bytes(), 0644)
}

func docxParagraph(block documentBlock) string {
	text := xmlEscape(block.Text)
	properties := ""
	switch block.Kind {
	case "h1":
		properties = `<w:b/><w:sz w:val="32"/>`
	case "h2":
		properties = `<w:b/><w:sz w:val="28"/>`
	case "h3", "h4":
		properties = `<w:b/><w:sz w:val="24"/>`
	}
	return `<w:p><w:r><w:rPr>` + properties + `</w:rPr><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

func stripHTML(value string) string {
	return html.UnescapeString(htmlTagPattern.ReplaceAllString(value, ""))
}

func xmlEscape(value string) string {
	var out strings.Builder
	_ = html.EscapeString
	for _, r := range value {
		switch r {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '"':
			out.WriteString("&quot;")
		case '\'':
			out.WriteString("&apos;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func stringValue(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if result, ok := value[key].(string); ok && result != "" {
			return result
		}
	}
	return ""
}

func boolValue(value map[string]any, key string) bool {
	result, _ := value[key].(bool)
	return result
}

func numberValue(value map[string]any, key string) (int, bool) {
	number, ok := value[key].(float64)
	return int(number), ok
}

func hasBlockType(value map[string]any) bool {
	_, ok := value["type"]
	return ok
}
