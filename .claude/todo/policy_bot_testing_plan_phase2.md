# Phase 2: Integration & Authentication Tests (Sprint 2)

## Overview
**Duration**: Week 2
**Objective**: Validate end-to-end authentication flows and SQS consumer lifecycle
**Success Criteria**: Integration tests passing with LocalStack, authentication validated for both HTTP and SQS paths

## Prerequisites
- [ ] Phase 1 unit tests completed and passing
- [ ] Install LocalStack: `pip install localstack`
- [ ] Install AWS CLI: `brew install awscli`
- [ ] Docker installed for LocalStack container
- [ ] Create test fixtures directory: `test/fixtures/`

## LocalStack Setup

### Task LS-01: Configure LocalStack Environment
- [ ] Create `test/localstack/docker-compose.yml`

```yaml
version: '3.8'
services:
  localstack:
    image: localstack/localstack:latest
    ports:
      - "4566:4566"
      - "4571:4571"
    environment:
      - SERVICES=sqs,cloudwatch
      - DEBUG=1
      - DATA_DIR=/tmp/localstack/data
      - DOCKER_HOST=unix:///var/run/docker.sock
    volumes:
      - "${TMPDIR:-/tmp}/localstack:/tmp/localstack"
      - "/var/run/docker.sock:/var/run/docker.sock"
```

### Task LS-02: Create LocalStack Helper Functions
- [ ] Create file: `test/localstack_helpers.go`

```go
package test

import (
    "context"
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/sqs"
)

func SetupLocalStackSQS(t *testing.T) (*sqs.Client, map[string]string) {
    cfg, err := config.LoadDefaultConfig(context.Background(),
        config.WithRegion("us-east-1"),
        config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
            func(service, region string, options ...interface{}) (aws.Endpoint, error) {
                return aws.Endpoint{
                    URL: "http://localhost:4566",
                }, nil
            },
        )),
    )
    require.NoError(t, err)

    client := sqs.NewFromConfig(cfg)

    // Create test queues
    queues := map[string]string{
        "pull_request": createQueue(t, client, "codegenie-car-policy-pr"),
        "status":       createQueue(t, client, "codegenie-car-policy-status"),
        "review":       createQueue(t, client, "codegenie-car-policy-review"),
    }

    return client, queues
}

func createQueue(t *testing.T, client *sqs.Client, name string) string {
    result, err := client.CreateQueue(context.Background(), &sqs.CreateQueueInput{
        QueueName: aws.String(name),
        Attributes: map[string]string{
            "VisibilityTimeout": "30",
            "MessageRetentionPeriod": "3600",
        },
    })
    require.NoError(t, err)
    return *result.QueueUrl
}
```

## Task Checklist

### 1. End-to-End Authentication Flow Tests

#### Task AUTH-INT-01: Create Authentication Integration Test File
- [ ] Create file: `test/auth_integration_test.go`
- [ ] Set up mock GitHub server
- [ ] Create test GitHub App credentials

#### Task AUTH-INT-02: Test HTTP Webhook to GitHub API Authentication
- [ ] **PRIMARY TEST FOR Q1 (HTTP Path)**
- [ ] Set up test server with mock handlers
- [ ] Send webhook with valid installation ID
- [ ] Verify GitHub API authentication token created
- [ ] Confirm API call succeeds with token

