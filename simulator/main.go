package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"job-scheduler/pkg/job"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	apiURL := os.Getenv("API_URL")
	if apiURL == "" {
		apiURL = "http://api:8080/jobs"
	}

	// New: configurable load parameters
	ratePerSec := 1
	if v := os.Getenv("RATE_PER_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ratePerSec = n
		}
	}

	concurrency := 1
	if v := os.Getenv("CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		}
	}

	rand.Seed(time.Now().UnixNano())

	// Launch concurrent workers
	for i := 0; i < concurrency; i++ {
		go submitLoop(apiURL, ratePerSec/concurrency)
	}

	select {} // block forever
}

func submitLoop(apiURL string, rps int) {
	interval := time.Second
	if rps > 0 {
		interval = time.Second / time.Duration(rps)
	}
	if interval < time.Millisecond {
		interval = time.Millisecond // prevent very tight loop that overwhelms API inside container
	}
	ticker := time.NewTicker(interval)
	for {
		<-ticker.C
		req := job.SubmissionRequest{
			Type:      randomJobType(),
			Priority:  randomPriority(),
			Payload:   randomPayload(),
			MaxRetries: 3,
		}
		body, _ := json.Marshal(req)
		resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("failed to submit job: %v", err)
			continue
		}
		log.Printf("submitted job: %s, status: %d", req.Type, resp.StatusCode)
		resp.Body.Close()
	}
}

func randomJobType() job.Type {
	if rand.Intn(2) == 0 {
		return job.TypeSendEmail
	}
	return job.TypeExportData
}

func randomPriority() job.Priority {
	switch rand.Intn(3) {
	case 0:
		return job.PriorityLow
	case 1:
		return job.PriorityNormal
	default:
		return job.PriorityHigh
	}
}

func randomPayload() string {
	return fmt.Sprintf(`{"user":"user%d","data":"payload"}`, rand.Intn(1000))
} 