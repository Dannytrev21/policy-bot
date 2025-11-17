# Distributed Idempotency for SQS Pipeline

## Executive Summary

The current in-memory idempotency implementation has **three critical flaws**:

1. **Wrong Key**: Uses SQS MessageId (changes on retry) instead of X-GitHub-Delivery (stable)
2. **Bug**: Messages are marked as processed BEFORE success, preventing retries on failure
3. **Scalability**: In-memory cache doesn't share state across replicas

This document analyzes solutions: shared store (Redis/DynamoDB) vs SQS content-based deduplication.

---

## 🚨 Critical Flaw #1: Wrong Idempotency Key

### Current Implementation (BROKEN)

```go
// processor.go:469 - Uses SQS MessageId
sqsMsg.DeliveryID = aws.ToString(message.MessageId)
```

**Problem:** When you re-publish a message for retry, SQS assigns a NEW MessageId. This means:
- Original message: MessageId = "abc-123"
- Retry message: MessageId = "xyz-789" (NEW!)
- Idempotency check sees different IDs → processes as new message

### Available GitHub Headers

```
X-GitHub-Delivery: 72d3162e-cc78-11e3-81ab-4c9367dc0958  ← STABLE across retries!
X-Hub-Signature-256: sha256=d57c68...
X-GitHub-Event: issues
X-GitHub-Hook-ID: 292430182
```

**Solution:** Use `X-GitHub-Delivery` from headers:
```go
// Extract GitHub's stable delivery ID
if headers := sqsMsg.Headers; headers != nil {
    if githubDeliveryID, ok := headers["X-GitHub-Delivery"].(string); ok {
        sqsMsg.DeliveryID = githubDeliveryID  // Stable across retries!
    }
}
```

This is a **game-changer** because:
1. GitHub guarantees uniqueness per webhook delivery
2. Stays the same when you re-queue for retry
3. Already in your message - just need to use it!

---

## 🚨 Critical Bug: Pre-Processing Idempotency Mark

### Current Flow (BROKEN)

```go
// processor.go:328
if p.idempotency != nil && p.idempotency.CheckAndMark(sqsMsg.DeliveryID) {
    // ❌ Message marked as processed IMMEDIATELY
    return p.deleteMessage(...)
}

// ... processing happens here ...
// If processing FAILS with retryable error:
// - Message is NOT deleted from queue
// - Message returns to queue for retry
// - On retry, idempotency check says "duplicate"
// - Message is deleted without being processed! ❌
```

### Impact

- Failed messages are incorrectly rejected as duplicates
- Retryable errors (rate limits, timeouts) cause permanent message loss
- Only succeeds if first attempt succeeds

### Correct Flow (TO IMPLEMENT)

```go
// Check if SUCCESSFULLY processed before (not just seen)
if p.idempotency != nil && p.idempotency.IsSuccessfullyProcessed(sqsMsg.DeliveryID) {
    return p.deleteMessage(...) // Skip - already succeeded
}

// Process the message
err := processHandler(...)

if err == nil || !isRetryable(err) {
    // Only mark as processed on SUCCESS or permanent failure
    p.idempotency.MarkProcessed(sqsMsg.DeliveryID)
}

return err // Let SQS retry if retryable error
```

---

## 📊 Solution Comparison

### Option 1: Redis (Recommended)

**Architecture:**
```
[Replica 1] ────┐
                │
[Replica 2] ────├───→ [Redis] (TTL-based keys)
                │
[Replica 3] ────┘
```

**Implementation:**
```go
type RedisIdempotencyManager struct {
    client redis.Client
    ttl    time.Duration
}

func (r *RedisIdempotencyManager) MarkSuccessfullyProcessed(deliveryID string) error {
    // Use SET NX (set if not exists) for atomic operation
    key := fmt.Sprintf("idempotency:success:%s", deliveryID)
    return r.client.Set(ctx, key, "processed", r.ttl).Err()
}

func (r *RedisIdempotencyManager) IsSuccessfullyProcessed(deliveryID string) (bool, error) {
    key := fmt.Sprintf("idempotency:success:%s", deliveryID)
    exists, err := r.client.Exists(ctx, key).Result()
    return exists > 0, err
}

// Content-addressable key for message deduplication
func (r *RedisIdempotencyManager) MarkByContentHash(payload []byte) error {
    hash := sha256.Sum256(payload)
    key := fmt.Sprintf("idempotency:content:%x", hash)
    return r.client.Set(ctx, key, "processed", r.ttl).Err()
}
```

