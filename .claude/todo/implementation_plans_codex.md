# Implementation Plans (Codex)

## Checklist
- [ ] Solution 1: Per-Installation Circuit Breakers — design, code, tests, docs
- [ ] Solution 6: Concurrent Request Limiting — design, code, tests, docs
- [ ] Solution 4: Graceful Shutdown — design, code, tests, docs
- [ ] Solution 3: Consistent Error Classification — design, code, tests, docs
- [ ] Solution 8: AppClient Caching — design, code, tests, docs
- [ ] Solution 10: VerifyInstallation Caching — design, code, tests, docs
- [ ] Solution 12: Enforce Org-Only Installations — design, code, tests, docs
- [ ] Solution 14: Secondary-Limit 403 Handling — design, code, tests, docs
- [ ] Solution 5: 429 Feedback Loop — design, code, tests, docs
- [ ] Solution 9: Owner ID Validation — design, code, tests, docs
- [ ] Solution 7: GraphQL Rate Limiting — design, code, tests, docs

## Plan Context
- Priority order: 1, 6 (P0) → 4, 3, 8, 10, 12, 14 (P1) → 5, 9, 7 (P2).
- Core code areas: `server/handler/installation_manager.go`, `server/handler/rate_limiter.go`, `server/handler/base.go`, `server/server.go`, `vendor/github.com/palantir/go-githubapp/...` (installations service, client creator), `server/handler/errors.go`.
- Docs to touch: `.claude/documentation/02-technical-architecture.md`, `.claude/documentation/03-operations-playbook.md`, `README.md`, `.claude/analysis/solution_application_codex.md` (sync status).
- Testing entry points: existing unit tests in `server/handler/*_test.go`, `server/config_validation_test.go`, and new targeted tests per feature. Prefer table-driven tests for error classification and limiter behavior.

## Detailed Plans

### Solution 1: Per-Installation Circuit Breakers (P0, Medium Effort, High Impact, Critique #3)
- **Context**: `InstallationManager` shares a single breaker; need per-installation + optional global.
- **Implementation Steps**:
  1) Add configurable breaker struct (threshold/timeout) and per-installation `sync.Map` in `installation_manager.go`; retain optional global breaker for service-level failures.
  2) Update `GetClients` to select breaker by installation ID, record success/failure to both per-installation and global breaker; expose metrics for per-installation state counts.
  3) Ensure breaker creation is lazy and eviction-safe (consider cleanup on cache eviction or keep bounded by cache size).
  4) Wire constructor to accept metrics registry; adjust tests/mocks accordingly.
- **Tests**:
  - Unit: trip per-install breaker without blocking another installation; trip global breaker blocks all.
  - Unit: half-open recovery closes after success, reopens on failure.
- **Acceptance Criteria**:
  - One installation’s failures do not block others; global breaker still available for GitHub-wide outages.
  - Metrics reflect breaker state transitions.
- **Docs**:
  - Note per-installation breaker behavior and thresholds in technical architecture and ops playbook.

### Solution 6: Concurrent Request Limiting (P0, Medium Effort, High Impact, Critique #13)
- **Context**: Secondary limit of 100 concurrent requests per installation is not enforced.
- **Implementation Steps**:
  1) Introduce per-owner concurrency guard (semaphore or weighted limiter) in `rate_limiter.go`; key by normalized owner or installation ID.
  2) Wrap client creation/transport so each GitHub call acquires/releases a slot; ensure both REST and GraphQL paths participate.
  3) Expose configuration (max concurrent, optional weight per request) in `RateLimitConfig`; default to GitHub guidance.
  4) Add metrics for current in-flight per org and rejections/waits.
- **Tests**:
  - Unit: exceeding concurrent limit blocks/waits; releases on completion.
  - Concurrency test: parallel goroutines respect cap.
- **Acceptance Criteria**:
  - No more than configured concurrent requests per installation/org.
  - No deadlocks or leaks when requests error.
- **Docs**:
  - Document concurrency limit and tuning knobs; mention GitHub secondary limit rationale.

### Solution 4: Graceful Shutdown (P1, Low Effort, Medium Impact, Critique #9)
- **Context**: ClientCache goroutines not stopped on server shutdown.
- **Implementation Steps**:
  1) Add stop hook from `server.Server.Start` to call `InstallationManager.StopClientCache()` for all base handlers (HTTP and SQS).
  2) Ensure `InstallationManager` exposes safe Stop that halts metrics/cleanup loops.
  3) Wire into shutdown path and tests.
- **Tests**:
  - Unit/integration: calling Stop closes channels without panic; no goroutine leaks (use race detector pattern or waitgroups).
- **Acceptance Criteria**:
  - Shutdown stops cache background loops; no lingering goroutines.
- **Docs**:
  - Update ops playbook shutdown steps.

### Solution 3: Consistent Error Classification (P1, Low Effort, Medium Impact, Critique #7)
- **Context**: `IsRetryableError` relies on string matching.
- **Implementation Steps**:
  1) Refactor `errors.go` to prefer typed errors (`RateLimitError`, `ErrorResponse`, `net.Error`, `url.Error`) with explicit status handling.
  2) Keep minimal fallback patterns for unknown transient network errors.
  3) Update call sites if needed to use refined semantics.
- **Tests**:
  - Table-driven tests for status codes 401/403/404/410/422 (non-retryable) vs 429/5xx (retryable) vs network errors.
- **Acceptance Criteria**:
  - Retryable classification matches GitHub guidance; no false positives on string fragments.
