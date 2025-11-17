# SQS Consumer Reliability Assessment & Improvement Plan

## 📊 Current State Analysis

### Overall Assessment
- **Test Coverage:** 86.9% ✅
- **All Tests Pass:** Yes ✅
- **Core Features:** Complete with circuit breakers, adaptive polling, retry logic, idempotency, DLQ monitoring, OpenTelemetry tracing

### Architecture Strengths
1. ✅ Circuit breakers for GHEC/GHES isolation
2. ✅ Exponential backoff with jitter for retries
3. ✅ Worker pools with panic recovery
4. ✅ Adaptive polling based on worker availability
5. ✅ Idempotency via LRU cache with TTL
6. ✅ DLQ monitoring with metrics
7. ✅ OpenTelemetry distributed tracing
8. ✅ Comprehensive metrics collection

---

## 🐛 Critical Issues Found

### Issue 1: Broken Circuit Breaker Error Detection (CRITICAL)
**File:** `server/sqsconsumer/processor.go:768-769, 834-835`
**Problem:** Comparing errors with `errors.New()` creates new error instances, so comparison always fails.

```go
// BROKEN CODE (lines 768-769, 834-835)
if err == errors.New("circuit breaker is open") || err == errors.New("too many requests") {
    // This NEVER executes - error comparison always false!
}
```

**Impact:** Circuit breaker rejection logging is completely broken. Operators have no visibility into when requests are rejected.

**Fix:**
```go
// Import gobreaker package
import "github.com/sony/gobreaker"

// Use exported error variables
if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
    logger.Warn().
        Str("environment", environment).
        Msg("Circuit breaker rejected request - system is unhealthy")
}
```

**Priority:** HIGH - This is a silent failure that masks system health issues.

---

### Issue 2: In-Memory Idempotency Loses State on Restart (MEDIUM)
**File:** `server/sqsconsumer/idempotency.go`
**Problem:** Idempotency cache is in-memory only. On pod restart/rollout, messages could be processed twice.

**Current Implementation:**
```go
type IdempotencyManager struct {
    cache    *lru.Cache[string, time.Time]  // In-memory LRU cache
    // ...
}
```

**Impact:** During deployments or pod restarts, duplicate message processing is possible.