**Pros:**
- ✅ Distributed across all replicas
- ✅ Atomic operations (SETNX, SETEX)
- ✅ Sub-millisecond latency (~0.5ms)
- ✅ TTL handles automatic cleanup
- ✅ Can store additional metadata (retry count, timestamp)
- ✅ Supports both delivery ID and content-hash keys
- ✅ High availability with Redis Cluster/Sentinel
- ✅ Mature ecosystem (monitoring, alerts)

**Cons:**
- ❌ Additional infrastructure dependency
- ❌ Network call for every message (but fast)
- ❌ Redis failure = idempotency disabled (need fallback)
- ❌ Cost: ~$15-50/month for managed Redis

**Failure Handling:**
```go
func (r *RedisIdempotencyManager) IsSuccessfullyProcessed(deliveryID string) bool {
    exists, err := r.client.Exists(ctx, key).Result()
    if err != nil {
        // Log error, fall back to allowing processing
        // Better to process twice than miss message
        r.logger.Warn().Err(err).Msg("Redis unavailable, allowing message")
        return false
    }
    return exists > 0
}
```

---

### Option 2: DynamoDB

**Architecture:**
```go
type DynamoIdempotencyManager struct {
    client    *dynamodb.Client
    tableName string
    ttl       time.Duration
}

// Table Schema:
// PK: delivery_id (String)
// status: "success" | "in_progress"
// processed_at: Unix timestamp (TTL attribute)
// content_hash: SHA256 of payload (GSI for content dedup)
```

**Implementation:**
```go
func (d *DynamoIdempotencyManager) MarkSuccessfullyProcessed(deliveryID string, payload []byte) error {
    hash := sha256.Sum256(payload)
    ttlTime := time.Now().Add(d.ttl).Unix()

    _, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
        TableName: aws.String(d.tableName),
        Item: map[string]types.AttributeValue{
            "delivery_id":  &types.AttributeValueMemberS{Value: deliveryID},
            "content_hash": &types.AttributeValueMemberS{Value: fmt.Sprintf("%x", hash)},
            "status":       &types.AttributeValueMemberS{Value: "success"},
            "ttl":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttlTime)},
        },
        ConditionExpression: aws.String("attribute_not_exists(delivery_id)"),
    })
    return err
}

func (d *DynamoIdempotencyManager) IsSuccessfullyProcessed(deliveryID string) (bool, error) {
    result, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
        TableName: aws.String(d.tableName),
        Key: map[string]types.AttributeValue{
            "delivery_id": &types.AttributeValueMemberS{Value: deliveryID},
        },
        ProjectionExpression: aws.String("status"),
    })
    if err != nil {
        return false, err
    }

    if result.Item == nil {
        return false, nil
    }

    status, ok := result.Item["status"].(*types.AttributeValueMemberS)
    return ok && status.Value == "success", nil
}

// Check by content hash (for exact duplicate payloads)
func (d *DynamoIdempotencyManager) IsContentDuplicate(payload []byte) (bool, error) {
    hash := sha256.Sum256(payload)

    // Query GSI on content_hash
    result, err := d.client.Query(ctx, &dynamodb.QueryInput{
        TableName:              aws.String(d.tableName),
        IndexName:              aws.String("content_hash_index"),
        KeyConditionExpression: aws.String("content_hash = :hash"),
        ExpressionAttributeValues: map[string]types.AttributeValue{
            ":hash": &types.AttributeValueMemberS{Value: fmt.Sprintf("%x", hash)},
        },
    })
    return len(result.Items) > 0, err
}
```

**Pros:**
- ✅ Fully managed, no operational overhead
- ✅ Conditional writes for race-free operations
- ✅ Built-in TTL for automatic expiration
- ✅ Global Secondary Index for content-hash lookups
- ✅ Already using AWS (SQS), consistent ecosystem
- ✅ Pay-per-request pricing (scale to zero)
- ✅ Strongly consistent reads available

**Cons:**
- ❌ Higher latency than Redis (~5-10ms)
- ❌ More expensive at high volume ($1.25 per million reads/writes)
- ❌ GSI adds cost for content-hash queries
- ❌ 25 writes/sec throughput limit per partition (need careful key design)

