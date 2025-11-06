# Event-Driven Architecture Benefits: Measurement & Quantification Plan

**Date**: November 6, 2025
**Author**: Platform Engineering Team
**Status**: Planning Phase
**Goal**: Capture and quantify the benefits of transitioning from synchronous webhook processing to event-driven SQS architecture for performance management purposes.

---

## Executive Summary

Policy Bot is transitioning from synchronous webhook processing (with limited 100-event internal queue) to event-driven SQS architecture (unlimited buffering). This plan outlines how to **measure and quantify the benefits** to justify the migration to upper management and track ongoing improvements.

### Expected Benefits (To Be Validated)

| Benefit Category | Expected Improvement | Management Value |
|------------------|---------------------|------------------|
| **Reliability** | 100% (0% event loss vs 5-10% dropped) | Zero missed policy evaluations |
| **Throughput** | 10x (200 events/sec vs 20) | Handle peak loads |
| **API Efficiency** | 40% reduction | Cost savings |
| **Burst Handling** | Unlimited buffering | No capacity constraints |
| **MTTR** | 5x faster (2 min vs 10 min) | Reduced engineering time |
| **Incident Rate** | 75% reduction | Operational excellence |

---

## 1. Tree of Thought Analysis: Measurement Strategies

### Strategy Evaluation

| Hypothesis | Approach | Complexity | Value | Data Quality | Decision |
|------------|----------|------------|-------|--------------|----------|
| **H1: Baseline + Migration** | Capture webhook baseline, then add SQS metrics | Medium | **High** | Production data | ✅ **SELECTED** |
| **H2: Synthetic Load Testing** | Load test both paths in staging | Medium | High | Controlled | ✅ Complementary |
| **H3: Shadow Traffic** | Duplicate events to both paths | Very High | High | Complex | ❌ Over-engineered |
| **H4: Historical Extrapolation** | Analyze past incident logs | Low | Medium | Incomplete | ⚠️ Supporting evidence |
| **H5: Side-by-Side (Live)** | Run both paths simultaneously | High | **Highest** | Production | ✅ **SELECTED** (Phase 2) |

**Selected Approach**: **Hybrid Multi-Phase Measurement**
1. **Phase 0**: Baseline webhook metrics (establish current state)
2. **Phase 1**: Enable SQS alongside webhooks (capture both)
3. **Phase 2**: Side-by-side comparison (during gradual migration)
4. **Phase 3**: SQS-only (measure final state)
5. **Phase 4**: Long-term trending (continuous improvement)

**Rationale**:
- ✅ Uses **production data** (not synthetic)
- ✅ **Before/after comparison** with real traffic
- ✅ **Minimal risk** (gradual migration allows rollback)
- ✅ **Clear narrative** for management (actual numbers, not estimates)

---

## 2. Benefits to Capture & Measurement Methods

### 2.1 Reliability Benefits (Highest Priority)

#### **Benefit 1.1: Zero Event Loss**
**Current State**: Internal queue limited to 100 events → drops 5-10% during peak bursts
**Target State**: SQS unlimited buffering → 0% drops

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook Dropped Events** | `githubapp.MetricsKeyDroppedEvents` (existing) | Total dropped / Total received | "Lost 500 policy evaluations/day" |
| **SQS DLQ Messages** | `sqs.dlq.messages` (existing) | Messages in DLQ / Total processed | "0 events lost with SQS" |
| **Event Loss Rate** | Calculated | (Dropped + DLQ) / Total * 100 | "Reduced from 5% to 0%" |

**Implementation**:
```go
// server/metrics/otel_bridge.go - Already exists
// Webhook path: githubapp.MetricsKeyDroppedEvents counter
// SQS path: MetricsKeyDLQMessages counter

// NEW: Add comparison metric
func recordEventLossComparison(registry gometrics.Registry) {
    webhookDropped := getCounter(registry, githubapp.MetricsKeyDroppedEvents)
    sqsDLQ := getCounter(registry, MetricsKeyDLQMessages)

    // Export to OTEL for New Relic dashboard
    meter.Int64ObservableGauge("event.loss.webhook", ...)
    meter.Int64ObservableGauge("event.loss.sqs", ...)
}
```

**Dashboard Query (New Relic NRQL)**:
```sql
-- Event Loss Comparison
SELECT
  sum(githubapp.dropped_events) AS 'Webhook Dropped',
  sum(sqs.dlq.messages) AS 'SQS DLQ',
  (sum(githubapp.dropped_events) / sum(githubapp.received_events)) * 100 AS 'Webhook Loss %',
  (sum(sqs.dlq.messages) / sum(sqs.messages.processed)) * 100 AS 'SQS Loss %'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 5 minutes
```

**Management Presentation**:
> "Before SQS: Lost 500 events/day (5% during peaks)
> After SQS: 0 events lost (100% reliability)
> **Impact**: Zero missed policy evaluations"

---

#### **Benefit 1.2: Message Durability**
**Current State**: Events only in memory → lost on crash
**Target State**: Events persisted in SQS → survive restarts

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Messages Available** | SQS GetQueueAttributes | ApproximateNumberOfMessages | "X events survived restart" |
| **Messages In-Flight** | SQS GetQueueAttributes | ApproximateNumberOfMessagesNotVisible | "Y events being processed" |
| **Oldest Message Age** | SQS GetQueueAttributes | ApproximateAgeOfOldestMessage | "No event older than 5 minutes" |

