# Policy-Bot TDD Testing Plan

## Executive Summary

After analyzing the policy-bot codebase using Tree of Thought (ToT) methodology, I've identified key areas for testing and optimization. The system is well-architected with clear separation between HTTP webhook and SQS message processing, both using the same schedulers and handlers. This plan follows a Test-Driven Development (TDD) approach to validate system behavior, answer critical questions about the architecture, and identify optimization opportunities.

## Answers to Your Critical Questions

### 1. Can the sqsconsumer/processor use GitHub cloud and enterprise and login to use it after receiving a webhook?

**Answer: YES** - The system already implements this correctly:
- The processor receives the installation ID from the webhook payload
- It uses `githubapp.ClientCreator` to create authenticated clients on-demand
- The `Base.NewInstallationClient()` method (handler/base.go:84-88) creates authenticated clients using installation tokens
- Authentication happens per-message, not per-session, which is the correct pattern for GitHub Apps

**Evidence:**
- processor.go:170-189 shows handler selection and scheduler dispatch
- handler/base.go:84-88 shows client creation with installation ID
- The handlers don't maintain state between invocations

### 2. Is the current way of using cloud/enterprise scheduler the best approach?

**Answer: YES with room for optimization** - The current approach is solid but can be improved:

**Current Strengths:**
- Separate schedulers prevent cross-contamination between environments
- Reuses the same schedulers for both HTTP and SQS (good consistency)
- Thread-safe through scheduler's internal queue management

**Optimization Opportunities:**
- Consider dynamic worker pool sizing based on queue depth
- Add circuit breaker patterns for failing queues
- Implement priority queues for critical events

### 3. How does SQS handle thread safety?

**Answer: Well-designed thread safety through multiple layers:**
- **Worker Isolation**: Each worker goroutine processes messages independently (consumer.go:417-472)
- **Scheduler Queue**: The `githubapp.QueueAsyncScheduler` provides thread-safe queueing
- **No Shared State**: Handlers are stateless, created fresh per event
- **Synchronized Metrics**: Uses thread-safe metrics registry
- **Graceful Shutdown**: Uses sync.WaitGroup and channels for coordinated shutdown

### 4. Do the SQS components need goji.Mux or goji.SubMux?

**Answer: NO** - The SQS components correctly do NOT use HTTP routing:
- SQS workers consume messages directly from queues
- Routing happens at the processor level based on headers/source detection
- Muxes are only needed for HTTP endpoints, not message processing

## Tree of Thought Analysis - Testing Hypotheses

### Hypothesis 1: Unit Testing First Approach ✅ (Selected)
**Score: 28/30**
- Start with isolated unit tests for core components
- Mock external dependencies (AWS SDK, GitHub clients)
- Test business logic without infrastructure
- **Pros**: Fast feedback, easy debugging, high confidence
- **Cons**: Doesn't test integration points

### Hypothesis 2: Integration Testing First Approach
**Score: 20/30**
- Start with LocalStack integration tests
- Test end-to-end flows immediately
- **Pros**: Tests real behavior, catches integration issues
- **Cons**: Slow, harder to debug, requires infrastructure

### Hypothesis 3: Contract Testing Approach
**Score: 18/30**
- Define contracts between components
- Test against contracts independently
- **Pros**: Good for microservices, clear boundaries
- **Cons**: Overhead for monolithic app

### Hypothesis 4: Behavior-Driven Development
**Score: 15/30**
- Write Gherkin scenarios first
- **Pros**: Business-readable tests
- **Cons**: Overkill for technical components

### Hypothesis 5: Property-Based Testing
**Score: 12/30**
- Generate random inputs to find edge cases
- **Pros**: Finds unexpected bugs
- **Cons**: Complex to implement, not suitable for all components

## Phase 1: Unit Tests - Authentication & Client Creation

### Test Suite: GitHub Authentication After SQS Receipt

```go
// File: server/sqsconsumer/processor_auth_test.go
```

#### Test 1.1: Verify Installation Client Creation
**Acceptance Criteria:**
- Processor can create authenticated GitHub clients for cloud events
- Processor can create authenticated GitHub clients for enterprise events
- Installation tokens are properly cached and reused
- Token refresh happens automatically when expired

**Context:**
This tests the critical path where SQS processor creates authenticated clients after receiving an event. This validates that the authentication chain works: SQS → Processor → Handler → ClientCreator → GitHub API

#### Test 1.2: Verify Cross-Installation Access
**Acceptance Criteria:**
- Cloud handlers cannot access enterprise installations
- Enterprise handlers cannot access cloud installations
- Each environment maintains separate authentication contexts
- Installation ID mapping works correctly

#### Test 1.3: Test Authentication Failure Scenarios
**Acceptance Criteria:**
- Invalid installation IDs are handled gracefully
- Revoked app permissions trigger appropriate errors
- Network failures during auth are retried with backoff
- Authentication errors are properly logged and metered

### Test Implementation Plan:

