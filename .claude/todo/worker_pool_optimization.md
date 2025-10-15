# Worker Pool Optimization Plan for Adaptive SQS Polling

## Executive Summary
Implement adaptive SQS polling that checks worker availability before receiving messages, preventing message processing failures when workers are saturated. This is critical for high-volume event queues like status events.

## Problem Statement
- **Current Issue**: `consumeQueue()` polls SQS continuously without checking worker availability
- **Impact**: Messages timeout when all workers busy, leading to reprocessing and potential data issues
- **Critical for**: High-volume events (status) that can saturate worker pools

## Solution Architecture

### Core Concept
Only poll SQS when workers are available to process messages immediately, implementing backpressure at the polling level rather than at processing level.

```
Current Flow (Problematic):
SQS Poll → Receive Messages → Try to Process → Timeout if Workers Busy

Optimized Flow:
Check Worker Availability → Poll SQS Only if Workers Available → Process Messages
```

## Implementation Tasks

### Task 1: Add Worker Availability Check to WorkerPool
**File**: `server/sqsconsumer/workerpool.go`
**Priority**: Critical
**Estimated Complexity**: Low

#### Steps:
1. Add method to check available worker slots:
```go
// HasAvailableWorkers checks if the pool has available worker slots
// This is a non-blocking check that returns immediately
func (p *WorkerPool) HasAvailableWorkers() bool {
    p.mu.RLock()
    defer p.mu.RUnlock()

    if p.closed {
        return false
    }

    // Check if we have available capacity
    availableSlots := p.capacity - int(p.activeWorkers)
    return availableSlots > 0
}

// GetAvailableCapacity returns the number of available worker slots
func (p *WorkerPool) GetAvailableCapacity() int {
    p.mu.RLock()
    defer p.mu.RUnlock()

    if p.closed {
        return 0
    }

    availableSlots := p.capacity - int(p.activeWorkers)
    if availableSlots < 0 {
        return 0
    }
    return availableSlots
}
```

2. Add method to WorkerPoolManager:
```go
// HasAvailableWorkersForEventType checks if a specific event type has available workers
func (m *WorkerPoolManager) HasAvailableWorkersForEventType(eventType string) bool {
    m.mu.RLock()
    defer m.mu.RUnlock()

    pool, exists := m.pools[eventType]
    if !exists {
        // Pool doesn't exist yet, so it will be created with full capacity
        return true
    }

    return pool.HasAvailableWorkers()
}

// GetAvailableCapacityForEventType returns available capacity for an event type
func (m *WorkerPoolManager) GetAvailableCapacityForEventType(eventType string, defaultCapacity int) int {
    m.mu.RLock()
    defer m.mu.RUnlock()

    pool, exists := m.pools[eventType]
    if !exists {
        // Pool doesn't exist yet, return default capacity
        return defaultCapacity
    }

    return pool.GetAvailableCapacity()
}
```

### Task 2: Modify Consumer for Adaptive Polling
**File**: `server/sqsconsumer/consumer.go`
**Priority**: Critical
**Estimated Complexity**: Medium