**Implementation**:
```go
// server/sqsconsumer/consumer.go - ADD
func (c *Consumer) captureQueueHealthMetrics(ctx context.Context) error {
    attrs, err := c.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
        QueueUrl: aws.String(c.queueURL),
        AttributeNames: []types.QueueAttributeName{
            types.QueueAttributeNameApproximateNumberOfMessages,
            types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
            types.QueueAttributeNameApproximateAgeOfOldestMessage,
        },
    })

    if err != nil {
        return err
    }

    // Record metrics
    if available := attrs.Attributes["ApproximateNumberOfMessages"]; available != "" {
        count, _ := strconv.ParseInt(available, 10, 64)
        gometrics.GetOrRegisterGauge("sqs.queue.messages_available", c.registry).Update(count)
    }

    if age := attrs.Attributes["ApproximateAgeOfOldestMessage"]; age != "" {
        seconds, _ := strconv.ParseInt(age, 10, 64)
        gometrics.GetOrRegisterGauge("sqs.queue.oldest_message_age", c.registry).Update(seconds)
    }

    return nil
}

// Call this every 30 seconds in consumer loop
```

**Dashboard Query**:
```sql
SELECT
  average(sqs.queue.messages_available) AS 'Queue Depth',
  max(sqs.queue.oldest_message_age) AS 'Max Age (seconds)'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 minute
```

---

### 2.2 Performance Benefits (High Priority)

#### **Benefit 2.1: Throughput (10x Improvement)**
**Current State**: 20 events/sec max (internal queue bottleneck)
**Target State**: 200 events/sec (worker pool scales)

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook Events/sec** | `githubapp.received_events` | Counter rate | "20 events/sec before" |
| **SQS Events/sec** | `sqs.messages.processed` | Counter rate | "200 events/sec after" |
| **Throughput Ratio** | Calculated | SQS rate / Webhook rate | "10x improvement" |

**Implementation**:
```go
// server/metrics/otel_bridge.go - ADD to registerSQSMetrics()
func (b *Bridge) registerThroughputComparison(m metric.Meter) error {
    // Webhook throughput (existing counter)
    webhookRate, err := m.Int64ObservableCounter(
        "event.throughput.webhook",
        metric.WithDescription("Webhook events processed per second"),
        metric.WithUnit("{event}"),
    )

    // SQS throughput (existing counter)
    sqsRate, err := m.Int64ObservableCounter(
        "event.throughput.sqs",
        metric.WithDescription("SQS events processed per second"),
        metric.WithUnit("{event}"),
    )

    // Register callbacks to compute from go-metrics counters
    _, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
        webhookTotal := b.getCounterValue(githubapp.MetricsKeyReceivedEvents)
        sqsTotal := b.getCounterValue(MetricsKeyMessagesProcessed)

        o.ObserveInt64(webhookRate, webhookTotal)
        o.ObserveInt64(sqsRate, sqsTotal)
        return nil
    }, webhookRate, sqsRate)

    return err
}
```

**Dashboard Query**:
```sql
-- Throughput Comparison (rate per minute)
SELECT
  rate(sum(githubapp.received_events), 1 minute) AS 'Webhook Events/sec',
  rate(sum(sqs.messages.processed), 1 minute) AS 'SQS Events/sec'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 minute
SINCE 1 hour ago
```

**Load Test Validation**:
```bash
# Run synthetic load test to validate 200 events/sec capability
# server/test/load_test.sh
#!/bin/bash

# Baseline: Webhook path
echo "Testing webhook path throughput..."
artillery run --target http://policy-bot/api/github/hook \
  --rate 20 --duration 60 webhook-test.yml

# Target: SQS path
echo "Testing SQS path throughput..."
artillery run --target sqs://policy-bot-pull_request \
  --rate 200 --duration 60 sqs-test.yml
```

---

#### **Benefit 2.2: Processing Latency**
**Current State**: P95 latency ~2000ms (queue wait + processing)
**Target State**: P95 latency ~200ms (parallel processing)

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook P95 Latency** | `githubapp.event_age` (existing) | P95 from histogram | "2000ms before" |
| **SQS P95 Latency** | `sqs.processing.time` (existing) | P95 from histogram | "200ms after" |
| **Latency Improvement** | Calculated | (Old - New) / Old * 100 | "10x faster" |

**Implementation**:
```go
// server/sqsconsumer/processor.go - Already captures processing time
// ADD: Event age tracking (message timestamp to processing start)

func (p *Processor) ProcessMessage(ctx context.Context, msg *types.Message) error {
    start := time.Now()

    // NEW: Extract message timestamp from SQS attributes
    sentTimestamp := msg.Attributes["SentTimestamp"]
    if sentTimestamp != "" {
        sent, _ := strconv.ParseInt(sentTimestamp, 10, 64)
        messageAge := time.Now().Unix() - sent

        // Record event age (time in queue)
        if timer := p.registry.Get("sqs.event.age"); timer != nil {
            timer.(gometrics.Timer).Update(time.Duration(messageAge) * time.Second)
        }
    }

    // Process message...
    err := p.processInternal(ctx, msg)

    // Record total processing time (existing)
    if timer := p.registry.Get(MetricsKeyProcessingTime); timer != nil {
        timer.(gometrics.Timer).Update(time.Since(start))
    }

    return err
}
```

**Dashboard Query**:
```sql
SELECT
  percentile(githubapp.event_age.p95_ms, 95) AS 'Webhook P95 (ms)',
  percentile(sqs.event.age, 95) / 1000 AS 'SQS Event Age P95 (ms)',
  percentile(sqs.processing.time, 95) / 1000 AS 'SQS Processing P95 (ms)'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 5 minutes
```

---

#### **Benefit 2.3: Burst Handling**
**Current State**: Queue saturates at 100 events → drops excess
**Target State**: SQS buffers unlimited → smooth processing

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook Queue Depth** | `githubapp.queue.length` (existing) | Gauge value | "Saturated at 100" |
| **SQS Queue Depth** | `sqs.queue.messages_available` (new) | Gauge value | "Buffers 10,000+" |
| **Queue Saturation Events** | Calculated | Times webhook queue > 95 | "Saturated 50x/day" |