**Cost Estimate:**
- 10,000 messages/day = ~$0.03/day = ~$1/month
- 1,000,000 messages/day = ~$3/day = ~$90/month

---

### Option 3: SQS FIFO with Content-Based Deduplication

**Architecture:**
```
[GitHub Webhook] → [Standard Queue] → [Convert to FIFO] → [FIFO Queue]
                                           ↓
                                    MessageDeduplicationId = SHA256(payload)
                                    MessageGroupId = owner_id or repo_id
```

**SQS FIFO Queue Configuration:**
```go
// Queue creation (one-time setup)
_, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{
    QueueName: aws.String("policy-bot-webhooks.fifo"),
    Attributes: map[string]string{
        "FifoQueue":                 "true",
        "ContentBasedDeduplication": "true", // SHA256 of message body
        "DeduplicationScope":        "messageGroup", // Per-group dedup
        "FifoThroughputLimit":       "perMessageGroupId", // Higher throughput
    },
})

// Sending message
func (p *Publisher) SendToFIFO(payload []byte, ownerID string) error {
    hash := sha256.Sum256(payload)

    _, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
        QueueUrl:               aws.String(p.fifoQueueURL),
        MessageBody:            aws.String(string(payload)),
        MessageGroupId:         aws.String(ownerID), // Ordering per owner
        MessageDeduplicationId: aws.String(fmt.Sprintf("%x", hash)),
    })
    return err
}
```

**How SQS Deduplication Works:**
1. Each message gets `MessageDeduplicationId` (content hash)
2. SQS rejects duplicates within **5-minute window**
3. Deduplication is per `MessageGroupId` (can be repo/owner)

**Pros:**
- ✅ No additional infrastructure
- ✅ Zero operational overhead (fully managed)
- ✅ Built-in exactly-once delivery
- ✅ Message ordering guarantees per group
- ✅ No application-level code for deduplication

**Cons:**
- ❌ **5-minute deduplication window only** (not configurable)
- ❌ **3,000 messages/sec limit** (vs 120,000 for standard)
- ❌ **$0.50 per million requests** (2x standard queue cost)
- ❌ Must migrate from standard to FIFO queue (breaking change)
- ❌ Doesn't distinguish success vs failure (rejects before processing)
- ❌ MessageGroupId ordering can cause head-of-line blocking
- ❌ **Still marks before processing** (same bug as current implementation)

**Critical Limitation:**
```
SQS FIFO rejects duplicates BEFORE your code sees them.
This means:
- Message A arrives → SQS accepts → Processing starts
- Message A arrives again (within 5 min) → SQS REJECTS
- But if Message A processing FAILED, you wanted retry!
- ❌ Same fundamental bug as current implementation
```

---

### Option 4: Content-Addressable Keys (Hybrid Approach)

**Concept:**
Instead of using `DeliveryID`, generate a key from the actual content:

```go
type ContentBasedIdempotency struct {
    store IdempotencyStore // Redis or DynamoDB
}

// Create idempotency key from message content
func (c *ContentBasedIdempotency) GenerateKey(msg *SQSMessage) string {
    // Normalize payload to handle field ordering
    normalized := normalizeJSON(msg.Payload)

    // Create hash of event type + normalized payload
    content := fmt.Sprintf("%s:%s", msg.EventType, normalized)
    hash := sha256.Sum256([]byte(content))

    return fmt.Sprintf("dedup:%x", hash)
}

// Store both delivery ID and content hash
type IdempotencyEntry struct {
    DeliveryID   string    // SQS delivery ID
    ContentHash  string    // SHA256 of payload
    Status       string    // "success", "failure", "non_retryable"
    ProcessedAt  time.Time
    ErrorMessage string    // Why it failed (if applicable)
}

func (c *ContentBasedIdempotency) CheckAndMarkSuccess(msg *SQSMessage) error {
    key := c.GenerateKey(msg)

    entry := IdempotencyEntry{
        DeliveryID:  msg.DeliveryID,
        ContentHash: key,
        Status:      "success",
        ProcessedAt: time.Now(),
    }

    return c.store.Store(key, entry, c.ttl)
}

// Check if exact same content was processed
func (c *ContentBasedIdempotency) IsContentDuplicate(msg *SQSMessage) bool {
    key := c.GenerateKey(msg)
    entry, exists := c.store.Get(key)

    if !exists {
        return false
    }

    // Only skip if successfully processed
    return entry.Status == "success"
}
```