```go
func TestProcessor_AuthenticatesWithGitHub(t *testing.T) {
    tests := []struct {
        name           string
        environment    string
        installationID int64
        expectAuth     bool
        expectError    bool
    }{
        {
            name:           "cloud_event_creates_cloud_client",
            environment:    "cloud",
            installationID: 12345,
            expectAuth:     true,
            expectError:    false,
        },
        {
            name:           "enterprise_event_creates_enterprise_client",
            environment:    "enterprise",
            installationID: 67890,
            expectAuth:     true,
            expectError:    false,
        },
        {
            name:           "invalid_installation_fails_gracefully",
            environment:    "cloud",
            installationID: 0,
            expectAuth:     false,
            expectError:    true,
        },
    }

    // Test implementation...
}
```

## Phase 2: Unit Tests - Message Processing & Routing

### Test Suite: SQS Message Routing

#### Test 2.1: Source Detection from Headers
**Acceptance Criteria:**
- Messages with "ghec" in Host header route to cloud
- Messages without "ghec" route to enterprise
- Legacy source field works for backward compatibility
- Missing headers default to cloud (consistent with HTTP)

#### Test 2.2: Event Type to Handler Mapping
**Acceptance Criteria:**
- pull_request events map to PullRequest handler
- status events map to Status handler
- Unknown event types are logged and deleted
- Handler selection is environment-specific

#### Test 2.3: Scheduler Selection
**Acceptance Criteria:**
- Cloud events use cloud scheduler
- Enterprise events use enterprise scheduler
- Scheduler queues are isolated per environment
- Queue depth metrics are tracked per scheduler

### Test Implementation:

```go
func TestProcessor_DetectsSourceFromHeaders(t *testing.T) {
    tests := []struct {
        name           string
        headers        map[string]interface{}
        legacySource   string
        expectedSource string
    }{
        {
            name:           "ghec_host_header_routes_to_cloud",
            headers:        map[string]interface{}{"Host": "api.ghec.github.com"},
            expectedSource: "cloud",
        },
        {
            name:           "enterprise_host_header_routes_to_enterprise",
            headers:        map[string]interface{}{"Host": "github.enterprise.com"},
            expectedSource: "enterprise",
        },
        {
            name:           "missing_headers_defaults_to_cloud",
            headers:        nil,
            expectedSource: "cloud",
        },
        {
            name:           "legacy_source_field_respected",
            headers:        nil,
            legacySource:   "enterprise",
            expectedSource: "enterprise",
        },
    }

    // Test implementation...
}
```

## Phase 3: Integration Tests - End-to-End Flow

### Test Suite: Complete SQS to GitHub Flow

#### Test 3.1: Pull Request Event Processing
**Acceptance Criteria:**
- SQS message triggers pull request evaluation
- Correct GitHub API calls are made
- Status is posted to correct commit SHA
- Metrics are recorded for the full flow

**Test Setup:**
```go
func TestIntegration_SQSToPullRequestProcessing(t *testing.T) {
    // 1. Start LocalStack SQS
    // 2. Create mock GitHub server
    // 3. Configure processor with test clients
    // 4. Send test message to queue
    // 5. Verify GitHub API calls
    // 6. Check metrics recorded
}
```

#### Test 3.2: Concurrent Processing
**Acceptance Criteria:**
- Multiple workers process messages concurrently
- No race conditions in shared resources
- Metrics are thread-safe
- Graceful shutdown waits for all workers

#### Test 3.3: Error Recovery
**Acceptance Criteria:**
- Failed messages are retried with exponential backoff
- DLQ receives messages after max retries
- Transient errors don't lose messages
- Circuit breaker prevents cascade failures

## Phase 4: Thread Safety Tests

### Test Suite: Concurrency and Thread Safety

#### Test 4.1: Concurrent Worker Safety
```go
func TestConcurrentWorkerSafety(t *testing.T) {
    // Spawn 100 workers
    // Send 1000 messages concurrently
    // Verify no data races (use -race flag)
    // Check all messages processed exactly once
}
```

#### Test 4.2: Scheduler Queue Safety
```go
func TestSchedulerQueueThreadSafety(t *testing.T) {
    // Multiple producers adding to scheduler
    // Verify FIFO ordering per queue
    // Check no lost events
    // Verify metrics consistency
}
```

#### Test 4.3: Graceful Shutdown Under Load
```go
func TestGracefulShutdownUnderLoad(t *testing.T) {
    // Start processing high message volume
    // Trigger shutdown
    // Verify all in-flight messages complete
    // Check no message loss
    // Verify shutdown completes within timeout
}
```

## Phase 5: Performance & Load Tests

### Test Suite: Performance Validation

#### Test 5.1: Throughput Testing
**Acceptance Criteria:**
- Process 1000 messages/second per queue
- P99 latency < 1 second
- Memory usage < 1GB under load
- CPU usage scales linearly with workers