#### Steps:
1. Add worker availability check before polling:
```go
// consumeQueue handles consuming messages from a single queue
func (c *consumer) consumeQueue(ctx context.Context, eventType, queueURL string, workerID int) {
    defer c.wg.Done()

    logger := c.logger.With().
        Str("event_type", eventType).
        Str("queue_url", queueURL).
        Int("worker_id", workerID).
        Logger()

    logger.Info().Msg("Starting SQS queue consumer worker")

    maxMessages := c.getMaxMessages()
    visibilityTimeout := c.getVisibilityTimeout()
    waitTimeSeconds := c.getWaitTimeSeconds()

    // Track consecutive saturation events for backoff
    consecutiveSaturations := 0

    for {
        select {
        case <-c.stopChan:
            logger.Info().Msg("SQS queue consumer worker stopping")
            return
        case <-ctx.Done():
            logger.Info().Msg("SQS queue consumer worker context cancelled")
            return
        default:
            // Continue processing
        }

        // OPTIMIZATION: Check worker availability before polling
        if c.config.ProcessingMode == "direct" && c.workerPoolMgr != nil {
            workerCount := c.getWorkersForQueue(eventType)
            availableCapacity := c.workerPoolMgr.GetAvailableCapacityForEventType(eventType, workerCount)

            if availableCapacity == 0 {
                // All workers busy, implement backoff
                consecutiveSaturations++
                backoffDuration := c.calculateBackoff(consecutiveSaturations)

                logger.Debug().
                    Int("consecutive_saturations", consecutiveSaturations).
                    Dur("backoff_duration", backoffDuration).
                    Msg("Worker pool saturated, backing off SQS polling")

                // Record saturation metric
                if c.registry != nil {
                    saturationCounter := metrics.GetOrRegisterCounter(
                        fmt.Sprintf("sqs.worker_pool.saturation_events.%s", eventType),
                        c.registry,
                    )
                    saturationCounter.Inc(1)
                }

                select {
                case <-time.After(backoffDuration):
                case <-c.stopChan:
                    return
                case <-ctx.Done():
                    return
                }
                continue
            }

            // Reset saturation counter when workers become available
            if consecutiveSaturations > 0 {
                logger.Debug().
                    Int("available_capacity", availableCapacity).
                    Msg("Workers available, resuming normal polling")
                consecutiveSaturations = 0
            }

            // Adjust max messages based on available capacity
            if availableCapacity < maxMessages {
                maxMessages = availableCapacity
                logger.Debug().
                    Int("adjusted_max_messages", maxMessages).
                    Int("available_capacity", availableCapacity).
                    Msg("Adjusted max messages to match available capacity")
            }
        }

        // Receive messages from SQS
        result, err := c.sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
            QueueUrl:            aws.String(queueURL),
            MaxNumberOfMessages: int32(maxMessages),
            VisibilityTimeout:   int32(visibilityTimeout),
            WaitTimeSeconds:     int32(waitTimeSeconds),
        })

        // ... rest of existing code ...
    }
}

// calculateBackoff calculates exponential backoff duration
func (c *consumer) calculateBackoff(consecutiveSaturations int) time.Duration {
    // Start with 1 second, double each time, max 30 seconds
    baseBackoff := time.Second
    maxBackoff := 30 * time.Second

    backoff := baseBackoff * time.Duration(1<<uint(consecutiveSaturations-1))
    if backoff > maxBackoff {
        backoff = maxBackoff
    }

    return backoff
}
```

### Task 3: Add Configuration for Adaptive Polling
**File**: `server/sqsconsumer/consumer.go` and `server/config.go`
**Priority**: Medium
**Estimated Complexity**: Low

#### Steps:
1. Add configuration options:
```go
// In Config struct
type Config struct {
    // ... existing fields ...

    // Adaptive polling configuration
    AdaptivePolling AdaptivePollingConfig `yaml:"adaptive_polling"`
}

type AdaptivePollingConfig struct {
    // Enable adaptive polling based on worker availability
    Enabled bool `yaml:"enabled"`

    // Base backoff duration when workers are saturated
    BaseBackoff time.Duration `yaml:"base_backoff"`

    // Maximum backoff duration
    MaxBackoff time.Duration `yaml:"max_backoff"`

    // Enable per-event-type configuration
    EventTypeOverrides map[string]AdaptivePollingEventConfig `yaml:"event_overrides"`
}

type AdaptivePollingEventConfig struct {
    Enabled     bool          `yaml:"enabled"`
    BaseBackoff time.Duration `yaml:"base_backoff"`
    MaxBackoff  time.Duration `yaml:"max_backoff"`
}
```

2. Default configuration in YAML:
```yaml
sqs:
  adaptive_polling:
    enabled: true
    base_backoff: 1s
    max_backoff: 30s
    event_overrides:
      status:
        enabled: true
        base_backoff: 500ms  # More aggressive for high-volume
        max_backoff: 10s
      pull_request:
        enabled: true
        base_backoff: 2s
        max_backoff: 30s
```

### Task 4: Enhanced Metrics and Monitoring
**File**: `server/sqsconsumer/workerpool.go`, `server/sqsconsumer/consumer.go`
**Priority**: Medium
**Estimated Complexity**: Low

#### Steps:
1. Add new metrics:
```go
// Worker pool saturation metrics
const (
    MetricsKeySaturationEvents    = "sqs.worker_pool.saturation_events"
    MetricsKeySaturationDuration  = "sqs.worker_pool.saturation_duration"
    MetricsKeyPollingBackoff      = "sqs.consumer.polling_backoff"
    MetricsKeyAdaptiveAdjustments = "sqs.consumer.adaptive_adjustments"
)
```

2. Track saturation duration:
```go
// In consumer struct, add saturation tracking
type consumer struct {
    // ... existing fields ...

    // Saturation tracking per event type
    saturationTrackers map[string]*SaturationTracker
}

type SaturationTracker struct {
    startTime          *time.Time
    totalDuration      time.Duration
    consecutiveCount   int
    mu                 sync.RWMutex
}
```