**Pros:**
- ✅ Deduplicates based on actual content, not just message ID
- ✅ Different delivery IDs with same payload = duplicate
- ✅ Can detect GitHub sending same webhook twice
- ✅ Survives message re-publishing after restart

**Cons:**
- ❌ JSON normalization complexity (field ordering, whitespace)
- ❌ Slight CPU overhead for hashing
- ❌ Need to handle payload variations (timestamps, etc.)

---

## 🎯 Revised Recommendation: Fix the Key First, Then Consider Distribution

### The Simplest Fix (HIGH IMPACT, LOW EFFORT)

**Just use X-GitHub-Delivery instead of SQS MessageId!**

This single change:
1. ✅ Fixes retry detection (same key across re-queues)
2. ✅ Uses GitHub's guaranteed-unique delivery ID
3. ✅ No infrastructure changes needed
4. ✅ Works with existing in-memory cache for single replica

### When You Actually Need Redis

Only if running **multiple replicas** that need to share idempotency state. Otherwise, the in-memory cache with correct key is sufficient.

### Why NOT SQS FIFO?

1. **Same pre-processing bug** - rejects before you process
2. **5-minute fixed window** - not configurable
3. **3,000 msg/sec limit** - vs 120,000 for standard
4. **Breaking migration** - queue infrastructure change
5. **Doesn't understand success** - just rejects duplicates blindly

---

## 📋 Revised Implementation Plan

### Phase 0: Fix Idempotency Key (CRITICAL - 1 hour) ✅ COMPLETED

**Location:** `server/sqsconsumer/processor.go:501-520`

**Implemented Code:**
```go
// CRITICAL: Extract X-GitHub-Delivery from headers for idempotency
// This header is stable across retries, unlike SQS MessageId which changes on re-queue
if sqsMsg.Headers != nil {
    if githubDeliveryID, ok := sqsMsg.Headers["X-GitHub-Delivery"].(string); ok && githubDeliveryID != "" {
        sqsMsg.DeliveryID = githubDeliveryID
        p.logger.Debug().
            Str("github_delivery_id", githubDeliveryID).
            Str("sqs_message_id", aws.ToString(message.MessageId)).
            Msg("Using X-GitHub-Delivery header for idempotency key")
    }
}

// Fallback to SQS MessageId if no delivery ID set
// This is not ideal for retry scenarios as MessageId changes on re-queue
if sqsMsg.DeliveryID == "" {
    sqsMsg.DeliveryID = aws.ToString(message.MessageId)
    p.logger.Warn().
        Str("sqs_message_id", aws.ToString(message.MessageId)).
        Msg("No X-GitHub-Delivery header found, using SQS MessageId (not retry-safe for idempotency)")
}
```

**Why this matters:**
- Retry sends same payload with same X-GitHub-Delivery
- SQS assigns new MessageId each time
- Using wrong key = idempotency check always misses retries
- **Now fixed:** Same GitHub webhook retried will have same DeliveryID for idempotency

---

### Phase 1: Fix Pre-Processing Bug (CRITICAL - 2 hours)

**Location:** `server/sqsconsumer/processor.go:328`

**Implementation Plan:**

1. **Update IdempotencyManager** (`idempotency.go`):
   - Add `IsProcessed(deliveryID string) bool` - check without marking
   - Add `MarkProcessed(deliveryID string)` - mark after success
   - Keep `CheckAndMark()` for backwards compatibility but deprecate it

2. **Update ProcessMessage** (`processor.go:328-437`):
   - Replace `CheckAndMark()` with `IsProcessed()` at line 328
   - Add `MarkProcessed()` after success (line ~437)
   - Add `MarkProcessed()` after non-retryable errors (line ~425)
   - Do NOT mark after retryable errors (allow retry)

3. **Update Tests**:
   - Add tests for `IsProcessed()` without side effects
   - Add tests for `MarkProcessed()` explicit marking
   - Add tests for success-based marking in processor
   - Add tests for retry scenarios (not marked until success)

