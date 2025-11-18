# Installation Token Validation & Refresh Plan

**⚠️ STATUS: NOT RECOMMENDED - See Critique Below**

**Date**: November 2025
**Goal**: Add explicit token validation to `retrieveClientAndInstallationId` and implement token refresh logic when validation fails.

**🚨 CRITICAL ISSUES IDENTIFIED**:
1. **Creates new tokens instead of validating** - API cost explosion
2. **ghinstallation already handles token refresh** - Solving non-existent problem
3. **Performance degradation**: 499x slower cache hits (+50ms latency)
4. **Rate limit exhaustion**: 142x over GitHub's 5,000 req/hour limit
5. **Violates GitHub best practices** for token caching

**📄 See comprehensive critique**: `.claude/analysis/installation_token_critique.md`

**✅ RECOMMENDED ALTERNATIVE**: Error-driven cache invalidation (reactive approach)
- Zero API cost increase
- Zero latency impact
- Follows GitHub best practices
- Simpler implementation (6-10 hours vs 14-20 hours)

---

## ⚠️ Original Plan (Not Recommended)

## Problem Statement

### Current Behavior
- `retrieveClientAndInstallationId` returns cached clients without validating token validity
- Tokens created by `ghinstallation` library auto-refresh (1-hour expiry)
- ClientCache uses 10-minute TTL, but doesn't verify tokens are actually valid
- If an installation is deleted/reinstalled with a new ID, cached clients become invalid

### Desired Behavior
After retrieving a client (from cache or fresh creation):
1. **Validate token** by calling `CreateInstallationToken` API
2. **On validation failure**: Re-fetch installation ID and create new clients
3. **Update cache** with new clients and correct installation ID
4. **Return validated clients** to caller

---

## Design Overview

### New Architecture Flow

```
retrieveClientAndInstallationId()
    ↓
1. Check cache for clients + installation ID
    ↓
2. If cache hit: Validate token
    ↓
    If valid: Return cached clients ✅
    ↓
    If invalid: Continue to step 3 (treat as cache miss)
    ↓
3. Lookup installation ID from GitHub API
    ↓
4. Create clients for installation
    ↓
5. Validate token (explicit check)
    ↓
    If valid: Cache + return ✅
    ↓
    If invalid: Handle installation ID mismatch
    ↓
6. Retry with repository-based lookup (fallback)
    ↓
7. Cache + return new clients
```

### Key Functions

#### 1. **validateInstallationToken** (NEW)
```go
// validateInstallationToken checks if a client has a valid installation token.
// This catches cases where:
//   - Installation was deleted/recreated with new ID
//   - Token expired and auto-refresh failed
//   - App permissions changed requiring new token
//
// Returns: true if valid, false if invalid
func (b *Base) validateInstallationToken(ctx context.Context, client *github.Client, installationID int64) bool
```

**Implementation approach:**
- Call `client.Apps.CreateInstallationToken(ctx, installationID, nil)`
- Return `true` if successful (200 OK)
- Return `false` on errors:
  - 404 Not Found: Installation deleted or ID changed
  - 401 Unauthorized: Token invalid/expired
  - 403 Forbidden: Permissions issue

**Why this approach:**
- `CreateInstallationToken` is idempotent - safe to call multiple times
- Actually tests connectivity to GitHub API
- Catches installation ID mismatches
- Lightweight operation (minimal API cost)

#### 2. **retrieveClientAndInstallationIdWithValidation** (REFACTOR)
```go
// retrieveClientAndInstallationIdWithValidation retrieves installation clients
// with explicit token validation. This is an enhanced version of the existing
// retrieveClientAndInstallationId that adds token validation.
//
// Flow:
// 1. Check cache by owner ID
// 2. If cache hit, validate token
// 3. If validation fails OR cache miss, lookup installation ID
// 4. Create clients and validate token
// 5. On validation failure, retry with fallback lookup
// 6. Cache and return validated clients
//
// Returns: InstallationClients, installation ID, error
func (b *Base) retrieveClientAndInstallationIdWithValidation(
    ctx context.Context,
    installationID int64,
    ownerID int64,
    ownerName string,
    repo string,
) (*InstallationClients, int64, error)
```