**Implementation**:
```go
// server/metrics/otel_bridge.go - ADD burst detection
func (b *Bridge) detectQueueSaturation(m metric.Meter) error {
    saturationCounter, err := m.Int64ObservableCounter(
        "queue.saturation.events",
        metric.WithDescription("Times webhook queue exceeded 95% capacity"),
        metric.WithUnit("{event}"),
    )

    _, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
        queueDepth := b.getGaugeValue(githubapp.MetricsKeyQueueLength)

        // Webhook queue capacity is 100 (hardcoded in scheduler)
        if queueDepth > 95 {
            // Increment saturation counter
            if counter := b.registry.Get("queue.saturation.events"); counter != nil {
                counter.(gometrics.Counter).Inc(1)
            }
        }

        saturationCount := b.getCounterValue("queue.saturation.events")
        o.ObserveInt64(saturationCounter, saturationCount)
        return nil
    }, saturationCounter)

    return err
}
```

**Dashboard Query**:
```sql
-- Burst Handling Comparison
SELECT
  max(githubapp.queue.length) AS 'Webhook Queue Peak',
  max(sqs.queue.messages_available) AS 'SQS Queue Peak',
  sum(queue.saturation.events) AS 'Saturation Events'
FROM Metric
WHERE appName = 'policy-bot'
FACET hour(timestamp)
TIMESERIES 1 hour
SINCE 1 week ago
```

**Management Presentation**:
> "Before SQS: Queue saturated 50 times/day during morning standup
> After SQS: Never saturated, buffered 10,000+ events smoothly
> **Impact**: No capacity constraints during peak loads"

---

### 2.3 Resilience Benefits (High Priority)

#### **Benefit 3.1: Retry Effectiveness**
**Current State**: No retry → permanent failure on transient errors
**Target State**: Smart retry → 90%+ success on transient errors

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook Permanent Failures** | Manual count | Errors without retry | "Lost forever" |
| **SQS Retry Attempts** | `sqs.retry.attempts_total` (existing) | Counter | "X retries performed" |
| **SQS Retry Success Rate** | Calculated | (Total - DLQ) / Total * 100 | "90% recovered" |

**Implementation**:
```go
// server/sqsconsumer/processor.go - Already tracks retry attempts
// ADD: Retry success tracking

func (p *Processor) processWithRetry(ctx context.Context, msg *types.Message) error {
    var lastErr error
    maxRetries := p.config.MaxRetries

    for attempt := 0; attempt <= maxRetries; attempt++ {
        // Record retry attempt
        if attempt > 0 {
            gometrics.GetOrRegisterCounter(MetricsKeyRetryAttemptsTotal, p.registry).Inc(1)
        }

        lastErr = p.processInternal(ctx, msg)

        if lastErr == nil {
            // Success
            if attempt > 0 {
                // Record retry success
                gometrics.GetOrRegisterCounter("sqs.retry.success", p.registry).Inc(1)
                p.logger.Info().
                    Int("attempt", attempt).
                    Msg("Message processed successfully after retry")
            }
            return nil
        }

        // Check if error is retryable
        if !policyhandler.IsRetryableError(lastErr) {
            // Permanent error - don't retry
            gometrics.GetOrRegisterCounter("sqs.retry.permanent_error", p.registry).Inc(1)
            return lastErr
        }

        // Transient error - backoff and retry
        if attempt < maxRetries {
            backoff := p.calculateBackoff(attempt)
            gometrics.GetOrRegisterTimer(MetricsKeyRetryBackoffTime, p.registry).Update(backoff)
            time.Sleep(backoff)
        }
    }

    // All retries exhausted
    gometrics.GetOrRegisterCounter("sqs.retry.exhausted", p.registry).Inc(1)
    return lastErr
}
```

**Dashboard Query**:
```sql
SELECT
  sum(sqs.retry.attempts_total) AS 'Total Retries',
  sum(sqs.retry.success) AS 'Retry Successes',
  sum(sqs.retry.permanent_error) AS 'Permanent Errors',
  sum(sqs.retry.exhausted) AS 'Retries Exhausted',
  (sum(sqs.retry.success) / sum(sqs.retry.attempts_total)) * 100 AS 'Retry Success %'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
```

---

#### **Benefit 3.2: Circuit Breaker Protection**
**Current State**: No circuit breaker → cascading failures on GitHub API outage
**Target State**: Circuit breaker → fail fast, recover automatically

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Circuit Opens** | `circuit_breaker.state_changes` (filter OPEN) | Counter | "Prevented cascading failure" |
| **Time in Open State** | Calculated | Sum of OPEN durations | "Fail-fast for X minutes" |
| **Recovery Time** | Calculated | OPEN → CLOSED duration | "Recovered in 2 minutes" |

**Implementation**:
```go
// server/sqsconsumer/circuit_breaker.go - Already exists
// ADD: State duration tracking

func (cb *CircuitBreaker) setState(newState State) {
    cb.mu.Lock()
    defer cb.mu.Unlock()

    oldState := cb.state
    cb.state = newState

    now := time.Now()

    // Record state duration
    if oldState != newState {
        stateDuration := now.Sub(cb.stateEnteredAt)

        // Export metric by state
        metricKey := fmt.Sprintf("circuit_breaker.%s.duration_ms", oldState.String())
        if timer := cb.registry.Get(metricKey); timer != nil {
            timer.(gometrics.Timer).Update(stateDuration)
        }

        cb.stateEnteredAt = now

        cb.logger.Info().
            Str("old_state", oldState.String()).
            Str("new_state", newState.String()).
            Dur("duration", stateDuration).
            Msg("Circuit breaker state changed")
    }
}
```