```go
// BEFORE (buggy):
if p.idempotency != nil && p.idempotency.CheckAndMark(sqsMsg.DeliveryID) {
    return p.deleteMessage(...)
}
// Process happens here...

// AFTER (correct):
// Check if SUCCESSFULLY processed before
if p.idempotency != nil && p.idempotency.IsProcessed(sqsMsg.DeliveryID) {
    msgLogger.Info().
        Str("delivery_id", sqsMsg.DeliveryID).
        Msg("Message already successfully processed - skipping")
    return p.deleteMessage(...)
}

// Process the message
err := p.processHandler(...)

// Only mark as processed on SUCCESS or NON-RETRYABLE error
if err == nil {
    p.idempotency.MarkProcessed(sqsMsg.DeliveryID)
    msgLogger.Debug().Msg("Marked message as successfully processed")
} else if !isRetryable(err) {
    p.idempotency.MarkProcessed(sqsMsg.DeliveryID)
    msgLogger.Info().Err(err).Msg("Marked non-retryable error as processed")
}
// Retryable errors: DON'T mark, allow retry to happen

return err
```

**Key insight:** A message is only a "duplicate" if it was **successfully handled** OR **permanently failed** (non-retryable). Retryable failures should NOT be marked as processed.

**Implementation Status: ✅ COMPLETED (2024-11-17)**

Files modified:
- `server/sqsconsumer/idempotency.go:83-124` - Added `IsProcessed()` and `MarkProcessed()` methods
- `server/sqsconsumer/processor.go:328-337` - Changed `CheckAndMark()` to `IsProcessed()` for check-only
- `server/sqsconsumer/processor.go:427-433` - Added `MarkProcessed()` after non-retryable errors
- `server/sqsconsumer/processor.go:446-453` - Added `MarkProcessed()` after successful processing
- `server/sqsconsumer/idempotency_test.go:184-316` - Added 8 comprehensive tests for new methods

Key changes:
1. `IsProcessed()` - checks without marking (no side effects)
2. `MarkProcessed()` - explicit marking after success
3. `CheckAndMark()` deprecated but kept for backwards compatibility
4. Processor now: checks → processes → marks (correct order)
5. Non-retryable errors are marked to prevent duplicate processing
6. Retryable errors are NOT marked (allows retry to succeed)

New tests added:
- `TestIdempotencyManager_IsProcessed` - 4 test cases for check-without-mark behavior
- `TestIdempotencyManager_MarkProcessed` - 4 test cases for explicit marking
- Key test: "separate marking allows retry semantics" demonstrates correct retry flow

Tests passing: All 44 sqsconsumer tests pass (11.4s)

---

### Phase 2: Update IdempotencyManager Interface (1 hour)

```go
// Clearer naming - "Processed" means successfully or permanently handled
type IdempotencyManager interface {
    // IsProcessed returns true if message was successfully handled
    // or had a non-retryable error (should not be retried)
    IsProcessed(deliveryID string) bool

    // MarkProcessed marks a message as successfully handled
    MarkProcessed(deliveryID string)

    // Remove removes an entry (for testing/manual cleanup)
    Remove(deliveryID string)
}

// Update existing implementation to match new semantics
func (im *IdempotencyManager) IsProcessed(deliveryID string) bool {
    // Same as current CheckAndMark but WITHOUT the mark
    im.mu.RLock()
    processedAt, exists := im.cache.Get(deliveryID)
    im.mu.RUnlock()

    if exists && time.Now().Sub(processedAt) < im.ttl {
        im.recordDuplicate()
        return true
    }
    return false
}

func (im *IdempotencyManager) MarkProcessed(deliveryID string) {
    im.mu.Lock()
    defer im.mu.Unlock()
    im.cache.Add(deliveryID, time.Now())
    im.updateCacheSizeMetric()
}
```

---

### Phase 3: Add Redis Backend (ONLY IF MULTI-REPLICA) - 4-6 hours

**New file: `server/sqsconsumer/idempotency_redis.go`**

