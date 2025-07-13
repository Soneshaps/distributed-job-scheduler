package observability

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	JobsSubmitted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobs_submitted_total",
		Help: "The total number of submitted jobs",
	}, []string{"type", "priority"})

	JobsProcessed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "jobs_processed_total",
		Help: "The total number of processed jobs",
	}, []string{"type", "status"}) // status: completed, failed, retried

	JobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "job_duration_seconds",
		Help:    "Duration of job processing.",
		Buckets: prometheus.LinearBuckets(0.1, 0.2, 10),
	}, []string{"type"})
)

// NewLogger creates a new structured logger.
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// StartMetricsServer runs an HTTP server to expose Prometheus metrics.
func StartMetricsServer(addr string) {
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(addr, nil); err != nil {
			slog.Error("metrics server failed", "error", err)
		}
	}()
} 