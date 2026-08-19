package control

import (
	"encoding/json"
	"time"
)

const (
	ModuleThoughts = "thoughts-export"
	ModuleTBFiles  = "tb-files"
	ModuleTBTasks  = "tb-tasks"
)

type ModuleInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
	Credential   string   `json:"credential"`
}

type Job struct {
	ID           string          `json:"id"`
	ModuleID     string          `json:"module_id"`
	ModuleName   string          `json:"module_name"`
	Status       string          `json:"status"`
	Stage        string          `json:"stage"`
	Message      string          `json:"message"`
	Input        json.RawMessage `json:"input,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
	ArtifactPath string          `json:"artifact_path"`
	OwnerID      int64           `json:"-"`
	OwnerName    string          `json:"-"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

type JobOwner struct {
	ID   int64
	Name string
}

type UsageEvent struct {
	EventID    string
	JobID      string
	EmployeeID int64
	UserName   string
	ModuleID   string
	Remark     string
	OccurredAt time.Time
}

type CreateJobRequest struct {
	ModuleID string         `json:"module_id"`
	Input    map[string]any `json:"input"`
}

type PreflightResult struct {
	OK       bool     `json:"ok"`
	Checks   []Check  `json:"checks"`
	Warnings []string `json:"warnings"`
}

type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