**Implementation Template**:
```go
func TestIntegration_HTTPWebhook_AuthenticatesWithGitHub(t *testing.T) {
    // Setup mock GitHub API server
    githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify authentication header
        authHeader := r.Header.Get("Authorization")
        assert.NotEmpty(t, authHeader)
        assert.Contains(t, authHeader, "Bearer")

        switch r.URL.Path {
        case "/app/installations/12345/access_tokens":
            // Installation token request
            w.WriteHeader(http.StatusCreated)
            json.NewEncoder(w).Encode(map[string]interface{}{
                "token": "test-installation-token",
                "expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
            })
        case "/repos/test-owner/test-repo/pulls/1":
            // PR API call with installation token
            assert.Equal(t, "token test-installation-token", authHeader)
            w.WriteHeader(http.StatusOK)
            json.NewEncoder(w).Encode(github.PullRequest{
                Number: github.Int(1),
                State:  github.String("open"),
            })
        default:
            t.Errorf("Unexpected API call: %s", r.URL.Path)
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    defer githubServer.Close()

    // Configure test server with mock GitHub URL
    config := &server.Config{
        GithubCloud: githubapp.Config{
            V3APIURL: githubServer.URL,
            App: githubapp.App{
                IntegrationID: 1,
                PrivateKey:    testPrivateKey, // Test RSA key
            },
        },
    }

    // Create and start server
    srv, err := server.New(config)
    require.NoError(t, err)

    // Send webhook
    webhook := createTestWebhook("pull_request", 12345)
    resp := sendWebhook(srv, webhook)

    assert.Equal(t, http.StatusOK, resp.StatusCode)
    // Verify authentication flow completed
}
```

#### Task AUTH-INT-03: Test SQS Message to GitHub API Authentication
- [ ] **PRIMARY TEST FOR Q1 (SQS Path)** - Critical for answering question
- [ ] Send SQS message with GitHub webhook payload
- [ ] Verify processor extracts installation ID
- [ ] Confirm authentication token created
- [ ] Validate GitHub API call with token

**Implementation Template**:
```go
func TestIntegration_SQSMessage_AuthenticatesWithGitHub(t *testing.T) {
    // Setup LocalStack SQS
    sqsClient, queues := SetupLocalStackSQS(t)

    // Setup mock GitHub API server (same as above)
    githubServer := setupMockGitHubServer(t)
    defer githubServer.Close()

    // Create processor with test configuration
    processor := createTestProcessor(githubServer.URL)

    // Create SQS message with GitHub webhook payload
    sqsMessage := SQSMessage{
        EventType:  "pull_request",
        DeliveryID: "test-delivery-123",
        Headers: map[string]interface{}{
            "Host": "api.ghec.github.com",
        },
        Payload: json.RawMessage(`{
            "action": "opened",
            "installation": {"id": 12345},
            "pull_request": {
                "number": 1,
                "head": {"sha": "abc123"}
            },
            "repository": {
                "owner": {"login": "test-owner"},
                "name": "test-repo"
            }
        }`),
    }

    // Send message to SQS
    messageBody, _ := json.Marshal(sqsMessage)
    _, err := sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
        QueueUrl:    aws.String(queues["pull_request"]),
        MessageBody: aws.String(string(messageBody)),
    })
    require.NoError(t, err)

    // Process message
    messages, err := sqsClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
        QueueUrl: aws.String(queues["pull_request"]),
    })
    require.NoError(t, err)
    require.Len(t, messages.Messages, 1)

    // Process through processor
    err = processor.ProcessMessage(
        context.Background(),
        "pull_request",
        queues["pull_request"],
        messages.Messages[0],
    )
    require.NoError(t, err)

    // Verify GitHub API was called with authentication
    // The mock server validates the token was used
}
```

#### Task AUTH-INT-04: Test Cloud vs Enterprise Credential Isolation
- [ ] Send cloud message (with "ghec" in host)
- [ ] Verify cloud app credentials used
- [ ] Send enterprise message
- [ ] Verify enterprise app credentials used
- [ ] Confirm no credential mixing

### 2. SQS Consumer Lifecycle Tests

#### Task CONSUMER-INT-01: Create Consumer Integration Test File
- [ ] Create file: `test/sqs_consumer_integration_test.go`
- [ ] Set up consumer test helpers
- [ ] Create message generation utilities

#### Task CONSUMER-INT-02: Test Complete Consumer Lifecycle
- [ ] Start consumer with LocalStack queues
- [ ] Verify workers begin polling
- [ ] Send test messages
- [ ] Confirm message processing and deletion
- [ ] Test graceful shutdown