```go
package sqsconsumer

import (
    "context"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/rcrowley/go-metrics"
    "github.com/rs/zerolog"
)

const (
    RedisKeyPrefix = "idempotency:processed:"
)

type RedisIdempotencyManager struct {
    client   *redis.Client
    ttl      time.Duration
    logger   zerolog.Logger
    registry metrics.Registry
}

func NewRedisIdempotencyManager(
    redisURL string,
    ttl time.Duration,
    logger zerolog.Logger,
    registry metrics.Registry,
) (*RedisIdempotencyManager, error) {
    opts, err := redis.ParseURL(redisURL)
    if err != nil {
        return nil, fmt.Errorf("invalid redis URL: %w", err)
    }

    client := redis.NewClient(opts)

    // Test connection
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis connection failed: %w", err)
    }

    return &RedisIdempotencyManager{
        client:   client,
        ttl:      ttl,
        logger:   logger,
        registry: registry,
    }, nil
}

func (r *RedisIdempotencyManager) IsProcessed(deliveryID string) bool {
    key := RedisKeyPrefix + deliveryID

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    exists, err := r.client.Exists(ctx, key).Result()
    if err != nil {
        // Redis failure - allow processing (better duplicate than miss)
        r.logger.Warn().Err(err).Str("delivery_id", deliveryID).
            Msg("Redis unavailable, allowing message processing")
        r.recordError()
        return false
    }

    if exists > 0 {
        r.recordDuplicate()
        return true
    }

    return false
}

func (r *RedisIdempotencyManager) MarkProcessed(deliveryID string) {
    key := RedisKeyPrefix + deliveryID

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    err := r.client.Set(ctx, key, "processed", r.ttl).Err()
    if err != nil {
        r.logger.Error().Err(err).Str("delivery_id", deliveryID).
            Msg("Failed to mark message as processed in Redis")
        r.recordError()
    }
}

func (r *RedisIdempotencyManager) MarkProcessedWithMetadata(
    deliveryID string,
    metadata map[string]string,
) {
    key := RedisKeyPrefix + deliveryID

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    // Store as hash with metadata
    pipe := r.client.Pipeline()
    pipe.HSet(ctx, key, metadata)
    pipe.Expire(ctx, key, r.ttl)

    if _, err := pipe.Exec(ctx); err != nil {
        r.logger.Error().Err(err).Msg("Failed to mark with metadata")
    }
}

func (r *RedisIdempotencyManager) Remove(deliveryID string) {
    key := RedisKeyPrefix + deliveryID

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    r.client.Del(ctx, key)
}

func (r *RedisIdempotencyManager) Close() error {
    return r.client.Close()
}

// Health check for monitoring
func (r *RedisIdempotencyManager) Healthy() bool {
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()

    return r.client.Ping(ctx).Err() == nil
}

func (r *RedisIdempotencyManager) recordDuplicate() {
    if r.registry != nil {
        if c := r.registry.Get("sqs.idempotency.duplicates"); c != nil {
            c.(metrics.Counter).Inc(1)
        }
    }
}

func (r *RedisIdempotencyManager) recordError() {
    if r.registry != nil {
        if c := r.registry.Get("sqs.idempotency.redis_errors"); c != nil {
            c.(metrics.Counter).Inc(1)
        }
    }
}
```

---

### Phase 3: Content-Based Deduplication (Optional - 4 hours)

**Add content hashing for exact duplicates:**

```go
package sqsconsumer

import (
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "sort"
)

func GenerateContentHash(eventType string, payload json.RawMessage) string {
    // Normalize JSON to handle field ordering differences
    var data map[string]interface{}
    if err := json.Unmarshal(payload, &data); err != nil {
        // Fallback to raw payload hash
        hash := sha256.Sum256(payload)
        return fmt.Sprintf("%x", hash)
    }

    // Sort keys for deterministic ordering
    normalized := normalizeMap(data)
    normalizedBytes, _ := json.Marshal(normalized)

    content := fmt.Sprintf("%s:%s", eventType, normalizedBytes)
    hash := sha256.Sum256([]byte(content))

    return fmt.Sprintf("sha256:%x", hash)
}

func normalizeMap(m map[string]interface{}) map[string]interface{} {
    result := make(map[string]interface{})
    keys := make([]string, 0, len(m))

    for k := range m {
        keys = append(keys, k)
    }
    sort.Strings(keys)

    for _, k := range keys {
        v := m[k]
        switch val := v.(type) {
        case map[string]interface{}:
            result[k] = normalizeMap(val)
        case []interface{}:
            result[k] = normalizeSlice(val)
        default:
            result[k] = val
        }
    }

    return result
}

// Dual-key idempotency check
func (r *RedisIdempotencyManager) IsProcessedOrDuplicate(
    deliveryID string,
    eventType string,
    payload json.RawMessage,
) bool {
    // Check delivery ID first (fast path)
    if r.IsProcessed(deliveryID) {
        return true
    }

    // Check content hash (catches re-delivered exact duplicates)
    contentHash := GenerateContentHash(eventType, payload)
    contentKey := "idempotency:content:" + contentHash

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    exists, err := r.client.Exists(ctx, contentKey).Result()
    if err != nil {
        r.logger.Warn().Err(err).Msg("Content hash check failed")
        return false
    }

    return exists > 0
}

func (r *RedisIdempotencyManager) MarkBothKeys(
    deliveryID string,
    eventType string,
    payload json.RawMessage,
) {
    contentHash := GenerateContentHash(eventType, payload)

    ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
    defer cancel()

    pipe := r.client.Pipeline()
    pipe.Set(ctx, RedisKeyPrefix+deliveryID, contentHash, r.ttl)
    pipe.Set(ctx, "idempotency:content:"+contentHash, deliveryID, r.ttl)

    if _, err := pipe.Exec(ctx); err != nil {
        r.logger.Error().Err(err).Msg("Failed to mark both idempotency keys")
    }
}
```

