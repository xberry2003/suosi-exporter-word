package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsageTrackerDisabledRecordsEachJobOnceLocally(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifact := filepath.Join(root, "result.txt")
	if err := os.WriteFile(artifact, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	tracker := NewUsageTracker(store, UsageConfig{Enabled: false, BaseURL: server.URL, ProductID: 101, Source: "suosi-control"})
	job := Job{ID: "job-usage-local", ModuleID: ModuleTBTasks, Status: "succeeded", ArtifactPath: root, OwnerID: 7, OwnerName: "测试员工"}
	tracker.RecordSuccessfulJob(job, map[string]any{})
	tracker.RecordSuccessfulJob(job, map[string]any{})

	var count int
	var status string
	if err := store.db.QueryRow("SELECT COUNT(*), MAX(delivery_status) FROM usage_events").Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "local_only" {
		t.Fatalf("unexpected local usage event: count=%d status=%q", count, status)
	}
	if requests.Load() != 0 {
		t.Fatalf("disabled tracker made %d HTTP requests", requests.Load())
	}
}

func TestUsageTrackerDeliversPayloadAndMarksEvent(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifact := filepath.Join(root, "result.docx")
	if err := os.WriteFile(artifact, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/products/usage-events" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	tracker := NewUsageTracker(store, UsageConfig{Enabled: true, BaseURL: server.URL + "/api", ProductID: 101, Source: "suosi-control", Timeout: time.Second})
	job := Job{ID: "job-usage-delivery", ModuleID: ModuleThoughts, Status: "succeeded", ArtifactPath: root, OwnerID: 8, OwnerName: "张三"}
	tracker.RecordSuccessfulJob(job, map[string]any{})
	if err := tracker.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := store.db.QueryRow("SELECT delivery_status FROM usage_events WHERE event_id=?", "suosi-control:job-usage-delivery").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("event was not delivered: %s", status)
	}
	if payload["product_id"] != float64(101) || payload["usage_count"] != float64(1) || payload["user_id"] != "8" || payload["user_name"] != "张三" {
		t.Fatalf("unexpected usage payload: %#v", payload)
	}
	if !strings.Contains(payload["event_id"].(string), "job-usage-delivery") {
		t.Fatalf("unexpected event id: %#v", payload["event_id"])
	}
}

func TestUsageTrackerFailureLeavesPendingEvent(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	var succeed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !succeed.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	tracker := NewUsageTracker(store, UsageConfig{Enabled: true, BaseURL: server.URL, ProductID: 101, Timeout: time.Second})
	tracker.RecordSuccessfulJob(Job{ID: "job-usage-failed", ModuleID: ModuleTBFiles, Status: "succeeded", ArtifactPath: root}, nil)
	if err := tracker.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := store.db.QueryRow("SELECT delivery_status FROM usage_events WHERE event_id=?", "suosi-control:job-usage-failed").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("failed delivery should remain pending: %s", status)
	}

	succeed.Store(true)
	tracker.RetryPending()
	if err := tracker.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := store.db.QueryRow("SELECT delivery_status, delivery_attempts FROM usage_events WHERE event_id=?", "suosi-control:job-usage-failed").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 2 {
		t.Fatalf("pending event was not retried exactly once: status=%s attempts=%d", status, attempts)
	}
}

func TestUsageTrackerCountsSuccessfulEmptyResultsButSkipsPartialAndDryRun(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "dry_run_tree.txt"), []byte("tree"), 0600); err != nil {
		t.Fatal(err)
	}
	tracker := NewUsageTracker(store, UsageConfig{})
	tracker.RecordSuccessfulJob(Job{ID: "partial", Status: "partial", ArtifactPath: artifactRoot}, map[string]any{})
	tracker.RecordSuccessfulJob(Job{ID: "dry-run", Status: "succeeded", ArtifactPath: artifactRoot}, map[string]any{"dry_run": true})
	tracker.RecordSuccessfulJob(Job{ID: "empty", Status: "succeeded", ArtifactPath: filepath.Join(root, "missing")}, map[string]any{})

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one successful job event, got %d", count)
	}
}