**Implementation Template**:
```go
func TestIntegration_Consumer_CompleteLifecycle(t *testing.T) {
    // Setup LocalStack
    sqsClient, queues := SetupLocalStackSQS(t)

    // Create test handlers that track invocations
    handlerCalls := &sync.Map{}
    testHandler := createTrackingHandler(handlerCalls)

    // Configure consumer
    config := &sqsconsumer.Config{
        Enabled:         true,
        Region:          "us-east-1",
        EndpointURL:     "http://localhost:4566",
        Queues:          queues,
        WorkersPerQueue: 2,
        WaitTimeSeconds: 1, // Short for testing
    }

    // Create consumer
    consumer, err := sqsconsumer.New(
        config,
        []githubapp.EventHandler{testHandler},
        []githubapp.EventHandler{testHandler},
        createTestScheduler(),
        createTestScheduler(),
        zerolog.New(nil),
        nil,
    )
    require.NoError(t, err)

    // Start consumer
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    err = consumer.Start(ctx)
    require.NoError(t, err)

    // Send messages to each queue
    for eventType, queueURL := range queues {
        message := createTestMessage(eventType)
        _, err := sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
            QueueUrl:    aws.String(queueURL),
            MessageBody: aws.String(message),
        })
        require.NoError(t, err)
    }

    // Wait for processing
    time.Sleep(2 * time.Second)

    // Verify all messages processed
    processedCount := 0
    handlerCalls.Range(func(key, value interface{}) bool {
        processedCount++
        return true
    })
    assert.Equal(t, len(queues), processedCount)

    // Test graceful shutdown
    shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer shutdownCancel()

    err = consumer.Stop(shutdownCtx)
    assert.NoError(t, err)

    // Verify no goroutine leaks
    goleak.VerifyNone(t)
}
```

#### Task CONSUMER-INT-03: Test Multi-Queue Processing
- [ ] Configure consumer with 3+ queues
- [ ] Send different event types to each queue
- [ ] Verify correct handler invoked per event type
- [ ] Confirm queue isolation (no cross-contamination)

#### Task CONSUMER-INT-04: Test Retry Mechanism
- [ ] Configure retry with exponential backoff
- [ ] Force handler failure on first attempts
- [ ] Verify message requeued with correct delay
- [ ] Confirm success on final attempt
- [ ] Validate message deletion after success

**Implementation Template**:
```go
func TestIntegration_Consumer_RetryMechanism(t *testing.T) {
    sqsClient, queues := SetupLocalStackSQS(t)

    // Handler that fails first 2 attempts
    attemptCounter := make(map[string]int)
    var mu sync.Mutex

    retryHandler := &MockEventHandler{}
    retryHandler.On("Handles").Return([]string{"pull_request"})
    retryHandler.On("Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
        Return(func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
            mu.Lock()
            defer mu.Unlock()

            attemptCounter[deliveryID]++
            if attemptCounter[deliveryID] < 3 {
                return errors.New("simulated failure")
            }
            return nil // Success on 3rd attempt
        })

    // Configure with retry enabled
    config := &sqsconsumer.Config{
        Enabled:     true,
        EnableRetry: true,
        MaxRetries:  3,
        Queues:      queues,
    }

    // ... consumer setup and testing ...

    // Verify retry delays are exponential
    // 1st retry: 1s, 2nd retry: 4s, 3rd attempt: success
}
```

### 3. Scheduler Performance Comparison

#### Task SCHED-INT-01: Create Scheduler Benchmark Test
- [ ] **PRIMARY TEST FOR Q2** - Quantifies scheduler value
- [ ] Create file: `test/scheduler_performance_test.go`
- [ ] Implement direct invocation benchmark
- [ ] Implement scheduler invocation benchmark
- [ ] Compare metrics and overhead

