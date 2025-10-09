# Phase 1: Foundation & Unit Tests (Sprint 1)

## Overview
**Duration**: Week 1
**Objective**: Establish unit test foundation with shared utilities and core component validation
**Success Criteria**: 30+ unit tests passing, >85% coverage on critical components

## Prerequisites
- [ ] Ensure Go test environment is set up
- [ ] Install testify: `go get github.com/stretchr/testify`
- [ ] Install mock: `go get github.com/stretchr/testify/mock`
- [ ] Verify existing tests pass: `go test ./...`

## Task Checklist

### 1. Create Shared Source Router Utility (Priority: CRITICAL)

#### Task SR-01: Create Source Router Package
- [ ] Create new file: `server/internal/sourcerouter/router.go`
- [ ] Implement `DetectSource(headers map[string]interface{}, queryParams map[string]string) (source string, method string)`
- [ ] Return values: ("cloud"|"enterprise", detection method)

**Implementation Template**:
```go
package sourcerouter

import (
    "strings"
)

type Router struct{}

func (r *Router) DetectSource(headers map[string]interface{}, queryParams map[string]string) (string, string) {
    // Priority 1: Check X-GitHub-Enterprise-Host header
    if enterpriseHost, ok := headers["X-GitHub-Enterprise-Host"].(string); ok && enterpriseHost != "" {
        return "enterprise", "enterprise_header"
    }

    // Priority 2: Check Host header for "ghec" marker
    if host, ok := headers["Host"].(string); ok {
        if strings.Contains(strings.ToLower(host), "ghec") {
            return "cloud", "ghec_host"
        }
        return "enterprise", "host_header"
    }

    // Priority 3: Check query parameter
    if source, ok := queryParams["source"]; ok {
        if source == "enterprise" {
            return "enterprise", "query_param"
        }
    }

    // Default to cloud
    return "cloud", "default"
}
```

#### Task SR-02: Create Source Router Tests
- [ ] Create test file: `server/internal/sourcerouter/router_test.go`
- [ ] Implement test cases for all detection scenarios
- [ ] Ensure 100% coverage of router logic

**Test Cases to Implement**:
```go
// Test SR-T01: Enterprise header detection
func TestRouter_DetectSource_EnterpriseHeader(t *testing.T) {
    headers := map[string]interface{}{
        "X-GitHub-Enterprise-Host": "github.company.com",
    }
    source, method := router.DetectSource(headers, nil)
    assert.Equal(t, "enterprise", source)
    assert.Equal(t, "enterprise_header", method)
}

// Test SR-T02: GHEC host detection
func TestRouter_DetectSource_GHECHost(t *testing.T) {
    headers := map[string]interface{}{
        "Host": "api.ghec.github.com",
    }
    source, method := router.DetectSource(headers, nil)
    assert.Equal(t, "cloud", source)
    assert.Equal(t, "ghec_host", method)
}

// Test SR-T03: Query parameter fallback
// Test SR-T04: Default to cloud
// Test SR-T05: Case insensitive GHEC detection
```

### 2. Unit Tests for Authentication (handler/base.go)

#### Task AUTH-01: Create Authentication Test File
- [ ] Create file: `server/handler/base_auth_test.go`
- [ ] Set up test fixtures and mocks
- [ ] Import necessary packages

#### Task AUTH-02: Test Base.NewInstallationClient Success
- [ ] Mock githubapp.ClientCreator
- [ ] Test cloud installation client creation
- [ ] Test enterprise installation client creation
- [ ] Verify correct client returned

**Implementation Template**:
```go
func TestBase_NewInstallationClient_Success(t *testing.T) {
    tests := []struct {
        name           string
        installationID int64
        isCloud        bool
        expectError    bool
    }{
        {"cloud_valid_id", 12345, true, false},
        {"enterprise_valid_id", 67890, false, false},
        {"invalid_id", 0, true, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            mockCreator := new(MockClientCreator)
            base := &Base{
                ClientCreator: mockCreator,
                GithubCloud:   tt.isCloud,
            }

            if !tt.expectError {
                mockClient := new(github.Client)
                mockCreator.On("NewInstallationClient", tt.installationID).Return(mockClient, nil)
            }

            client, err := base.NewInstallationClient(tt.installationID)

            if tt.expectError {
                assert.Error(t, err)
                assert.Nil(t, client)
            } else {
                assert.NoError(t, err)
                assert.NotNil(t, client)
            }
            mockCreator.AssertExpectations(t)
        })
    }
}
```

#### Task AUTH-03: Test Base.NewEvalContext
- [ ] Test context creation with valid locator
- [ ] Verify both REST and GraphQL clients created
- [ ] Test PullContext initialization
- [ ] Verify config fetcher integration

### 3. SQS Processor Routing Tests

#### Task PROC-01: Create Processor Routing Test File
- [ ] Create file: `server/sqsconsumer/processor_routing_test.go`
- [ ] Set up mock SQS client
- [ ] Set up mock handlers and schedulers