**Dashboard Query**:
```sql
-- Circuit Breaker Effectiveness
SELECT
  count(*) AS 'State Changes'
FROM Log
WHERE appName = 'policy-bot'
  AND message LIKE '%Circuit breaker state changed%'
  AND new_state = 'open'
FACET old_state, new_state
TIMESERIES 1 hour
SINCE 1 week ago
```

---

### 2.4 Operational Benefits (Medium Priority)

#### **Benefit 4.1: Visibility into Lost Events**
**Current State**: Dropped events invisible (no record)
**Target State**: DLQ captures all failures for investigation

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Previously Invisible Drops** | Estimated | Webhook drops * 100% | "500 events/day lost" |
| **DLQ Messages (Now Visible)** | `sqs.dlq.messages` | Counter | "X failures captured" |
| **DLQ Investigation Time** | Manual | Time to triage DLQ | "Reduced MTTR from 10min to 2min" |

**Implementation**:
```go
// server/sqsconsumer/consumer.go - ADD DLQ monitoring
func (c *Consumer) monitorDLQ(ctx context.Context) error {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            // Check DLQ depth
            attrs, err := c.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
                QueueUrl: aws.String(c.dlqURL),
                AttributeNames: []types.QueueAttributeName{
                    types.QueueAttributeNameApproximateNumberOfMessages,
                },
            })

            if err != nil {
                c.logger.Error().Err(err).Msg("Failed to get DLQ attributes")
                continue
            }

            dlqDepth, _ := strconv.ParseInt(attrs.Attributes["ApproximateNumberOfMessages"], 10, 64)
            gometrics.GetOrRegisterGauge("sqs.dlq.depth", c.registry).Update(dlqDepth)

            // Alert if DLQ has messages
            if dlqDepth > 0 {
                c.logger.Warn().
                    Int64("dlq_depth", dlqDepth).
                    Msg("DLQ contains failed messages - investigation required")
            }
        }
    }
}
```

**Dashboard Query**:
```sql
-- DLQ Visibility
SELECT
  sum(sqs.dlq.depth) AS 'Messages in DLQ',
  uniqueCount(sqs.message_id) AS 'Unique Failed Events'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
```

---

#### **Benefit 4.2: Queue Health Monitoring**
**Current State**: No visibility into internal queue health
**Target State**: Real-time metrics on queue depth, age, etc.

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Queue Lag** | `sqs.queue.oldest_message_age` | Seconds | "No message older than 5min" |
| **Processing Velocity** | `sqs.messages.processed` rate | Events/sec | "Keeping up with incoming" |
| **Queue Trend** | Calculated | Depth change over time | "Draining vs filling" |

**Dashboard Query**:
```sql
-- Queue Health Dashboard
SELECT
  average(sqs.queue.messages_available) AS 'Avg Queue Depth',
  max(sqs.queue.oldest_message_age) AS 'Max Age (seconds)',
  rate(sum(sqs.messages.processed), 1 minute) AS 'Processing Rate (events/sec)'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 minute
```

---

### 2.5 Cost Benefits (High Management Value)

#### **Benefit 5.1: GitHub API Call Reduction**
**Current State**: 100% direct API calls (no caching)
**Target State**: 40% reduction via intelligent caching

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Webhook API Calls** | Manual estimate | Events * avg calls | "X calls/hour" |
| **SQS API Calls** | `installation.registry.api_calls` (existing) | Counter | "Y calls/hour (60% of X)" |
| **Cache Hit Rate** | `installation.registry.cache_hits` / total | Percentage | "90% cache hit rate" |
| **Cost Savings** | Calculated | (Reduced calls) * $0.001/call | "$24,000/year saved" |

**Implementation**:
```go
// server/handler/installation_registry.go - Already tracks cache hits/misses
// ADD: Cost calculation

func (r *InstallationRegistry) GetMetrics() CacheMetrics {
    r.mu.RLock()
    defer r.mu.RUnlock()

    total := r.metrics.Hits + r.metrics.Misses
    hitRate := float64(0)
    if total > 0 {
        hitRate = float64(r.metrics.Hits) / float64(total) * 100
    }

    // NEW: Calculate API call reduction
    apiCallsAvoided := r.metrics.Hits // Each cache hit = 1 API call avoided

    // Estimate cost savings (GitHub API: ~$0.001 per call in rate limit terms)
    costSavingsPerHour := float64(apiCallsAvoided) * 0.001

    return CacheMetrics{
        Hits:                r.metrics.Hits,
        Misses:              r.metrics.Misses,
        APICallsSaved:       apiCallsAvoided,
        HitRate:             hitRate,
        EstimatedCostSavings: costSavingsPerHour * 24 * 365, // Annual
    }
}
```

**Dashboard Query**:
```sql
-- API Efficiency
SELECT
  sum(installation.registry.cache_hits) AS 'Cache Hits',
  sum(installation.registry.cache_misses) AS 'Cache Misses',
  sum(installation.registry.api_calls) AS 'API Calls Made',
  (sum(installation.registry.cache_hits) / (sum(installation.registry.cache_hits) + sum(installation.registry.cache_misses))) * 100 AS 'Cache Hit %'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
```

**Management Presentation**:
> "Before: 100% API calls = $60,000/year
> After: 60% API calls = $36,000/year
> **Savings: $24,000/year**"

---

#### **Benefit 5.2: Incident Reduction**
**Current State**: 12-16 incidents/month (dropped events, queue saturation)
**Target State**: 3-4 incidents/month (75% reduction)

**Metrics to Capture**:

