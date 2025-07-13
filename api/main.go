package main

import (
	"context"
	"encoding/json"
	"job-scheduler/pkg/database"
	"job-scheduler/pkg/job"
	"job-scheduler/pkg/mq"
	"job-scheduler/pkg/observability"
	"log/slog"
	"net/http"
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
    
    // In a real app, you might use a migration tool. For this demo, we ensure the schema exists.
    if err := dbClient.InitSchema(context.Background()); err != nil {
        slog.Error("failed to initialize schema", "error", err)
    }

	mqClient, err = mq.New()
	if err != nil {
		slog.Error("failed to connect to rabbitmq", "error", err)
		return
	}
	defer mqClient.Close()

	if err := mqClient.SetupTopology(); err != nil {
		slog.Error("failed to setup rabbitmq topology", "error", err)
		return
	}

	observability.StartMetricsServer(":8081")

	http.HandleFunc("/jobs", handleSubmitJob)
	slog.Info("API server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		slog.Error("api server failed", "error", err)
	}
}

func handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req job.SubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	jobID, err := dbClient.CreateJobAndOutboxMessage(r.Context(), &req)
	if err != nil {
		slog.Error("failed to create job and outbox message", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	observability.JobsSubmitted.WithLabelValues(string(req.Type), string(req.Priority)).Inc()
	slog.Info("job submitted successfully", "job_id", jobID, "type", req.Type)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
} 