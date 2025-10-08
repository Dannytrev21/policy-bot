# End-to-End Testing Plan for Policy-Bot with Dual Event Processing

## Executive Summary

This document outlines a comprehensive end-to-end testing strategy for policy-bot that validates both HTTP webhook and SQS event processing paths. Using Tree of Thought methodology, we evaluated five testing approaches and selected a **Contract Testing with Simulated Events** strategy enhanced with multi-layer validation.

## Tree of Thought Analysis

### Evaluation Criteria (1-5 scale, 5 = best)
- **Realism**: How closely it simulates real GitHub behavior
- **Coverage**: How thoroughly it tests all paths
- **Maintainability**: How easy it is to maintain and update
- **Speed**: How quickly tests can run
- **Reliability**: How consistent and non-flaky are the tests
- **Cost**: Resource requirements and setup complexity

### Hypotheses Evaluated

#### Hypothesis 1: Full GitHub Mock Server
- **Approach**: Complete mock GitHub API server
- **Score**: 21/30
- **Verdict**: Rejected - Too complex to maintain, may drift from real GitHub

#### Hypothesis 2: Real GitHub with Test Repository
- **Approach**: Use actual GitHub repositories and real events
- **Score**: 17/30
- **Verdict**: Rejected as primary - Too slow and flaky, useful for smoke tests only

#### Hypothesis 3: Hybrid with Recorded Responses
- **Approach**: Recorded API responses + real smoke tests + LocalStack
- **Score**: 23/30
- **Verdict**: Partially adopted - Recording concept useful for specific scenarios

#### Hypothesis 4: Contract Testing with Simulated Events ✅
- **Approach**: Test event processing with crafted events + LocalStack
- **Score**: 26/30
- **Verdict**: **Selected** - Best balance of coverage, speed, and maintainability

#### Hypothesis 5: Multi-Layer Testing Strategy
- **Approach**: Different test types for different layers
- **Score**: 24/30
- **Verdict**: Partially adopted - Layer separation concept incorporated

### Selected Approach: Contract Testing with Multi-Layer Validation

The optimal approach combines:
1. **Contract testing** with carefully crafted GitHub events
2. **LocalStack** for realistic SQS queue behavior
3. **Multi-layer validation** for comprehensive coverage
4. **Optional real GitHub smoke tests** for critical paths

---

## Testing Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   Test Orchestrator                       │
│                 (Go test framework)                       │
└─────────────┬──────────────────────┬────────────────────┘
              │                      │
    ┌─────────▼─────────┐  ┌────────▼─────────┐
    │  Event Generator  │  │  Event Validator  │
    │  - HTTP webhooks  │  │  - Status checks  │
    │  - SQS messages   │  │  - Metrics        │
    └─────────┬─────────┘  │  - Event routing  │
              │            └──────────────────┘
    ┌─────────▼──────────────────────┐
    │       Policy-Bot Server         │
    │   (Running in test mode)        │
    ├─────────────────────────────────┤
    │ • HTTP webhook endpoint         │
    │ • SQS consumer workers          │
    │ • Per-environment routing       │
    └────────┬──────────┬────────────┘
             │          │
    ┌────────▼───┐  ┌───▼──────────┐
    │ LocalStack │  │ Mock GitHub   │
    │    SQS     │  │     API       │
    └────────────┘  └──────────────┘
```

---

## Implementation Phases

### Phase 1: Test Infrastructure Setup

#### Design
Create the foundational test infrastructure including LocalStack integration, event generators, and test harness.

#### Implementation Tasks

1. **LocalStack Integration Enhancement** (`test/e2e_helpers.go`)
```go
package test

import (
    "context"
    "testing"
    "time"

    "github.com/aws/aws-sdk-go-v2/service/sqs"
    "github.com/palantir/policy-bot/server"
)

type E2ETestEnvironment struct {
    t              *testing.T
    localStack     *LocalStackManager
    server         *server.Server
    eventGenerator *EventGenerator
    validator      *EventValidator
    config         *E2EConfig
}