#### 3. **refreshInstallationClients** (NEW)
```go
// refreshInstallationClients handles the case where cached clients have invalid tokens.
// It re-fetches the installation ID and creates new clients.
//
// This handles scenarios like:
//   - App was uninstalled and reinstalled (new installation ID)
//   - Installation moved to different organization
//   - Token permissions changed
//
// Returns: new InstallationClients, new installation ID, error
func (b *Base) refreshInstallationClients(
    ctx context.Context,
    ownerID int64,
    ownerName string,
    repo string,
) (*InstallationClients, int64, error)
```

---

## Implementation Plan

### Phase 1: Add Token Validation Helper (1-2 hours)

**File**: `server/handler/base.go`

```go
// validateInstallationToken checks if a client has a valid installation token.
// Returns true if the token is valid, false otherwise.
func (b *Base) validateInstallationToken(ctx context.Context, client *github.Client, installationID int64) bool {
    logger := zerolog.Ctx(ctx)

    // Try to create a new token (idempotent operation)
    token, resp, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)

    if err != nil {
        logger.Warn().
            Err(err).
            Int64("installation_id", installationID).
            Int("status_code", resp.StatusCode).
            Msg("Token validation failed")
        return false
    }

    if token == nil || token.Token == nil {
        logger.Warn().
            Int64("installation_id", installationID).
            Msg("Token validation returned nil token")
        return false
    }

    logger.Debug().
        Int64("installation_id", installationID).
        Time("expires_at", *token.ExpiresAt).
        Msg("Token validation successful")

    return true
}
```

**Tests**: `server/handler/base_token_validation_test.go` (NEW)
- Test successful validation
- Test 404 error (installation deleted)
- Test 401 error (token expired)
- Test 403 error (permissions issue)
- Test nil token response

---

### Phase 2: Add Client Refresh Logic (2-3 hours)

**File**: `server/handler/base.go`

```go
// refreshInstallationClients re-fetches installation ID and creates new clients.
// Used when cached clients have invalid tokens (e.g., app was reinstalled).
func (b *Base) refreshInstallationClients(
    ctx context.Context,
    ownerID int64,
    ownerName string,
    repo string,
) (*InstallationClients, int64, error) {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Msg("Refreshing installation clients due to invalid token")

    // Invalidate cache entry
    if b.ClientCache != nil && ownerID > 0 {
        b.ClientCache.Delete(ownerID)
        logger.Debug().
            Int64("owner_id", ownerID).
            Msg("Invalidated cache entry for owner")
    }

    // Re-lookup installation ID from API
    var installationID int64
    var err error

    if b.GithubCloud {
        // GHEC: Use org/owner name
        installation, lookupErr := b.Installations.GetByOwner(ctx, ownerName)
        if lookupErr != nil {
            logger.Error().Err(lookupErr).
                Str("owner", ownerName).
                Msg("Failed to refresh installation via owner lookup")
            err = lookupErr
        } else {
            installationID = installation.ID
        }
    } else {
        // GHES: Use repository lookup
        if repo == "" {
            return nil, 0, fmt.Errorf("repository required for GHES installation refresh")
        }
        installation, lookupErr := b.Installations.GetByRepository(ctx, ownerName, repo)
        if lookupErr != nil {
            logger.Error().Err(lookupErr).
                Str("owner", ownerName).
                Str("repo", repo).
                Msg("Failed to refresh installation via repo lookup")
            err = lookupErr
        } else {
            installationID = installation.ID
        }
    }

    // Try fallback lookup if primary failed
    if err != nil && repo != "" {
        logger.Debug().
            Str("owner", ownerName).
            Str("repo", repo).
            Msg("Attempting fallback repository lookup")

        installation, fallbackErr := b.Installations.GetByRepository(ctx, ownerName, repo)
        if fallbackErr != nil {
            return nil, 0, errors.Wrapf(fallbackErr, "failed to refresh installation for owner %s", ownerName)
        }
        installationID = installation.ID
    } else if err != nil {
        return nil, 0, errors.Wrapf(err, "failed to refresh installation for owner %s", ownerName)
    }

    // Create new clients
    clients, err := b.createClientsForOwner(ctx, ownerName, installationID)
    if err != nil {
        return nil, 0, errors.Wrapf(err, "failed to create refreshed clients for owner %s", ownerName)
    }

    // Validate new clients
    if !b.validateInstallationToken(ctx, clients.V3Client, installationID) {
        return nil, 0, fmt.Errorf("refreshed token validation failed for owner %s (installation %d)", ownerName, installationID)
    }

    // Cache new clients
    if b.ClientCache != nil && ownerID > 0 {
        b.ClientCache.PutWithInstallationID(ownerID, clients, installationID)
        logger.Info().
            Str("owner", ownerName).
            Int64("installation_id", installationID).
            Msg("Cached refreshed clients with new installation ID")
    }

    return clients, installationID, nil
}
```

