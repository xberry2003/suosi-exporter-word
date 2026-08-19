package control

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAndRecoversInterruptedJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job-1", ModuleID: ModuleThoughts, ModuleName: "所思导出", Status: "queued", Stage: "queued", Message: "queued", Input: json.RawMessage(`{"url":"https://example.test"}`), ArtifactPath: "artifacts/job-1", CreatedAt: time.Now().UTC()}
	if err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := store.Start(job.ID, "starting", "running"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Stage != "interrupted" || got.FinishedAt == nil {
		t.Fatalf("unexpected recovered job: %#v", got)
	}
}

func TestStoreJobLifecycle(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job := Job{ID: "job-2", ModuleID: ModuleTBFiles, ModuleName: "TB 文件下载", Status: "queued", Stage: "queued", Message: "queued", Input: json.RawMessage(`{}`), ArtifactPath: "artifacts/job-2", CreatedAt: time.Now().UTC()}
	if err := store.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := store.Start(job.ID, "discovering", "running"); err != nil {
		t.Fatal(err)
	}
	if err := store.Progress(job.ID, "downloading", "downloading"); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(job.ID, "succeeded", "finished", "done", map[string]any{"files": 3}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" || got.StartedAt == nil || got.FinishedAt == nil || len(got.Result) == 0 {
		t.Fatalf("unexpected completed job: %#v", got)
	}
}