---

### Phase 4: Configuration & Migration (2-4 hours)

**Update configuration:**

```go
// server/config.go
type IdempotencyConfig struct {
    Enabled       bool          `yaml:"enabled"`
    Backend       string        `yaml:"backend"` // "memory", "redis", "dynamodb"
    RedisURL      string        `yaml:"redis_url"`
    TTL           time.Duration `yaml:"ttl"`
    CacheSize     int           `yaml:"cache_size"` // for memory backend
    ContentDedup  bool          `yaml:"content_dedup"` // enable SHA256 dedup
}

type SQSConsumerConfig struct {
    // ... existing fields ...
    Idempotency IdempotencyConfig `yaml:"idempotency"`
}
```

**Factory function:**

```go
func NewIdempotencyBackend(
    cfg IdempotencyConfig,
    logger zerolog.Logger,
    registry metrics.Registry,
) (IdempotencyManager, error) {
    switch cfg.Backend {
    case "redis":
        return NewRedisIdempotencyManager(cfg.RedisURL, cfg.TTL, logger, registry)
    case "dynamodb":
        return NewDynamoIdempotencyManager(cfg.TableName, cfg.TTL, registry)
    case "memory":
        fallthrough
    default:
        return NewIdempotencyManager(cfg.CacheSize, cfg.TTL, registry)
    }
}
```

---

## 📊 Cost & Performance Comparison

| Metric | In-Memory (Fixed) | Redis | DynamoDB | SQS FIFO |
|--------|-------------------|-------|----------|----------|
| **Latency** | <0.1ms | 0.5-1ms | 5-10ms | N/A |
| **Distributed** | ❌ | ✅ | ✅ | ✅ |
| **Uses X-GitHub-Delivery** | ✅ (with fix) | ✅ | ✅ | ❌ (content hash) |
| **Success-Based Marking** | ✅ (with fix) | ✅ | ✅ | ❌ (rejects before) |
| **TTL Configurable** | ✅ | ✅ | ✅ | ❌ (5 min fixed) |
| **Cost (10K msg/day)** | $0 | $15/month | $1/month | +$0.50/million |
| **Cost (1M msg/day)** | $0 | $25/month | $90/month | +$15/month |
| **Throughput Limit** | CPU-bound | 100K+ ops/sec | 40K+ reads/sec | 3K msgs/sec |
| **Content Dedup** | Can add | ✅ | ✅ (GSI) | ✅ (built-in) |
| **Ops Overhead** | None | Medium | Low | None |
| **Implementation Effort** | 3-4 hours | 8-10 hours | 10-12 hours | Major migration |

**Recommended Path:**
1. In-Memory with fixes (single replica) → costs $0, 3-4 hours
2. Redis (multi-replica) → costs $15-25/month, additional 4-6 hours

---

## ✅ Success Criteria

### Phase 0 (Fix Key) - CRITICAL ✅ COMPLETED
- [x] Uses X-GitHub-Delivery header as DeliveryID
- [x] Fallback to SQS MessageId with warning log
- [x] Retry preserves same DeliveryID
- [x] All existing tests pass
- [x] New tests for retry scenarios with same X-GitHub-Delivery

**Implementation Details (Completed 2024-11-17):**

Files modified:
- `server/sqsconsumer/processor.go:501-520` - Added X-GitHub-Delivery extraction after parsing
- `server/sqsconsumer/processor_test.go` - Updated 3 existing tests, added 4 new tests

Key changes:
1. Extract X-GitHub-Delivery from headers (overrides JSON delivery_id)
2. Fallback to SQS MessageId with warning log if header missing
3. Debug log when using GitHub delivery ID

