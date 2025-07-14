# **Distributed Job Scheduler**

This repository contains the source code and documentation for a production-grade, distributed job scheduling system built in Go. The platform is engineered for high-throughput, reliability, and elastic scalability, designed to run mission-critical background tasks like email dispatch, data exports, and payment processing.

This document serves as the primary design document, fulfilling all requirements of the original assignment brief.

## **1. High-Level Architecture**

The system is built on a set of decoupled, event-driven microservices that communicate asynchronously. This design ensures that components can be scaled and deployed independently, and that the system remains resilient to partial failures.

![Architecture Diagram](https://i.ibb.co/wr7CvYkJ/light-h-l-a.png)

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


---

## **Getting Started**

This project is fully containerized and can be launched with a single command. The following instructions will guide you through building the Go applications, launching the entire stack, and interacting with the system.

### **Prerequisites**

*   **Docker:** [Install Docker](https://docs.docker.com/get-docker/)
*   **Docker Compose:** [Install Docker Compose](https://docs.docker.com/compose/install/) (Included with Docker Desktop)
*   **Go (v1.21+):** [Install Go](https://go.dev/doc/install) (for running tests locally)

### **Running the Application**

1.  **Clone the Repository:**
    ```sh
    git clone <your-repo-url>
    cd job-scheduler
    ```

2.  **Launch the Entire Stack:**
    The `docker-compose.yml` file orchestrates all services: our Go applications (`api`, `publisher`, `worker`), `postgres`, `rabbitmq`, `prometheus`, `grafana`, and a `simulator` to generate load.

    The following command will build the application images, start all containers in detached mode, and scale the `worker` service to 10 replicas to demonstrate horizontal scalability.

    ```sh
    docker-compose up --build -d --scale worker=10
    ```
    *   `--build`: Forces a rebuild of the Go application images if there are code changes.
    *   `-d`: Runs the containers in the background.
    *   `--scale worker=10`: Starts 10 instances of our stateless `worker` service.

3.  **Verify Services are Running:**
    You can check the status of all running containers:
    ```sh
    docker-compose ps
    ```
    You should see all services (`postgres`, `rabbitmq`, `api`, etc.) with a `running` or `healthy` status.

### **Accessing Services & Dashboards**

Once the stack is running, you can access the following service UIs in your browser:

*   **RabbitMQ Management UI:** `http://localhost:15672`
    *   **Credentials:** `user` / `password`
    *   **What to look for:** Inspect the `jobs.exchange`, the individual work queues (`jobs.queue.send_email`, etc.), and the `jobs.dead_letter.queue`. You can see message rates and the number of jobs waiting.

*   **Grafana Dashboard:** `http://localhost:3000`
    *   **Credentials:** `admin` / `admin`
    *   **What to look for:** Navigate to the pre-configured "Job Scheduler Overview" dashboard to see a live visualization of job throughput, processing latency, and error rates.

*   **Prometheus UI:** `http://localhost:9090`
    *   **What to look for:** Go to `Status -> Targets` to verify that Prometheus is successfully scraping metrics from the `api` and `worker` services. You can run queries like `rate(jobs_processed_total[1m])` to explore the raw data.

### **Interacting with the System**

#### **Submitting a Job**
You can submit a new job by sending a `POST` request to the `api` service from your terminal.

```sh
curl -X POST http://localhost:8080/jobs \
-H "Content-Type: application/json" \
-d '{
    "type": "send_email",
    "priority": "high",
    "payload": "{\"to\": \"test@example.com\", \"subject\": \"Hello from the Job Scheduler!\"}",
    "max_retries": 3
}'
```
The system will respond with a `202 Accepted` status and the `job_id`:
```json
{"job_id":"a1b2c3d4-e5f6-...."}
```

#### **Checking the Logs**
To see the structured JSON logs from the services, you can follow the logs of a specific service. For example, to see the workers processing jobs:
```sh
docker-compose logs -f worker
```

## **Testing the System**

The project includes integration tests that can be run against the live Docker Compose environment.

1.  **Run the Go Integration Test:**
    This test includes a health check and verifies the job submission endpoint. Execute it from your host machine while the containers are running.

    ```sh
    docker-compose exec api go test ./tests/... -v
    ```
    *This command executes `go test` inside the running `api` container.*

2.  **Run the Shell Script Test:**
    A simple shell script is also available to quickly test the health and job submission endpoints from your terminal.
    ```sh
    ./tests/test_api.sh
    ```

## **Stopping the Application**

To stop and remove all running containers, networks, and volumes, run:
```sh
docker-compose down -v
```
The `-v` flag ensures the PostgreSQL data volume is also removed, giving you a clean slate for the next run.


---

### **Production Deployment on AWS EKS**

While the `docker-compose.yml` provides a complete environment for local development, this project is designed for a production-grade, cloud-native deployment on **Amazon EKS (Elastic Kubernetes Service)**, orchestrated with **Terraform**.

The `/terraform` directory contains the necessary Infrastructure as Code (IaC) to provision the entire cloud environment.

#### **Key Deployment Features:**

1.  **Infrastructure as Code (IaC):**
    *   **Terraform Modules:** The infrastructure is modularized using official Terraform modules for VPC (`terraform-aws-modules/vpc/aws`) and EKS (`terraform-aws-modules/eks/aws`), ensuring best practices for networking and cluster configuration.
    *   **VPC & Networking:** A new VPC is created with public and private subnets across multiple availability zones for high availability.
    *   **EKS Cluster:** A robust EKS cluster is provisioned with separate managed node groups for general workloads and our dedicated `job-workers`.

2.  **Kubernetes Manifests & Kustomize:**
    *   **Base Configuration:** The `/terraform/kubernetes/base` directory contains the baseline Kubernetes manifests for all services (`api`, `worker`, `publisher`, etc.).
    *   **Environment Overlays:** We use **Kustomize** to manage environment-specific configurations. The `/terraform/kubernetes/overlays/production` overlay, for example, replaces placeholder image names with the actual Amazon ECR image URIs, demonstrating a clean separation of configuration from base definitions.

3.  **Event-Driven Autoscaling with KEDA:**
    *   The worker deployment is managed by a **KEDA `ScaledObject`** (`/terraform/kubernetes/base/06-worker-scaledobject.yaml`).
    *   KEDA directly monitors the RabbitMQ work queues. If the queue length exceeds a defined threshold (e.g., 10 messages), KEDA automatically scales up the number of `worker` pods. When the queue is empty, it scales the workers down (potentially to zero) to save costs. This provides a truly elastic and responsive execution engine.

This Terraform setup provides a repeatable, automated, and production-ready path for deploying and managing the entire job scheduler system on AWS.