#!/bin/bash

# Test script for the API
set -e

API_URL=${API_URL:-"http://localhost:8080"}
echo "Testing API at: $API_URL"

# Wait for API to be ready
echo "Waiting for API to be ready..."
for i in {1..30}; do
    if curl -f "$API_URL/health" > /dev/null 2>&1; then
        echo "API is ready!"
        break
    fi
    echo "Attempt $i/30: API not ready yet..."
    sleep 2
done

# Test health endpoint
echo "Testing health endpoint..."
curl -f "$API_URL/health"
echo "Health check passed!"

# Test job submission
echo "Testing job submission..."
response=$(curl -s -X POST "$API_URL/jobs" \
    -H "Content-Type: application/json" \
    -d '{
        "type": "send_email",
        "priority": "normal",
        "payload": "{\"to\": \"test@example.com\", \"subject\": \"Test\"}",
        "max_retries": 1
    }')

echo "Response: $response"

# Check if response contains job_id
if echo "$response" | grep -q "job_id"; then
    echo "✅ Job submission test PASSED!"
    exit 0
else
    echo "❌ Job submission test FAILED!"
    exit 1
fi 