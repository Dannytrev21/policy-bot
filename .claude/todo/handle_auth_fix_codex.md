# Handle Auth Failure Fix Plan (Codex)

## Plan Status Checklist
- [x] **Step 1 – Propagate immutable owner identity through handlers** (owner IDs not yet captured or passed to caching helpers)
- [x] **Step 2 – Introduce an auth-aware helper that wraps GitHub API calls and invokes `HandleAuthFailure`** (helper not implemented)
- [x] **Step 3 – Apply the helper across evaluation, status posting, and event handlers** (call sites still use raw clients)
- [x] **Step 4 – Align SQS processor behavior, metrics, and tests with the new auth refresh flow** (SQS still treats auth errors as final failures)

## Context
- `HandleAuthFailure` (`server/handler/base.go:808-847`) has comprehensive tests but is never invoked by runtime code, so cached clients are never refreshed when GitHub responds with 401/403/404/410/422.
- `retrieveClientAndInstallationId` (`server/handler/base.go:671-805`) correctly rebuilds clients + installation IDs, yet it is only reachable via `HandleAuthFailure`, leaving most code paths unable to reacquire clients after auth errors.
- `GetOwnerIDFromEvent` (`server/handler/event_owner.go`) is unused, meaning cache entries (keyed by owner ID) are never populated/invalidated, and `HandleAuthFailure` cannot correlate auth errors back to cache entries.
- The SQS processor differentiates retryable vs non-retryable errors (`server/sqsconsumer/processor.go:323-440`), but without any auth refresh, it immediately drops auth failures as non-retryable, leading to silent data loss when caches hold invalid tokens.

## Constraints
- Maintain behavior for both GHEC (owner-based caching) and GHES (installation-based caching via `InstallationManager`).
- Keep the reactive model (only refresh after an observed failure) to avoid excess GitHub API calls.
- Preserve existing metrics/idempotency semantics (SQS `IdempotencyManager`, auth-refresh counters, cache hit tracking).
- Avoid modifying vendored dependencies or introducing additional network calls during happy path execution.
- Changes must be testable via unit tests (handlers, cache, SQS) without requiring live GitHub/SQS access.

## References
- `server/handler/base.go:294-361` – `NewEvalContext` path that currently skips owner IDs and auth recovery.
- `server/handler/base.go:566-612` – `GetClientsByOwner` logic that only caches when owner ID is provided.
- `server/handler/issue_comment.go:38-105`, `server/handler/status.go:55-129`, `server/handler/merge_group.go:96-154` – direct GitHub client usage without auth awareness.
- `server/handler/event_owner.go:19-77` – helper for extracting owner IDs from webhook payloads.
- `server/handler/errors.go:26-155` – common error classifiers used by both handlers and SQS.
- `.claude/documentation/02-technical-architecture.md` & `.claude/documentation/03-operations-playbook.md` – describe resilience goals for auth/token handling.
- `server/sqsconsumer/processor.go:323-457` – retry/non-retry handling and logging for queue processing.

## Where to Look for Information
- Event handlers in `server/handler/*.go` (identify every GitHub API call using `InstallationClients`).
- `server/handler/base_getclientsbyowner_test.go` (shows intended behavior for `HandleAuthFailure` and cache refresh metrics).
- SQS processor tests (`server/sqsconsumer/processor_test.go`, `server/sqsconsumer/consumer_test.go`) for expected retry semantics.
- Documentation under `.claude/documentation` plus `TESTING.md` for operational expectations and test harness guidance.

## Things to Keep in Mind
- `ghinstallation.Transport` already refreshes tokens near expiry; our changes should only handle invalid installations/suspensions.
- Owner names can change; owner IDs are immutable, so cache invalidation must rely on IDs.
- Ensure we do not create double-processing loops in SQS: only retry once per auth failure, and surface clear telemetry when refresh succeeds/fails.
- Maintain logging/metrics parity so existing dashboards (per docs) continue to reflect success/failure counts.
- GHES code paths still rely on installation IDs; helper must accommodate both owner-based and installation-based lookups.

## Detailed Steps

### Step 1 – Propagate immutable owner identity through handlers
**Information & Inputs:**
- `GetOwnerIDFromEvent` helper exists but is unused (`server/handler/event_owner.go`).
- `GetClientsByOwner` (`server/handler/base.go:566-612`) only caches/invalidates when owner ID > 0.
- Event handlers currently call `GetClientsForEvent`/`GetClientsByOwner` without providing owner IDs (e.g., `server/handler/issue_comment.go:53-71`, `server/handler/pull_request.go:58-75`).

