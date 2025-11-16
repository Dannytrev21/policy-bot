# Installation Registry & Client System Consolidation Plan v4

## Plan Status Checklist
- [ ] **Step 1:** Baseline inventory & profiling of installation/client stack
- [ ] **Step 2:** Unify installation caching + locator pathways
- [ ] **Step 3:** Simplify client cache + rate-limited manager responsibilities
- [ ] **Step 4:** Configuration-driven filtering & routing controls
- [ ] **Step 5:** Validation (tests, profiling snapshots, documentation updates)

---

## Context
- Policy Bot now spans dual ingress (webhook + SQS) with installation registry, locator, filter, client cache, and rate-limited client creator layered on top of each other.
- Recent phases added multiple caches (installation registry, repo/org mapping, client cache) plus locator + filter toggles; some duplication and unclear ownership remain.
- Production issue: limited scheduler throughput, rate-limit pressure, need to rely on status/pull_request events only (no installation events) yet still cache clients effectively.

## Constraints & Principles
- KISS + SOLID: only add abstractions that reduce complexity.
- Avoid modifying handler logic unless necessary; wrap or inject dependencies instead.
- Thread safety for 200 events/sec; use RWMutex, atomics, semaphore controls.
- Prefer reuse of go-githubapp/go-metrics instrumentation; Otel bridge already in place.
- SQS path must stay off internal scheduler; filtering should be configurable (webhook vs SQS).
- Must support GitHub Enterprise Cloud + Server simultaneously.
- Optimize for low allocations (sync.Pool, bytes.Buffer) and avoid ping-pong conversions.
- Rate limiting must remain idempotent-safe; operations must tolerate retries.

## References / Where to Look
- `.claude/documentation/02-technical-architecture.md`, `.claude/documentation/03-operations-playbook.md`
- `.claude/todo/installation_redo_plan2.md`, `.claude/todo/github_app_optimization.md`
- `server/handler/{installation_registry.go, installation_locator.go, installation_filter.go, installation_manager.go, client_cache.go}`
- `server/server.go`, `server/config.go`, `server/handler/rate_limiter.go`
- `TESTING.md`, `README.md` sections on selective filtering & rate limiting

## Tree-of-Thought Decisions

### Installation Caching & Lookup
1. **Option A:** Leave registry + locator + mapping caches separate (status quo).  
2. **Option B:** Collapse into a single registry struct that handles compound keys, repo/org mappings, and lifecycle hooks; locator becomes thin wrapper.  
3. **Option C:** Externalize cache into Redis/SQLite for multi-instance sharing.  
**Chosen:** Option B — keeps deployment simple (no new infra) while removing duplicated caches; aligns with KISS and still thread-safe inside process.

### Client Cache & Manager
1. **Option A:** Maintain dedicated `ClientCache` + `InstallationManager` layering (current).  
2. **Option B:** Embed client cache into manager and expose small interface (`ClientProvider`) so handlers see only one dependency.  
3. **Option C:** Use sync.Pool per handler to reuse clients (risking stale auth).  
**Chosen:** Option B — consolidates responsibilities (verification + rate limiting + caching) without reusing expired tokens incorrectly.

### Rate Limiting Integration
1. **Option A:** Keep rate limiter separate from manager; callers compose.  
2. **Option B:** Manager owns rate limiter tokens per installation + global limiter; filter/locator just pass context.  
3. **Option C:** Introduce adaptive limiter per queue only.  
**Chosen:** Option B — single choke point guaranteeing every client creation obeys limits while remaining configurable.

### Filtering Control
1. **Option A:** Dual filter implementations (HTTP vs SQS).  
2. **Option B:** Single filter with runtime detection + config toggles.  
3. **Option C:** Pull filtering into dispatcher middleware.  
**Chosen:** Option B — already partly implemented; finish consolidation with explicit config booleans and shared locator.

---

## Detailed Steps

### Step 1: Inventory & Profiling
- **Information:** Gather references above; capture current cache hit/miss metrics (registry, client cache) plus rate limiter stats. Use existing coverage reports + go test `-run Installation` suites.  
- **Implementation:** Script or doc summarizing flows; note where repo/org mapping duplicates registry data; capture pprof (cpu/mem) for GetClients hot path.  
- **Testing Plan:** Re-run `go test ./server/handler -run Installation -cover` to ensure baseline passes; store pprof snapshots for comparison.  
- **Acceptance Criteria:** Documented baseline metrics + flamegraphs; confirm current tests green for handler packages before refactors.

### Step 2: Unify Installation Caching & Locator
- **Information:** Files `installation_registry.go`, `installation_locator.go`, mapping cache utilities, server wiring (`Base.Initialize`).  
- **Implementation:** Refactor registry to own repo/org indexes (compound keys, owner-only). Expose methods `LookupByCompound`, `UpdateFromEvent`, `RecordRepoAssociation`. Make locator a thin strategy (webhook vs SQS) using registry + rate limiter hooks. Deprecate standalone `MappingCache`.  
- **Testing Plan:** Extend `installation_registry_test.go` and `installation_locator_test.go` with concurrency + TTL cases; add integration test ensuring repo lookups work without installation events.  
- **Acceptance Criteria:** Mapping cache removed or no longer referenced; registry API new methods covered ≥85%; locator tests updated; server wiring uses unified structures.

### Step 3: Simplify Client Cache & Manager + Rate Limiting
- **Information:** `installation_manager.go`, `client_cache.go`, `rate_limiter.go`, metrics definitions.  
- **Implementation:** Embed client cache and rate limiter into manager: expose `ClientProvider` interface returning `InstallationClients`. Ensure caching key is `(installationID, repoKey)` to support owner/repo fallback. Add hooks for invalidation (installation event, repo removal). Maintain circuit breaker & retry logic.  
- **Testing Plan:**  
  - Unit: simulate cache hits/misses, TTL expiry, repo-based eviction, rate limiter exhaustion.  
  - Integration: `TestInstallationManager_GetClients` verifying reused clients when only status events received.  
  - Benchmark: `BenchmarkInstallationManager_GetClientsCached`.  
- **Acceptance Criteria:** Manager API unchanged externally; coverage ≥80%; rate limiter metrics still emitted; tests confirm repo+installation caching works without installation events.

### Step 4: Configuration-Driven Filtering & Routing
- **Information:** `server/config.go`, `server/server.go`, `TESTING.md`, docs.  
- **Implementation:** Finalize `installation_filter` config (webhook + SQS toggles + optional per-event overrides). Ensure dispatcher builder only wraps handlers when enabled and locator available. Remove legacy SQS-only filter files. Provide defaults (SQS on, webhook off).  
- **Testing Plan:**  
  - Config marshal/unmarshal tests for new fields.  
  - Server integration tests ensuring toggles respected.  
  - Handler tests verifying filter no-op when disabled.  
- **Acceptance Criteria:** No compile errors, config example updated, docs highlight toggles, automated tests prove gating logic.

### Step 5: Validation, Docs, & Rollout Playbook
- **Information:** `.claude/documentation/*.md`, README, TESTING, Ops Playbook.  
- **Implementation:** Update documentation with simplified architecture diagrams + instructions (how locator + manager interact, config toggles, performance expectations). Provide rollout steps for enabling webhook filtering + consolidated caches.  
- **Testing Plan:** Run full `go test ./...` with coverage ≥80% in touched packages; re-run lint/format.  
- **Acceptance Criteria:** Build green; coverage threshold met; docs highlight new flow; plan status checklist remains updated with dates/results.
