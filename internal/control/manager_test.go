package control

import (
	"path/filepath"
	"testing"
	"time"
)

func TestQueuedJobCanBeCancelledBeforeWorkerStarts(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, filepath.Join(root, "artifacts"), filepath.Join(root, "data"), 1)
	manager.workerSlots <- struct{}{}

	owner := JobOwner{ID: 42, Name: "测试员工"}
	job, err := manager.Submit(ModuleThoughts, map[string]any{"url": "https://thoughts.teambition.com/workspaces/123456/overview", "format": "docx"}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if job.ArtifactPath != filepath.Join(root, "artifacts", ModuleThoughts, "user-42", job.ID) {
		t.Fatalf("unexpected server artifact path: %s", job.ArtifactPath)
	}
	if err := manager.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	<-manager.workerSlots

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := store.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "cancelled" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("queued job was not cancelled")
}

func TestPreflightIgnoresClientOutputDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, filepath.Join(root, "artifacts"), filepath.Join(root, "data"), 1)
	result := manager.Preflight(ModuleThoughts, map[string]any{
		"url":        "https://thoughts.teambition.com/workspaces/123456/overview",
		"format":     "docx",
		"output_dir": filepath.Join("relative", "output"),
	})
	if !result.OK {
		t.Fatal("server archive preflight should ignore client output directory")
	}
}

func TestPreflightRejectsUnsupportedSourceAndConcurrency(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, filepath.Join(root, "artifacts"), filepath.Join(root, "data"), 1)
	result := manager.Preflight(ModuleTBFiles, map[string]any{
		"project_url": "https://www.teambition.com/project/123456/works/654321",
		"source":      "unknown",
		"concurrency": float64(99),
	})
	if result.OK {
		t.Fatal("unsupported source and concurrency passed preflight")
	}
}
