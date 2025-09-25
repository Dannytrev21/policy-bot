#!/bin/bash

# Demonstration of per-queue worker allocation
# This script shows how to configure different worker counts for different event types

echo "🏗️  Per-Queue Worker Allocation Demo"
echo "===================================="
echo

cat << 'EOF'
Configuration Example:
----------------------

sqs:
  enabled: true
  region: "us-east-1"
  
  # All 6 event handler queues
  queues:
    installation: "https://sqs.us-east-1.amazonaws.com/123/github-installation"
    pull_request: "https://sqs.us-east-1.amazonaws.com/123/github-pull-request"
    pull_request_review: "https://sqs.us-east-1.amazonaws.com/123/github-pull-request-review"
    issue_comment: "https://sqs.us-east-1.amazonaws.com/123/github-issue-comment"
    status: "https://sqs.us-east-1.amazonaws.com/123/github-status"
    check_run: "https://sqs.us-east-1.amazonaws.com/123/github-check-run"
  
  # Default worker count (fallback)
  workers_per_queue: 5
  
  # Per-queue worker allocation based on event volume
  queue_workers:
    installation: 2       # Low volume - app installs are rare
    pull_request: 8       # Medium-high volume - core functionality
    pull_request_review: 5  # Medium volume - reviews are common
    issue_comment: 6      # Medium volume - comments can be bursty
    status: 15            # HIGH volume - status checks fire for every commit
    check_run: 10         # High volume - CI/CD systems generate many check runs

Total Workers: 46 (vs 30 with uniform allocation)

Worker Allocation Benefits:
--------------------------
✅ High-volume queues (status) get more workers → better throughput
✅ Low-volume queues (installation) use fewer resources
✅ Optimized resource utilization based on actual usage patterns
✅ Can be adjusted per organization's specific event patterns

Event Volume Patterns:
---------------------
📈 VERY HIGH: status (every commit triggers multiple status checks)
📊 HIGH: check_run (CI/CD pipelines, external integrations)
📈 MEDIUM-HIGH: pull_request (core workflow events)
📊 MEDIUM: pull_request_review, issue_comment (user interactions)
📉 LOW: installation (rare app installation events)

Monitoring and Tuning:
---------------------
Monitor these CloudWatch metrics per queue:
- ApproximateNumberOfMessages (queue depth)
- ApproximateAgeOfOldestMessage (latency)
- NumberOfMessagesReceived (throughput)

Increase workers if:
- Queue depth consistently > 0
- Message age > visibility_timeout
- Processing latency is high

Example Monitoring Commands:
---------------------------
# Check queue depths
aws sqs get-queue-attributes \
  --queue-url "https://sqs.us-east-1.amazonaws.com/123/github-status" \
  --attribute-names ApproximateNumberOfMessages

# List all your queues
aws sqs list-queues --queue-name-prefix "github-"

EOF

echo "💡 Tips:"
echo "   - Start with recommended values above"
echo "   - Monitor queue metrics for 1-2 weeks"
echo "   - Adjust worker counts based on observed patterns"
echo "   - Status queues typically need 2-3x more workers than others"
echo
