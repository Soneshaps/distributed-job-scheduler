package tests

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "testing"
    "time"
    "os"
    "log"
)

// waitUntil retries fn until it returns nil or timeout occurs.
func waitUntil(timeout time.Duration, fn func() error) error {
    deadline := time.Now().Add(timeout)
    for {
        if err := fn(); err == nil {
            return nil
        }
        if time.Now().After(deadline) {
            return fn() // return last error
        }
        time.Sleep(2 * time.Second) // Increased sleep time
    }
}

// healthCheck verifies the API is ready to accept requests
func healthCheck(apiURL string) error {
    resp, err := http.Get(apiURL + "/health")
    if err != nil {
        return fmt.Errorf("health check failed: %v", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("health check returned status %d", resp.StatusCode)
    }
    return nil
}

func TestSubmitJob(t *testing.T) {
    base := os.Getenv("API_URL")
    if base == "" {
        // Use Docker service name when running in container
        if os.Getenv("DOCKER_ENV") == "true" {
            base = "http://api:8080"
        } else {
            base = "http://localhost:8080"
        }
    }
    apiURL := base + "/jobs"
    
    log.Printf("Testing API at: %s", apiURL)

    // First, wait for the API to be healthy
    log.Printf("Waiting for API to be ready...")
    err := waitUntil(60*time.Second, func() error {
        return healthCheck(base)
    })
    if err != nil {
        t.Fatalf("API health check failed: %v", err)
    }
    log.Printf("API is ready!")

    // Now test job submission
    log.Printf("Testing job submission...")
    err = waitUntil(30*time.Second, func() error {
        reqBody := map[string]any{
            "type":        "send_email",
            "priority":    "normal",
            "payload":     "{\"to\": \"test@example.com\", \"subject\": \"Test\"}",
            "max_retries": 1,
        }
        b, _ := json.Marshal(reqBody)
        
        resp, err := http.Post(apiURL, "application/json", bytes.NewReader(b))
        if err != nil {
            return fmt.Errorf("HTTP request failed: %v", err)
        }
        defer resp.Body.Close()
        
        log.Printf("Response status: %d", resp.StatusCode)
        
        if resp.StatusCode != http.StatusAccepted {
            return fmt.Errorf("expected status 202, got %d", resp.StatusCode)
        }
        
        var out struct {
            JobID string `json:"job_id"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
            return fmt.Errorf("failed to decode response: %v", err)
        }
        
        if out.JobID == "" {
            return fmt.Errorf("job_id is empty in response")
        }
        
        log.Printf("Job submitted successfully with ID: %s", out.JobID)
        return nil
    })
    
    if err != nil {
        t.Fatalf("Job submission test failed: %v", err)
    }
    
    log.Printf("Test completed successfully!")
}

// tempError is used to implement retryable errors.
type tempError struct{ status int }

func (e *tempError) Error() string   { return http.StatusText(e.status) }
func (e *tempError) Temporary() bool { return true } 