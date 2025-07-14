package database

import (
	"context"
	"fmt"
	"job-scheduler/pkg/job"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"database/sql"
)

type Client struct {
	pool *pgxpool.Pool
}

func New() (*Client, error) {
	ctx := context.Background()

	// Parse connection string into pgxpool.Config to allow tweaking settings.
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("unable to parse database URL: %w", err)
	}

	// Allow tuning the maximum connections via environment variable to avoid exhausting Postgres.
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		if n, errConv := strconv.Atoi(v); errConv == nil && n > 0 {
			cfg.MaxConns = int32(n)
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}
	return &Client{pool: pool}, nil
}

func (c *Client) Close() {
	c.pool.Close()
}

// InitSchema creates the necessary tables and types.
func (c *Client) InitSchema(ctx context.Context) error {
	schema := `
    CREATE TYPE job_status AS ENUM ('PENDING', 'IN_PROGRESS', 'COMPLETED', 'FAILED', 'RETRYING');
    CREATE TYPE job_priority AS ENUM ('low', 'normal', 'high');
    CREATE TYPE job_type AS ENUM ('send_email', 'export_data');
    CREATE TABLE IF NOT EXISTS jobs (
        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        type job_type NOT NULL,
        status job_status NOT NULL DEFAULT 'PENDING',
        priority job_priority NOT NULL DEFAULT 'normal',
        payload TEXT,
        max_retries INTEGER NOT NULL DEFAULT 3,
        retry_count INTEGER NOT NULL DEFAULT 0,
        last_error TEXT,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );
    CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs (status);

    -- Outbox table for transactional outbox pattern
    CREATE TABLE IF NOT EXISTS job_outbox (
        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
        job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
        exchange TEXT NOT NULL,
        routing_key TEXT NOT NULL,
        payload TEXT NOT NULL,
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );
    `
	_, err := c.pool.Exec(ctx, schema)
	return err
}

func (c *Client) CreateJob(ctx context.Context, j *job.SubmissionRequest) (string, error) {
	var jobID string
	query := `INSERT INTO jobs (type, priority, payload, max_retries) VALUES ($1, $2, $3, $4) RETURNING id`
	err := c.pool.QueryRow(ctx, query, j.Type, j.Priority, j.Payload, j.MaxRetries).Scan(&jobID)
	return jobID, err
}

func (c *Client) GetJob(ctx context.Context, jobID string) (*job.Job, error) {
	j := &job.Job{}
	var lastError sql.NullString
	query := `SELECT id, type, status, priority, payload, max_retries, retry_count, last_error, created_at, updated_at 
              FROM jobs WHERE id = $1`
	err := c.pool.QueryRow(ctx, query, jobID).Scan(
		&j.ID, &j.Type, &j.Status, &j.Priority, &j.Payload, &j.MaxRetries,
		&j.RetryCount, &lastError, &j.CreatedAt, &j.UpdatedAt,
	)
	if lastError.Valid {
		j.LastError = lastError.String
	} else {
		j.LastError = ""
	}
	return j, err
}

// ClaimJob atomically fetches a job and marks it as IN_PROGRESS. Returns nil if no job was claimed.
func (c *Client) ClaimJob(ctx context.Context, jobID string) (*job.Job, error) {
	j := &job.Job{}
	var lastError sql.NullString
	query := `
        UPDATE jobs
        SET status = 'IN_PROGRESS', updated_at = NOW()
        WHERE id = $1 AND status = 'PENDING'
        RETURNING id, type, status, priority, payload, max_retries, retry_count, last_error, created_at, updated_at
    `
	err := c.pool.QueryRow(ctx, query, jobID).Scan(
		&j.ID, &j.Type, &j.Status, &j.Priority, &j.Payload, &j.MaxRetries,
		&j.RetryCount, &lastError, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		// pgx.ErrNoRows means another worker claimed it. This is not an application error.
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if lastError.Valid {
		j.LastError = lastError.String
	} else {
		j.LastError = ""
	}
	return j, nil
}

func (c *Client) UpdateJobStatus(ctx context.Context, jobID string, status job.Status, errStr string) error {
	query := `UPDATE jobs SET status = $1, last_error = $2, updated_at = NOW() WHERE id = $3`
	if status == job.StatusRetrying {
		query = `UPDATE jobs SET status = $1, last_error = $2, retry_count = retry_count + 1, updated_at = NOW() WHERE id = $3`
	}
	_, err := c.pool.Exec(ctx, query, status, errStr, jobID)
	return err
}

// OutboxMessage represents a row in the job_outbox table.
type OutboxMessage struct {
    ID         string
    JobID      string
    Exchange   string
    RoutingKey string
    Payload    string
    CreatedAt  string
}

// CreateJobAndOutboxMessage inserts the job and a corresponding outbox message in a single transaction.
func (c *Client) CreateJobAndOutboxMessage(ctx context.Context, j *job.SubmissionRequest) (string, error) {
    tx, err := c.pool.Begin(ctx)
    if err != nil {
        return "", err
    }
    defer tx.Rollback(ctx)

    var jobID string
    insertJob := `INSERT INTO jobs (type, priority, payload, max_retries) VALUES ($1, $2, $3, $4) RETURNING id`
    if err := tx.QueryRow(ctx, insertJob, j.Type, j.Priority, j.Payload, j.MaxRetries).Scan(&jobID); err != nil {
        return "", err
    }

    insertOutbox := `INSERT INTO job_outbox (job_id, exchange, routing_key, payload) VALUES ($1, $2, $3, $4)`
    if _, err := tx.Exec(ctx, insertOutbox, jobID, "jobs.exchange", string(j.Type), jobID); err != nil {
        return "", err
    }

    if err := tx.Commit(ctx); err != nil {
        return "", err
    }
    return jobID, nil
}

// FetchOutboxMessages retrieves up to 'limit' outbox messages ordered by creation time.
func (c *Client) FetchOutboxMessages(ctx context.Context, limit int) ([]OutboxMessage, error) {
    query := `SELECT id, job_id, exchange, routing_key, payload, created_at FROM job_outbox ORDER BY created_at LIMIT $1`
    rows, err := c.pool.Query(ctx, query, limit)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    messages := []OutboxMessage{}
    for rows.Next() {
        var m OutboxMessage
        var ts time.Time
        if err := rows.Scan(&m.ID, &m.JobID, &m.Exchange, &m.RoutingKey, &m.Payload, &ts); err != nil {
            return nil, err
        }
        m.CreatedAt = ts.Format(time.RFC3339)
        messages = append(messages, m)
    }
    return messages, rows.Err()
}

// DeleteOutboxMessage removes an outbox message after successful publish.
func (c *Client) DeleteOutboxMessage(ctx context.Context, id string) error {
    _, err := c.pool.Exec(ctx, `DELETE FROM job_outbox WHERE id = $1`, id)
    return err
}