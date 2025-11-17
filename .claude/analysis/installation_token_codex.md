# Installation Tokens and Clients in GHEC

## Should we mint multiple tokens concurrently?
- A GHEC org has a single installation; all installation tokens share the same 15k req/hr installation limit (and GitHub caps token creation to ~1k/hr). Best practice is to **reuse one installation token/client per installation** and let concurrent requests share that client; only create tokens concurrently for different orgs/installations.
- go-github clients are safe for concurrent use, so sharing a cached client per org is preferable to creating parallel clients/tokens for the same org. Pair this with per-org rate limiting to stay under 15k/hr.

## Current behavior
- The GHEC path calls `GetClientsByOwner` without an `ownerID`, so the `ClientCache` fast path is never used and every evaluation builds fresh clients/tokens (`server/handler/base.go:264-515`).
- Handlers call `GetClientsForEvent` without passing `ownerID`, so webhook traffic also bypasses caching and reuses nothing (`server/handler/issue_comment.go:55-73` and other handlers follow the same pattern).
- Per-org rate limiting is only wrapped around the SQS client creators; webhook handlers use the raw client creator and can burst token/API calls without org-level throttling (`server/server.go:210-238`).
- Result: multiple installation tokens can be created concurrently for the same org, and they are not reused; this risks the token-creation limit and adds avoidable latency.

## Plan to align with best practices (reuse, not spray)
1) **Use the cache in the hot path:** Thread `ownerID` from event payloads into `GetClientsForEvent` and `NewEvalContext` so GHEC requests hit `ClientCache` and reuse shared clients/tokens. Keep the existing fallback for cases where the ID is missing.
2) **Prevent stampedes on cache miss:** Add a keyed `singleflight` (by owner/org) around `GetClientsByOwner` cache misses so only one goroutine mints clients/tokens per org when the cache is cold.
3) **Throttle webhooks too:** Instantiate the `RateLimitedClientCreator` (org/global limits) for webhook handlers, not just SQS, so token creation and API calls honor per-org rate ceilings.
4) **Reduce churn:** Consider raising the positive cache TTL to ~45–50m (tokens last 60m) to cut token creation from ~6/hour to ~1–2/hour per org, and emit a metric/counter for token creation attempts to watch the rate.
