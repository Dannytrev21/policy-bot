# Policy-Bot Testing Guide

This comprehensive guide explains how to test both HTTP webhook and SQS event processing in policy-bot, including LocalStack setup, server management, and running all integration tests.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Environment Setup](#environment-setup)
- [Running Tests](#running-tests)
- [Manual Testing](#manual-testing)
- [Performance Testing](#performance-testing)
- [Troubleshooting](#troubleshooting)
- [Advanced Testing](#advanced-testing)

## Prerequisites

### Required Tools
- **Docker** - For LocalStack container management
- **Go 1.19+** - For building and running tests
- **AWS CLI** - For SQS queue management
- **curl** - For HTTP webhook testing

### Environment Variables
```bash
# Optional: Customize LocalStack settings
export LOCALSTACK_CONTAINER_NAME=policy-bot-localstack
export LOCALSTACK_PORT=4566
export LOCALSTACK_URL=http://localhost:4566
```

## Quick Start

### 1. Setup LocalStack
```bash
# Start LocalStack and create all required SQS queues
./scripts/setup-localstack.sh start
```

### 2. Build Policy-Bot
```bash
# Build the binary
go build -o policy-bot ./main.go
```

### 3. Start Test Server
```bash
# Start server in test mode (bypasses GitHub App validation)
./policy-bot server --config config/test-config.yml --test-mode
```

### 4. Verify Setup
```bash
# Check server health
curl http://localhost:8080/health

# Check LocalStack status
./scripts/setup-localstack.sh status
```

### 5. Run Integration Tests
```bash
# Run all integration tests
go test ./test -v

# Run specific integration test
go test ./test -run TestIntegration_HTTPAndSQSEventProcessing -v

# Run performance tests
go test ./test -run TestIntegration_SQSBurstPerformance -v
```

### 6. Cleanup
```bash
# Stop the server (Ctrl+C)
# Stop LocalStack
./scripts/setup-localstack.sh stop
```

## Environment Setup

### LocalStack Management

The `scripts/setup-localstack.sh` script provides complete LocalStack lifecycle management:

```bash
# Start LocalStack and create all queues
./scripts/setup-localstack.sh start

# Check status
./scripts/setup-localstack.sh status

# Recreate queues only (if they get corrupted)
./scripts/setup-localstack.sh queues

# Stop LocalStack
./scripts/setup-localstack.sh stop

# Help
./scripts/setup-localstack.sh help
```

**Queues Created:**
- `github-installation`
- `github-pull-request`
- `github-pull-request-review`
- `github-issue-comment`
- `github-status`
- `github-check-run`

### Server Configuration

#### Test Mode vs Production Mode

**Test Mode** (recommended for development):
```bash
# Uses NewWithTestHandlers() - bypasses GitHub App validation
./policy-bot server --config config/test-config.yml --test-mode
```

**Production Mode** (requires real GitHub App):
```bash
# Uses New() - validates GitHub App with real API calls
./policy-bot server --config config/production.yml
```

#### Test Configuration Features

The `config/test-config.yml` includes:

- **Event Routing Configuration**:
  - `pull_request`: Both HTTP and SQS
  - `issue_comment`: Both HTTP and SQS
  - `status`: SQS only
  - `check_run`: HTTP only

- **Worker Allocation**:
  - `installation`: 1 worker (low volume)
  - `pull_request`: 3 workers (medium volume)
  - `status`: 5 workers (high volume)

- **LocalStack Integration**:
  - Endpoint: `http://localhost:4566`
  - Correct queue URL format for LocalStack

## Running Tests

### Integration Tests

Run comprehensive integration tests that validate both HTTP and SQS processing:

```bash
# All integration tests
go test ./test -v

# Specific test with detailed output
go test ./test -run TestIntegration_HTTPAndSQSEventProcessing -v -count=1

# Performance burst test
go test ./test -run TestIntegration_SQSBurstPerformance -v

# Skip long-running tests
go test ./test -short
```

### Unit Tests

Run unit tests for SQS components:

```bash
# SQS consumer tests
go test ./server/sqsconsumer -v

# All unit tests
go test ./... -short
```

### Manual Interactive Testing

Use the interactive test script:

```bash
# Start interactive testing (requires server running)
go run scripts/test-event-processing.go
```

This script:
- Sends test events via both HTTP and SQS
- Shows real-time event processing
- Displays performance metrics
- Validates parallel processing

## Manual Testing

### HTTP Webhook Testing

#### Simple Test
```bash
curl -X POST http://localhost:8080/api/github/hook \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-GitHub-Delivery: test-$(date +%s)" \
  -H "X-Hub-Signature-256: sha256=$(echo -n '{"action":"opened","number":123}' | openssl dgst -sha256 -hmac 'test-webhook-secret-123' | sed 's/^.* //')" \
  -d '{"action":"opened","number":123,"repository":{"name":"test","owner":{"login":"test"}}}'
```

#### Using Manual Test Script
```bash
./scripts/manual-test.sh
```

### SQS Message Testing

#### Send Single Message
```bash
aws --endpoint-url=http://localhost:4566 sqs send-message \
  --queue-url "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/github-pull-request" \
  --message-body '{"event_type":"pull_request","delivery_id":"sqs-test-123","payload":{"action":"opened","number":456},"source":"sqs"}' \
  --region us-east-1
```

#### Batch Testing
```bash
# Send multiple messages quickly
for i in {1..10}; do
  aws --endpoint-url=http://localhost:4566 sqs send-message \
    --queue-url "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/github-pull-request" \
    --message-body "{\"event_type\":\"pull_request\",\"delivery_id\":\"batch-$i\",\"payload\":{\"action\":\"synchronize\",\"number\":$i},\"source\":\"sqs\"}" \
    --region us-east-1 &
done
wait
```

### Event Verification

Monitor server logs to verify event processing:

```bash
# Expected log patterns:
# HTTP: "Test handler received event" ... "source=http"
# SQS:  "Test handler received event" ... "source=sqs"
```

## Performance Testing

### Load Testing with Integration Tests

```bash
# Run performance test (20 events in <5 seconds)
go test ./test -run TestIntegration_SQSBurstPerformance -v

# Monitor performance
go test ./test -run TestIntegration_SQSBurstPerformance -v -count=5
```

### Manual Load Testing

#### HTTP Load Test
```bash
# Send 50 concurrent HTTP events
for i in {1..50}; do
  curl -X POST http://localhost:8080/api/github/hook \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: pull_request" \
    -H "X-GitHub-Delivery: load-test-$i" \
    -H "X-Hub-Signature-256: sha256=$(echo -n "{\"action\":\"opened\",\"number\":$i}" | openssl dgst -sha256 -hmac 'test-webhook-secret-123' | sed 's/^.* //')" \
    -d "{\"action\":\"opened\",\"number\":$i}" &
done
wait
```

#### SQS Load Test
```bash
# Send 100 SQS messages rapidly
for i in {1..100}; do
  aws --endpoint-url=http://localhost:4566 sqs send-message \
    --queue-url "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/github-status" \
    --message-body "{\"event_type\":\"status\",\"delivery_id\":\"load-$i\",\"payload\":{\"state\":\"success\",\"context\":\"test-$i\"},\"source\":\"sqs\"}" \
    --region us-east-1 &

  # Batch in groups of 10 to avoid overwhelming the system
  if [ $((i % 10)) -eq 0 ]; then
    wait
    sleep 0.1
  fi
done
wait
```

### Performance Monitoring

```bash
# Monitor queue depth
watch -n 1 'aws --endpoint-url=http://localhost:4566 sqs get-queue-attributes --queue-url "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/github-pull-request" --attribute-names ApproximateNumberOfMessages --region us-east-1'

# Monitor server resource usage
top -pid $(pgrep policy-bot)
```

## Troubleshooting

### Common Issues

#### 1. LocalStack Not Available
```bash
# Check Docker status
docker ps | grep localstack

# Check LocalStack health
curl http://localhost:4566

# Restart LocalStack
./scripts/setup-localstack.sh stop
./scripts/setup-localstack.sh start
```

#### 2. SQS Queue Errors
```bash
# List all queues
aws --endpoint-url=http://localhost:4566 sqs list-queues --region us-east-1

# Recreate queues
./scripts/setup-localstack.sh queues

# Check queue attributes
aws --endpoint-url=http://localhost:4566 sqs get-queue-attributes \
  --queue-url "http://sqs.us-east-1.localhost.localstack.cloud:4566/000000000000/github-pull-request" \
  --attribute-names All --region us-east-1
```

#### 3. Server Startup Issues
```bash
# GitHub App validation error (use test-mode)
./policy-bot server --config config/test-config.yml --test-mode

# Port already in use
lsof -i :8080
kill -9 <PID>

# Config file issues
./policy-bot server --config config/test-config.yml --test-mode --help
```

#### 4. Test Failures
```bash
# Run tests with verbose output
go test ./test -v -count=1

# Check LocalStack before testing
./scripts/setup-localstack.sh status

# Clean test environment
./scripts/setup-localstack.sh stop
./scripts/setup-localstack.sh start
go test ./test -v
```

### Logs and Debugging

#### Server Logs
```bash
# Start with debug logging
./policy-bot server --config config/test-config.yml --test-mode

# Expected healthy startup logs:
# "Starting SQS consumer"
# "Server listening on 127.0.0.1:8080"
# "Starting SQS workers for queue"
```

#### LocalStack Logs
```bash
# View LocalStack container logs
docker logs policy-bot-localstack

# Follow logs in real-time
docker logs -f policy-bot-localstack
```

### Health Checks

```bash
# Server health
curl http://localhost:8080/health
# Expected: {"status":"ok","message":"Server is healthy"}

# LocalStack health
curl http://localhost:4566
# Expected: {"status": "running"}

# SQS connectivity
aws --endpoint-url=http://localhost:4566 sqs list-queues --region us-east-1
# Expected: List of 6 queues
```

## Advanced Testing

### Custom Event Routing

Modify `config/test-config.yml` to test different routing scenarios:

```yaml
sqs:
  event_routing:
    pull_request: "sqs"      # SQS only
    issue_comment: "http"    # HTTP only
    status: "both"           # Both paths
    check_run: "http"        # HTTP only
```

### Integration with CI/CD

```bash
# Example CI test script
#!/bin/bash
set -e

# Setup
./scripts/setup-localstack.sh start
go build -o policy-bot ./main.go

# Start server in background
./policy-bot server --config config/test-config.yml --test-mode &
SERVER_PID=$!

# Wait for startup
sleep 5

# Run tests
go test ./test -v

# Cleanup
kill $SERVER_PID
./scripts/setup-localstack.sh stop
```

### Event Handler Testing

Create custom test handlers by extending the integration test framework:

```go
// Example custom handler in test
testHandler := NewTestEventHandler([]string{"custom_event"})
srv, _, cleanup := setupTestServer(t, config, testHandler)
defer cleanup()
```

### Memory and Performance Profiling

```bash
# Run with profiling
go test ./test -run TestIntegration_SQSBurstPerformance -cpuprofile=cpu.prof -memprofile=mem.prof

# Analyze profiles
go tool pprof cpu.prof
go tool pprof mem.prof
```

## Event Types and Expected Behavior

| Event Type | HTTP Route | SQS Queue | Default Routing | Workers |
|------------|------------|-----------|-----------------|---------|
| `installation` | ✅ | ✅ | both | 1 |
| `pull_request` | ✅ | ✅ | both | 3 |
| `pull_request_review` | ✅ | ✅ | both | 2 |
| `issue_comment` | ✅ | ✅ | both | 2 |
| `status` | ✅ | ✅ | sqs | 5 |
| `check_run` | ✅ | ✅ | http | 3 |

### Expected Performance Targets

- **Throughput**: >1000 events/minute per worker
- **Latency**: <500ms average processing time
- **Memory**: <100MB with 10 workers under load
- **Reliability**: >99% message processing success rate

## Quick Reference Commands

```bash
# Full test cycle
./scripts/setup-localstack.sh start
go build -o policy-bot ./main.go
./policy-bot server --config config/test-config.yml --test-mode &
go test ./test -v
kill %1
./scripts/setup-localstack.sh stop

# Health checks
curl http://localhost:8080/health
./scripts/setup-localstack.sh status

# Manual testing
./scripts/manual-test.sh
go run scripts/test-event-processing.go

# Performance testing
go test ./test -run TestIntegration_SQSBurstPerformance -v
```

---

For more detailed information about the SQS architecture and implementation, see `.claude/architecture/policy-bot-sqs_plan.md`.