**Tests**: `server/handler/base_refresh_test.go` (NEW)
- Test successful refresh with new installation ID
- Test refresh when installation deleted (should fail)
- Test fallback to repo lookup on owner lookup failure
- Test cache invalidation and re-caching
- Test GHEC vs GHES refresh paths

---

### Phase 3: Integrate Validation into Main Function (2-3 hours)

**File**: `server/handler/base.go`

**Option A: Non-Breaking Enhancement** (Recommended)
Keep existing `retrieveClientAndInstallationId`, add validation as optional feature:

```go
// retrieveClientAndInstallationId retrieves both the installation client and installation ID
// using a cache-first approach. This is the primary method for obtaining installation information.
//
// Enhanced with token validation (November 2025):
//   - Validates cached tokens before returning
//   - Automatically refreshes on validation failure
//   - Handles installation ID changes (app reinstalls)
//
// Flow:
// 1. Check cache by owner ID for cached installation ID
// 2. If cache hit, validate token
// 3. If validation fails, refresh clients (re-lookup + recreate)
// 4. If cache miss, lookup installation ID and create clients
// 5. Validate token before caching
// 6. Cache and return validated clients
//
// Returns: InstallationClients, installation ID, error
func (b *Base) retrieveClientAndInstallationId(ctx context.Context, installationID int64, ownerID int64, ownerName string, repo string) (*InstallationClients, int64, error) {
    logger := zerolog.Ctx(ctx)

    // Step 1: Check cache by owner ID first (fast path)
    if b.ClientCache != nil && ownerID > 0 {
        if clients, cachedInstallationID := b.ClientCache.GetWithInstallationID(ownerID); clients != nil {
            logger.Debug().
                Str("owner", ownerName).
                Int64("owner_id", ownerID).
                Int64("installation_id", cachedInstallationID).
                Msg("Cache hit - retrieved clients and installation ID from cache")

            // NEW: Validate cached token
            if b.validateInstallationToken(ctx, clients.V3Client, cachedInstallationID) {
                logger.Debug().
                    Str("owner", ownerName).
                    Int64("installation_id", cachedInstallationID).
                    Msg("Cached token validation successful")
                return clients, cachedInstallationID, nil
            }

            // Token invalid - refresh clients
            logger.Warn().
                Str("owner", ownerName).
                Int64("owner_id", ownerID).
                Int64("cached_installation_id", cachedInstallationID).
                Msg("Cached token invalid - refreshing clients")

            refreshedClients, newInstallationID, err := b.refreshInstallationClients(ctx, ownerID, ownerName, repo)
            if err != nil {
                // Refresh failed - invalidate cache and continue to normal lookup
                logger.Error().Err(err).
                    Str("owner", ownerName).
                    Msg("Client refresh failed - will attempt normal lookup")
                if b.ClientCache != nil && ownerID > 0 {
                    b.ClientCache.Delete(ownerID)
                }
                // Continue to step 2 (normal lookup flow)
            } else {
                // Refresh successful
                return refreshedClients, newInstallationID, nil
            }
        }

        // Check for negative cache entry
        if b.ClientCache.IsNegativelyCached(ownerID) {
            logger.Debug().
                Str("owner", ownerName).
                Int64("owner_id", ownerID).
                Msg("Negative cache hit - installation not found (cached)")
            return nil, 0, fmt.Errorf("installation not found for owner %s (negatively cached)", ownerName)
        }
    }

    logger.Debug().
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Int64("provided_installation_id", installationID).
        Str("repo", repo).
        Msg("Cache miss - looking up installation")

    // Step 2-3: Get installation ID from API if not provided
    // (Keep existing lookup logic - lines 562-641)
    var finalInstallationID int64 = installationID
    var lookupErr error

    if finalInstallationID == 0 {
        if !b.GithubCloud {
            // GHES: Use repository-based lookup
            if repo == "" {
                return nil, 0, fmt.Errorf("repository name required for GHES installation lookup")
            }
            installation, err := b.Installations.GetByRepository(ctx, ownerName, repo)
            if err != nil {
                lookupErr = err
            } else {
                finalInstallationID = installation.ID
            }
        } else {
            // GHEC: Use org/owner name
            installation, err := b.Installations.GetByOwner(ctx, ownerName)
            if err != nil {
                lookupErr = err
            } else {
                finalInstallationID = installation.ID
            }
        }
    }

    // Fallback logic (keep existing - lines 607-641)
    if lookupErr != nil && repo != "" {
        installation, err := b.Installations.GetByRepository(ctx, ownerName, repo)
        if err != nil {
            if b.ClientCache != nil && ownerID > 0 {
                b.ClientCache.PutNegative(ownerID)
            }
            return nil, 0, errors.Wrapf(err, "failed to find installation for owner %s", ownerName)
        }
        finalInstallationID = installation.ID
    } else if lookupErr != nil {
        if b.ClientCache != nil && ownerID > 0 {
            b.ClientCache.PutNegative(ownerID)
        }
        return nil, 0, errors.Wrap(lookupErr, "failed to find installation")
    }

    if finalInstallationID == 0 {
        return nil, 0, fmt.Errorf("failed to obtain installation ID for owner %s", ownerName)
    }

    // Step 4: Create installation clients
    clients, err := b.createClientsForOwner(ctx, ownerName, finalInstallationID)
    if err != nil {
        return nil, 0, errors.Wrapf(err, "failed to create clients for owner %s", ownerName)
    }

    // NEW: Step 5: Validate token before caching
    if !b.validateInstallationToken(ctx, clients.V3Client, finalInstallationID) {
        logger.Error().
            Str("owner", ownerName).
            Int64("installation_id", finalInstallationID).
            Msg("Token validation failed for newly created client")

        // Try refresh as last resort
        refreshedClients, newInstallationID, refreshErr := b.refreshInstallationClients(ctx, ownerID, ownerName, repo)
        if refreshErr != nil {
            return nil, 0, errors.Wrapf(refreshErr, "token validation failed and refresh failed for owner %s", ownerName)
        }

        return refreshedClients, newInstallationID, nil
    }

    // Step 6: Cache clients with installation ID
    if b.ClientCache != nil && ownerID > 0 {
        b.ClientCache.PutWithInstallationID(ownerID, clients, finalInstallationID)
        logger.Debug().
            Str("owner", ownerName).
            Int64("owner_id", ownerID).
            Int64("installation_id", finalInstallationID).
            Msg("Cached validated clients and installation ID")
    }

    return clients, finalInstallationID, nil
}
```