#### Task PROC-02: Test detectSourceFromHeaders
- [ ] Test GHEC detection in Host header
- [ ] Test enterprise default for non-GHEC hosts
- [ ] Test missing headers defaults to cloud
- [ ] Test legacy source field compatibility

**Implementation Template**:
```go
func TestProcessor_detectSourceFromHeaders(t *testing.T) {
    processor := &Processor{
        logger: zerolog.New(nil),
    }

    tests := []struct {
        name           string
        sqsMsg         SQSMessage
        expectedSource string
    }{
        {
            name: "ghec_in_host",
            sqsMsg: SQSMessage{
                Headers: map[string]interface{}{
                    "Host": "api.ghec.github.com",
                },
            },
            expectedSource: "cloud",
        },
        {
            name: "enterprise_host",
            sqsMsg: SQSMessage{
                Headers: map[string]interface{}{
                    "Host": "github.enterprise.com",
                },
            },
            expectedSource: "enterprise",
        },
        {
            name: "no_headers_default_cloud",
            sqsMsg: SQSMessage{},
            expectedSource: "cloud",
        },
        {
            name: "legacy_source_field",
            sqsMsg: SQSMessage{
                Source: "enterprise",
            },
            expectedSource: "enterprise",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            source := processor.detectSourceFromHeaders(tt.sqsMsg)
            assert.Equal(t, tt.expectedSource, source)
        })
    }
}
```

#### Task PROC-03: Test selectHandler
- [ ] Test cloud message gets cloud handler/scheduler
- [ ] Test enterprise message gets enterprise handler/scheduler
- [ ] Test unknown event type returns nil
- [ ] Verify handler and scheduler are paired correctly

### 4. Consumer Thread Safety Tests

#### Task THREAD-01: Create Consumer Concurrency Test File
- [ ] Create file: `server/sqsconsumer/consumer_concurrency_test.go`
- [ ] Set up concurrent test helpers
- [ ] Import sync and testing packages

#### Task THREAD-02: Test Concurrent Worker Creation
- [ ] Test multiple workers start correctly
- [ ] Verify no duplicate workers
- [ ] Test WaitGroup tracking
- [ ] Verify goroutine count matches configuration

**Implementation Template**:
```go
func TestConsumer_ConcurrentWorkers_Creation(t *testing.T) {
    config := &Config{
        Enabled:         true,
        WorkersPerQueue: 5,
        Queues: map[string]string{
            "pull_request": "queue-url-1",
            "status":       "queue-url-2",
        },
    }

    mockSQSClient := new(MockSQSClient)
    consumer := &consumer{
        config:    config,
        sqsClient: mockSQSClient,
        stopChan:  make(chan struct{}),
        logger:    zerolog.New(nil),
    }

    // Set up mock to block on ReceiveMessage
    blockChan := make(chan struct{})
    mockSQSClient.On("ReceiveMessage", mock.Anything, mock.Anything).
        WaitUntil(blockChan).
        Return(&sqs.ReceiveMessageOutput{}, nil)

    ctx := context.Background()
    err := consumer.Start(ctx)
    assert.NoError(t, err)

    // Give workers time to start
    time.Sleep(100 * time.Millisecond)

    // Verify expected number of goroutines
    // 2 queues * 5 workers = 10 goroutines
    runtime.GC()
    expectedGoroutines := 10
    // Actual verification would use runtime inspection

    close(blockChan)
    consumer.Stop(ctx)
}
```

#### Task THREAD-03: Test Race Conditions
- [ ] Create test with `-race` flag instructions
- [ ] Test concurrent message processing
- [ ] Verify no shared state mutations
- [ ] Test metrics thread safety

### 5. Scheduler Architecture Tests

#### Task SCHED-01: Create Scheduler Test File
- [ ] Create file: `server/handler/scheduler_test.go`
- [ ] Set up mock scheduler implementation
- [ ] Create test handlers

#### Task SCHED-02: Test Scheduler Backpressure
- [ ] Test queue size limits
- [ ] Verify blocking when queue full
- [ ] Test no message drops
- [ ] Verify memory bounds

**Implementation Template**:
```go
func TestScheduler_ProvidesBackpressure(t *testing.T) {
    scheduler := githubapp.QueueAsyncScheduler(10, 2) // queue=10, workers=2

    // Create slow handler that takes 100ms per event
    slowHandler := &MockEventHandler{}
    slowHandler.On("Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
        Return(func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
            time.Sleep(100 * time.Millisecond)
            return nil
        })

    // Try to schedule 20 events (queue size is 10)
    scheduled := 0
    for i := 0; i < 20; i++ {
        dispatch := githubapp.Dispatch{
            Handler:    slowHandler,
            EventType:  "test",
            DeliveryID: fmt.Sprintf("test-%d", i),
            Payload:    []byte("{}"),
        }

        done := make(chan bool, 1)
        go func() {
            scheduler.Schedule(context.Background(), dispatch)
            done <- true
        }()

        select {
        case <-done:
            scheduled++
        case <-time.After(10 * time.Millisecond):
            // Scheduling blocked due to full queue
            break
        }
    }

    // Should have scheduled ~10-12 (queue size + in-flight)
    assert.LessOrEqual(t, scheduled, 12)
}
```

