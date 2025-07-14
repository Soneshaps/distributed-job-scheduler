# **Distributed Job Scheduler**

This repository contains the source code and documentation for a production-grade, distributed job scheduling system built in Go. The platform is engineered for high-throughput, reliability, and elastic scalability, designed to run mission-critical background tasks like email dispatch, data exports, and payment processing.

This document serves as the primary design document, fulfilling all requirements of the original assignment brief.

## **1. High-Level Architecture**

The system is built on a set of decoupled, event-driven microservices that communicate asynchronously. This design ensures that components can be scaled and deployed independently, and that the system remains resilient to partial failures.

![Architecture Diagram](https://i.ibb.co/LDG1rfVT/light-mode-high-level-architecture.png)

#### **Architectural Flow:**

1.  **Job Ingress (`API Service`):** A client submits a job via a REST API. The API's only responsibility is to validate the request and persist it atomically using the **Transactional Outbox** pattern. This guarantees that no job is ever lost after being accepted.
2.  **Reliable Publishing (`Publisher Service`):** This background service acts as a bridge, polling the database's `outbox` table and reliably publishing the Job ID to RabbitMQ. This decouples the API from the message broker's availability.
3.  **Asynchronous Backbone (`RabbitMQ`):** The message broker receives a lightweight message containing only the Job ID. It is configured to intelligently handle routing, prioritization, and failure scenarios.
4.  **Execution Engine (`Worker Service`):** A horizontally scalable pool of stateless workers consumes jobs from the queue. Each worker fetches the full job payload from the database, executes the task, and updates the final status.
5.  **Elastic Scaling (`KEDA`):** In a cloud-native environment, KEDA monitors the queue length and automatically scales the worker pool from zero to N instances, ensuring resources are used efficiently.

## **2. System Capabilities & Features**

This system was designed to meet a specific set of operational requirements, which are addressed as follows:

*   **Pushing Jobs with Metadata:** The API endpoint accepts a flexible JSON payload, allowing clients to push any required metadata for a job.
*   **Prioritizing Jobs:** We leverage RabbitMQ's native message priority feature. Jobs can be submitted with a `high`, `normal`, or `low` priority, ensuring urgent tasks are processed first.
*   **Configurable Retries:** Failed jobs are automatically retried. The `max_retries` count is a configurable parameter submitted with each job.
*   **Dead-Letter Queue:** After exhausting all retries, jobs are automatically routed to a Dead-Letter Queue (DLQ) for manual inspection and debugging, preventing them from being lost.
*   **Horizontal Scalability:** The `Worker Service` is stateless, allowing it to be scaled horizontally to thousands of concurrent instances to handle any load. The `docker-compose.yml` demonstrates this, and the design is built for autoscalers like KEDA.

## **3. Queue Design: Priority, Retries, and DLQ**

Our RabbitMQ topology is the heart of the system's resiliency and is configured to offload complex logic from the application.

#### **3.1. Job Isolation & Priority**
To prevent slow jobs from blocking fast ones (**head-of-line blocking**), we use dedicated queues per job type (e.g., `jobs.queue.send_email`). For priority within a queue, we use RabbitMQ's native message priority.

*   **Mechanism:**
    1.  A `direct` exchange (`jobs.exchange`) routes jobs based on a `routing_key` matching the job type.
    2.  Each work queue is created with the `x-max-priority: 10` property.
    3.  When a job is published, it's assigned a priority number (e.g., `high`=9, `normal`=5). RabbitMQ delivers higher-priority messages first.

#### **3.2. Delayed Retries**
Workers do not block or `sleep`. Retries are offloaded to the broker using a "holding pen" pattern.

*   **Mechanism:** A failed job is published to a dedicated retry queue (e.g., `jobs.retry.5s`). This queue uses two properties:
    1.  `x-message-ttl`: The message "expires" after a set duration.
    2.  `x-dead-letter-exchange`: When the TTL expires, RabbitMQ automatically routes the message back to the main `jobs.exchange` to be re-processed.

#### **3.3. Dead-Lettering**
Terminally failed jobs are automatically isolated.

*   **Mechanism:** Our main work queues are configured with the `x-dead-letter-exchange` property. When a worker has exhausted all retries, it issues a negative acknowledgement (`nack`) with `requeue=false`. This signals RabbitMQ to automatically route the message to our Dead-Letter Queue (`jobs.dead_letter.queue`).

## **4. Database Schema**

We use PostgreSQL as our source of truth. The schema is designed to store job state and support the Transactional Outbox pattern.

```sql
-- An ENUM type for job statuses for data integrity
CREATE TYPE job_status AS ENUM (
    'PENDING', 'IN_PROGRESS', 'COMPLETED', 'FAILED', 'RETRYING'
);

-- The authoritative record for every job.
CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_type VARCHAR(255) NOT NULL,
    status job_status NOT NULL DEFAULT 'PENDING',
    payload JSONB,
    max_retries INTEGER NOT NULL DEFAULT 3,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The outbox table for reliable message publishing.
CREATE TABLE job_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    routing_key VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

## **5. High-Level Implementation & Logic**

#### **5.1. Key Component Responsibilities**

*   **`API Service`**: Validates requests and atomically persists jobs using the Transactional Outbox pattern.
*   **`Publisher Service`**: Reliably relays messages from the `outbox` table to RabbitMQ.
*   **`Worker Service`**: Consumes jobs, executes business logic, and handles the success/failure lifecycle.

#### **5.2. Code: Transactional Job Creation**
*File: `pkg/database/database.go`*

This function ensures that a job and its corresponding outbox message are created atomically.

```go
// CreateJobAndOutboxMessage inserts the job and a corresponding outbox message in a single transaction.
func (c *Client) CreateJobAndOutboxMessage(ctx context.Context, j *job.SubmissionRequest) (string, error) {
    tx, err := c.pool.Begin(ctx)
    // ... error handling ...
    defer tx.Rollback(ctx)

    // 1. Insert the job into the 'jobs' table.
    var jobID string
    insertJob := `INSERT INTO jobs (...) VALUES (...) RETURNING id`
    if err := tx.QueryRow(ctx, insertJob, ...).Scan(&jobID); err != nil {
        return "", err
    }

    // 2. Insert the message reference into the 'job_outbox' table.
    insertOutbox := `INSERT INTO job_outbox (...) VALUES (...)`
    if _, err := tx.Exec(ctx, insertOutbox, ...); err != nil {
        return "", err
    }

    // 3. Commit the transaction. Both writes succeed or fail together.
    return jobID, tx.Commit(ctx)
}
```

#### **5.3. Retry Logic Flow**

The flow for handling a failed job within a `Worker` is as follows:

1.  **Execution Fails:** A worker attempts to process a job, and the operation returns an error.
2.  **Fetch Latest State:** The worker fetches the job's current `retry_count` from the database to ensure it has the latest information.
3.  **Check Retry Policy:**
    *   **IF `retry_count < max_retries`:**
        a. The worker updates the job's status to `RETRYING` and increments the `retry_count` in the database.
        b. It calculates the appropriate backoff delay (e.g., 5s, 30s).
        c. It publishes the job ID to the corresponding **retry queue** in RabbitMQ.
        d. It acknowledges (`ack`) the original message to remove it from the work queue.
    *   **ELSE (Retries Exhausted):**
        a. The worker updates the job's status to `FAILED` in the database.
        b. It negatively acknowledges (`nack` with `requeue=false`) the message.
        c. RabbitMQ's dead-lettering configuration automatically routes the message to the **Dead-Letter Queue**.