type E2EConfig struct {
    // Server configuration
    ServerPort     int
    WebhookSecret  string

    // SQS configuration
    SQSEnabled     bool
    UseLocalStack  bool

    // Event routing configuration
    EventRouting   map[string]map[string]string // environment -> event -> strategy

    // Test configuration
    TestTimeout    time.Duration
    ParallelTests  bool
}

func NewE2EEnvironment(t *testing.T, config *E2EConfig) *E2ETestEnvironment {
    // Initialize LocalStack if SQS testing is enabled
    // Start policy-bot server with test configuration
    // Initialize event generators and validators
}
```

2. **Event Generator** (`test/event_generator.go`)
```go
type EventGenerator struct {
    webhookSecret string
    serverURL     string
    sqsClient     *sqs.Client
    queueURLs     map[string]string
}

// Generate various GitHub event types
func (g *EventGenerator) GeneratePullRequestEvent(opts PullRequestOptions) *GitHubEvent {
    return &GitHubEvent{
        Type:       "pull_request",
        DeliveryID: generateUUID(),
        Headers:    g.buildHeaders(opts.Environment),
        Payload:    g.buildPullRequestPayload(opts),
    }
}

// Send events via HTTP webhook
func (g *EventGenerator) SendWebhook(event *GitHubEvent) error {
    signature := g.calculateSignature(event.Payload)
    // Send HTTP POST to webhook endpoint
}

// Send events via SQS
func (g *EventGenerator) SendSQSMessage(event *GitHubEvent, queueName string) error {
    message := g.buildSQSMessage(event)
    // Send to appropriate SQS queue
}
```

3. **Event Validator** (`test/event_validator.go`)
```go
type EventValidator struct {
    receivedEvents *sync.Map
    metrics        *MetricsCollector
}

func (v *EventValidator) ValidateEventProcessed(
    deliveryID string,
    expectedSource string,
    timeout time.Duration,
) error {
    // Wait for event to be processed
    // Verify it was processed by the expected source (HTTP/SQS)
    // Check metrics and status updates
}

func (v *EventValidator) ValidateRouting(
    event *GitHubEvent,
    expectedStrategy string,
) error {
    // Verify event was routed according to strategy (http/sqs/both)
}
```

#### Acceptance Criteria
- [ ] LocalStack starts automatically for SQS tests
- [ ] Test server starts with configurable routing
- [ ] Event generator creates valid GitHub events
- [ ] Event validator tracks processing accurately
- [ ] Test environment cleanup is reliable

---

### Phase 2: Core Event Processing Tests

#### Design
Test all GitHub event types through both HTTP and SQS paths with various routing configurations.

#### Implementation Tasks

1. **Basic Event Processing** (`test/e2e/basic_events_test.go`)
```go
func TestBasicEventProcessing(t *testing.T) {
    tests := []struct {
        name        string
        eventType   string
        environment string
        routing     string
        testFunc    func(*testing.T, *E2ETestEnvironment)
    }{
        {
            name:        "Cloud PR via SQS only",
            eventType:   "pull_request",
            environment: "cloud",
            routing:     "sqs",
            testFunc:    testCloudPRViaSQS,
        },
        {
            name:        "Enterprise PR via HTTP only",
            eventType:   "pull_request",
            environment: "enterprise",
            routing:     "http",
            testFunc:    testEnterprisePRViaHTTP,
        },
        {
            name:        "Cloud Status via both paths",
            eventType:   "status",
            environment: "cloud",
            routing:     "both",
            testFunc:    testCloudStatusViaBoth,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            env := setupTestEnvironment(t, tt.routing)
            defer env.Cleanup()

            tt.testFunc(t, env)
        })
    }
}

