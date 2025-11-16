# Installation Registry & Lookup Redesign – Codex Plan

## Plan Status
- [ ] **Step 1 – Registry Multi-Key Upgrade:** Extend `InstallationRegistry` entries to capture installation ID, owner/org identifiers, repo metadata, and canonical status with TTL-aware indexing.
- [ ] **Step 2 – Installation Locator Service:** Introduce a reusable lookup service that layers the richer registry over `MappingCache` and GitHub API fallbacks, exposing explicit strategies for SQS vs webhook callers.
- [ ] **Step 3 – Filtering & Cache Population Rules:** Align webhook vs SQS filtering paths, ensure only eligible events mutate caches, and honor “check_run should not affect cache” guardrails.
- [ ] **Step 4 – Client Creation & Instrumentation Updates:** Wire Base/InstallationManager/SQS processor to the locator, add metrics/tracing, and validate the end-to-end flows.

---

## Context
- Webhooks and SQS traffic run concurrently; SQS must continue bypassing the internal scheduler queue.
- App sees webhook traffic from orgs where it is not installed, generating intentional 404s; we must filter without breaking auto-merge flows.
- Installation IDs may be missing/zero in some events (especially SQS); repo/owner identifiers must be leveraged for cache lookups.
- Existing components (`InstallationRegistry`, `MappingCache`, `InstallationFilterHandler`, `InstallationManager`) already provide partial caching—it now needs to support multi-key lookups and richer metadata for reuse.
- Observability already routes go-metrics through the OTEL bridge; reuse keys when possible to avoid metric sprawl.

## Constraints
- Follow KISS and clean architecture: prefer extending existing primitives over inventing new frameworks.
- Maintain thread safety under 200 events/sec bursts; use RW locks/atomics and avoid unnecessary allocations.
- Respect GitHub API guidance: treat 401/403/404 as terminal, back off on 5xx, minimize redundant calls via caching and adaptive rate limiting.
- SQS filtering should drop only when installation is definitively absent; webhook filtering should pass through when ID is missing/zero.
- Some events (e.g., `check_run`) must not poison caches because they often omit repository context.
- Avoid reintroducing the internal scheduler for SQS execution, and keep rate limiting + circuit breaker protections intact.

## References
- `.claude/documentation/02-technical-architecture.md`
- `.claude/todo/github_app_optimization.md`
- `.claude/todo/installation_redo.md`
- `.claude/todo/installation_optimization.md`
- `.claude/todo/optimization.md`
- `.claude/todo/optimization_sqs.md`
- `.claude/todo/rate_limiting_plan.md`
- `server/handler/installation_registry.go`
- `server/handler/installation_filter.go`
- `server/handler/base.go`
- `server/handler/installation_manager.go`
- `server/sqs/…` processor and middleware for SQS context propagation

## Where to Look
- Registry, mapping cache, and filter logic under `server/handler`.
- Base initialization + cache lifecycle (`server/handler/base.go`).
- SQS processor context flags (`server/sqs/processor.go`, `server/sqs/consumer.go`).
- Docs/TODOs listed above for requirements, constraints, and already completed phases.

## Things to Keep in Mind
- Favor adapting existing metrics keys and OTEL bridge adapters; only add new ones if the signal is missing.
- Negative caching TTL must remain short (default 5m) to avoid stale denials when customers install the app.
- Ensure shared caches are initialized exactly once per Base to avoid divergent views between webhook and SQS handlers.
- Include profiling hooks (trace spans, metrics) where changes could affect hot paths for debugging bottlenecks.

## Solution Exploration (Tree of Thought)
1. **Option A – Keep Registry ID-Only + Expand MappingCache Usage:** Minimal change, but every consumer must stitch caches manually; duplication risk and harder debugging.
2. **Option B – Build External Metadata Store (Redis/DB):** Would centralize lookups but violates KISS, adds infra, and increases latency.
3. **Option C – Enrich Registry Entries + Provide Installation Locator Facade:** Reuses in-memory cache, offers consistent API for ID/repo/org lookups, concentrates instrumentation, and keeps webhook vs SQS policies configurable.

**Decision:** Option C best balances reuse, simplicity, and performance. Registry already exists; augmenting it with metadata plus a locator facade avoids new infra while enabling owner/repo lookups anywhere (filters, InstallationManager, SQS, future schedulers).

---

## Detailed Steps

### Step 1 – Registry Multi-Key Upgrade
- **Information & Inputs:** Study current structs in `installation_registry.go`, cache lifecycle hooks in `base.go`, and requirements in `.claude/todo/installation_redo.md`.
- **Implementation Outline:**
  - Introduce `InstallationRecord` capturing installation ID, owner login/ID, repo name/ID, org/repo lists, status, timestamps.
  - Maintain primary map by installation ID plus secondary maps (or embedded `MappingCache`) for `owner/login`, `ownerID`, and `owner/repo`.
  - Provide APIs: `UpsertRecord`, `MarkNotInstalled(record)`, `LookupByInstallationID`, `LookupByRepo`, `LookupByOwner`, and helpers to translate repo/org lookups back into installation IDs.
  - Preserve TTL differences (1h positive, 5m negative) per record, sharing logic across mappings.
  - Update metrics to report counts per index without exploding metric cardinality.
  - Ensure cache lifecycle helpers in `Base` delegate to new registry methods (so Installation handler remains source of truth).