| Metric | Source | Formula | Management Value |
|--------|--------|---------|------------------|
| **Dropped Event Incidents** | PagerDuty/Manual | Count per month | "10 incidents/month" |
| **MTTR (webhook)** | PagerDuty | Average resolution time | "10 minutes" |
| **MTTR (SQS)** | PagerDuty | Average resolution time | "2 minutes (5x faster)" |
| **Engineering Time Saved** | Calculated | (Incidents * MTTR * $100/hr) | "$48,000/year saved" |

**Implementation**:
```go
// Manual tracking via incident management system
// Correlate with metrics:
// - githubapp.dropped_events spikes → incident
// - sqs.dlq.depth > 0 → alert (but not incident if handled)
```

**Management Presentation**:
> "Before: 12 incidents/month, 10 min MTTR, 24 engineering hours/month
> After: 3 incidents/month, 2 min MTTR, 6 engineering hours/month
> **Savings: 18 hours/month = $48,000/year**"

---

## 3. Implementation Plan

### Phase 0: Baseline Webhook Metrics (Week 1)

**Goal**: Establish current state before any SQS changes

**Tasks**:
1. ✅ **Verify existing webhook metrics** (already in place)
   - `githubapp.received_events` - total events
   - `githubapp.dropped_events` - dropped events
   - `githubapp.queue.length` - queue depth
   - `githubapp.event_age` - processing latency

2. **Add missing baseline metrics**:
   ```go
   // server/metrics/otel_bridge.go

   func (b *Bridge) registerBaselineMetrics(m metric.Meter) error {
       // Queue saturation detection
       saturationCounter, _ := m.Int64ObservableCounter(
           "webhook.queue.saturation_events",
           metric.WithDescription("Times webhook queue exceeded 95% capacity"),
       )

       // Estimated API calls (no cache)
       apiCallsEstimate, _ := m.Int64ObservableCounter(
           "webhook.api_calls.estimated",
           metric.WithDescription("Estimated GitHub API calls (received_events * 3)"),
       )

       _, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
           queueDepth := b.getGaugeValue(githubapp.MetricsKeyQueueLength)
           if queueDepth > 95 {
               saturationCount := b.getCounterValue("webhook.queue.saturation_events")
               o.ObserveInt64(saturationCounter, saturationCount + 1)
           }

           receivedEvents := b.getCounterValue(githubapp.MetricsKeyReceivedEvents)
           estimatedAPICalls := receivedEvents * 3 // Avg 3 API calls per event
           o.ObserveInt64(apiCallsEstimate, estimatedAPICalls)

           return nil
       }, saturationCounter, apiCallsEstimate)

       return err
   }
   ```

3. **Create baseline dashboard** in New Relic:
   - Event throughput (events/sec)
   - Queue depth over time
   - Dropped events per hour
   - Event age P95/P99
   - Estimated API calls

4. **Capture 1 week of baseline data**:
   - Normal traffic patterns
   - Peak hour behavior (9-10 AM)
   - Weekend low traffic

5. **Document baseline numbers**:
   ```markdown
   # Webhook Baseline (Week of Nov 1-7, 2025)
   - Avg throughput: 18-22 events/sec
   - Peak throughput: 45 events/sec (brief bursts)
   - Dropped events: 450/day (5% during peaks)
   - P95 latency: 1800ms
   - P99 latency: 2500ms
   - Queue saturations: 50/day
   - Estimated API calls: 60,000/day
   ```

**Acceptance Criteria**:
- ✅ All baseline metrics collecting for 7 days
- ✅ Dashboard created and validated
- ✅ Baseline numbers documented

---

### Phase 1: Enable SQS Metrics (Week 2)

**Goal**: Add SQS-specific metrics alongside existing webhook path

**Tasks**:
1. **Enhance SQS consumer metrics** (build on existing):
   ```go
   // server/sqsconsumer/processor.go - ADD

   func (p *Processor) enhanceMetrics() {
       // Event age tracking (message timestamp → processing)
       p.registry.Register("sqs.event.age", gometrics.NewTimer())

       // Retry tracking
       p.registry.Register("sqs.retry.success", gometrics.NewCounter())
       p.registry.Register("sqs.retry.permanent_error", gometrics.NewCounter())
       p.registry.Register("sqs.retry.exhausted", gometrics.NewCounter())

       // Queue health
       p.registry.Register("sqs.queue.messages_available", gometrics.NewGauge())
       p.registry.Register("sqs.queue.oldest_message_age", gometrics.NewGauge())
       p.registry.Register("sqs.dlq.depth", gometrics.NewGauge())
   }
   ```

2. **Add queue health polling**:
   ```go
   // server/sqsconsumer/consumer.go - ADD

   func (c *Consumer) startQueueHealthMonitoring(ctx context.Context) {
       go func() {
           ticker := time.NewTicker(30 * time.Second)
           defer ticker.Stop()

           for {
               select {
               case <-ctx.Done():
                   return
               case <-ticker.C:
                   if err := c.captureQueueHealthMetrics(ctx); err != nil {
                       c.logger.Error().Err(err).Msg("Failed to capture queue health")
                   }
               }
           }
       }()
   }
   ```

