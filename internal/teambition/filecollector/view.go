package filecollector

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type browseViewEntry struct {
	ExternalID string `json:"external_id"`
	ViewPath   string `json:"view_path"`
	AssetRef   string `json:"local_asset_ref"`
	Material   string `json:"materialization"`
}

// writeBrowseView creates a human-facing tree without making it an identity
// store. Hard links avoid a second copy on the same filesystem.
func writeBrowseView(root string, nodes []Node) error {
	viewRoot := filepath.Join(root, "view")
	if err := os.MkdirAll(viewRoot, 0755); err != nil {
		return err
	}
	byID := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byID[node.ExternalID] = node
	}
	entries := make([]browseViewEntry, 0)
	usedViewPaths := map[string]bool{}
	for _, node := range nodes {
		if node.NodeKind != "file" || node.DownloadStatus != "downloaded" {
			continue
		}
		asset, ok := node.LocalAssetRef.(string)
		if !ok || asset == "" {
			continue
		}
		parts, err := browseParentParts(node, byID)
		if err != nil {
			return err
		}
		name := browseComponent(stringValue(node.Name), "_unnamed_"+node.ExternalID)
		parts = append(parts, name)
		destination := filepath.Join(append([]string{viewRoot}, parts...)...)
		if err := ensureWithin(viewRoot, destination); err != nil {
			return err
		}
		viewParts := append([]string(nil), parts...)
		viewPath := filepath.ToSlash(filepath.Join(viewParts...))
		if usedViewPaths[viewPath] {
			destination = collisionPath(destination, node.ExternalID)
			viewParts[len(viewParts)-1] = filepath.Base(destination)
			viewPath = filepath.ToSlash(filepath.Join(viewParts...))
		}
		if existing, err := os.Stat(destination); err == nil {
			if existing.IsDir() {
				return fmt.Errorf("browse view path is a directory: %s", destination)
			}
			if hash, ok := node.ContentSHA256.(string); ok && verifyFile(destination, existing.Size(), hash) {
				usedViewPaths[viewPath] = true
				entries = append(entries, browseViewEntry{ExternalID: node.ExternalID, ViewPath: viewPath, AssetRef: asset, Material: "hardlink-or-existing"})
				continue
			}
			destination = collisionPath(destination, node.ExternalID)
			viewParts[len(viewParts)-1] = filepath.Base(destination)
			viewPath = filepath.ToSlash(filepath.Join(viewParts...))
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		material := "hardlink"
		if err := os.Link(filepath.Join(root, filepath.FromSlash(asset)), destination); err != nil {
			material = "copy"
			if err := copyFile(filepath.Join(root, filepath.FromSlash(asset)), destination); err != nil {
				return err
			}
		}
		usedViewPaths[viewPath] = true
		entries = append(entries, browseViewEntry{ExternalID: node.ExternalID, ViewPath: viewPath, AssetRef: asset, Material: material})
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(viewRoot, "view_manifest.json"), append(b, '\n'))
}

func browseParentParts(node Node, byID map[string]Node) ([]string, error) {
	var reverse []string
	seen := map[string]bool{node.ExternalID: true}
	parent := node.ParentExternalID
	for parent != nil && *parent != "" {
		if seen[*parent] {
			return nil, fmt.Errorf("browse view parent cycle at %s", node.ExternalID)
		}
		seen[*parent] = true
		parentNode, ok := byID[*parent]
		if !ok {
			return nil, fmt.Errorf("browse view unresolved parent %s", *parent)
		}
		if !parentNode.Root {
			reverse = append(reverse, browseComponent(stringValue(parentNode.Name), "_unnamed_"+parentNode.ExternalID))
		}
		parent = parentNode.ParentExternalID
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, nil
}

func browseComponent(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune("<>:\"/\\|?*\x00", r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
		if b.Len() >= 120 {
			break
		}
	}
	value = strings.TrimRight(b.String(), " .")
	if value == "" {
		value = fallback
	}
	reserved := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "COM1": true, "LPT1": true}
	if reserved[strings.ToUpper(value)] {
		value = "_" + value
	}
	return value
}

func collisionPath(path, externalID string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	return filepath.Join(filepath.Dir(path), browseComponent(base+" ["+externalID+"]", "_unnamed_")+ext)
}

func ensureWithin(root, target string) error {
	r, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	t, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if t != r && !strings.HasPrefix(t, r+string(filepath.Separator)) {
		return fmt.Errorf("browse view path escapes root")
	}
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".view-*.partial")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}