**Implementation Template**:
```go
func TestIntegration_Scheduler_PerformanceComparison(t *testing.T) {
    // Test configuration
    numEvents := 1000
    handler := createTestHandler()

    // Benchmark 1: Direct invocation (no scheduler)
    directStart := time.Now()
    directErrors := 0

    for i := 0; i < numEvents; i++ {
        err := handler.Handle(context.Background(), "test", fmt.Sprintf("id-%d", i), []byte("{}"))
        if err != nil {
            directErrors++
        }
    }
    directDuration := time.Since(directStart)

    // Benchmark 2: With scheduler
    scheduler := githubapp.QueueAsyncScheduler(100, 10) // queue=100, workers=10

    schedulerStart := time.Now()
    schedulerErrors := 0
    var wg sync.WaitGroup

    for i := 0; i < numEvents; i++ {
        wg.Add(1)
        dispatch := githubapp.Dispatch{
            Handler:    handler,
            EventType:  "test",
            DeliveryID: fmt.Sprintf("id-%d", i),
            Payload:    []byte("{}"),
        }

        go func() {
            defer wg.Done()
            if err := scheduler.Schedule(context.Background(), dispatch); err != nil {
                schedulerErrors++
            }
        }()
    }

    wg.Wait()
    schedulerDuration := time.Since(schedulerStart)

    // Calculate overhead
    overhead := float64(schedulerDuration-directDuration) / float64(directDuration) * 100

    // Generate report
    t.Logf("Performance Comparison Report:")
    t.Logf("================================")
    t.Logf("Direct Invocation:")
    t.Logf("  Duration: %v", directDuration)
    t.Logf("  Throughput: %.2f events/sec", float64(numEvents)/directDuration.Seconds())
    t.Logf("  Errors: %d", directErrors)
    t.Logf("")
    t.Logf("Scheduler Invocation:")
    t.Logf("  Duration: %v", schedulerDuration)
    t.Logf("  Throughput: %.2f events/sec", float64(numEvents)/schedulerDuration.Seconds())
    t.Logf("  Errors: %d", schedulerErrors)
    t.Logf("")
    t.Logf("Overhead: %.2f%%", overhead)

    // Assert acceptable overhead (20% max)
    assert.LessOrEqual(t, overhead, 20.0, "Scheduler overhead exceeds 20%")

    // Test memory with scheduler (prevents OOM)
    testMemoryUnderLoad(t, scheduler)
}

func testMemoryUnderLoad(t *testing.T, scheduler githubapp.Scheduler) {
    var m runtime.MemStats

    // Baseline memory
    runtime.GC()
    runtime.ReadMemStats(&m)
    baselineMemory := m.Alloc

    // Send 10000 events rapidly
    for i := 0; i < 10000; i++ {
        dispatch := githubapp.Dispatch{
            Handler:    createTestHandler(),
            EventType:  "test",
            DeliveryID: fmt.Sprintf("load-%d", i),
            Payload:    make([]byte, 1024), // 1KB payload
        }
        scheduler.Schedule(context.Background(), dispatch)
    }

    // Check memory after load
    runtime.GC()
    runtime.ReadMemStats(&m)
    peakMemory := m.Alloc

    memoryIncrease := peakMemory - baselineMemory
    t.Logf("Memory increase under load: %d MB", memoryIncrease/1024/1024)

    // Assert memory stays under 1GB
    assert.Less(t, memoryIncrease, uint64(1024*1024*1024), "Memory usage exceeds 1GB")
}
```

### 4. Health Check Integration Tests

#### Task HEALTH-INT-01: Test Consumer Health Endpoints
- [ ] Start consumer with queues
- [ ] Call Health() endpoint
- [ ] Verify queue connectivity
- [ ] Test DetailedHealth() with metrics

**Implementation Template**:
```go
func TestIntegration_Consumer_HealthChecks(t *testing.T) {
    sqsClient, queues := SetupLocalStackSQS(t)
    consumer := createTestConsumer(sqsClient, queues)

    // Basic health check
    err := consumer.Health()
    assert.NoError(t, err)

    // Detailed health check
    health, err := consumer.DetailedHealth(context.Background())
    assert.NoError(t, err)

    // Verify health for each queue
    for eventType := range queues {
        queueHealth, exists := health[eventType]
        assert.True(t, exists)
        assert.Equal(t, "healthy", queueHealth.Status)
        assert.GreaterOrEqual(t, queueHealth.ApproxMessages, int64(0))
    }

    // Test unhealthy scenario (stop LocalStack)
    // ... simulate failure ...

    err = consumer.Health()
    assert.Error(t, err)
}
```

## Test Data Fixtures

### Create Test Webhook Payloads
- [ ] Create `test/fixtures/pull_request_opened.json`
- [ ] Create `test/fixtures/status_success.json`
- [ ] Create `test/fixtures/pull_request_review.json`