- **Docs**:
  - Note classification rules in technical architecture (resilience section).

### Solution 8: AppClient Caching (P1, Low Effort, Medium Impact, Critique #15)
- **Context**: `VerifyInstallation` creates a new app client every call.
- **Implementation Steps**:
  1) Add `sync.Once` + cached app client in `Base`; expose `GetAppClient`.
  2) Replace `NewAppClient` calls in verification paths with cached accessor.
- **Tests**:
  - Unit: `GetAppClient` called multiple times only constructs once; error path propagates.
- **Acceptance Criteria**:
  - App client JWT created once per process; repeated verifications reuse client.
- **Docs**:
  - Mention app-client caching in architecture doc (auth section).

### Solution 10: VerifyInstallation Caching (P1, Low Effort, Medium Impact, Critique #21)
- **Context**: Verification bypasses cache and does an API call each time.
- **Implementation Steps**:
  1) In `Base.VerifyInstallation`, check `ClientCache` (positive/negative) before API call.
  2) On successful verification, populate cache; on not-found, negative-cache.
  3) Reuse cached app client (from Solution 8).
- **Tests**:
  - Unit: cache hit skips API; negative cache returns false; fallthrough performs call once then caches.
- **Acceptance Criteria**:
  - Subsequent checks avoid redundant API calls; negative results cached per TTL.
- **Docs**:
  - Describe verification cache behavior.

### Solution 12: Enforce Org-Only Installations (P1, Low Effort, Medium Impact, Critique #23)
- **Context**: User installations are accepted; need org-only constraint.
- **Implementation Steps**:
  1) Wrap `InstallationsService` or add validation so account type must be `Organization`; reject otherwise (permanent error).
  2) Guard installation webhooks: if account type is user, log and skip cache population/status updates.
  3) Optional flag to allow user installs for tests; default false.
- **Tests**:
  - Unit: `GetByOwner` with user install returns error; webhook handler ignores user installs.
- **Acceptance Criteria**:
  - Policy Bot only processes org installs by default; user installs fail fast without caching.
- **Docs**:
  - Explicitly state org-only requirement and optional override.

### Solution 14: Secondary-Limit 403 Handling (P1, Medium Effort, High Impact, Critique #25)
- **Context**: Secondary-limit 403s treated as auth errors, triggering cache churn.
- **Implementation Steps**:
  1) Extend `classifyGitHubError` to detect `AbuseRateLimitError`, `Retry-After` on 403, and known phrases (“secondary rate limit”, “abuse detection”) and mark as rate-limit, not auth.
  2) In auth refresh paths (`HandleAuthFailure`, `WithAuthRefresh`), skip cache invalidation when `isRateLimit` is true; optionally feed retry-after to limiter.
  3) Optionally surface retry delay to caller/log.
- **Tests**:
  - Table-driven classification: 403 with Retry-After → rate-limit; 403 without headers but abuse text → rate-limit; auth 403 remains auth.
  - Auth refresh test: rate-limit 403 does not invalidate cache.
- **Acceptance Criteria**:
  - Secondary-limit responses trigger backoff, not auth refresh or cache eviction.
- **Docs**:
  - Document secondary-limit handling and retry-after behavior.

### Solution 5: 429 Feedback Loop (P2, Medium Effort, Medium Impact, Critique #12)
- **Context**: 429s do not feed back into adaptive limiter immediately.
- **Implementation Steps**:
  1) In adaptive transport (`rate_limiter.go`), detect 429 responses, read `Retry-After`, and temporarily reduce org rate to minimum.
  2) Record metric/log for exceeded events.
  3) Restore rate based on reset time or adaptive calculation on next success.
- **Tests**:
  - Unit: 429 response triggers rate reduction and respects Retry-After default when missing.
- **Acceptance Criteria**:
  - After 429, subsequent requests slow until reset; metrics captured.
- **Docs**:
  - Mention feedback loop in rate limiting section.

### Solution 9: Owner ID Validation (P2, Low Effort, Medium Impact, Critique #20)
- **Context**: Cache lookup does not validate owner name vs ID.
- **Implementation Steps**:
  1) Store owner name with cache entry; on lookup, verify requested owner matches cached name; miss otherwise.
  2) Optionally prefer ownerID key but ensure mismatch logs warnings.
  3) Update cache accessors and `GetClientsByOwner` to use validation-aware methods.
- **Tests**:
  - Unit: mismatched owner returns miss; matched owner hits; negative cache preserved.
- **Acceptance Criteria**:
  - Wrong owner/ID combinations cannot serve cached clients.
- **Docs**:
  - Document validation behavior and logging.

### Solution 7: GraphQL Rate Limiting (P2, High Effort, Medium Impact, Critique #14)
- **Context**: GraphQL uses point-based limits; current limiter only counts requests.
- **Implementation Steps**:
  1) Add GraphQL point limiter (token bucket on points/sec) with conservative default costs; allow estimated cost per query type.
  2) Optionally parse `extensions.rateLimit` from responses to adjust state asynchronously.
  3) Wire into GraphQL client creation/transport so every query reserves points before sending.
  4) Configurable bounds via `RateLimitConfig`.
- **Tests**:
  - Unit: reservation blocks when insufficient points; remaining points adjust after update; high-cost query consumes more points.
  - Integration-style: mock response with `extensions.rateLimit` updates limiter.
- **Acceptance Criteria**:
  - GraphQL requests respect point budget; limiter adapts when low.
- **Docs**:
  - Document point model, defaults, and how to tag query types for cost estimation.
