# Plan: Improve installation token/client retrieval (retrieveClientAndInstallationId)

## Goal
Guarantee a valid installation token/client when retrieving from cache or minting new, and recover gracefully (refresh installation ID, update cache, and recreate client) on token errors.

## Design shape (functions to add/refine)
1) **validateOrRefreshInstallationToken(ctx, client *github.Client, installationID int64) error**
   - Uses app client `Apps.CreateInstallationToken` to mint a fresh token for the given installation ID.
   - Returns nil on success; on errors, classifies: auth/404 -> trigger re-lookup; transient -> bubble up for retry.
2) **resolveInstallationID(ctx, ownerID, ownerName, repo) (installationID int64, fromCache bool, err)**
   - Extracted from current lookup logic (owner cache → owner lookup → repo fallback).
   - On successful lookup, updates negative/positive cache as today.
3) **buildClientsForInstallation(ctx, owner, installationID) (*InstallationClients, error)**
   - Wraps `createClientsForOwner`; optionally instrument to log token creation attempts.
4) **refreshInstallationAfterTokenFailure(ctx, ownerID, ownerName, repo) (*InstallationClients, int64, error)**
   - Calls `resolveInstallationID` (no cached value accepted) to re-fetch fresh installation ID, rebuild clients, update cache with new ID.

## New flow for retrieveClientAndInstallationId
1) Try cache (`ClientCache.GetWithInstallationID`); if hit, run `validateOrRefreshInstallationToken` on the v3 client.
   - If validation succeeds → return cached clients/installation ID.
   - If validation fails with auth/404 → go to step 3 (refresh).
   - If transient → return error to retry (do not evict cache).
2) If cache miss, call `resolveInstallationID` (owner-based for GHEC, repo-based for GHES, repo fallback) to get installation ID.
3) Build clients via `buildClientsForInstallation`.
4) Validate token via `validateOrRefreshInstallationToken`.
   - On success → cache with `PutWithInstallationID` and return.
   - On auth/404 → call `refreshInstallationAfterTokenFailure` to re-lookup installation ID (avoids stale IDs), rebuild clients, update cache, return.
   - On transient → return error; keep cache untouched.

## Error-handling / cache updates
- **Auth/404 on token create**: assume stale installation ID; evict/update cache entry and re-lookup installation ID, then recreate clients.
- **Transient (5xx/timeout)**: surface error; do not evict cache; let caller retry with same cached ID.
- **Negative cache**: maintain current behavior; skip token validation if negatively cached.

## Best-practice considerations
- Token validation is done via `Apps.CreateInstallationToken` using the app client (not the cached installation client) to avoid circular dependency.
- Keep per-org rate limiting in client creator to avoid token storms.
- Instrument token creation attempts/errors for observability.
- Ensure ownerID is threaded so cache is used for GHEC (and stampede control can be added later if needed).
