package control

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	_, err := s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  module_id TEXT NOT NULL,
  module_name TEXT NOT NULL,
  status TEXT NOT NULL,
  stage TEXT NOT NULL,
  message TEXT NOT NULL,
  input_json TEXT NOT NULL,
  result_json TEXT,
  error TEXT,
  artifact_path TEXT NOT NULL,
  owner_id INTEGER NOT NULL DEFAULT 0,
  owner_name TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at DESC);`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN owner_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE jobs ADD COLUMN owner_name TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_jobs_owner_created ON jobs(owner_id, created_at DESC)`)
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS usage_events (
  event_id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL UNIQUE,
  employee_id INTEGER NOT NULL,
  user_name TEXT NOT NULL DEFAULT '',
  module_id TEXT NOT NULL,
  remark TEXT NOT NULL DEFAULT '',
  occurred_at TEXT NOT NULL,
  delivery_status TEXT NOT NULL,
  delivery_attempts INTEGER NOT NULL DEFAULT 0,
  delivered_at TEXT,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_events_delivery ON usage_events(delivery_status, occurred_at);
CREATE INDEX IF NOT EXISTS idx_usage_events_employee ON usage_events(employee_id, occurred_at);`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE jobs SET status='failed', stage='interrupted', message='服务重启，任务已中断', error='service restarted before the job completed', finished_at=? WHERE status IN ('queued','running','cancelling')`, formatTime(time.Now().UTC()))
	return err
}

func (s *Store) RecordUsageEvent(event UsageEvent, deliveryStatus string) (bool, error) {
	now := formatTime(time.Now().UTC())
	result, err := s.db.Exec(`INSERT OR IGNORE INTO usage_events(
  event_id,job_id,employee_id,user_name,module_id,remark,occurred_at,delivery_status,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.JobID, event.EmployeeID, event.UserName, event.ModuleID, event.Remark, formatTime(event.OccurredAt), deliveryStatus, now, now)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) MarkUsageDelivered(eventID string) error {
	_, err := s.db.Exec(`UPDATE usage_events SET delivery_status='delivered',delivery_attempts=delivery_attempts+1,delivered_at=?,last_error='',updated_at=? WHERE event_id=?`, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()), eventID)
	return err
}

func (s *Store) MarkUsageFailed(eventID, message string) error {
	_, err := s.db.Exec(`UPDATE usage_events SET delivery_status='pending',delivery_attempts=delivery_attempts+1,last_error=?,updated_at=? WHERE event_id=?`, truncateError(message), formatTime(time.Now().UTC()), eventID)
	return err
}

func (s *Store) PendingUsageEvents(limit int) ([]UsageEvent, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT event_id,job_id,employee_id,user_name,module_id,remark,occurred_at FROM usage_events WHERE delivery_status='pending' ORDER BY occurred_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]UsageEvent, 0)
	for rows.Next() {
		var event UsageEvent
		var occurred string
		if err := rows.Scan(&event.EventID, &event.JobID, &event.EmployeeID, &event.UserName, &event.ModuleID, &event.Remark, &occurred); err != nil {
			return nil, err
		}
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func truncateError(value string) string {
	if len(value) > 500 {
		return value[:500]
	}
	return value
}

func (s *Store) Create(job Job) error {
	_, err := s.db.Exec(`INSERT INTO jobs(id,module_id,module_name,status,stage,message,input_json,artifact_path,owner_id,owner_name,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID, job.ModuleID, job.ModuleName, job.Status, job.Stage, job.Message, string(job.Input), job.ArtifactPath, job.OwnerID, job.OwnerName, formatTime(job.CreatedAt))
	return err
}

func (s *Store) Start(id, stage, message string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.Exec(`UPDATE jobs SET status='running',stage=?,message=?,started_at=? WHERE id=? AND status='queued'`, stage, message, now, id)
	return err
}

func (s *Store) Progress(id, stage, message string) error {
	_, err := s.db.Exec(`UPDATE jobs SET stage=?,message=? WHERE id=? AND status IN ('running','cancelling')`, stage, message, id)
	return err
}

func (s *Store) Finish(id, status, stage, message string, result any, runErr error) error {
	var resultJSON []byte
	if result != nil {
		resultJSON, _ = json.Marshal(result)
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	_, err := s.db.Exec(`UPDATE jobs SET status=?,stage=?,message=?,result_json=?,error=?,finished_at=? WHERE id=?`,
		status, stage, message, nullableJSON(resultJSON), errText, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) MarkCancelling(id string) (bool, error) {
	result, err := s.db.Exec(`UPDATE jobs SET status='cancelling',stage='cancelling',message='正在取消任务' WHERE id=? AND status IN ('queued','running')`, id)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

func (s *Store) Get(id string) (Job, error) {
	row := s.db.QueryRow(`SELECT id,module_id,module_name,status,stage,message,input_json,result_json,error,artifact_path,owner_id,owner_name,created_at,started_at,finished_at FROM jobs WHERE id=?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job not found")
	}
	return job, err
}

func (s *Store) GetForOwner(id string, ownerID int64) (Job, error) {
	row := s.db.QueryRow(`SELECT id,module_id,module_name,status,stage,message,input_json,result_json,error,artifact_path,owner_id,owner_name,created_at,started_at,finished_at FROM jobs WHERE id=? AND owner_id=?`, id, ownerID)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job not found")
	}
	return job, err
}

func (s *Store) List(limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,module_id,module_name,status,stage,message,input_json,result_json,error,artifact_path,owner_id,owner_name,created_at,started_at,finished_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ListForOwner(ownerID int64, limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,module_id,module_name,status,stage,message,input_json,result_json,error,artifact_path,owner_id,owner_name,created_at,started_at,finished_at FROM jobs WHERE owner_id=? ORDER BY created_at DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, scanErr := scanJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (Job, error) {
	var job Job
	var input, result, errText, created, started, finished sql.NullString
	err := row.Scan(&job.ID, &job.ModuleID, &job.ModuleName, &job.Status, &job.Stage, &job.Message, &input, &result, &errText, &job.ArtifactPath, &job.OwnerID, &job.OwnerName, &created, &started, &finished)
	if err != nil {
		return Job{}, err
	}
	job.Input = json.RawMessage(input.String)
	if result.Valid {
		job.Result = json.RawMessage(result.String)
	}
	job.Error = errText.String
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	job.StartedAt = parseOptionalTime(started)
	job.FinishedAt = parseOptionalTime(finished)
	return job, nil
}

func formatTime(value time.Time) string { return value.Format(time.RFC3339Nano) }

func parseOptionalTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func nullableJSON(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