New tests added:
- `TestProcessor_ParseMessage_XGitHubDeliveryOverridesJSONDeliveryID` - Header overrides JSON field
- `TestProcessor_ParseMessage_XGitHubDeliveryStableAcrossRetries` - Same ID across different SQS MessageIds
- `TestProcessor_ParseMessage_FallbackToSQSMessageIdWhenNoHeader` - Fallback behavior
- `TestProcessor_ParseMessage_EmptyXGitHubDeliveryFallsBack` - Empty header falls back

Tests passing: All 36 sqsconsumer tests pass (11.9s)

### Phase 1 (Fix Pre-Processing Bug) ✅ COMPLETED
- [x] Messages are only marked after successful processing
- [x] Failed retryable errors are NOT marked
- [x] Non-retryable errors are marked (no retry)
- [x] CheckAndMark split into IsProcessed + MarkProcessed
- [x] Tests for success-based marking

**Implementation Details (Completed 2024-11-17):**

Files modified:
- `server/sqsconsumer/idempotency.go:83-124` - Added IsProcessed() and MarkProcessed() methods
- `server/sqsconsumer/processor.go:328-337, 427-433, 446-453` - Updated to mark after success/non-retryable
- `server/sqsconsumer/idempotency_test.go:184-316` - Added 8 new tests

Results:
- Retryable errors now allow retry (not marked as processed)
- Success and non-retryable errors are marked (prevents duplicates)
- CheckAndMark() deprecated but kept for backwards compatibility
- All 44 sqsconsumer tests pass (11.4s)

### Phase 2 (Update Interface) ✅ COMPLETED (merged with Phase 1)
- [x] IdempotencyManager has IsProcessed() and MarkProcessed()
- [x] Backwards compatible or clear migration
- [x] Metrics still tracked (duplicates, cache size)
- [x] Thread-safe operation maintained

**Note:** Phase 2 was implemented as part of Phase 1 since the interface changes were required for the success-based marking fix.

### Phase 3 (Redis Backend - OPTIONAL)
- [ ] Only if multi-replica deployment
- [ ] Redis connection health check
- [ ] Graceful fallback on Redis failure
- [ ] TTL-based expiration working
- [ ] Metrics for Redis operations

### Phase 4 (Content Dedup - OPTIONAL)
- [ ] Only if seeing duplicate payloads from GitHub
- [ ] SHA256 hash generation working
- [ ] JSON normalization handles field ordering
- [ ] Dual-key lookup (delivery ID + content hash)

---

## 🚫 Anti-Patterns to Avoid

1. **DON'T mark before processing** - Current bug
2. **DON'T use SQS FIFO for this** - Same bug, more constraints
3. **DON'T ignore Redis failures** - Fall back to allowing processing
4. **DON'T over-engineer** - Start simple, add content dedup if needed
5. **DON'T forget metrics** - Monitor duplicate rate, Redis latency

---

## 📝 Summary

### Immediate Actions (3-4 hours total)

**Phase 0 (1 hour):** Fix the idempotency key to use `X-GitHub-Delivery` header instead of SQS MessageId. This is the MOST IMPORTANT fix - without it, retries always look like new messages.

**Phase 1 (2 hours):** Fix pre-processing bug - only mark messages as processed AFTER successful handling. This prevents message loss when retryable errors occur.

**Phase 2 (1 hour):** Update IdempotencyManager interface to separate IsProcessed() from MarkProcessed() for clearer semantics.

### Optional Enhancements

**Redis Backend:** Only needed if running multiple replicas. In-memory cache with correct key is sufficient for single replica.

**Content Deduplication:** Only if GitHub sends exact duplicate webhooks (same payload with different delivery IDs). Rare in practice.

**Not Recommended:** SQS FIFO queues - they reject before processing (same bug), have fixed 5-minute window, and 3,000 msg/sec throughput limit.

### Key Insights

1. **X-GitHub-Delivery is the correct idempotency key** - stable across retries, unique per webhook
2. **Mark AFTER success, not before** - retryable errors should allow retries
3. **In-memory is fine for single replica** - Redis only needed for horizontal scaling
4. **SQS FIFO has fundamental flaw** - deduplicates before processing, can't distinguish success from failure

The simplest solution is often the best: use the right key (X-GitHub-Delivery) and mark at the right time (after success).
