# Proposed Architecture: Decoupled Event Processing for Policy Bot

## Executive Summary

This document proposes a hybrid event processing architecture that **bypasses the internal scheduler for SQS events** while maintaining it for HTTP webhooks. This eliminates the queue bottleneck that causes dropped events, leverages SQS's native queueing capabilities, and follows the KISS principle.

## Problem Analysis

### Current Architecture Issues

1. **Shared Queue Bottleneck**
   - Both HTTP webhooks and SQS messages feed into the same `QueueAsyncScheduler`
   - Fixed queue size (default 100) and workers (default 10)
   - When queue fills, events are **dropped silently**
   - No backpressure mechanism

2. **Redundant Queueing**
   - SQS already provides robust, distributed queueing
   - Adding events from SQS to another in-memory queue is redundant
   - Creates an unnecessary chokepoint

3. **Resource Competition**
   - High-volume events (e.g., `status` updates) crowd out critical events
   - No priority mechanism
   - HTTP burst traffic affects SQS processing

### Current Flow Visualization

```
HTTP Webhooks ──┐
                ├──> QueueAsyncScheduler ──> Workers ──> Handlers
SQS Messages ───┘    (Fixed Size Queue)
                     ↓ Queue Full
                   DROPPED EVENTS ❌
```

## Architectural Hypotheses Considered

### Hypothesis 1: Separate Schedulers
**Concept**: Dedicated schedulers for HTTP and SQS paths
- ✅ Pros: Isolation, independent tuning
- ❌ Cons: More complex resource management, doesn't solve fundamental issue

### Hypothesis 2: Direct Handler Invocation (Bypass Scheduler Completely)
**Concept**: SQS processes events directly without any scheduler
- ✅ Pros: Eliminates bottleneck, uses SQS natural queueing
- ❌ Cons: Loss of unified metrics, no rate limiting for handlers

### Hypothesis 3: Priority Queue with Dynamic Scaling
**Concept**: Replace scheduler with priority-based dynamic queue
- ✅ Pros: Better resource utilization
- ❌ Cons: Complex implementation, still has limits, violates KISS

### Hypothesis 4: Event Router Pattern
**Concept**: Central router distributing to specialized worker pools
- ✅ Pros: Dedicated paths, natural backpressure
- ❌ Cons: Major architectural change, complex routing logic

### Hypothesis 5: Hybrid Approach (Selected) ✅
**Concept**: Direct processing for SQS, maintain scheduler for HTTP
- ✅ Pros: Best of both worlds, simple, effective
- ✅ Cons: Minimal - just different execution patterns

## Proposed Solution: Hybrid Processing Architecture

### Core Design Principles

1. **Leverage Natural Queueing**: SQS is already a queue - don't queue twice
2. **Maintain Backward Compatibility**: HTTP path unchanged
3. **KISS Principle**: Simplest solution that solves the problem
4. **Fail-Safe Design**: No dropped events from queue overflow

### Architectural Changes

```
HTTP Webhooks ──> QueueAsyncScheduler ──> Workers ──> Handlers
                  (Unchanged)

SQS Messages ──> Worker Pool ──> Direct Handler Execution
                 (New Path)
```

### Implementation Components

#### 1. SQS Worker Pool Manager
```go
type WorkerPoolManager struct {
    pools    map[string]*WorkerPool  // Per event type
    handlers map[string]EventHandler
    metrics  *MetricsCollector
}

type WorkerPool struct {
    eventType   string
    workerCount int
    semaphore   chan struct{}  // Concurrency control
    handler     EventHandler
}
```

#### 2. Direct Event Processor
```go
func (p *Processor) ProcessMessageDirect(ctx context.Context, msg SQSMessage) error {
    // 1. Select handler based on source
    handler := p.selectHandler(msg)

    // 2. Acquire worker slot (backpressure)
    pool := p.getWorkerPool(msg.EventType)
    select {
    case pool.semaphore <- struct{}{}:
        defer func() { <-pool.semaphore }()
    case <-ctx.Done():
        return ctx.Err()
    }

    // 3. Execute handler directly
    return handler.Handle(ctx, msg.EventType, msg.DeliveryID, msg.Payload)
}
```

