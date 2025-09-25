# Testing HTTP and SQS Event Processing

This document explains how to test both HTTP webhook and SQS event processing in policy-bot.

## Quick Start

### 1. Start LocalStack (for SQS testing)

```bash
docker run --rm -d -p 4566:4566 --name localstack localstack/localstack
```

### 2. Build and Run the Test Server

```bash
# Build policy-bot
go build -o policy-bot ./main.go

# Run with test configuration
./policy-bot server --config config/test-config.yml
```

The server will start on `http://localhost:8080` with both HTTP and SQS event processing enabled.

### 3. Test HTTP Webhooks

Use the manual test script:

```bash
./scripts/manual-test.sh
```

Or send a manual webhook:

```bash
curl -X POST http://localhost:8080/api/github/hook \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-GitHub-Delivery: test-123" \
  -H "X-Hub-Signature-256: sha256=$(echo -n '{"action":"opened"}' | openssl dgst -sha256 -hmac 'test-webhook-secret-123' | sed 's/^.* //')" \
  -d '{"action":"opened","number":123,"repository":{"name":"test","owner":{"login":"test"}}}'
```

### 4. Test SQS Events

Send a message to the SQS queue:

```bash
aws --endpoint-url=http://localhost:4566 sqs send-message \
  --queue-url "http://localhost:4566/000000000000/github-pull-request" \
  --message-body '{"event_type":"pull_request","delivery_id":"sqs-test-123","payload":{"action":"opened","number":456},"source":"sqs"}' \
  --region us-east-1
```

## What to Expect

When events are processed, you should see log messages like:

```
🎯 [PR-Handler] pull_request event 'test-123' received via http (delivery: test-123)
🎯 [PR-Handler] pull_request event 'sqs-test-123' received via sqs (delivery: sqs-test-123)
```

## Event Routing Testing

The test configuration includes event routing:

- `pull_request`: Processed via both HTTP and SQS
- `issue_comment`: Processed via both HTTP and SQS  
- `status`: SQS only
- `check_run`: HTTP only

## Verification

1. **HTTP Events**: Check server logs for `received via http`
2. **SQS Events**: Check server logs for `received via sqs`
3. **Both Paths**: Send the same event type via both methods and verify both are processed

## Troubleshooting

### LocalStack Issues

```bash
# Check if LocalStack is running
curl http://localhost:4566

# View LocalStack logs
docker logs localstack

# Restart LocalStack
docker stop localstack
docker run --rm -d -p 4566:4566 --name localstack localstack/localstack
```

### SQS Queue Issues

```bash
# List queues
aws --endpoint-url=http://localhost:4566 sqs list-queues --region us-east-1

# Check queue attributes
aws --endpoint-url=http://localhost:4566 sqs get-queue-attributes \
  --queue-url "http://localhost:4566/000000000000/github-pull-request" \
  --attribute-names All --region us-east-1
```

### Server Issues

```bash
# Check if server is responding
curl http://localhost:8080/health

# Check webhook endpoint
curl -I http://localhost:8080/api/github/hook
```

## Advanced Testing

### Custom Event Handlers

You can create custom test handlers by modifying the test script in `scripts/test-event-processing.go`.

### Load Testing

Send multiple events simultaneously to test parallel processing:

```bash
# Send multiple HTTP events
for i in {1..5}; do
  curl -X POST http://localhost:8080/api/github/hook \
    -H "Content-Type: application/json" \
    -H "X-GitHub-Event: pull_request" \
    -H "X-GitHub-Delivery: load-test-$i" \
    -H "X-Hub-Signature-256: sha256=$(echo -n "{\"action\":\"opened\",\"number\":$i}" | openssl dgst -sha256 -hmac 'test-webhook-secret-123' | sed 's/^.* //')" \
    -d "{\"action\":\"opened\",\"number\":$i}" &
done
wait
```

### Event Routing Changes

Modify `config/test-config.yml` to test different routing scenarios:

```yaml
sqs:
  event_routing:
    pull_request: "sqs"    # SQS only
    issue_comment: "http"  # HTTP only
    status: "both"         # Both paths
```

Restart the server and test to verify the routing works as expected.
