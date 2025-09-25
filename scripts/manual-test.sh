#!/bin/bash

# Manual testing script for HTTP and SQS events
# This script sends test events to demonstrate both paths work

SERVER_URL="http://localhost:8080"
WEBHOOK_SECRET="test-webhook-secret-123"

echo "🧪 Manual Event Testing for Policy Bot"
echo "======================================"
echo
echo "Make sure the server is running with the test configuration!"
echo "Server URL: $SERVER_URL"
echo

# Test HTTP webhook
echo "📨 Testing HTTP Webhook..."
echo

# Create test payload
PAYLOAD='{
  "action": "opened",
  "number": 123,
  "pull_request": {
    "id": 456,
    "title": "Test PR from manual script"
  },
  "repository": {
    "name": "test-repo",
    "owner": {
      "login": "test-org"
    }
  }
}'

# Create signature
SIGNATURE=$(echo -n "$PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | sed 's/^.* //')

echo "Sending HTTP webhook to $SERVER_URL/api/github/hook"

# Send HTTP request
curl -X POST "$SERVER_URL/api/github/hook" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-GitHub-Delivery: test-$(date +%s)" \
  -H "X-Hub-Signature-256: sha256=$SIGNATURE" \
  -d "$PAYLOAD" \
  -v

echo
echo "✅ HTTP webhook sent"
echo

# Test SQS (if LocalStack is available)
if curl -s http://localhost:4566 > /dev/null 2>&1; then
    echo "📮 Testing SQS Message..."
    echo
    
    # Create SQS message
    SQS_MESSAGE='{
      "event_type": "pull_request",
      "delivery_id": "sqs-test-'$(date +%s)'",
      "payload": '"$PAYLOAD"',
      "source": "sqs"
    }'
    
    echo "Sending SQS message to LocalStack..."
    
    # Send to SQS queue
    aws --endpoint-url=http://localhost:4566 sqs send-message \
      --queue-url "http://localhost:4566/000000000000/github-pull-request" \
      --message-body "$SQS_MESSAGE" \
      --region us-east-1 \
      --no-cli-pager 2>/dev/null || {
        echo "⚠️  Failed to send SQS message. Make sure:"
        echo "   1. LocalStack is running: docker run --rm -p 4566:4566 localstack/localstack"
        echo "   2. AWS CLI is installed"
        echo "   3. Queue exists (the server should create it automatically)"
    }
    
    echo "✅ SQS message sent"
else
    echo "⚠️  LocalStack not available at http://localhost:4566"
    echo "   Start with: docker run --rm -p 4566:4566 localstack/localstack"
fi

echo
echo "🔍 Check the server logs to see if events were processed!"
echo "   Look for messages like: 'Test handler received event'"
echo