#### 3. Configuration Updates
```yaml
sqs:
  processing_mode: "direct"  # New: "direct" or "scheduled"

  worker_pools:  # Replaces queue_workers
    pull_request:
      workers: 10
      buffer_size: 20  # Small buffer for smoothing
    status:
      workers: 25      # High volume
      buffer_size: 50
    installation:
      workers: 2       # Low volume
      buffer_size: 5
```

### Key Design Decisions

#### 1. Worker Pool per Event Type
- **Rationale**: Different events have different volumes and processing times
- **Benefit**: Prevents high-volume events from starving others
- **Implementation**: Dedicated semaphore-controlled pools

#### 2. Semaphore-Based Backpressure
- **Rationale**: Natural flow control without dropping events
- **Benefit**: SQS visibility timeout handles retry if processing is slow
- **Implementation**: Blocking on semaphore acquisition

#### 3. Maintain HTTP Scheduler
- **Rationale**: HTTP webhooks need burst absorption
- **Benefit**: No changes to existing HTTP flow
- **Implementation**: Keep `QueueAsyncScheduler` for HTTP only

#### 4. Unified Metrics Collection
- **Rationale**: Consistent observability across both paths
- **Benefit**: Single dashboard for monitoring
- **Implementation**: Shared metrics registry with path labels

### Edge Case Handling

#### 1. Worker Pool Exhaustion
```go
// If all workers busy, SQS visibility timeout ensures retry
if !pool.TryAcquire(timeout) {
    metrics.RecordBackpressure(eventType)
    return ErrWorkerPoolBusy  // Message stays in SQS
}
```

#### 2. Handler Panics
```go
func safeExecute(handler EventHandler, ...) (err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("handler panic: %v", r)
            metrics.RecordPanic(eventType)
        }
    }()
    return handler.Handle(...)
}
```

#### 3. Graceful Shutdown
```go
func (m *WorkerPoolManager) Shutdown(ctx context.Context) error {
    // 1. Stop accepting new work
    for _, pool := range m.pools {
        close(pool.semaphore)
    }

    // 2. Wait for in-flight work with timeout
    done := make(chan struct{})
    go func() {
        m.waitGroup.Wait()
        close(done)
    }()

    select {
    case <-done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

## Migration Strategy

### Phase 1: Parallel Testing (Week 1-2)
1. Implement worker pool manager alongside existing scheduler
2. Add feature flag for processing mode
3. Deploy with "scheduled" mode (no changes)

### Phase 2: Gradual Rollout (Week 3-4)
1. Enable "direct" mode for low-volume event types
2. Monitor metrics for performance comparison
3. Gradually migrate high-volume events

### Phase 3: Full Migration (Week 5)
1. Switch all SQS events to direct processing
2. Reduce scheduler queue size (only needs HTTP capacity)
3. Update monitoring dashboards

## Benefits Analysis

### 1. Eliminates Dropped Events
- SQS provides infinite queue depth (within AWS limits)
- No more silent event loss
- Visibility timeout ensures retry on failure

### 2. Better Resource Utilization
- Workers dedicated to event types
- No wasted cycles on queue management
- Direct execution path reduces latency

### 3. Natural Backpressure
- SQS handles flow control
- Messages stay in queue when workers busy
- No need for complex buffering logic

### 4. Improved Observability
- Clear separation of concerns
- Per-event-type worker pool metrics
- Easier to identify bottlenecks

### 5. Scalability
- Can scale worker pools independently
- Add more SQS consumers horizontally
- No shared state between consumers

## Metrics and Monitoring

### New Metrics
```prometheus
# Worker pool utilization
sqs_worker_pool_active_workers{event_type="pull_request"}
sqs_worker_pool_capacity{event_type="pull_request"}
sqs_worker_pool_rejected_total{event_type="pull_request"}

# Processing latency comparison
event_processing_duration_seconds{source="sqs", mode="direct"}
event_processing_duration_seconds{source="http", mode="scheduled"}

