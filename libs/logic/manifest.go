package logic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type ManifestEntry struct {
	Title      string `json:"title"`
	NodeID     string `json:"node_id"`
	URL        string `json:"url,omitempty"`
	Parent     string `json:"parent,omitempty"`
	LocalPath  string `json:"local_path"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	ExportTime string `json:"export_time"`
}

type ManifestStore struct {
	mu       sync.Mutex
	path     string
	entries  map[string]ManifestEntry
	failed   map[string]ManifestEntry
}

func NewManifestStore(path string) *ManifestStore {
	return &ManifestStore{path: path, entries: map[string]ManifestEntry{}, failed: map[string]ManifestEntry{}}
}

func manifestKey(nodeID, localPath string) string {
	return nodeID + "::" + localPath
}

func (m *ManifestStore) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.path)
	if err != nil {
		return nil
	}
	var list []ManifestEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, item := range list {
		key := manifestKey(item.NodeID, item.LocalPath)
		m.entries[key] = item
		if item.Status == "failed" {
			m.failed[key] = item
		}
	}
	return nil
}

func (m *ManifestStore) Upsert(entry ManifestEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := manifestKey(entry.NodeID, entry.LocalPath)
	m.entries[key] = entry
	if entry.Status == "failed" {
		m.failed[key] = entry
	} else {
		delete(m.failed, key)
	}
}

func (m *ManifestStore) ShouldSkip(entry ManifestEntry, overwrite bool, retryFailed bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := manifestKey(entry.NodeID, entry.LocalPath)
	old, ok := m.entries[key]
	if !ok {
		return false
	}
	if overwrite {
		return false
	}
	if old.Status == "success" {
		return true
	}
	if old.Status == "failed" && !retryFailed {
		return false
	}
	return false
}

func (m *ManifestStore) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]ManifestEntry, 0, len(m.entries))
	for _, item := range m.entries {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LocalPath < list[j].LocalPath })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(m.path, data, 0644); err != nil {
		return err
	}
	failed := make([]ManifestEntry, 0, len(m.failed))
	for _, item := range m.failed {
		failed = append(failed, item)
	}
	failedData, err := json.MarshalIndent(failed, "", "  ")
	if err != nil {
		return err
	}
	failedPath := filepath.Join(filepath.Dir(m.path), "failed_docs.json")
	return os.WriteFile(failedPath, failedData, 0644)
}

