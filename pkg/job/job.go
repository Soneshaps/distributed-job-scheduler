package job

import "time"

type Type string
type Status string
type Priority string

const (
	TypeSendEmail    Type = "send_email"
	TypeExportData   Type = "export_data"
)

const (
	StatusPending    Status = "PENDING"
	StatusInProgress Status = "IN_PROGRESS"
	StatusCompleted  Status = "COMPLETED"
	StatusFailed     Status = "FAILED"
	StatusRetrying   Status = "RETRYING"
)

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

type Job struct {
	ID          string    `json:"id"`
	Type        Type      `json:"type"`
	Status      Status    `json:"status"`
	Priority    Priority  `json:"priority"`
	Payload     string    `json:"payload"` // Using string for simplicity, JSONB in DB
	MaxRetries  int       `json:"max_retries"`
	RetryCount  int       `json:"retry_count"`
	LastError   string    `json:"last_error"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type SubmissionRequest struct {
	Type       Type      `json:"type"`
	Priority   Priority  `json:"priority"`
	Payload    string    `json:"payload"`
	MaxRetries int       `json:"max_retries"`
} 