# Backpressure events
sqs_backpressure_events_total{event_type="status"}
```

### Success Criteria
1. **Zero dropped events** from queue overflow
2. **P99 processing latency** < 2 seconds
3. **Worker utilization** between 60-80%
4. **SQS queue depth** remains manageable
5. **Memory usage** reduced by ~20%

## Risk Mitigation

### Risk 1: Different Execution Patterns
- **Mitigation**: Comprehensive testing, gradual rollout
- **Monitoring**: A/B metrics comparison

### Risk 2: Worker Pool Sizing
- **Mitigation**: Start conservative, auto-scaling logic
- **Monitoring**: Utilization metrics and alerts

### Risk 3: Hidden Dependencies on Scheduler
- **Mitigation**: Code audit, maintain scheduler interface
- **Monitoring**: Error rates during migration

## Alternative Considerations

### Why Not Use AWS Lambda?
- Need persistent connections to GitHub
- Warm start requirements
- Cost at high volume
- Existing infrastructure investment

### Why Not Use Temporal/Cadence?
- Overkill for simple event processing
- Additional infrastructure complexity
- Learning curve for team
- Not solving core problem

### Why Not Increase Queue Size?
- Just delays the problem
- Memory constraints
- Still drops events eventually
- Doesn't leverage SQS capabilities

## Conclusion

The proposed hybrid architecture solves the event dropping problem by:
1. **Eliminating redundant queueing** for SQS events
2. **Leveraging SQS's natural queueing** capabilities
3. **Maintaining backward compatibility** for HTTP webhooks
4. **Following KISS principle** with minimal changes
5. **Providing clear scalability path** for future growth

This approach requires minimal code changes, reduces complexity, and provides immediate benefits while setting up for future scaling needs.

## Appendix: Code Snippets

### Worker Pool Implementation
```go
type WorkerPool struct {
    eventType string
    workers   int
    semaphore chan struct{}
    handler   githubapp.EventHandler
    metrics   *PoolMetrics
}

func NewWorkerPool(eventType string, workers int, handler githubapp.EventHandler) *WorkerPool {
    return &WorkerPool{
        eventType: eventType,
        workers:   workers,
        semaphore: make(chan struct{}, workers),
        handler:   handler,
        metrics:   NewPoolMetrics(eventType),
    }
}

func (p *WorkerPool) Process(ctx context.Context, msg SQSMessage) error {
    // Acquire worker
    timer := p.metrics.StartTimer()
    select {
    case p.semaphore <- struct{}{}:
        p.metrics.RecordAcquired()
    case <-time.After(5 * time.Second):
        p.metrics.RecordTimeout()
        return ErrWorkerPoolTimeout
    case <-ctx.Done():
        return ctx.Err()
    }

    defer func() {
        <-p.semaphore
        timer.ObserveDuration()
    }()

    // Execute handler
    p.metrics.RecordActive()
    defer p.metrics.RecordComplete()

    return p.handler.Handle(ctx, msg.EventType, msg.DeliveryID, msg.Payload)
}
```

### Modified Processor
```go
func (p *Processor) ProcessMessage(ctx context.Context, eventType, queueURL string, message types.Message) error {
    sqsMsg, err := p.parseMessage(eventType, message)
    if err != nil {
        return err
    }

    // NEW: Direct processing path
    if p.config.ProcessingMode == "direct" {
        pool := p.poolManager.GetPool(sqsMsg.EventType)
        if pool == nil {
            return fmt.Errorf("no worker pool for event type: %s", sqsMsg.EventType)
        }

        err = pool.Process(ctx, sqsMsg)
        if err != nil {
            p.logger.Error().Err(err).Msg("Failed to process message directly")
            return err
        }

        return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, p.logger)
    }

    // LEGACY: Scheduler path (kept for compatibility)
    return p.processViaScheduler(ctx, sqsMsg, queueURL, message)
}
```

### Configuration Example
```yaml
sqs:
  enabled: true
  processing_mode: "direct"

  worker_pools:
    pull_request:
      workers: 15
      timeout: "30s"
      max_retries: 3

    pull_request_review:
      workers: 10
      timeout: "20s"
      max_retries: 3

    status:
      workers: 30
      timeout: "10s"
      max_retries: 2

    workflow_run:
      workers: 20
      timeout: "15s"
      max_retries: 3

# HTTP still uses scheduler
workers:
  workers: 10
  queue_size: 50  # Reduced - only for HTTP now
```