- **Testing Plan:**
  - Unit tests covering concurrent reads/writes, TTL expiry per key, multi-key lookups, positive vs negative transitions, and lifecycle helpers.
  - Fuzz/benchmark optional to confirm no regression in allocation hot paths.
- **Acceptance Criteria:**
  - Registry can return installation ID given owner/repo or owner alone without API calls when cached.
  - TTL enforcement works per status; removing or updating entries updates all indexes.
  - Metrics reflect cache sizes for positive/negative + multi-key indexes.

### Step 2 – Installation Locator Service
- **Information & Inputs:** Examine `InstallationFilterHandler.lookupInstallationWithSmartCache`, SQS context flags, and InstallationManager expectations.
- **Implementation Outline:**
  - Create `InstallationLocator` (or expand InstallationManager) that orchestrates: registry lookups (ID/owner/repo), mapping cache hits, and fallback GitHub API calls (`GetInstallation`, `GetByRepository`, `GetByOwner`).
  - Define lookup strategies (direct ID, repo, owner) and make them configurable per caller (webhook vs SQS).
  - Provide methods like `Locate(ctx, identifiers, options)` returning installation ID + provenance, plus `ShouldCacheEvent(eventType)` to prevent check_run pollution.
  - Move existing lookup code from filter into the locator to avoid duplication; filter + manager + SQS consumer call into the shared service.
  - Ensure locator records which method succeeded (for metrics) and updates registry/mapping caches consistently through Step 1 APIs.
- **Testing Plan:**
  - Unit tests mocking GitHub installation service to cover success/failure per strategy, negative caching, and concurrency.
  - Tests for webhook vs SQS strategy selection, ensuring SQS tries owner/repo when ID missing while webhooks stop at ID.
  - Validate events flagged as “do not cache” skip cache writes.
- **Acceptance Criteria:**
  - Single entry point for installation lookups used by filter, InstallationManager, and future components.
  - Strategy policy toggles behave per requirement (webhook filters only by ID, SQS uses multi-method, both pass through when unresolved).
  - Metrics/logs indicate lookup method and cache effectiveness.

### Step 3 – Filtering & Cache Population Rules
- **Information & Inputs:** Requirements in `.claude/todo/installation_redo.md`, event behavior in `installation_filter.go`, and SQS filter expectations in `.claude/todo/optimization_sqs.md`.
- **Implementation Outline:**
  - Rewire `InstallationFilterHandler` to call the new locator; for webhooks, configure to only drop when locator finds a negative status for the explicit installation ID.
  - For SQS, enable full multi-key lookup but still pass through when locator returns ErrNoInstallation (no definitive negative).
  - Add event-type gating so `check_run` (and other configured types) mark `cacheMutationAllowed=false`.
  - Ensure registry updates triggered by Installation handler (install/uninstall) feed both ID and owner/repo metadata.
  - Update logging to include lookup provenance for debugging bursts or cache gaps.
- **Testing Plan:**
  - Extend `installation_filter_test.go` to cover webhook vs SQS flows, event-type gating, and the new locator integration.
  - Regression tests ensuring Installation handler still manages cache lifecycle correctly.
  - Concurrency/race tests around filter metrics and cache writes.
- **Acceptance Criteria:**
  - Webhook filtering only occurs when installation ID exists + registry marks it as not installed; otherwise events proceed.
  - SQS filtering drops only when locator determines installation is definitively absent; zero IDs flow to handlers.
  - check_run events (and any configured skip list) never mutate caches.

### Step 4 – Client Creation & Instrumentation Updates
- **Information & Inputs:** Look at `Base.NewEvalContext`, `InstallationManager.GetClients`, SQS processor message parsing, and OTEL metrics definitions.
- **Implementation Outline:**
  - Update Base and SQS processor to fetch installation IDs through the locator when payloads lack them before calling `InstallationManager`.
  - Ensure InstallationManager consumes the richer registry metadata (e.g., to log owner/repo, update caches on success/failure).
  - Add OTEL spans/attributes for lookup strategy, cache hit/miss, and installation verification path.
  - Validate rate limiter + circuit breaker still wrap client creation (no bypass).
  - Document operational runbooks referencing new metrics and debugging steps.
- **Testing Plan:**
  - Handler integration tests (webhook + SQS) ensuring client creation works when installation ID was recovered via repo/owner.
  - Tests verifying metrics counters increment under success/failure, and traces include expected attributes.
  - End-to-end SQS test (existing harness) to confirm no regressions in throughput/backpressure.
- **Acceptance Criteria:**
  - Clients can be created for events lacking installation ID as long as owner/repo is cached or discoverable.
  - Observability reflects lookup paths, and rate limiting/circuit breaker metrics stay intact.
  - Documentation updated with new behavior and troubleshooting guidance.

---

**Next Review:** Revisit after implementing Step 1 to ensure data model supports remaining steps before coding.