### Task 5: Testing Strategy
**File**: New test files in `server/sqsconsumer/`
**Priority**: High
**Estimated Complexity**: Medium

#### Steps:
1. Unit tests for worker availability checks:
```go
// workerpool_adaptive_test.go
func TestWorkerPool_HasAvailableWorkers(t *testing.T) {
    // Test with available workers
    // Test with saturated pool
    // Test with closed pool
}

func TestWorkerPoolManager_GetAvailableCapacityForEventType(t *testing.T) {
    // Test with existing pool
    // Test with non-existent pool
    // Test with partially saturated pool
}
```

2. Integration tests for adaptive polling:
```go
// consumer_adaptive_test.go
func TestConsumer_AdaptivePolling(t *testing.T) {
    // Test polling stops when workers saturated
    // Test polling resumes when workers available
    // Test backoff calculation
    // Test max messages adjustment
}
```

3. Performance tests:
```go
// performance_adaptive_test.go
func TestAdaptivePolling_Performance(t *testing.T) {
    // Measure throughput with adaptive polling
    // Compare with non-adaptive polling
    // Test high-volume scenario (status events)
}
```

### Task 6: Gradual Rollout Plan
**Priority**: Critical
**Estimated Complexity**: Low

#### Steps:
1. **Phase 1**: Deploy with adaptive polling disabled (feature flag off)
2. **Phase 2**: Enable for low-volume queues (installation events)
3. **Phase 3**: Enable for medium-volume queues (pull_request events)
4. **Phase 4**: Enable for high-volume queues (status events)
5. **Phase 5**: Make adaptive polling the default

#### Rollback Triggers:
- Message processing latency increases > 20%
- SQS queue depth increases > 50%
- Worker utilization drops < 50%
- Error rate increases > 1%

## Success Metrics

### Primary Metrics
- **Zero message timeouts** due to worker saturation
- **Worker utilization** maintained at 70-90%
- **SQS queue depth** remains stable
- **Message processing latency** P99 < 2 seconds

### Secondary Metrics
- **Saturation events** reduced by 90%
- **Polling efficiency** (messages received vs processed) > 95%
- **Resource usage** (CPU/memory) reduced by 20%

## Risk Mitigation

### Risk 1: Polling Starvation
**Mitigation**: Implement maximum backoff and periodic forced polling

### Risk 2: Thundering Herd
**Mitigation**: Add jitter to backoff calculations

### Risk 3: Metric Overhead
**Mitigation**: Use sampling for high-frequency metrics

## Dependencies
- No external library dependencies
- Requires Go 1.19+ for generic support (optional optimization)
- AWS SDK v2 already in use

## Estimated Timeline
- **Implementation**: 2-3 days
- **Testing**: 1-2 days
- **Rollout**: 1 week (phased)
- **Total**: ~2 weeks

## Code Snippets for Quick Reference

### Check Before Poll Pattern
```go
if !workerPoolMgr.HasAvailableWorkersForEventType(eventType) {
    // Back off
    continue
}
// Safe to poll
```

### Adaptive Max Messages
```go
availableCapacity := workerPoolMgr.GetAvailableCapacityForEventType(eventType)
maxMessages = min(maxMessages, availableCapacity)
```

### Metrics Recording
```go
metrics.GetOrRegisterCounter(
    fmt.Sprintf("%s.%s", MetricsKeySaturationEvents, eventType),
    registry,
).Inc(1)
```

## Next Steps
1. Review and approve plan
2. Create feature branch
3. Implement Task 1 (Worker availability check)
4. Implement Task 2 (Adaptive polling)
5. Write comprehensive tests
6. Deploy with feature flag disabled
7. Gradual rollout with monitoring

## Notes for AI Agent Implementation

### Critical Success Factors
1. **Preserve existing behavior** when adaptive polling is disabled
2. **Test thoroughly** with high-volume scenarios
3. **Monitor metrics** closely during rollout
4. **Document configuration** clearly for operators

### Code Quality Requirements
1. **Thread-safe** - All shared state must be protected
2. **Non-blocking** - Availability checks must not block
3. **Graceful degradation** - System must work if worker pool unavailable
4. **Observable** - All decisions must be logged/metered

### Testing Focus Areas
1. **Saturation scenarios** - Test with fully busy workers
2. **Recovery scenarios** - Test transition from saturated to available
3. **Edge cases** - Test with 0 workers, closed pools, etc.
4. **Performance** - Ensure no regression in throughput

---

*Document Version: 1.0*
*Created: 2024-10-15*
*Target Implementation: Policy Bot v2.x*