**Implementation Outline:**
- Update each webhook handler to extract owner ID via `GetOwnerIDFromEvent` immediately after parsing the payload.
- Extend `pull.Locator`/`EvalContext` (and any helper structs) to carry owner ID when available so downstream helpers can invalidate caches deterministically.
- Update `GetClientsForEvent` and `NewEvalContext` callers to pass owner ID through to `GetClientsByOwner` and future auth helpers.
- Ensure `PreparePRContext` (and any context-enrichment helpers) attaches owner ID to the logger/context for reuse.

**Testing Plan:**
- Add unit tests for representative handlers (PR, issue_comment, status) verifying that owner IDs are passed into `GetClientsForEvent` (use spies/fakes to assert invocation arguments).
- Extend `base_getclientsbyowner_test.go` to add cases where owner ID is optional vs provided, ensuring cached clients are used when ID is supplied.
- Add regression test confirming `ClientCache` entries are populated/invalidated when owner IDs flow through.

**Acceptance Criteria:**
- All handler code paths obtain owner ID when GitHub payload supplies it and pass it to `GetClientsForEvent`/`NewEvalContext`.
- `ClientCache` hit/miss metrics reflect owner-ID-based caching during tests (cache hits observed in logs/tests).
- No handler panics if owner ID is missing (helper safely falls back to zero).

**Status:** Completed – owner IDs are now extracted from every webhook handler via `GetOwnerIDFromEvent`, plumbed through `pull.Locator`, `EvalContext`, and `GetClientsForEvent`, and logged through `PreparePRContext`. Regression coverage includes a cache invalidation test for real installation events and context attachment verification.

### Step 2 – Introduce auth-aware helper that invokes `HandleAuthFailure`
**Information & Inputs:**
- `HandleAuthFailure` and `retrieveClientAndInstallationId` (`server/handler/base.go:671-847`) encapsulate cache invalidation and client recreation but are unused.
- Call sites need a consistent mechanism to catch auth-related `github.ErrorResponse` values (classified via `classifyGitHubError` in `server/handler/errors.go`).
- Need to support both owner-based (GHEC) and installation-based (GHES) lookups.

**Implementation Outline:**
- Add a method on `Base`, e.g., `func (b *Base) WithAuthRefresh(ctx context.Context, meta AuthMeta, fn func(*InstallationClients) error) error`, where `AuthMeta` carries owner/ownerID/repo/installationID.
- Inside the helper:
  - Execute `fn` once with the current clients.
  - If the error is auth-related (as determined by `classifyGitHubError`), call `HandleAuthFailure` with captured metadata to refresh clients, swap them into the caller (and optionally update any shared structures such as `EvalContext`), and retry exactly once.
  - Respect 404/410 negative-caching by surfacing the wrapped error without retry.
  - Emit auth-refresh metrics using existing counters (`installation.auth_refresh.*`).
- Provide a way for the helper to supply refreshed clients back to the caller (`EvalContext`, handler struct, etc.) so subsequent calls reuse the new tokens.

**Testing Plan:**
- Create dedicated unit tests that simulate: (a) first call returning 401 followed by success, (b) 404 leading to negative caching + failure, (c) non-auth errors bypassing refresh.
- Use fake `InstallationClients` whose methods return controlled errors, verifying that `HandleAuthFailure` metrics counters increment as expected.
- Include GHES-specific test ensuring installation-based metadata triggers `InstallationManager` usage instead of owner cache.

**Acceptance Criteria:**
- Helper retries exactly once only for auth-related responses and propagates refreshed clients upon success.
- `HandleAuthFailure` metrics increment (attempt/success/failure/cache-evicted) in tests when refresh logic triggers.
- Non-auth errors and rate-limit responses skip refresh logic entirely.

**Status:** Completed – added `AuthMetadata` plus `Base.WithAuthRefresh`, which delegates to `HandleAuthFailure`, retries once, and returns the refreshed clients to callers. Tests (`server/handler/base_auth_helper_test.go`) cover auth success, refresh failure, rate-limit bypass, and generic error passthrough, ensuring the helper only engages when classification marks an auth issue.

### Step 3 – Apply the helper across evaluation, status posting, and event handlers
**Information & Inputs:**
- `NewEvalContext`, `EvalContext.PostStatus`, `IssueComment.Handle`, `Status.Handle`, `MergeGroup.Handle`, and `Installation.postRepoInstallationStatus` all perform GitHub API calls without auth-aware retries.
- Some flows (e.g., `IssueComment`) make API calls before creating an `EvalContext`; we must ensure helper usage at every GitHub interaction point.