**Tests**: Update `server/handler/base_getclientsbyowner_test.go`
- Add test for cached token validation success
- Add test for cached token validation failure + refresh
- Add test for new client validation failure + refresh
- Add test for refresh failure fallback
- Update existing tests to expect validation calls

**Option B: Separate Function** (If you want backwards compatibility)
Create `retrieveClientAndInstallationIdWithValidation` as new function, keep old one unchanged.

---

### Phase 4: Add Metrics & Observability (1 hour)

**File**: `server/handler/base.go`

Add new metrics:
```go
const (
    MetricsKeyTokenValidationSuccess = "installation.token_validation.success"
    MetricsKeyTokenValidationFailure = "installation.token_validation.failure"
    MetricsKeyClientRefreshSuccess   = "installation.client_refresh.success"
    MetricsKeyClientRefreshFailure   = "installation.client_refresh.failure"
)
```

Update validation function:
```go
func (b *Base) validateInstallationToken(ctx context.Context, client *github.Client, installationID int64) bool {
    // ... validation logic ...

    if err != nil {
        b.recordInstallationClientMetric(MetricsKeyTokenValidationFailure)
        return false
    }

    b.recordInstallationClientMetric(MetricsKeyTokenValidationSuccess)
    return true
}
```

**Dashboard Updates**: `.claude/dashboards/operational-dashboard.md`
- Add panel for token validation success rate
- Add panel for client refresh frequency
- Add alert for high validation failure rate (> 5%)