#### Task SCHED-03: Test Scheduler Metrics
- [ ] Verify queue depth metric
- [ ] Test processing time recording
- [ ] Verify error count increments
- [ ] Check metric labels

### 6. SQS Independence Tests

#### Task INDEP-01: Create Independence Test File
- [ ] Create file: `server/sqsconsumer/independence_test.go`
- [ ] Set up import analysis utilities
- [ ] Create architecture validation functions

#### Task INDEP-02: Test No HTTP Dependencies
- [ ] Analyze processor package imports
- [ ] Verify no goji.io imports
- [ ] Check no net/http dependencies
- [ ] Validate clean separation

**Implementation Template**:
```go
func TestProcessor_NoHTTPDependencies(t *testing.T) {
    // Parse the processor.go file
    fset := token.NewFileSet()
    file, err := parser.ParseFile(fset, "processor.go", nil, parser.ImportsOnly)
    require.NoError(t, err)

    // Check imports
    for _, imp := range file.Imports {
        importPath := strings.Trim(imp.Path.Value, `"`)

        // These imports are NOT allowed
        assert.NotContains(t, importPath, "goji.io")
        assert.NotContains(t, importPath, "net/http")
        assert.NotContains(t, importPath, "server/middleware")
    }
}
```

## Mock Implementations Needed

### MockClientCreator
```go
type MockClientCreator struct {
    mock.Mock
}

func (m *MockClientCreator) NewInstallationClient(id int64) (*github.Client, error) {
    args := m.Called(id)
    if args.Get(0) == nil {
        return nil, args.Error(1)
    }
    return args.Get(0).(*github.Client), args.Error(1)
}
```

### MockEventHandler
```go
type MockEventHandler struct {
    mock.Mock
}

func (m *MockEventHandler) Handles() []string {
    args := m.Called()
    return args.Get(0).([]string)
}

func (m *MockEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
    args := m.Called(ctx, eventType, deliveryID, payload)
    return args.Error(0)
}
```

### MockScheduler
```go
type MockScheduler struct {
    mock.Mock
}

func (m *MockScheduler) Schedule(ctx context.Context, dispatch githubapp.Dispatch) error {
    args := m.Called(ctx, dispatch)
    return args.Error(0)
}
```

## Test Execution Instructions

### Run All Phase 1 Tests
```bash
# Run unit tests with coverage
go test -v -cover ./server/internal/sourcerouter/...
go test -v -cover ./server/handler/ -run "TestBase_"
go test -v -cover ./server/sqsconsumer/ -run "TestProcessor_|TestConsumer_"

# Run with race detector
go test -race ./server/sqsconsumer/...

# Generate coverage report
go test -coverprofile=phase1_coverage.out ./...
go tool cover -html=phase1_coverage.out -o phase1_coverage.html
```

## Acceptance Criteria

### Code Coverage
- [ ] sourcerouter package: 100% coverage
- [ ] processor routing functions: >95% coverage
- [ ] handler authentication: >90% coverage
- [ ] consumer concurrency: >85% coverage

### Test Results
- [ ] All 30+ unit tests passing
- [ ] No race conditions detected
- [ ] All mocks properly configured
- [ ] Test execution time <10 seconds

### Documentation
- [ ] All test functions have descriptive names
- [ ] Complex tests have inline comments
- [ ] Mock implementations documented
- [ ] Coverage report generated

## Common Issues & Solutions

### Issue: Mock not returning expected value
**Solution**: Ensure mock expectations are set before calling the method

### Issue: Race condition in tests
**Solution**: Use proper synchronization with channels or WaitGroups

### Issue: Tests hanging
**Solution**: Add timeouts to context and use shorter timeouts in tests

### Issue: Coverage not meeting targets
**Solution**: Add table-driven tests for edge cases

## Next Phase Preview

Phase 2 will build upon these unit tests by:
- Adding integration tests with LocalStack
- Testing end-to-end authentication flow
- Validating complete SQS consumer lifecycle
- Verifying multi-queue processing

## Notes for AI Agent

1. **Test First**: Write failing test, then implementation
2. **Use Table Tests**: Group similar test cases
3. **Mock External Only**: Don't mock internal components
4. **Clear Names**: Test names should describe behavior
5. **Independent Tests**: Each test must run standalone
6. **Descriptive Failures**: Use assert messages that explain the issue
7. **Coverage Focus**: Aim for behavior coverage, not line coverage

## Completion Checklist

- [ ] All tasks marked complete
- [ ] Coverage targets met
- [ ] No test failures
- [ ] Race detector passes
- [ ] Documentation updated
- [ ] Code committed with descriptive messages
- [ ] Ready for Phase 2

## References

- [Testing in Go](https://golang.org/doc/tutorial/add-a-test)
- [Testify Documentation](https://github.com/stretchr/testify)
- [Table Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
- [Go Race Detector](https://golang.org/doc/articles/race_detector)