func testCloudPRViaSQS(t *testing.T, env *E2ETestEnvironment) {
    // Generate PR event
    event := env.eventGenerator.GeneratePullRequestEvent(PullRequestOptions{
        Environment: "cloud",
        Action:      "opened",
        Number:      123,
    })

    // Send via SQS
    err := env.eventGenerator.SendSQSMessage(event, "pull_request")
    require.NoError(t, err)

    // Validate processing
    err = env.validator.ValidateEventProcessed(
        event.DeliveryID,
        "sqs",
        10*time.Second,
    )
    require.NoError(t, err)
}
```

2. **Routing Validation** (`test/e2e/routing_test.go`)
```go
func TestEventRouting(t *testing.T) {
    // Test that events are routed correctly based on configuration
    testCases := []struct {
        name            string
        config          map[string]map[string]string
        sendMethod      string // "http", "sqs", "both"
        expectedSources []string
    }{
        {
            name: "HTTP-only routing blocks SQS",
            config: map[string]map[string]string{
                "cloud": {"pull_request": "http"},
            },
            sendMethod:      "both",
            expectedSources: []string{"http"},
        },
        {
            name: "SQS-only routing blocks HTTP",
            config: map[string]map[string]string{
                "cloud": {"pull_request": "sqs"},
            },
            sendMethod:      "both",
            expectedSources: []string{"sqs"},
        },
        {
            name: "Both routing processes twice",
            config: map[string]map[string]string{
                "cloud": {"pull_request": "both"},
            },
            sendMethod:      "both",
            expectedSources: []string{"http", "sqs"},
        },
    }
}
```

3. **Error Handling** (`test/e2e/error_handling_test.go`)
```go
func TestErrorHandling(t *testing.T) {
    // Test SQS retry logic
    t.Run("SQS retry on failure", func(t *testing.T) {
        // Send malformed message
        // Verify retry attempts
        // Verify DLQ if configured
    })

    // Test queue unavailability
    t.Run("Queue unavailable fallback", func(t *testing.T) {
        // Stop LocalStack
        // Send SQS message
        // Verify graceful handling
    })

    // Test webhook signature validation
    t.Run("Invalid webhook signature", func(t *testing.T) {
        // Send webhook with bad signature
        // Verify rejection
    })
}
```

#### Acceptance Criteria
- [ ] All event types tested (PR, status, review, etc.)
- [ ] Cloud events route correctly
- [ ] Enterprise events use HTTP only
- [ ] "Both" routing creates two processing events
- [ ] Error scenarios handled gracefully

---

### Phase 3: Performance and Load Testing

#### Design
Validate system performance under load with concurrent events from both sources.

#### Implementation Tasks

1. **Load Generation** (`test/e2e/load_test.go`)
```go
func TestHighVolumeEvents(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping load test in short mode")
    }

    env := setupTestEnvironment(t, "both")
    defer env.Cleanup()

    // Generate 1000 events
    events := generateMixedEvents(1000)

    // Send concurrently via both paths
    var wg sync.WaitGroup
    for _, event := range events {
        wg.Add(1)
        go func(e *GitHubEvent) {
            defer wg.Done()

            if e.Source == "http" {
                env.eventGenerator.SendWebhook(e)
            } else {
                env.eventGenerator.SendSQSMessage(e, e.Type)
            }
        }(event)
    }

    // Wait for all events to be sent
    wg.Wait()

    // Validate all processed within SLA
    for _, event := range events {
        err := env.validator.ValidateEventProcessed(
            event.DeliveryID,
            event.Source,
            30*time.Second,
        )
        require.NoError(t, err)
    }

    // Check metrics
    metrics := env.validator.GetMetrics()
    assert.Less(t, metrics.P99Latency, 1*time.Second)
    assert.Less(t, metrics.ErrorRate, 0.001) // <0.1% errors
}
```

2. **Queue Depth Testing** (`test/e2e/queue_depth_test.go`)
```go
func TestQueueDepthHandling(t *testing.T) {
    // Flood specific queue
    // Monitor processing rate
    // Verify no message loss
    // Check DLQ for failures
}
```

3. **Worker Allocation** (`test/e2e/worker_allocation_test.go`)
```go
func TestWorkerAllocation(t *testing.T) {
    config := &E2EConfig{
        QueueWorkers: map[string]int{
            "status":       15, // High volume
            "pull_request": 5,  // Medium volume
            "installation": 1,  // Low volume
        },
    }

    // Send events to each queue
    // Verify processing rates match worker allocation
}
```

#### Acceptance Criteria
- [ ] System handles 1000+ events/minute
- [ ] P99 latency under 1 second
- [ ] No message loss under load
- [ ] Worker pools scale appropriately
- [ ] Memory usage stable under load

---

### Phase 4: Integration Scenarios

#### Design
Test realistic scenarios that involve multiple events and complex workflows.

#### Implementation Tasks

1. **PR Lifecycle Test** (`test/e2e/scenarios/pr_lifecycle_test.go`)
```go
func TestPullRequestLifecycle(t *testing.T) {
    // Simulate complete PR lifecycle

    // 1. PR opened (via HTTP)
    prEvent := generatePREvent("opened")
    sendWebhook(prEvent)

    // 2. Status checks (via SQS)
    statusEvent := generateStatusEvent(prEvent.Number)
    sendSQSMessage(statusEvent)

    // 3. Review submitted (via SQS)
    reviewEvent := generateReviewEvent(prEvent.Number)
    sendSQSMessage(reviewEvent)

    // 4. PR synchronized (via HTTP)
    syncEvent := generatePREvent("synchronized")
    sendWebhook(syncEvent)

    // Validate entire flow processed correctly
    validatePRLifecycle(prEvent.Number)
}
```

2. **Cloud to Enterprise Migration** (`test/e2e/scenarios/migration_test.go`)
```go
func TestCloudToEnterpriseMigration(t *testing.T) {
    // Start with cloud configuration
    env := setupCloudEnvironment(t)

    // Send cloud events via SQS
    sendCloudEvents(env)

    // Reconfigure for enterprise (HTTP only)
    env.ReconfigureForEnterprise()

    // Send enterprise events via HTTP
    sendEnterpriseEvents(env)

    // Validate both processed correctly
}
```

3. **Failover Scenarios** (`test/e2e/scenarios/failover_test.go`)
```go
func TestSQSFailoverToHTTP(t *testing.T) {
    // Configure "both" routing
    // Stop SQS processing
    // Verify HTTP continues working
    // Restart SQS
    // Verify both paths resume
}
```

#### Acceptance Criteria
- [ ] Complete PR lifecycle works correctly
- [ ] Cloud and enterprise events isolated
- [ ] Failover scenarios handled gracefully
- [ ] Configuration changes take effect
- [ ] No event loss during transitions

---

### Phase 5: Real GitHub Validation (Optional)

#### Design
Limited smoke tests with real GitHub to validate critical integration points.

#### Implementation Tasks

1. **GitHub App Setup** (`test/e2e/real_github/setup.sh`)
```bash
#!/bin/bash
# Setup script for real GitHub testing