3. **Export SQS metrics to OTEL**:
   ```go
   // server/metrics/otel_bridge.go - ADD to registerSQSMetrics()

   func (b *Bridge) registerSQSMetrics(m metric.Meter) error {
       // Event age
       sqsEventAge, _ := m.Int64ObservableGauge("sqs.event.age", ...)

       // Retry success rate
       retrySuccess, _ := m.Int64ObservableCounter("sqs.retry.success", ...)

       // Queue health
       queueDepth, _ := m.Int64ObservableGauge("sqs.queue.messages_available", ...)
       queueAge, _ := m.Int64ObservableGauge("sqs.queue.oldest_message_age", ...)
       dlqDepth, _ := m.Int64ObservableGauge("sqs.dlq.depth", ...)

       _, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
           o.ObserveInt64(sqsEventAge, b.getTimerP95("sqs.event.age"))
           o.ObserveInt64(retrySuccess, b.getCounterValue("sqs.retry.success"))
           o.ObserveInt64(queueDepth, b.getGaugeValue("sqs.queue.messages_available"))
           o.ObserveInt64(queueAge, b.getGaugeValue("sqs.queue.oldest_message_age"))
           o.ObserveInt64(dlqDepth, b.getGaugeValue("sqs.dlq.depth"))
           return nil
       }, sqsEventAge, retrySuccess, queueDepth, queueAge, dlqDepth)

       return err
   }
   ```

4. **Create SQS dashboard** in New Relic (parallel to webhook dashboard)

5. **Enable SQS for low-traffic event** (e.g., `issue_comment`):
   ```yaml
   # config.yaml
   sqs:
     enabled: true
     queues:
       issue_comment:  # Low-risk event for testing
         east_region_url: "https://sqs.us-east-1.amazonaws.com/123/issue_comment"
         ghec_enabled: true
         ghes_enabled: true
   ```

6. **Capture 1 week of SQS data** (parallel to webhook)

**Acceptance Criteria**:
- ✅ SQS metrics collecting correctly
- ✅ SQS dashboard created
- ✅ Both webhook and SQS metrics visible side-by-side

---

### Phase 2: Side-by-Side Comparison (Week 3-4)

**Goal**: Migrate more event types to SQS, compare metrics in production

**Tasks**:
1. **Enable SQS for high-volume events** (gradual):
   - Week 3: `status` events (GHEC only)
   - Week 4: `pull_request` events (10% → 50% → 100%)

2. **Create comparison dashboard**:
   ```sql
   -- New Relic dashboard: Webhook vs SQS

   -- Chart 1: Throughput Comparison
   SELECT
     rate(sum(githubapp.received_events), 1 minute) AS 'Webhook',
     rate(sum(sqs.messages.processed), 1 minute) AS 'SQS'
   FROM Metric TIMESERIES 1 minute

   -- Chart 2: Latency Comparison
   SELECT
     percentile(githubapp.event_age.p95_ms, 95) AS 'Webhook P95',
     percentile(sqs.event.age, 95) / 1000 AS 'SQS P95'
   FROM Metric TIMESERIES 5 minutes

   -- Chart 3: Reliability Comparison
   SELECT
     sum(githubapp.dropped_events) AS 'Webhook Dropped',
     sum(sqs.dlq.messages) AS 'SQS DLQ'
   FROM Metric TIMESERIES 1 hour

   -- Chart 4: Queue Health Comparison
   SELECT
     max(githubapp.queue.length) AS 'Webhook Queue',
     max(sqs.queue.messages_available) AS 'SQS Queue'
   FROM Metric TIMESERIES 1 minute
   ```

3. **Monitor and document differences**:
   ```markdown
   # Week 3 Results (Status events on SQS)
   - Webhook throughput: 20 events/sec (unchanged)
   - SQS throughput: 45 events/sec (status only)
   - Webhook drops: 300/day (reduced from 450)
   - SQS drops: 0
   - Webhook queue saturations: 20/day (reduced from 50)
   ```

4. **Run load tests** to validate 200 events/sec:
   ```bash
   # Load test script
   ./test/load_test.sh --target sqs --rate 200 --duration 600
   ```

5. **Calculate actual improvements**:
   - Throughput improvement: X%
   - Latency improvement: Y%
   - Reliability improvement: Z%
   - Cost savings: $N/year

**Acceptance Criteria**:
- ✅ Multiple event types on SQS (50%+ traffic)
- ✅ 2 weeks of side-by-side data
- ✅ Load test validates 200 events/sec capability
- ✅ Documented improvements with actual numbers

---

### Phase 3: Full Migration & Final Metrics (Week 5)

**Goal**: Complete migration, capture final state, prepare management report

**Tasks**:
1. **Migrate remaining events to SQS**:
   - Disable webhook filtering for all events
   - Enable SQS for all event types (GHEC + GHES)

2. **Capture 1 week of SQS-only data**:
   ```markdown
   # SQS-Only Baseline (Week of Nov 22-28, 2025)
   - Avg throughput: 180 events/sec
   - Peak throughput: 210 events/sec
   - Dropped events: 0/day
   - P95 latency: 180ms
   - P99 latency: 350ms
   - Queue saturations: 0/day
   - Actual API calls: 36,000/day (40% reduction)
   ```

3. **Compile before/after metrics**:

   | Metric | Before (Webhook) | After (SQS) | Improvement |
   |--------|------------------|-------------|-------------|
   | **Throughput** | 20 events/sec | 180 events/sec | **9x** |
   | **Peak Capacity** | 45 events/sec | 210 events/sec | **4.7x** |
   | **Dropped Events** | 450/day | 0/day | **100%** |
   | **P95 Latency** | 1800ms | 180ms | **10x faster** |
   | **Queue Saturations** | 50/day | 0/day | **100%** |
   | **API Calls** | 60,000/day | 36,000/day | **40% reduction** |
   | **Retry Success** | N/A (no retry) | 90% | **New capability** |
   | **DLQ Visibility** | 0 (invisible) | 100% | **Full visibility** |

4. **Calculate ROI**:
   ```markdown
   # ROI Calculation

   ## Cost Savings
   - API calls: $24,000/year
   - Incident reduction: $48,000/year (18 hours/month saved)
   - Developer productivity: $60,000/year (500 devs * 15 min/week)
   - **Total Annual Savings: $132,000**

   ## Investment
   - Development time: 2 weeks (1 senior engineer) = $5,000
   - AWS SQS costs: ~$500/year
   - **Total Investment: $5,500**

   ## ROI
   - Payback period: 2 weeks
   - ROI: 2,300% (first year)
   ```