**Implementation Outline:**
- Refactor handlers to fetch clients once (via updated `GetClientsForEvent`) and then execute API calls through the new helper. Example: wrap `clients.V3Client.PullRequests.Get` invocation inside `WithAuthRefresh`, passing owner metadata and a closure that performs the API call.
- Update `NewEvalContext` so that creating the pull context (`pull.NewGitHubContext`) and fetching repo config happens inside the helper; if refresh succeeds, use the refreshed clients for the resulting `EvalContext`.
- Change `EvalContext.PostStatus`, reviewer dismissal, and any other GitHub writes to route through the helper (or share refreshed client references) so status updates recover automatically.
- Ensure GHES-specific flows (installation events, circuit-breaker-managed clients) integrate by providing installation ID metadata to the helper.
- Update logging to indicate when an auth refresh was triggered for a handler (use structured fields `auth_refresh=true`, `installation_id`, etc.).

**Testing Plan:**
- Expand handler-level tests (or add new ones) to simulate auth failure followed by success. For example, stub `PullRequests.Get` to return 401 once and 200 after refresh, verifying handler completes successfully.
- Add integration-style tests around `EvalContext` to assert that `PostStatus` retries after an injected 403.
- Ensure negative scenarios (installation removed) bubble up deterministic errors so SQS can treat them as non-retryable.

**Acceptance Criteria:**
- All GitHub API interactions in handlers/evaluation path leverage the auth-aware helper (no direct raw client calls remain in these files without documented exceptions).
- Auth failures during PR evaluation/status posting succeed after a single refresh in tests.
- Logs/metrics clearly show refresh attempts, aligning with documentation expectations.

**Status:** Completed – `Base.NewEvalContext`, `EvalContext.PostStatus`, and high-volume handlers (issue_comment, status, merge_group, installation events) now route their GitHub calls through `WithAuthRefresh`, replacing stale `InstallationClients` as needed. A new unit test (`server/handler/eval_context_test.go`) simulates a 401 on status posting to verify retries refresh the clients before succeeding.

### Step 4 – Align SQS processor behavior, metrics, and tests with the new auth refresh flow
**Information & Inputs:**
- Current SQS processor (`server/sqsconsumer/processor.go:323-440`) marks auth errors as non-retryable without attempting recovery.
- After Steps 2-3 most auth errors should be resolved inside handlers, but permanent failures (installation removed) still need to be marked as non-retryable.
- Need traceable signaling from handlers back to SQS when an auth refresh was attempted and either succeeded or failed.

**Implementation Outline:**
- Define explicit error types/flags (e.g., `type AuthRefreshError struct { Permanent bool; Err error }`) returned by the helper when refresh fails irrecoverably (installation deleted) vs when refresh logic is intentionally skipped. Surface them via `errors.Is`/`errors.As` so SQS can log accurately instead of relying solely on string matching.
- Update SQS processor to recognize the new error types alongside existing classifiers, ensuring recoverable auth errors never reach this layer (handlers already retried) and permanent ones continue to be treated as non-retryable and deleted.
- Emit new metrics/attributes (e.g., `sqs.auth_refresh.attempted`, `sqs.auth_refresh.success`) using the registry to observe how often auth refresh occurs during SQS processing.
- Adjust processor tests to cover: (a) handler returns `AuthRefreshError{Permanent:true}` → message deleted without retry; (b) handler returns transient non-auth errors → existing retry logic unaffected.

**Testing Plan:**
- Extend `processor_test.go` or add new tests using stub handlers that return specific error types to confirm SQS classification and logging.
- Verify metrics counters increment for auth refresh scenarios using the in-memory registry.
- Regression-test existing retry logic (rate limits, network errors) to ensure no behavior regressions.

**Acceptance Criteria:**
- SQS processor differentiates between refreshed (handled) auth failures and permanent auth errors via explicit error types rather than fragile string inspection.
- Metrics/logs reflect auth refresh attempts/outcomes at the queue level.
- No messages are dropped without at least one auth-refresh attempt logged when errors are recoverable.
- All updated processor tests pass, covering both retryable and permanent failure paths.

**Status:** Completed – `handler.WithAuthRefresh` now wraps unrecoverable cases in `AuthRefreshError`, records SQS-scoped attempt/success/failure metrics (gated via context), and `sqsconsumer.Processor` recognizes the new error type to decide retry vs delete while logging specific messages. New tests cover permanent vs retryable auth refresh errors.