# 1. Create test repository
gh repo create policy-bot-e2e-test --private

# 2. Install policy-bot app
echo "Install the policy-bot GitHub App on the test repository"
echo "App URL: ${GITHUB_APP_URL}"

# 3. Configure webhooks
echo "Webhook URL: ${WEBHOOK_URL}"

# 4. Create test policy
cat > .policy.yml << EOF
policy:
  approval:
    - rule: test-approval
      requires:
        count: 1
EOF

git add .policy.yml
git commit -m "Add test policy"
git push
```

2. **Real Event Test** (`test/e2e/real_github/smoke_test.go`)
```go
// +build real_github

func TestRealGitHubIntegration(t *testing.T) {
    if os.Getenv("ENABLE_REAL_GITHUB_TESTS") != "true" {
        t.Skip("Real GitHub tests disabled")
    }

    // Create real PR
    pr := createRealPullRequest(t)
    defer cleanupPullRequest(pr)

    // Wait for webhook
    waitForWebhookEvent(pr.Number, 30*time.Second)

    // Verify status check posted
    status := getStatusCheck(pr.HeadSHA)
    assert.Equal(t, "policy-bot", status.Context)
}
```

3. **GitHub Actions Integration** (`.github/workflows/e2e-real.yml`)
```yaml
name: Real GitHub E2E Tests

