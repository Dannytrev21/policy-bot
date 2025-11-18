# Review of installation token update plan (codex)

## What looks good
- Calls out splitting lookup/creation into smaller helpers and handling stale installation IDs vs transient errors.
- Emphasizes using the app client for token creation and keeping per-org rate limiting in place.
- Notes the need to thread `ownerID` so caches are actually used.

## Gaps / risks
- **Eager token minting on every cache hit will explode token creation** (GitHub caps ~1k token creates/hr). The ghinstallation transport already caches/refreshes tokens and retries on 401/403; calling `Apps.CreateInstallationToken` for validation fights that cache and adds latency/cost.
- **Token health should be reactive, not proactive**. You only need to mint a new token after an API call fails with 401/403/404/422 indicating revoked/suspended installation, not up-front.
- **403/422 classification matters**: Suspended or permission changes should invalidate cache; transient 403 rate limits should not. The current plan lumps “auth/404” only.
- **Installation removal path**: 404/410 from GitHub should evict/negative-cache and avoid repeated token creation attempts.
- **Concurrency/stampede control**: The plan mentions it indirectly; you likely need a per-owner singleflight around client creation/token refresh to avoid multiple callers recreating clients/tokens simultaneously.
- **V4 client**: Token refresh needs to cover both transports; relying on ghinstallation handles both v3/v4 automatically—manual minting would need to plumb tokens into both, which is easy to miss.

## Recommendations / adjustments
- **Do not create tokens on cache hits.** Trust ghinstallation’s token cache. Let API call failures (401/403/404/410/422) trigger refresh logic.
- **Handle auth errors via a retry wrapper**: On first auth failure, evict client cache for that owner, re-resolve installation ID, recreate clients. If 404/410/422 → mark negative or re-lookup; if 401/403 with “suspended” → invalidate and bubble a clear error. If rate-limit 403 → retry later, don’t recreate.
- **Keep token creation inside ghinstallation**: Avoid manual `Apps.CreateInstallationToken` unless you need a custom-scoped token; otherwise you’re duplicating the transport’s job and doubling token usage.
- **Singleflight client creation**: Wrap cache-miss and refresh paths with per-owner singleflight to avoid token storms under load.
- **Metrics**: Track auth refresh attempts, cache evictions due to auth failures, and token creation failures from ghinstallation (via transport hooks) rather than proactively minting tokens.
- **Cache TTL**: Lengthen positive TTL to ~45–50m (token life is 60m) to minimize re-minting; negative TTL can stay short.

## Suggested adjusted flow
1) Cache hit → return clients; no token mint.
2) First API call failure with auth-ish code (401/403/404/410/422) → evict cache entry, singleflight re-resolve installation ID, recreate clients; if resolved, return new clients (and cache them). If 404/410 → negative-cache.
3) Cache miss → resolve installation ID → create clients → cache → return.
4) Keep per-org rate limiting for both webhook and SQS paths to reduce pressure on token creation endpoints.
