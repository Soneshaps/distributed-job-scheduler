package main

import (
	"context"
	"errors"
	"fmt"
	"job-scheduler/pkg/database"
	"job-scheduler/pkg/job"
	"job-scheduler/pkg/mq"
	"job-scheduler/pkg/observability"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	dbClient *database.Client
	mqClient *mq.Client
	logger   *slog.Logger
)

func main() {
	logger = observability.NewLogger()
	slog.SetDefault(logger)

	var err error
	dbClient, err = database.New()
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		return
	}
	defer dbClient.Close()

	mqClient, err = mq.New()
	if err != nil {
		slog.Error("failed to connect to rabbitmq", "error", err)
		return
	}
	defer mqClient.Close()

	observability.StartMetricsServer(":9091")

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	// Start workers for each job type
	jobTypes := []job.Type{job.TypeSendEmail, job.TypeExportData}
	for _, jt := range jobTypes {
		wg.Add(1)
		go startWorker(ctx, &wg, jt)
	}

	slog.Info("all workers started. waiting for jobs...")

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	slog.Info("shutdown signal received, stopping workers...")
	cancel()
	wg.Wait()
	slog.Info("all workers stopped gracefully")
}

func startWorker(ctx context.Context, wg *sync.WaitGroup, jobType job.Type) {
	defer wg.Done()

	deliveryChan, err := mqClient.ConsumeJobs(jobType)
	if err != nil {
		logger.Error("failed to start consuming jobs", "type", jobType, "error", err)
		return
	}

	// Determine concurrency per job type
	concurrency := 10
	if v := os.Getenv("WORKER_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	logger.Info("worker started", "type", jobType, "concurrency", concurrency)

	innerWg := sync.WaitGroup{}
	innerWg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer innerWg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-deliveryChan:
					handleMessage(msg)
				}
			}
		}()
	}

	<-ctx.Done()
	innerWg.Wait()
	logger.Info("worker shutting down", "type", jobType)
}

func handleMessage(msg amqp.Delivery) {
	jobID := string(msg.Body)
	l := logger.With("job_id", jobID)

	ctx := context.Background()

	// 1. Atomically claim the job. If it returns nil, another worker got it first.
	claimedJob, err := dbClient.ClaimJob(ctx, jobID)
	if err != nil {
		l.Error("failed to claim job", "error", err)
		msg.Nack(false, true) // Requeue on transient DB error
		return
	}
	if claimedJob == nil {
		l.Info("job already claimed by another worker")
		msg.Ack(false) // Job is handled, so ACK
		return
	}
	l = l.With("job_type", claimedJob.Type)
	l.Info("job claimed, starting processing")

	// 2. Process the job
	timer := time.Now()
	processingErr := processJob(claimedJob)
	observability.JobDuration.WithLabelValues(string(claimedJob.Type)).Observe(time.Since(timer).Seconds())

	// 3. Handle outcome
	if processingErr != nil {
		l.Error("job processing failed", "error", processingErr)
		handleFailure(l, claimedJob, processingErr, msg)
	} else {
		l.Info("job completed successfully")
		dbClient.UpdateJobStatus(ctx, jobID, job.StatusCompleted, "")
		observability.JobsProcessed.WithLabelValues(string(claimedJob.Type), "completed").Inc()
		msg.Ack(false)
	}
}

func handleFailure(l *slog.Logger, j *job.Job, processingErr error, msg amqp.Delivery) {
	ctx := context.Background()
	
    // We need the most up-to-date version of the job to check retry count
    currentJob, err := dbClient.GetJob(ctx, j.ID)
    if err != nil {
        l.Error("failed to get job for failure handling", "error", err)
        msg.Nack(false, true) // Requeue
        return
    }

	if currentJob.RetryCount < currentJob.MaxRetries {
		// Retry logic
		delay := getRetryDelay(currentJob.RetryCount + 1)
		l.Info("retrying job", "attempt", currentJob.RetryCount+1, "delay", delay)
		
		dbClient.UpdateJobStatus(ctx, j.ID, job.StatusRetrying, processingErr.Error())
		mqClient.PublishToRetry(ctx, j.Type, j.ID, delay)
		
		observability.JobsProcessed.WithLabelValues(string(j.Type), "retried").Inc()
	} else {
		// Dead-letter logic
		l.Warn("job failed after all retries, sending to dead-letter queue")
		dbClient.UpdateJobStatus(ctx, j.ID, job.StatusFailed, processingErr.Error())
		// The message will be automatically dead-lettered by RabbitMQ because the queue
		// is configured with a DLX. We just need to Nack it without requeueing.
		observability.JobsProcessed.WithLabelValues(string(j.Type), "failed").Inc()
	}

	msg.Ack(false) // Acknowledge the original message; it's been handled (retried or failed)
}

func getRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 5 * time.Minute
	}
}

// processJob simulates the actual work.
func processJob(j *job.Job) error {
	switch j.Type {
	case job.TypeSendEmail:
		time.Sleep(100 * time.Millisecond) // Simulate fast work
		if rand.Intn(10) == 0 { // 10% chance of failure
			return errors.New("failed to connect to SMTP server")
		}
		return nil
	case job.TypeExportData:
		time.Sleep(1 * time.Second) // Simulate slow work
		if rand.Intn(5) == 0 { // 20% chance of failure
			return errors.New("external API returned 503")
		}
		return nil
	default:
		return fmt.Errorf("unknown job type: %s", j.Type)
	}
} 