**Recommendations:**
1. **Short-term:** Document this limitation in operational runbook
2. **Medium-term:** Consider Redis/DynamoDB for distributed idempotency (if SQS FIFO isn't available)
3. **Mitigation:** Make handlers idempotent themselves (best practice anyway)

**Priority:** MEDIUM - Deployments could cause duplicate processing, but handlers should be idempotent.

---

### Issue 3: No Periodic TTL Cleanup in Idempotency Cache (LOW)
**File:** `server/sqsconsumer/idempotency.go`
**Problem:** Expired entries are only cleaned up when checked, not proactively.

**Current Behavior:**
```go
func (im *IdempotencyManager) CheckAndMark(deliveryID string) bool {
    // Expired entries only cleaned when accessed
    if exists && now.Sub(processedAt) >= im.ttl {
        // Entry expired, treat as new
    }
}
```

**Impact:** Cache could hold stale entries until they're evicted by LRU or accessed.

**Fix (Optional):**
```go
// Add background cleanup goroutine
func (im *IdempotencyManager) startCleanupLoop(interval time.Duration) {
    go func() {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for range ticker.C {
            im.cleanupExpired()
        }
    }()
}
```

**Priority:** LOW - LRU eviction handles this naturally, and expired entries are checked on access.

---

### Issue 4: Worker Pool Timeout is Fixed (LOW)
**File:** `server/sqsconsumer/workerpool.go:114`
**Problem:** 5-second timeout for acquiring worker slot is hardcoded.

```go
case <-time.After(5 * time.Second):
    // Timeout acquiring worker - queue is full
```

**Impact:** Under burst traffic, messages may be rejected even though they could wait longer.

**Recommendation:** Make timeout configurable in WorkerPoolConfig.

**Priority:** LOW - 5 seconds is reasonable for most cases.

---

### Issue 5: Message Delete After Processing (ARCHITECTURAL)
**File:** `server/sqsconsumer/processor.go:436`
**Problem:** Message is deleted after successful processing. If delete fails, message reappears and could be reprocessed.

**Current Flow:**
```
1. Process message ✅
2. Delete message ❌ (fails)
3. Message reappears after visibility timeout
4. Message processed AGAIN (duplicate)
```

**Mitigation:** Idempotency cache handles this, but with in-memory cache, restart window is vulnerable.

**Priority:** MEDIUM - Idempotency helps, but not bulletproof during restarts.

---

## 🔧 Recommended Fixes (Priority Order)

### Phase 1: Critical Bug Fix (IMMEDIATE)
**Fix broken circuit breaker error detection**

```go
// processor.go - Import
import "github.com/sony/gobreaker"

// processor.go lines 768-769
if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
    logger.Warn().
        Str("environment", environment).
        Msg("Circuit breaker rejected request - system is unhealthy")
}

// processor.go lines 834-835 (same fix)
if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
    logger.Warn().
        Str("environment", environment).
        Msg("Circuit breaker rejected scheduler request - system is unhealthy")
}
```

**Effort:** 30 minutes
**Risk:** LOW
**Files to modify:** `server/sqsconsumer/processor.go`

---

### Phase 2: Configuration Improvements (SHORT-TERM)

1. **Make worker pool timeout configurable**
```go
type WorkerPoolConfig struct {
    AcquireTimeout time.Duration // Default: 5s
}
```

2. **Add shutdown drain timeout**
```go
type ShutdownConfig struct {
    DrainTimeout    time.Duration // Wait for in-flight messages
    ForceStopAfter time.Duration // Force stop if drain takes too long
}
```

**Effort:** 2 hours
**Risk:** LOW

---

### Phase 3: Enhanced Observability (MEDIUM-TERM)

1. **Add circuit breaker state change alerts**
2. **Track retry exhaustion rates**
3. **Monitor idempotency cache hit rates**
4. **Add SQS message age metrics**

**Effort:** 4 hours
**Risk:** LOW

---

### Phase 4: Distributed Idempotency (LONG-TERM - OPTIONAL)

If duplicate processing during deployments is unacceptable:

1. **Option A:** Use SQS FIFO with deduplication
2. **Option B:** Redis-backed idempotency cache
3. **Option C:** DynamoDB with conditional writes

**Effort:** 1-2 days
**Risk:** MEDIUM (adds external dependency)

---

## ✅ Current Reliability Features (Working Well)

1. **Retry Logic with Smart Classification**
   - Uses `policyhandler.IsRetryableError()` for consistent error handling
   - Exponential backoff with jitter prevents thundering herd
   - Non-retryable errors (404, auth) are handled gracefully

2. **Circuit Breaker Implementation**
   - Per-environment isolation (GHEC/GHES)
   - Configurable trip thresholds
   - Metrics recording (though logging is broken)

3. **Graceful Shutdown**
   - Configurable timeout
   - Waits for in-flight workers
   - Worker pool manager shutdown

4. **Adaptive Polling**
   - Adjusts based on worker availability
   - Prevents over-polling when saturated
   - Per-event-type configuration

5. **Dead Letter Queue Monitoring**
   - Regular health checks
   - Metrics for DLQ depth
   - Warning logs when messages appear

---

## 📈 Metrics Coverage (Comprehensive)

- `sqs.messages.processed` - Success count
- `sqs.messages.failed` - Failure count
- `sqs.processing.time` - Processing latency
- `sqs.dlq.messages` - DLQ depth
- `sqs.worker_pool.active_workers` - Worker utilization
- `sqs.worker_pool.saturation_events` - Backpressure detection
- `sqs.circuit_breaker.state` - Circuit health
- `sqs.circuit_breaker.rejections` - Failed fast requests
- `sqs.idempotency.duplicates` - Duplicate detection
- `sqs.retry.attempts_total` - Retry frequency

---

## 🎯 Summary

**Overall:** The SQS consumer is well-architected with good test coverage (86.9%) and comprehensive features. However, there's one **critical bug** that needs immediate attention:

1. **CRITICAL FIX NEEDED:** Circuit breaker error comparison is broken (silent failure)
2. **ACCEPTABLE RISK:** In-memory idempotency (handlers should be idempotent anyway)
3. **NICE TO HAVE:** Configurable timeouts, background cleanup

**Recommendation:** Fix the circuit breaker error comparison immediately, then consider other improvements based on operational needs.

---

## 🔄 Next Steps

1. [ ] Fix circuit breaker error comparison (CRITICAL)
2. [ ] Add tests for circuit breaker error handling
3. [ ] Document idempotency limitations in runbook
4. [ ] Consider making worker pool timeout configurable
5. [ ] Monitor retry exhaustion rates in production