on:
  schedule:
    - cron: '0 0 * * 0' # Weekly
  workflow_dispatch:

jobs:
  real-github-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Setup Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Run real GitHub tests
        env:
          ENABLE_REAL_GITHUB_TESTS: true
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          GITHUB_APP_ID: ${{ secrets.APP_ID }}
          GITHUB_APP_KEY: ${{ secrets.APP_KEY }}
        run: |
          go test -tags=real_github ./test/e2e/real_github/...
```

#### Acceptance Criteria
- [ ] Test repository created successfully
- [ ] GitHub App installed and configured
- [ ] Real webhooks received and processed
- [ ] Status checks posted to GitHub
- [ ] Cleanup removes all test artifacts

---

### Phase 6: Continuous Testing & Monitoring

#### Design
Establish continuous testing pipeline and production monitoring.

#### Implementation Tasks

1. **CI/CD Integration** (`.github/workflows/e2e.yml`)
```yaml
name: E2E Tests

on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main, develop]

jobs:
  e2e-tests:
    runs-on: ubuntu-latest
    services:
      localstack:
        image: localstack/localstack
        ports:
          - 4566:4566
        env:
          SERVICES: sqs

    steps:
      - uses: actions/checkout@v3

      - name: Setup Go
        uses: actions/setup-go@v4

      - name: Run E2E tests
        run: |
          make test-e2e

      - name: Upload test results
        if: always()
        uses: actions/upload-artifact@v3
        with:
          name: e2e-test-results
          path: test-results/
```

2. **Test Report Generation** (`test/e2e/reporting.go`)
```go
type TestReport struct {
    StartTime    time.Time
    EndTime      time.Time
    TotalEvents  int
    SuccessRate  float64
    P50Latency   time.Duration
    P99Latency   time.Duration
    FailedEvents []FailedEvent
}

func GenerateTestReport(results []TestResult) *TestReport {
    // Aggregate test results
    // Calculate metrics
    // Generate HTML/JSON report
}
```

3. **Production Validation** (`test/e2e/production/canary.go`)
```go
func RunCanaryTests(config *CanaryConfig) error {
    // Send synthetic events to production
    // Verify processing within SLA
    // Alert on failures

    event := generateCanaryEvent()

    // Send via configured path
    if config.UseSQS {
        sendToProductionSQS(event)
    } else {
        sendToProductionWebhook(event)
    }

    // Validate processing
    if !waitForProcessing(event.ID, config.Timeout) {
        return fmt.Errorf("canary event %s not processed", event.ID)
    }

    return nil
}
```

#### Acceptance Criteria
- [ ] E2E tests run on every PR
- [ ] Test results tracked over time
- [ ] Failures trigger alerts
- [ ] Canary tests run in production
- [ ] Performance trends monitored

---

## Test Execution Plan

### Local Development
```bash
# Run all E2E tests with LocalStack
make test-e2e

# Run specific test suite
go test ./test/e2e/basic_events_test.go

# Run with verbose output
go test -v ./test/e2e/...