#### Test 5.2: Resource Leak Detection
```go
func TestNoResourceLeaks(t *testing.T) {
    // Run for 1 hour with constant load
    // Monitor goroutine count
    // Check memory growth
    // Verify file descriptor usage
}
```

## Phase 6: Configuration & Routing Tests

### Test Suite: Dynamic Configuration

#### Test 6.1: Per-Environment Routing Configuration
**Acceptance Criteria:**
- Cloud events respect cloud routing config
- Enterprise events respect enterprise routing config
- "both" routing processes in both paths
- "http" routing skips SQS processing

#### Test 6.2: Queue Worker Allocation
```go
func TestDynamicWorkerAllocation(t *testing.T) {
    configs := []struct {
        eventType       string
        expectedWorkers int
    }{
        {"pull_request", 10},
        {"status", 15},
        {"check_run", 5},
    }

    // Test each configuration
}
```

## Optimization Recommendations

Based on the analysis, here are the key optimizations to implement:

### 1. Connection Pool Optimization
```go
// Current: Each worker creates separate connections
// Optimized: Shared connection pool

type ConnectionPool struct {
    mu         sync.RWMutex
    clients    map[int64]*github.Client
    maxAge     time.Duration
    maxSize    int
}
```

### 2. Batch Processing
```go
// Current: Process one message at a time
// Optimized: Process in batches where possible

func (p *Processor) ProcessBatch(messages []types.Message) []error {
    // Process multiple messages in parallel
    // Return individual errors for retry logic
}
```

### 3. Circuit Breaker Pattern
```go
type CircuitBreaker struct {
    failureThreshold int
    resetTimeout     time.Duration
    state            atomic.Value
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    if cb.IsOpen() {
        return ErrCircuitOpen
    }
    // Execute with circuit breaker logic
}
```

### 4. Priority Queue Implementation
```go
type PriorityQueue struct {
    high   chan types.Message
    normal chan types.Message
    low    chan types.Message
}

func (pq *PriorityQueue) Consume() types.Message {
    select {
    case msg := <-pq.high:
        return msg
    default:
        select {
        case msg := <-pq.normal:
            return msg
        default:
            return <-pq.low
        }
    }
}
```

### 5. Metrics Enhancement
```go
// Add detailed metrics for better observability
metrics := struct {
    QueueDepth       prometheus.GaugeVec
    ProcessingTime   prometheus.HistogramVec
    ErrorRate        prometheus.CounterVec
    CircuitState     prometheus.GaugeVec
}{
    // Initialize with labels for environment, event_type, queue
}
```

## Testing Execution Order (TDD)

1. **Week 1: Unit Tests**
   - [ ] Authentication tests (Phase 1)
   - [ ] Routing tests (Phase 2)
   - [ ] Mock all external dependencies
   - [ ] Achieve 90% unit test coverage

2. **Week 2: Integration Tests**
   - [ ] LocalStack setup
   - [ ] End-to-end flow tests (Phase 3)
   - [ ] Error recovery tests
   - [ ] DLQ behavior validation

3. **Week 3: Concurrency & Performance**
   - [ ] Thread safety tests (Phase 4)
   - [ ] Load tests (Phase 5)
   - [ ] Resource leak detection
   - [ ] Benchmark critical paths

4. **Week 4: Optimization Implementation**
   - [ ] Implement connection pooling
   - [ ] Add circuit breakers
   - [ ] Batch processing where applicable
   - [ ] Deploy and monitor

## Success Metrics

### Functional Metrics
- ✅ All authentication tests pass
- ✅ 100% routing accuracy
- ✅ Zero message loss under normal conditions
- ✅ Graceful degradation during failures

### Performance Metrics
- ⚡ P50 latency < 100ms
- ⚡ P99 latency < 1s
- ⚡ Throughput > 1000 msg/s per queue
- ⚡ Memory < 1GB under load

### Reliability Metrics
- 🛡️ 99.9% uptime
- 🛡️ Automatic recovery from transient failures
- 🛡️ Zero data races (verified with -race)
- 🛡️ Clean shutdown in < 30s

## Test Coverage Requirements

| Component | Current | Target | Priority |
|-----------|---------|--------|----------|
| processor.go | 0% | 95% | Critical |
| consumer.go | 0% | 90% | Critical |
| handler/base.go | 0% | 85% | High |
| config.go (SQS) | 0% | 90% | High |
| middleware routing | 0% | 85% | Medium |

## Conclusion

The policy-bot system is well-architected for handling both HTTP webhooks and SQS messages. The authentication chain works correctly, thread safety is properly implemented, and the routing logic is sound. The main opportunities for improvement are in:

1. **Performance optimization** through connection pooling and batch processing
2. **Reliability enhancement** with circuit breakers and better retry logic
3. **Observability improvement** with comprehensive metrics
4. **Testing coverage** to ensure long-term maintainability

By following this TDD plan, we can validate the current implementation, identify edge cases, and safely implement optimizations while maintaining system stability.