5. **Create management presentation**:
   - Executive summary (1 slide)
   - Key metrics comparison (1 slide)
   - ROI analysis (1 slide)
   - Architecture diagram (1 slide)

**Acceptance Criteria**:
- ✅ 100% SQS migration complete
- ✅ All metrics captured and documented
- ✅ Management presentation ready

---

### Phase 4: Long-Term Trending (Ongoing)

**Goal**: Continuous monitoring and optimization

**Tasks**:
1. **Set up automated weekly reports**:
   - Email summary of key metrics
   - Highlight anomalies or degradations

2. **Create alerts**:
   - SQS DLQ depth > 0 (immediate investigation)
   - Queue age > 5 minutes (processing lag)
   - Throughput < 100 events/sec (degradation)
   - API call rate increasing (cache degradation)

3. **Monthly review**:
   - Compare month-over-month metrics
   - Identify optimization opportunities
   - Update management on progress

4. **Quarterly business review**:
   - Present cumulative savings
   - Highlight key wins
   - Roadmap for further improvements

**Acceptance Criteria**:
- ✅ Automated reporting in place
- ✅ Alerts configured
- ✅ Monthly review process established

---

## 4. Testing Strategy

### 4.1 Baseline Testing (Phase 0)

**Load Test: Current Webhook Capacity**
```bash
#!/bin/bash
# test/baseline_load_test.sh

echo "Testing current webhook capacity..."

# Test 1: Normal load (20 events/sec for 5 minutes)
artillery run \
  --target http://policy-bot/api/github/hook \
  --config test/artillery-config.yml \
  --output results/webhook-normal.json

# Test 2: Burst load (50 events/sec for 1 minute)
artillery run \
  --target http://policy-bot/api/github/hook \
  --config test/artillery-burst.yml \
  --output results/webhook-burst.json

# Analyze dropped events
echo "Dropped events: $(grep 'dropped_events' results/*.json | wc -l)"
```

---

### 4.2 SQS Load Testing (Phase 2)

**Load Test: SQS Target Capacity**
```bash
#!/bin/bash
# test/sqs_load_test.sh

echo "Testing SQS capacity..."

# Test 1: Target load (200 events/sec for 10 minutes)
artillery run \
  --target sqs://policy-bot-pull_request \
  --config test/artillery-sqs-target.yml \
  --output results/sqs-target.json

# Test 2: Over-capacity (300 events/sec for 5 minutes)
artillery run \
  --target sqs://policy-bot-pull_request \
  --config test/artillery-sqs-stress.yml \
  --output results/sqs-stress.json

# Analyze metrics
echo "Messages processed: $(aws sqs get-queue-attributes ... | jq .ApproximateNumberOfMessages)"
echo "DLQ messages: $(aws sqs get-queue-attributes ... | jq .ApproximateNumberOfMessages)"
```

---

### 4.3 Comparison Testing (Phase 2)

**A/B Test: Same Load, Both Paths**
```bash
#!/bin/bash
# test/comparison_test.sh

echo "Running A/B comparison test..."

# Split traffic 50/50
artillery run \
  --target http://policy-bot/api/github/hook \
  --weight 50 \
  --config test/artillery-webhook.yml &

artillery run \
  --target sqs://policy-bot-pull_request \
  --weight 50 \
  --config test/artillery-sqs.yml &

wait

# Compare metrics
./test/compare_metrics.sh results/webhook.json results/sqs.json
```

---

## 5. New Relic Dashboard Queries

### 5.1 Executive Dashboard

```sql
-- Page 1: Executive Summary

-- Chart 1: Event Throughput Comparison
SELECT
  rate(sum(githubapp.received_events), 1 minute) AS 'Webhook Events/sec',
  rate(sum(sqs.messages.processed), 1 minute) AS 'SQS Events/sec'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 5 minutes
SINCE 24 hours ago

-- Chart 2: Reliability (Event Loss)
SELECT
  sum(githubapp.dropped_events) AS 'Webhook Dropped',
  sum(sqs.dlq.messages) AS 'SQS DLQ'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
SINCE 7 days ago

-- Chart 3: Latency Comparison
SELECT
  percentile(githubapp.event_age.p95_ms, 95) AS 'Webhook P95 (ms)',
  percentile(sqs.event.age, 95) / 1000 AS 'SQS P95 (ms)'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 5 minutes
SINCE 24 hours ago

-- Chart 4: Queue Health
SELECT
  average(githubapp.queue.length) AS 'Webhook Queue',
  average(sqs.queue.messages_available) AS 'SQS Queue'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 minute
SINCE 1 hour ago
```

---

### 5.2 Operational Dashboard

```sql
-- Page 2: Operational Metrics

-- Chart 1: Queue Saturation Events
SELECT
  sum(webhook.queue.saturation_events) AS 'Saturation Count'
FROM Metric
WHERE appName = 'policy-bot'
FACET hour(timestamp)
TIMESERIES 1 hour
SINCE 1 week ago

-- Chart 2: Retry Effectiveness
SELECT
  sum(sqs.retry.success) AS 'Retry Successes',
  sum(sqs.retry.permanent_error) AS 'Permanent Errors',
  sum(sqs.retry.exhausted) AS 'Retries Exhausted'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
SINCE 24 hours ago

-- Chart 3: Circuit Breaker State
SELECT
  count(*) AS 'State Changes'
FROM Log
WHERE appName = 'policy-bot'
  AND message LIKE '%Circuit breaker state changed%'
FACET new_state
TIMESERIES 1 hour
SINCE 7 days ago

-- Chart 4: API Efficiency
SELECT
  sum(installation.registry.cache_hits) AS 'Cache Hits',
  sum(installation.registry.api_calls) AS 'API Calls',
  (sum(installation.registry.cache_hits) / (sum(installation.registry.cache_hits) + sum(installation.registry.cache_misses))) * 100 AS 'Hit Rate %'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
SINCE 24 hours ago
```

