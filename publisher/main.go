package main

import (
    "context"
    "job-scheduler/pkg/database"
    "job-scheduler/pkg/mq"
    "log/slog"
    "os"
    "time"
)

func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    slog.SetDefault(logger)

    dbClient, err := database.New()
    if err != nil {
        logger.Error("failed to connect to database", "error", err)
        return
    }
    defer dbClient.Close()

    mqClient, err := mq.New()
    if err != nil {
        logger.Error("failed to connect to rabbitmq", "error", err)
        return
    }
    defer mqClient.Close()

    // Ensure topology exists; safe if already declared
    if err := mqClient.SetupTopology(); err != nil {
        logger.Error("failed to setup rabbitmq topology", "error", err)
        return
    }

    ctx := context.Background()
    ticker := time.NewTicker(1 * time.Second)
    for {
        select {
        case <-ticker.C:
            processOutbox(ctx, dbClient, mqClient, logger)
        }
    }
}

func processOutbox(ctx context.Context, db *database.Client, mqClient *mq.Client, logger *slog.Logger) {
    messages, err := db.FetchOutboxMessages(ctx, 100)
    if err != nil {
        logger.Error("failed to fetch outbox messages", "error", err)
        return
    }
    for _, m := range messages {
        // Fetch job to get priority and type details.
        jobObj, err := db.GetJob(ctx, m.JobID)
        if err != nil {
            logger.Error("failed to fetch job for outbox publish", "error", err, "job_id", m.JobID)
            continue
        }

        if err := mqClient.PublishJob(ctx, jobObj); err != nil {
            logger.Error("failed to publish job from outbox", "error", err, "job_id", m.JobID)
            continue
        }

        if err := db.DeleteOutboxMessage(ctx, m.ID); err != nil {
            logger.Error("failed to delete outbox message after publish", "error", err, "outbox_id", m.ID)
            continue
        }
        logger.Info("published job from outbox", "job_id", m.JobID)
    }
} 