Example fixture:
```json
{
  "action": "opened",
  "pull_request": {
    "number": 1,
    "state": "open",
    "head": {
      "sha": "abc123",
      "ref": "feature-branch"
    },
    "base": {
      "sha": "def456",
      "ref": "main"
    }
  },
  "repository": {
    "owner": {
      "login": "test-owner"
    },
    "name": "test-repo"
  },
  "installation": {
    "id": 12345
  }
}
```

## Test Helpers

### Create Mock GitHub Server Helper
```go
func setupMockGitHubServer(t *testing.T) *httptest.Server {
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Route to appropriate handler based on path
        switch {
        case strings.Contains(r.URL.Path, "/access_tokens"):
            handleInstallationToken(w, r)
        case strings.Contains(r.URL.Path, "/pulls"):
            handlePullRequest(w, r)
        case strings.Contains(r.URL.Path, "/statuses"):
            handleStatus(w, r)
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
}
```

## Test Execution Instructions

### Start LocalStack
```bash
# Start LocalStack container
cd test/localstack
docker-compose up -d

# Verify LocalStack is running
aws --endpoint-url=http://localhost:4566 sqs list-queues

# Create test queues
./scripts/setup-localstack.sh
```

### Run Integration Tests
```bash
# Run all Phase 2 integration tests
go test -v ./test/... -tags=integration

# Run specific test suites
go test -v ./test/auth_integration_test.go
go test -v ./test/sqs_consumer_integration_test.go
go test -v ./test/scheduler_performance_test.go

# Run with coverage
go test -coverprofile=phase2_coverage.out ./test/...
go tool cover -html=phase2_coverage.out -o phase2_coverage.html

# Cleanup LocalStack
docker-compose -f test/localstack/docker-compose.yml down
```

## Acceptance Criteria

### Test Coverage
- [ ] Authentication flow tested end-to-end
- [ ] Consumer lifecycle fully validated
- [ ] Scheduler performance quantified
- [ ] All integration tests passing

### Performance Metrics
- [ ] Scheduler overhead <20%
- [ ] Memory usage <1GB under load
- [ ] Consumer shutdown <5 seconds
- [ ] Message processing 100% success

### Primary Questions Answered
- [ ] Q1: SQS can authenticate with GitHub ✅
- [ ] Q2: Scheduler provides value (quantified) ✅
- [ ] LocalStack integration stable
- [ ] No flaky tests

## Common Issues & Solutions

### Issue: LocalStack connection refused
**Solution**: Ensure Docker is running and port 4566 is available

### Issue: Authentication test fails
**Solution**: Verify mock server handles all required GitHub API endpoints

### Issue: Consumer doesn't process messages
**Solution**: Check queue URLs and visibility timeout settings

### Issue: Flaky timing in tests
**Solution**: Use proper synchronization with channels or polling with timeout

## Next Phase Preview

Phase 3 will focus on:
- Concurrent stress testing with 1000+ messages
- Race condition detection
- Memory leak detection
- Performance profiling

## Notes for AI Agent

1. **LocalStack First**: Always ensure LocalStack is running before tests
2. **Mock Carefully**: GitHub API mocks must be comprehensive
3. **Timing Matters**: Use appropriate timeouts and waits
4. **Clean State**: Reset test data between runs
5. **Verify Cleanup**: Ensure no goroutine leaks
6. **Document Failures**: Capture logs when tests fail
7. **Performance Baseline**: Record metrics for comparison

## Completion Checklist

- [ ] LocalStack configuration complete
- [ ] All integration tests implemented
- [ ] Authentication flow validated (Q1 answered)
- [ ] Scheduler performance measured (Q2 answered)
- [ ] Consumer lifecycle tested
- [ ] No test failures
- [ ] Documentation updated
- [ ] Ready for Phase 3

## References

- [LocalStack Documentation](https://docs.localstack.cloud/getting-started/)
- [AWS SDK Go v2](https://aws.github.io/aws-sdk-go-v2/docs/)
- [GitHub API Testing](https://docs.github.com/en/rest)
- [Integration Testing in Go](https://github.com/golang/go/wiki/IntegrationTesting)