# Run load tests
go test -run TestHighVolumeEvents ./test/e2e/load_test.go
```

### CI/CD Pipeline
```bash
# GitHub Actions automatically runs on PR
# Includes LocalStack setup
# Generates test reports
# Posts results to PR
```

### Production Validation
```bash
# Canary tests run every 5 minutes
# Synthetic events sent to production
# Alerts on processing failures
```

---

## Limitations and Mitigations

### Limitation 1: Cannot Fully Replicate GitHub
**Impact**: Some GitHub-specific behaviors may not be caught
**Mitigation**:
- Regular real GitHub smoke tests
- Monitor production for unexpected behaviors
- Maintain test event library from real events

### Limitation 2: LocalStack vs Real SQS
**Impact**: LocalStack may behave differently than AWS SQS
**Mitigation**:
- Periodic testing against real AWS SQS in staging
- Monitor LocalStack updates for breaking changes
- Maintain abstraction layer for queue operations

### Limitation 3: Test Data Management
**Impact**: Complex test scenarios require extensive test data
**Mitigation**:
- Test data factory patterns
- Reusable event generators
- Clear test data cleanup

### Limitation 4: Network and Timing Issues
**Impact**: Tests may be flaky due to timing
**Mitigation**:
- Generous timeouts with early success exits
- Retry logic for transient failures
- Proper synchronization primitives

---

## Success Metrics

### Test Coverage
- **Target**: >90% code coverage for event processing paths
- **Measurement**: Go coverage reports

### Test Reliability
- **Target**: <1% flaky test rate
- **Measurement**: CI failure analysis over 30 days

### Test Speed
- **Target**: Full E2E suite under 5 minutes
- **Measurement**: CI execution time

### Bug Detection
- **Target**: >80% of bugs caught before production
- **Measurement**: Bug tracking and root cause analysis

### Test Maintenance
- **Target**: <10% of development time on test maintenance
- **Measurement**: Time tracking on test-related tasks

---

## Next Steps

1. **Week 1**: Implement Phase 1 (Test Infrastructure)
2. **Week 2**: Implement Phase 2 (Core Event Tests)
3. **Week 3**: Implement Phase 3 (Performance Tests)
4. **Week 4**: Implement Phase 4 (Integration Scenarios)
5. **Week 5**: Setup CI/CD and monitoring
6. **Ongoing**: Maintain and enhance test suite

---

## Appendix A: Test Event Examples

### Pull Request Event (Cloud via SQS)
```json
{
  "event_type": "pull_request",
  "delivery_id": "12345-67890-abcdef",
  "headers": {
    "Host": "ghec.example.com",
    "X-GitHub-Event": "pull_request",
    "X-GitHub-Delivery": "12345-67890-abcdef"
  },
  "payload": {
    "action": "opened",
    "number": 123,
    "pull_request": {
      "id": 456,
      "title": "Test PR",
      "head": {
        "sha": "abc123"
      }
    }
  }
}
```

### Status Event (Enterprise via HTTP)
```http
POST /api/github/hook HTTP/1.1
Host: policy-bot.example.com
X-GitHub-Event: status
X-GitHub-Delivery: 98765-43210-fedcba
X-GitHub-Enterprise-Host: github.enterprise.com
X-Hub-Signature-256: sha256=...

{
  "sha": "def456",
  "state": "success",
  "description": "Tests passed",
  "context": "continuous-integration"
}
```

---

## Appendix B: LocalStack Configuration

### Docker Compose Setup
```yaml
version: '3.8'
services:
  localstack:
    image: localstack/localstack:latest
    ports:
      - "4566:4566"
    environment:
      - SERVICES=sqs
      - DEBUG=1
      - DATA_DIR=/tmp/localstack/data
    volumes:
      - localstack-data:/tmp/localstack

volumes:
  localstack-data:
```

### Queue Creation Script
```bash
#!/bin/bash
# Create required SQS queues in LocalStack

QUEUES=(
  "github-pull-request"
  "github-status"
  "github-pull-request-review"
  "github-issue-comment"
  "github-check-run"
  "github-workflow-run"
  "github-merge-group"
)

for queue in "${QUEUES[@]}"; do
  aws --endpoint-url=http://localhost:4566 \
      sqs create-queue \
      --queue-name "$queue" \
      --region us-east-1

  # Create DLQ
  aws --endpoint-url=http://localhost:4566 \
      sqs create-queue \
      --queue-name "${queue}-dlq" \
      --region us-east-1
done
```

---

## Conclusion

This comprehensive E2E testing plan provides a robust framework for validating policy-bot's dual event processing capabilities. By combining simulated events with LocalStack for SQS testing and optional real GitHub validation, we achieve excellent test coverage while maintaining fast, reliable test execution.

The multi-layered approach ensures that:
1. **All event paths are thoroughly tested** (HTTP and SQS)
2. **Cloud and enterprise routing works correctly**
3. **Performance meets requirements** under load
4. **Integration scenarios reflect real usage**
5. **Tests are maintainable** and easy to extend

With this testing strategy, teams can confidently deploy changes knowing that both webhook and SQS event processing paths are properly validated.