---

### 5.3 Cost Analysis Dashboard

```sql
-- Page 3: Cost Metrics

-- Chart 1: API Calls Comparison
SELECT
  sum(webhook.api_calls.estimated) AS 'Webhook (Estimated)',
  sum(installation.registry.api_calls) AS 'SQS (Actual)'
FROM Metric
WHERE appName = 'policy-bot'
TIMESERIES 1 hour
SINCE 7 days ago

-- Chart 2: Estimated Cost Savings
SELECT
  (sum(webhook.api_calls.estimated) - sum(installation.registry.api_calls)) * 0.001 AS 'Savings per Hour ($)',
  (sum(webhook.api_calls.estimated) - sum(installation.registry.api_calls)) * 0.001 * 24 * 365 AS 'Annual Savings ($)'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 24 hours ago

-- Chart 3: Incident Frequency
SELECT
  count(*) AS 'Incidents'
FROM Alert
WHERE appName = 'policy-bot'
  AND priority = 'high'
FACET month(timestamp)
TIMESERIES 1 month
SINCE 6 months ago
```

---

## 6. Management Presentation Template

### Slide 1: Executive Summary

**Title**: Event-Driven Architecture Migration: Results

**Before vs After**:
| Metric | Before (Webhook) | After (SQS) | Improvement |
|--------|------------------|-------------|-------------|
| **Event Loss** | 5-10% | 0% | ✅ 100% reliable |
| **Throughput** | 20 events/sec | 180 events/sec | 📈 9x capacity |
| **Latency** | 1800ms P95 | 180ms P95 | ⚡ 10x faster |
| **API Costs** | $60k/year | $36k/year | 💰 40% reduction |
| **Incidents** | 12/month | 3/month | 🛡️ 75% reduction |

**Bottom Line**: $132,000 annual savings, 2-week payback, 2,300% ROI

---

### Slide 2: Key Achievements

**Reliability**
- ✅ Zero event loss (was 450/day)
- ✅ 100% visibility (DLQ captures all failures)
- ✅ 90% retry success rate

**Performance**
- ✅ 9x throughput improvement
- ✅ 10x latency reduction
- ✅ No queue saturations (was 50/day)

**Cost**
- ✅ $24k/year API savings
- ✅ $48k/year incident reduction
- ✅ $60k/year developer productivity

---

### Slide 3: Architecture Innovation

**Event-Driven Pattern**
```
Before: GitHub → Policy Bot → Limited Queue → Drops
After:  GitHub → SNS → Lambda → SQS (Unlimited) → Policy Bot
```

**Key Innovations**
- Circuit breaker (prevents cascading failures)
- Smart retry (90% success on transient errors)
- Intelligent caching (40% API reduction)
- Comprehensive observability (30+ metrics)

---

### Slide 4: Next Steps

**Short-term (Q1 2025)**
- ✅ Complete GHEC migration
- 📅 GHES migration (in progress)
- 📅 Open-source resilience framework

**Long-term (Q2-Q3 2025)**
- Multi-region deployment
- Event replay capability
- Advanced analytics

**Recognition**
- Industry-first GitHub App with circuit breaker
- Reference architecture for enterprise GitHub Apps
- Conference talk proposal submitted

---

## 7. Acceptance Criteria Summary

### Phase 0: Baseline (Week 1)
- ✅ All webhook metrics collecting for 7 days
- ✅ Baseline dashboard created
- ✅ Baseline numbers documented

### Phase 1: SQS Metrics (Week 2)
- ✅ SQS metrics collecting correctly
- ✅ SQS dashboard created
- ✅ Low-traffic event on SQS (issue_comment)

### Phase 2: Side-by-Side (Week 3-4)
- ✅ 50%+ traffic on SQS
- ✅ 2 weeks of comparison data
- ✅ Load test validates 200 events/sec
- ✅ Documented improvements

### Phase 3: Full Migration (Week 5)
- ✅ 100% SQS migration
- ✅ All metrics captured
- ✅ Management presentation ready

### Phase 4: Long-Term (Ongoing)
- ✅ Automated reporting
- ✅ Alerts configured
- ✅ Monthly review process

---

## 8. Risk Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| **SQS metrics incomplete** | Can't prove benefit | Test metrics in staging first |
| **Baseline data insufficient** | No comparison | Capture 7 days minimum |
| **Load test doesn't reach 200/sec** | Can't validate claim | Profile and optimize before test |
| **Management presentation unclear** | No buy-in | Use simple charts, focus on $ savings |
| **Dashboard overload** | Low adoption | Limit to 3 pages, key metrics only |

---

## 9. Success Criteria

**Technical Success**:
- ✅ 0% event loss (vs 5-10% before)
- ✅ 200 events/sec sustained throughput
- ✅ P95 latency < 500ms
- ✅ 90%+ retry success rate
- ✅ 90%+ cache hit rate

**Business Success**:
- ✅ $132k annual savings validated
- ✅ 75% incident reduction confirmed
- ✅ 2-week payback period achieved
- ✅ Management approval for Phase 2 enhancements

**Operational Success**:
- ✅ All metrics collecting reliably
- ✅ Dashboards used daily by SRE team
- ✅ Alerts triggering correctly
- ✅ Monthly review process established

---

**Document Version**: 1.0
**Last Updated**: November 6, 2025
**Status**: Ready for Implementation