---

### Phase 5: Configuration & Feature Flag (1 hour)

**File**: `config/policy-bot.example.yml`

Add configuration option:
```yaml
github:
  app:
    # Enable token validation before returning cached clients
    # Recommended: true (default)
    # Set to false to disable validation (use cached clients without validation)
    validate_cached_tokens: true

    # Token validation timeout (seconds)
    # Default: 5 seconds
    token_validation_timeout: 5
```

**File**: `server/handler/config.go`

```go
type Config struct {
    // ... existing fields ...

    ValidateCachedTokens     bool          `yaml:"validate_cached_tokens" json:"validate_cached_tokens"`
    TokenValidationTimeout   time.Duration `yaml:"token_validation_timeout" json:"token_validation_timeout"`
}
```

Update `validateInstallationToken` to check config:
```go
func (b *Base) validateInstallationToken(ctx context.Context, client *github.Client, installationID int64) bool {
    // Check if validation is enabled
    if !b.Config.ValidateCachedTokens {
        return true // Skip validation if disabled
    }

    // Add timeout
    ctx, cancel := context.WithTimeout(ctx, b.Config.TokenValidationTimeout)
    defer cancel()

    // ... rest of validation logic ...
}
```

---

## Testing Strategy

### Unit Tests (6-8 hours)

**New test files:**
1. `server/handler/base_token_validation_test.go` - Token validation tests
2. `server/handler/base_refresh_test.go` - Client refresh tests

**Updated test files:**
1. `server/handler/base_getclientsbyowner_test.go` - Add validation scenarios

**Test coverage targets:**
- Token validation: 100% coverage
- Client refresh: 100% coverage
- Integration scenarios: 95% coverage

**Key scenarios to test:**
1. ✅ Cache hit + valid token → Return cached clients
2. ✅ Cache hit + invalid token → Refresh clients
3. ✅ Cache miss → Create + validate + cache
4. ✅ New client invalid token → Refresh
5. ✅ Refresh success → Return new clients
6. ✅ Refresh failure → Return error
7. ✅ Installation ID changed → Detect via validation, refresh succeeds
8. ✅ Installation deleted → Validation fails, refresh fails, return error
9. ✅ Validation disabled via config → Skip validation
10. ✅ Validation timeout → Treat as failure

### Integration Tests (2-3 hours)

**File**: `test/installation_token_integration_test.go` (NEW)

Test scenarios:
1. Full flow with real GitHub API (using test installation)
2. Token validation with expired installation
3. Client refresh after app reinstall
4. Concurrent token validation requests

---

## Rollout Strategy

### Phase 1: Development & Testing (2 weeks)
- Implement all phases
- Comprehensive unit + integration tests
- Code review

