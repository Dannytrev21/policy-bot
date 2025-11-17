# SQS Idempotency (In-Memory First, Redis-Ready Later)
**Key SQS fact:** On visibility-timeout retries, `MessageId` stays the same; only `ReceiptHandle` changes. `X-GitHub-Delivery` is stable per webhook and is the strongest dedup key.

## Goals
- Cross-replica duplicate suppression.
- Suppress only after success or **non-retryable** failure; retry transient errors.
- Ship fast with an in-memory store; leave a clean seam for Redis later.

## Approach v1 (ship now)
**Backend:** In-memory store with a background TTL eviction loop (no pre-marking).  
**Key:** `dedup:{environment}:{event_type}:{github_delivery}` (fallback to SQS `MessageId` if header missing).  
**Value:** Status enum `success` | `final_failure`.  
**TTL:** Align with queue retention (e.g., 6–24h) to cover DLQ replays.

### Processor Flow (replace current pre-mark)
1) On receive: `status := store.Check(key)`  
   - If `success` or `final_failure`: log + delete from SQS, return.
2) Process message.
3) On **success**: `store.MarkSuccess(key, ttl)`, then delete.
4) On **non-retryable** (auth/404/etc.): `store.MarkFinalFailure(key, ttl)`, then delete.
5) On **retryable**: do **not** mark; let SQS retry/DLQ.
6) If store errors: warn and continue (fail-open to avoid message loss).

### Metrics (small set)
- `idempotency.store_hits`, `store_misses`, `duplicates_skipped`, `mark_success`, `mark_final_failure`, `store_errors`.
- Log: delivery_id (GitHub), message_id (SQS), event_type, queue.

### FIFO note
- Current deployment uses **standard SQS queues**. Skip FIFO content-based dedup; it’s not applicable. Idempotency relies solely on the in-memory store (and later Redis for cross-replica).

## Approach v2 (later: Redis swap)
- Add Redis backend behind the same interface (SETNX + EX). Config toggle `backend: redis` with `redis_url`, `ttl`, timeouts.
- Use Redis for true cross-replica suppression; keep in-memory for dev/tests or as a fallback.

## Implementation Steps
1) Define `IdempotencyStore` interface: `Check(key) (status, found, err)`, `MarkSuccess(key, ttl)`, `MarkFinalFailure(key, ttl)`.  
2) Implement in-memory store with TTL map + cleanup goroutine.  
3) Wire processor to the new flow above; remove pre-processing `CheckAndMark`.  
4) Config block `sqs.idempotency`: `enabled`, `backend (inmemory|redis)`, `ttl`, `redis_url` (future).  
5) Tests: decision matrix (success, retryable, non-retryable, duplicate after success/final failure) and concurrency for the store.

## Why This Stays Simple
- Minimal states, minimal metrics, no early marking.  
- Ships with zero new infra; adding Redis later is a drop-in via the interface.  
- Fits standard queues; no FIFO prerequisites or configuration needed.
