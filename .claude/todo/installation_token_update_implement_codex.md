# Installation Token Update — Implementation Plan (codex)

## Status Checklist
- [x] Phase 0: Align on approach (reactive auth handling, no eager token mint) ✅ COMPLETE
- [x] Phase 1: Refactor retrieval helpers (ID resolution, cache use, singleflight guard) ✅ COMPLETE
- [x] Phase 2: Auth failure handling & cache invalidation ✅ COMPLETE
- [x] Phase 3: Telemetry & configurability ✅ COMPLETE
- [x] Phase 4: Tests & verification ✅ COMPLETE
- [ ] Phase 5: Rollout prep (docs, toggles, follow-ups)

## Context
- Preferred approach from `installation_token_update_codex2.md`: rely on `ghinstallation` for token caching/refresh; react to auth errors (401/403/404/410/422) by evicting cache, re-resolving installation ID, and recreating clients under singleflight; avoid proactive `Apps.CreateInstallationToken` on cache hits.
- Original plan in `installation_token_update.md` is marked “NOT RECOMMENDED” due to token churn/latency/rate-limit risk.

## Constraints
- Keep GitHub best practices: minimize installation token creation; let `ghinstallation` manage token lifecycle.
- Preserve rate limiting (per-org) and caching semantics; avoid breaking GHES path.
- Stay wary of rate limits (token create ~1k/hr cap; installs 15k req/hr).

## References
- Critique/adjusted flow: `.claude/todo/installation_token_update_codex2.md`
- Original deprecated plan: `.claude/todo/installation_token_update.md`
- Code hotspots: `server/handler/base.go` (retrieveClientAndInstallationId, GetClientsByOwner/GetClientsForEvent), `server/handler/client_cache.go`, `server/handler/rate_limiter.go`, `server/server.go` (client creator wiring), `vendor/github.com/palantir/go-githubapp/githubapp/client_creator.go` (ghinstallation usage).

## Where to look
- Auth failures classification: go-github errors/status codes in handlers/tests.
- Cache behavior: `ClientCache` TTLs and methods (`GetWithInstallationID`, `PutWithInstallationID`, `PutNegative`).
- Rate limiting: `RateLimitedClientCreator` usage (webhooks currently not wrapped).

## Things to keep in mind
- Do not mint tokens on cache hits; let first auth failure trigger refresh.
- Ensure both v3 and v4 clients are recreated together.
- Add singleflight per owner to prevent stampedes on cache-miss/refresh.
- Keep GHES path (installation-based) intact; owner-based logic only for GHEC.

---

## Phase 0: Align on approach ✅ COMPLETE
- Info: Summarize chosen reactive strategy vs deprecated proactive plan.
- Implementation: ✅ Updated internal comments in `base.go` near retrieval functions to reflect reactive approach plan.
  - Updated `retrieveClientAndInstallationId()` comment (line 518+) to document:
    * Token management is handled by ghinstallation.Transport (auto-refresh 1 min before expiry)
    * NO proactive token creation or validation
    * Auth failures (401/403/404/410/422) trigger cache invalidation (reactive approach)
    * Noted that callers should use handleAuthFailure() (to be implemented in Phase 2)
  - Updated `GetClientsByOwner()` comment (line 401+) with similar reactive approach documentation
  - Clarified that ghinstallation.Transport handles token creation automatically
- Testing: ✅ Verified code compiles successfully with `go build ./server/handler`
- Acceptance: ✅ Documentation clearly states reactive approach; no proactive validation added.

### Changes Made:
1. **File**: `server/handler/base.go`
   - Line 518-540: Updated `retrieveClientAndInstallationId()` function comment
   - Line 401-423: Updated `GetClientsByOwner()` function comment
   - Added "Token Management Strategy (Reactive Approach)" sections to both
   - Removed misleading reference to "Create installation token" in flow
   - Added note about handleAuthFailure() for error recovery (Phase 2)

### Verification:
```bash
go build ./server/handler  # ✅ Compiles successfully
```

## Phase 1: Refactor retrieval helpers
- Info: Current retrieval in `server/handler/base.go:518+`.
- Implementation: ✅ Done
  - Added `resolveInstallationID` helper (owner cache → owner lookup → repo fallback with negative caching) in `server/handler/base.go`.
  - Added per-owner/installation singleflight guard (`clientSingleflight` + key helper) around cache-miss/refresh paths for both `GetClientsByOwner` and `retrieveClientAndInstallationId`.
  - Kept ownerID threading and cache semantics; no proactive token creation added.
- Testing: ✅ Done
  - Added concurrency test `TestGetClientsByOwner_SingleflightPreventsStampede` and ensured existing cache-hit/miss/negative-cache tests remain.
  - `go test ./server/handler/...` (passes, ~46s).
- Acceptance: ✅ Met
  - Cache hits remain fast; cache misses guarded by singleflight; GHES repo lookup path preserved; helpers do not mint tokens.

## Phase 2: Auth failure handling & cache invalidation
- Info: Auth-ish errors handled: 401/403/404/410/422; rate-limit 403 (RateLimitError) is exempt.
- Implementation: ✅ Done
  - Added `classifyGitHubError` helper in `server/handler/errors.go` to distinguish auth vs rate-limit errors using go-github types.
  - Added `Base.HandleAuthFailure` to reactively invalidate cache and recreate clients (401/403/422) or negative-cache on 404/410; no proactive token minting.
  - Reuse existing singleflight/caching pathways via `retrieveClientAndInstallationId`.
- Testing: ✅ Done
  - Added auth handling tests: rate-limit passthrough, 404 negative cache, 401 refresh with new clients (`server/handler/base_getclientsbyowner_test.go`).
  - `go test ./server/handler/...` (passes).
- Acceptance: ✅ Met
  - Cache is not mutated on rate limits; 404/410 produce negative cache; auth failures refresh clients and update cache.

## Phase 3: Telemetry & configurability
- Info: Metrics registry already present.
- Implementation: ✅ Done
  - Added auth refresh counters (`installation.auth_refresh.{attempt,success,failure,cache_evicted}`) and recording helper.
  - Added optional flag `Base.AuthRefreshEnabled` (default true in Initialize) to allow disabling reactive refresh if ever needed.
  - Wired `HandleAuthFailure` to emit metrics and honor the flag; no proactive token creation added.
- Testing: ✅ Done
  - Auth handling tests assert counters for rate-limit passthrough (no increment), 404 negative cache (attempt+failure), and 401 refresh (attempt+success).
  - `go test ./server/handler/...` (passes).
- Acceptance: ✅ Met
  - Metrics emitted for auth refresh paths; flag defaults keep reactive behavior enabled.

## Phase 4: Tests & verification
- Info: Combined coverage across cache, singleflight, and auth-refresh paths.
- Implementation: ✅ Done in prior phases and validated here.
- Testing: ✅ Done
  - `go test ./server/handler/...` (cached) after Phase 3 changes.
  - Sustained concurrency test for singleflight; auth failure tests (rate-limit passthrough, 404 negative cache, 401 refresh) all passing.
- Acceptance: ✅ Met
  - Handler test suite fully green; new tests stable.

## Phase 5: Rollout prep
- Info: Docs and toggles.
- Implementation:
  - Document behavior in `TESTING.md` or a short note in `.claude/analysis` if needed; highlight that tokens are not minted on cache hits and refresh is reactive to auth failures.
  - If a config flag was added, update sample configs and README as needed.
- Testing:
  - N/A (doc review).
- Acceptance:
  - Documentation updated; sample configs (if any) reflect new flag defaults.