### Phase 2: Staging Deployment (1 week)
- Deploy with `validate_cached_tokens: true`
- Monitor metrics:
  - Token validation failure rate (expect < 1%)
  - Client refresh rate (expect < 0.1%)
  - P95 latency impact (expect < 50ms increase)

### Phase 3: Production Rollout (1 week)
- Gradual rollout: 10% → 50% → 100%
- Monitor for errors and latency impact
- Rollback plan: Set `validate_cached_tokens: false`

---

## Performance Considerations

### Latency Impact

| Scenario | Current | With Validation | Delta |
|----------|---------|-----------------|-------|
| Cache hit (99%) | ~0.1ms | ~50ms | +49.9ms |
| Cache miss (1%) | ~50ms | ~100ms | +50ms |
| Overall P95 | ~50ms | ~100ms | +50ms |

**Mitigation:**
- Validation runs only for cache hits (99% of requests)
- Validation is a single API call (~50ms)
- Can be disabled via config if latency is critical

### API Cost

- **Additional API calls**: 1 per cache hit (99% of requests)
- **GitHub rate limit**: 5,000/hour per installation
- **Expected usage**: ~200 events/sec × 60% cache hit × 1 validation call = ~120 calls/sec = 432,000 calls/hour
- **Concern**: May exceed rate limits for high-traffic orgs

**Optimization idea:**
- Add validation TTL: Only validate once per 5 minutes
- Cache validation results separately

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Increased latency | High | Medium | Make validation optional via config |
| Rate limit exhaustion | Medium | High | Add validation result caching (5 min TTL) |
| Validation API failures | Low | Medium | Retry with exponential backoff |
| Cache thrashing on refresh | Low | Low | Rate limit refresh operations |

---

## Future Enhancements

### Phase 6: Validation Result Caching (Optional)
Cache validation results for 5 minutes to reduce API calls:

```go
type ValidationCache struct {
    cache sync.Map // map[int64]time.Time (key = installation ID, value = last validated)
    ttl   time.Duration // 5 minutes
}

func (b *Base) validateInstallationToken(ctx context.Context, client *github.Client, installationID int64) bool {
    // Check if recently validated
    if lastValidated, ok := b.ValidationCache.Get(installationID); ok {
        if time.Since(lastValidated) < b.ValidationCache.ttl {
            return true // Skip validation, recently validated
        }
    }

    // Perform validation...
    if valid {
        b.ValidationCache.Put(installationID, time.Now())
    }

    return valid
}
```

---

## Summary

**Total Effort Estimate**: 14-20 hours (2-3 days)

**Benefits:**
- ✅ Catches invalid cached tokens
- ✅ Handles installation ID changes automatically
- ✅ Improves reliability for app reinstall scenarios
- ✅ Adds observability via metrics

**Trade-offs:**
- ⚠️ Adds ~50ms latency to cache hits
- ⚠️ Increases API call volume (~2x)
- ⚠️ May hit rate limits for high-traffic orgs

**Recommendation:**
- Implement with config option (default: enabled)
- Add validation result caching (5 min TTL) to reduce API cost
- Monitor metrics closely during rollout
- Consider making validation async for performance-critical paths

---

## Open Questions

1. Should validation be synchronous or asynchronous?
   - **Synchronous**: Guaranteed valid token, but adds latency
   - **Asynchronous**: No latency impact, but may return invalid token

2. Should we validate on every cache hit or periodically?
   - **Every hit**: Most reliable, highest API cost
   - **Periodic** (e.g., once per 5 min): Lower cost, slight reliability trade-off

3. What's the expected rate of installation ID changes?
   - Need metrics to determine if this optimization is worth the complexity

4. Should validation have a circuit breaker?
   - If GitHub API is down, validation always fails → skip validation temporarily

---

**Next Steps:**
1. Review this plan with team
2. Decide on synchronous vs asynchronous validation
3. Implement Phase 1 (token validation helper)
4. Write tests for Phase 1
5. Iterate through remaining phases
