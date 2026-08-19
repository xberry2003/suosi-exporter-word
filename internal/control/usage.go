package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UsageConfig struct {
	Enabled   bool
	BaseURL   string
	ProductID int64
	Source    string
	Timeout   time.Duration
}

func UsageConfigFromEnv() UsageConfig {
	timeout := 3 * time.Second
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SUOSI_USAGE_TIMEOUT_MS"))); err == nil && value >= 100 {
		timeout = time.Duration(value) * time.Millisecond
	}
	productID, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("SUOSI_USAGE_PRODUCT_ID")), 10, 64)
	return UsageConfig{
		Enabled:   enabledEnv(os.Getenv("SUOSI_USAGE_TRACKING_ENABLED")),
		BaseURL:   strings.TrimRight(strings.TrimSpace(os.Getenv("SUOSI_USAGE_API_BASE_URL")), "/"),
		ProductID: productID,
		Source:    defaultEnv(os.Getenv("SUOSI_USAGE_SOURCE"), "suosi-control"),
		Timeout:   timeout,
	}
}

type UsageTracker struct {
	store  *Store
	config UsageConfig
	client *http.Client
	logger *log.Logger
	wg     sync.WaitGroup
}

func NewUsageTracker(store *Store, config UsageConfig) *UsageTracker {
	if config.Timeout < 100*time.Millisecond {
		config.Timeout = 3 * time.Second
	}
	return &UsageTracker{store: store, config: config, client: &http.Client{Timeout: config.Timeout}, logger: log.Default()}
}

func (t *UsageTracker) deliveryEnabled() bool {
	return t.config.Enabled && t.config.BaseURL != "" && t.config.ProductID > 0
}

func (t *UsageTracker) RecordSuccessfulJob(job Job, result map[string]any) {
	if job.Status == "" {
		job.Status = "succeeded"
	}
	// Usage measures completed collection jobs. A module may legitimately finish
	// without a non-log file (for example, an empty but valid result), so the
	// job status is the source of truth; dry-runs remain excluded.
	if job.Status != "succeeded" || boolValue(result, "dry_run") {
		return
	}
	finished := time.Now().UTC()
	if job.FinishedAt != nil {
		finished = job.FinishedAt.UTC()
	}
	event := UsageEvent{
		EventID:    fmt.Sprintf("suosi-control:%s", job.ID),
		JobID:      job.ID,
		EmployeeID: job.OwnerID,
		UserName:   job.OwnerName,
		ModuleID:   job.ModuleID,
		Remark:     fmt.Sprintf("job_succeeded | job:%s | module:%s", job.ID, job.ModuleID),
		OccurredAt: finished,
	}
	status := "local_only"
	if t.deliveryEnabled() {
		status = "pending"
	}
	inserted, err := t.store.RecordUsageEvent(event, status)
	if err != nil || !inserted {
		if err != nil {
			t.logger.Printf("[usage] record failed for %s: %v", event.EventID, err)
		}
		return
	}
	if !t.deliveryEnabled() {
		return
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if err := t.deliver(event); err != nil {
			_ = t.store.MarkUsageFailed(event.EventID, err.Error())
			t.logger.Printf("[usage] delivery failed for %s: %v", event.EventID, err)
		}
	}()
}

func (t *UsageTracker) RetryPending() {
	if !t.deliveryEnabled() {
		return
	}
	events, err := t.store.PendingUsageEvents(100)
	if err != nil {
		t.logger.Printf("[usage] pending event query failed: %v", err)
		return
	}
	for _, event := range events {
		t.wg.Add(1)
		go func(event UsageEvent) {
			defer t.wg.Done()
			if err := t.deliver(event); err != nil {
				_ = t.store.MarkUsageFailed(event.EventID, err.Error())
				t.logger.Printf("[usage] retry failed for %s: %v", event.EventID, err)
			}
		}(event)
	}
}

func (t *UsageTracker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *UsageTracker) deliver(event UsageEvent) error {
	payload := map[string]any{
		"product_id":  t.config.ProductID,
		"usage_count": 1,
		"source":      t.config.Source,
		"user_name":   event.UserName,
		"user_id":     strconv.FormatInt(event.EmployeeID, 10),
		"event_id":    event.EventID,
		"remark":      event.Remark,
		"occurred_at": event.OccurredAt.Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, t.config.BaseURL+"/products/usage-events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("usage API returned HTTP " + response.Status)
	}
	return t.store.MarkUsageDelivered(event.EventID)
}

func enabledEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func defaultEnv(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func hasDownloadableArtifact(root string, result map[string]any) bool {
	if boolValue(result, "dry_run") {
		return false
	}
	found := false
	_ = filepathWalk(root, func(path string, regular bool) error {
		if regular && !strings.HasSuffix(path, ".log") {
			found = true
		}
		return nil
	})
	return found
}

func filepathWalk(root string, visit func(path string, regular bool) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			return visit(path, true)
		}
		return nil
	})
}
