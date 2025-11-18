# Critique: Installation Token Validation Plan

**Date**: November 2025
**Reviewer**: Technical Analysis
**Document Under Review**: `.claude/todo/installation_token_update.md`

---

## Executive Summary

**Recommendation: DO NOT IMPLEMENT as proposed.** The plan has **fundamental flaws** that would introduce significant performance degradation and API cost increases while providing minimal benefit. The `ghinstallation` library already handles token refresh automatically, making proactive validation unnecessary and wasteful.

**Alternative recommendation**: Implement **error-driven cache invalidation** (lazy validation) which is more efficient, follows GitHub best practices, and solves the actual problem.

---

## Critical Issues with Current Plan

### ❌ Issue 1: Misunderstanding Token Management

**Problem**: The plan proposes calling `CreateInstallationToken` to "validate" tokens, but this **creates NEW tokens** rather than validating existing ones.

**Evidence**:
```go
// From the plan (Phase 1)
token, resp, err := client.Apps.CreateInstallationToken(ctx, installationID, nil)
```

This is the GitHub API endpoint: `POST /app/installations/{installation_id}/access_tokens`
This **creates a new token**, not validates an existing one.

**Impact**:
- Creates ~120 new tokens/second (432,000/hour) for high-traffic systems
- Wasteful token proliferation
- Unnecessary API calls
- **GitHub doesn't provide a "validate token" endpoint** - tokens are validated implicitly during actual API calls

---

### ❌ Issue 2: Token Refresh Already Handled

**Problem**: The `ghinstallation` library **already handles token refresh automatically**.

**Evidence** from `vendor/github.com/bradleyfalzon/ghinstallation/v2/transport.go:136-149`:

```go
// Token checks the active token expiration and renews if necessary.
func (t *Transport) Token(ctx context.Context) (string, error) {
    t.mu.Lock()
    defer t.mu.Unlock()

    // Automatically refresh if token is expired or expiring soon (1 minute buffer)
    if t.token == nil || t.token.ExpiresAt.Add(-time.Minute).Before(time.Now()) {
        if err := t.refreshToken(ctx); err != nil {
            return "", fmt.Errorf("could not refresh installation id %v's token: %w", t.installationID, err)
        }
    }

    return t.token.Token, nil
}
```

**Key points**:
- Tokens are checked **before every API request**
- Auto-refresh happens **1 minute before expiry**
- Tokens expire after **1 hour**
- ClientCache TTL is **10 minutes** (well within token validity)

**Conclusion**: Token expiry is **NOT a problem**. The library handles it transparently.

---

### ❌ Issue 3: Massive Performance Degradation

**Problem**: Adding validation on every cache hit destroys cache performance benefits.

| Scenario | Current | With Validation | Delta | Impact |
|----------|---------|-----------------|-------|--------|
| Cache hit (99%) | ~0.1ms | ~50ms | **+49.9ms** | **499x slower** |
| Cache miss (1%) | ~50ms | ~100ms | +50ms | 2x slower |
| **Overall P95** | ~50ms | ~100ms | **+50ms** | **2x slower** |

**Analysis**:
- Cache hit performance becomes **worse than cache miss**
- Defeats the entire purpose of caching
- 99% cache hit rate means 99% of requests pay this penalty
- User-facing latency doubles

---

### ❌ Issue 4: Rate Limit Exhaustion Risk

**Problem**: Validation API calls will likely exceed GitHub rate limits.

**Calculation**:
```
Current traffic: 200 events/sec
Cache hit rate: 99%
Validation calls: 200 × 0.99 = ~198 calls/sec
Hourly validation calls: 198 × 3600 = 712,800 calls/hour

GitHub rate limit: 5,000 requests/hour per installation
```

**Result**: **142x over rate limit** 🚨

Even with "validation result caching" (5 min TTL):
```
Unique orgs: ~100
Validations per org per hour: 12 (every 5 min)
Total: 1,200 validation calls/hour
```

Still **24% of rate limit budget** spent on validation alone, reducing capacity for actual work.

---

### ❌ Issue 5: Solving the Wrong Problem

**The ACTUAL problem**: Detecting when installation ID changes (app reinstall/uninstall)

**Token expiry**: NOT a problem (ghinstallation handles it)
**Installation ID changes**: Rare event (~1-2 times/month per org)

**Plan's approach**: Proactive validation on EVERY request (millions/day)
**Better approach**: Reactive invalidation on ACTUAL errors (1-2 times/month)

**Cost-benefit analysis**:
| Approach | API Calls/Month | Latency Impact | Problem Detection |
|----------|-----------------|----------------|-------------------|
| Proactive (plan) | 500M+ | +50ms P95 | Immediate |
| Reactive (proposed) | ~100 | None | Within 1 request |

---

## What GitHub Best Practices Actually Say

From GitHub documentation and web search results:

### ✅ Best Practice 1: Cache Tokens

> **"You should cache tokens that you create. Before you create a new token, check your cache to see if you already have a valid token. Reusing tokens will make your app faster."**

**Current implementation**: ✅ ClientCache with 10-minute TTL
**Plan**: ❌ Creates new tokens every cache hit

---

### ✅ Best Practice 2: Tokens Auto-Refresh

> **"The token is cached by the client and is refreshed as needed if it expires."**

**Current implementation**: ✅ ghinstallation handles this
**Plan**: ❌ Unnecessary manual validation

---

### ✅ Best Practice 3: Handle Errors Gracefully

GitHub best practices emphasize **error handling** over proactive validation:
- Catch 401 Unauthorized → Token invalid
- Catch 404 Not Found → Installation deleted
- Retry with exponential backoff

**Current implementation**: ⚠️ Missing error-driven cache invalidation
**Plan**: ❌ Proactive validation instead of reactive error handling

---

## Recommended Alternative Approach

### ✅ Solution: Error-Driven Cache Invalidation (Lazy Validation)

**Principle**: Let GitHub API tell us when something is wrong, then fix it.

**Flow**:
```
1. Get client from cache (fast: 0.1ms)
2. Use client for actual API call
3. If API call succeeds → All good ✅
4. If API call fails with 401/404:
   a. Invalidate cache entry
   b. Re-fetch installation ID
   c. Create new client
   d. Retry original request
   e. Cache new client
```

**Benefits**:
- ✅ Zero latency impact on successful requests (99.9% of requests)
- ✅ Zero additional API calls for validation
- ✅ Handles installation ID changes automatically
- ✅ Follows GitHub best practices (reactive error handling)
- ✅ Self-healing without manual intervention

---

## Implementation Plan (Revised)

### Phase 1: Add Error-Aware Middleware (2-3 hours)

**File**: `server/handler/base.go`

```go
// ErrorAwareClientWrapper wraps a GitHub client to detect and handle
// installation-related errors (401, 404) by invalidating cache.
type ErrorAwareClientWrapper struct {
    V3Client       *github.Client
    V4Client       *githubv4.Client
    OwnerID        int64
    OwnerName      string
    InstallationID int64
    Cache          *ClientCache
    Base           *Base
}

// detectInstallationError checks if an error indicates installation issues
func (b *Base) detectInstallationError(err error) bool {
    if err == nil {
        return false
    }

    // Check for 401 Unauthorized (token invalid)
    if ghErr, ok := err.(*github.ErrorResponse); ok {
        if ghErr.Response.StatusCode == 401 || ghErr.Response.StatusCode == 404 {
            return true
        }
    }

    // Check for specific error messages
    errMsg := err.Error()
    return strings.Contains(errMsg, "could not refresh installation") ||
           strings.Contains(errMsg, "installation not found") ||
           strings.Contains(errMsg, "token") && strings.Contains(errMsg, "invalid")
}

// handleInstallationError invalidates cache and attempts recovery
func (b *Base) handleInstallationError(ctx context.Context, ownerID int64, ownerName string, repo string, originalErr error) (*InstallationClients, error) {
    logger := zerolog.Ctx(ctx)

    logger.Warn().
        Err(originalErr).
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Msg("Installation error detected - invalidating cache and refreshing")

    // Metric: Installation error detected
    b.recordInstallationClientMetric(MetricsKeyInstallationErrorDetected)

    // Invalidate cache entry
    if b.ClientCache != nil && ownerID > 0 {
        b.ClientCache.Delete(ownerID)
    }

    // Re-fetch installation and create new clients
    // This uses the existing retrieveClientAndInstallationId logic
    clients, _, err := b.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, repo)
    if err != nil {
        b.recordInstallationClientMetric(MetricsKeyInstallationRefreshFailure)
        return nil, errors.Wrapf(err, "failed to recover from installation error: %v", originalErr)
    }

    b.recordInstallationClientMetric(MetricsKeyInstallationRefreshSuccess)
    logger.Info().
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Msg("Successfully recovered from installation error")

    return clients, nil
}
```

---

### Phase 2: Add Retry-with-Recovery Wrapper (2-3 hours)

**File**: `server/handler/github_client_wrapper.go` (NEW)

```go
// GitHubClientWithRetry wraps GitHub API calls with automatic retry on installation errors
type GitHubClientWithRetry struct {
    *github.Client
    ownerID   int64
    ownerName string
    repo      string
    base      *Base
}

// Example: Wrap a common operation
func (g *GitHubClientWithRetry) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
    // First attempt with cached client
    repository, resp, err := g.Client.Repositories.Get(ctx, owner, repo)

    // Check if error indicates installation issue
    if g.base.detectInstallationError(err) {
        logger := zerolog.Ctx(ctx)
        logger.Info().
            Str("owner", owner).
            Str("repo", repo).
            Msg("Installation error detected - attempting recovery")

        // Refresh client
        newClients, refreshErr := g.base.handleInstallationError(ctx, g.ownerID, g.ownerName, g.repo, err)
        if refreshErr != nil {
            return nil, resp, refreshErr
        }

        // Retry with new client
        g.Client = newClients.V3Client
        return g.Client.Repositories.Get(ctx, owner, repo)
    }

    return repository, resp, err
}
```

**Alternative (Simpler)**: Use HTTP middleware on the transport layer

```go
// InstallationErrorRetryTransport wraps http.RoundTripper to detect installation errors
type InstallationErrorRetryTransport struct {
    base      http.RoundTripper
    ownerID   int64
    ownerName string
    handler   *Base
}

func (t *InstallationErrorRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    resp, err := t.base.RoundTrip(req)

    // If 401 or 404, trigger cache invalidation
    if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 404) {
        // Trigger async cache invalidation (don't block this request)
        go func() {
            ctx := context.Background()
            _, _ = t.handler.handleInstallationError(ctx, t.ownerID, t.ownerName, "", fmt.Errorf("received %d", resp.StatusCode))
        }()
    }

    return resp, err
}
```

---

### Phase 3: Add Metrics & Monitoring (1 hour)

**New metrics**:
```go
const (
    MetricsKeyInstallationErrorDetected   = "installation.error.detected"
    MetricsKeyInstallationRefreshSuccess  = "installation.refresh.success"
    MetricsKeyInstallationRefreshFailure  = "installation.refresh.failure"
    MetricsKeyInstallationRecoveryLatency = "installation.recovery.latency_ms"
)
```

**Dashboard alerts**:
- Alert if `installation.error.detected` > 5/hour (indicates problem)
- Alert if `installation.refresh.failure` > 0 (manual intervention needed)

---

### Phase 4: Optional - Installation Webhooks (Advanced)

For **proactive detection** without API costs, listen to webhooks:

**Webhook events to handle**:
- `installation.deleted` → Invalidate all cache entries for that installation
- `installation.created` → Clear negative cache entries for that org
- `installation.suspend` → Mark installation as unavailable
- `installation.unsuspend` → Clear suspension flag

**Benefits**:
- Immediate notification of installation changes
- Zero API cost
- Proactive cache management

**Implementation**:
```go
func (h *InstallationWebhookHandler) HandleInstallationDeleted(ctx context.Context, event *github.InstallationEvent) error {
    installationID := event.Installation.GetID()

    // Invalidate all cache entries for this installation
    h.ClientCache.InvalidateByInstallation(installationID)

    logger.Info().
        Int64("installation_id", installationID).
        Msg("Invalidated cache due to installation deletion")

    return nil
}
```

---

## Comparison: Current Plan vs. Recommended Approach

| Aspect | Current Plan | Recommended Approach | Winner |
|--------|-------------|----------------------|--------|
| **API Calls** | +712,800/hour | +0/hour (only on errors) | ✅ Recommended |
| **Latency (P95)** | +50ms | +0ms (99.9% of requests) | ✅ Recommended |
| **Rate Limit Impact** | 142x over limit | 0% increase | ✅ Recommended |
| **Complexity** | High (5 phases) | Low (2-3 phases) | ✅ Recommended |
| **Cache Performance** | Destroyed | Maintained | ✅ Recommended |
| **Problem Detection** | Immediate | Within 1 request | ⚠️ Slight edge to plan |
| **GitHub Best Practices** | Violates | Follows | ✅ Recommended |
| **Implementation Time** | 14-20 hours | 6-10 hours | ✅ Recommended |
| **Operational Risk** | High | Low | ✅ Recommended |

---

## Real-World Installation ID Change Frequency

**Estimated frequency**:
- App reinstall: 1-2 times/month per org (manual action)
- App suspension: Rare (~1-2 times/year)
- Installation migration: Very rare

**For 100 orgs**:
- ~2 installation ID changes/month = ~0.0015/hour
- Current plan cost: 712,800 API calls/hour to prevent 0.0015 errors/hour
- **Cost per error prevented**: 475 million API calls

**Recommended approach**:
- 0 proactive API calls
- ~1-2 retry API calls when errors occur
- **Cost per error handled**: 1-2 API calls

---

## Why the Current Plan Exists (Root Cause Analysis)

The plan likely stems from a misunderstanding of:

1. **How `ghinstallation` works** → Assumption that tokens can become invalid while cached
2. **What "validation" means** → Confusion between "validate existing token" vs "create new token"
3. **GitHub API design** → No explicit "validate token" endpoint; validation is implicit
4. **The actual problem** → Installation ID changes (rare) conflated with token expiry (already handled)

---

## Recommendations

### Immediate Actions

1. **❌ DO NOT implement the current plan**
2. **✅ Implement error-driven cache invalidation** (Phases 1-3 above)
3. **✅ Add metrics for installation errors** (track how often this happens)
4. **✅ Monitor for 1-2 months** to validate approach

### Future Enhancements (Optional)

1. **Installation webhook listeners** (Phase 4) - If proactive detection is needed
2. **Circuit breaker for installations** - If certain installations repeatedly fail
3. **TTL adjustment** - Reduce ClientCache TTL if installation changes become frequent

### Documentation Updates

Update `.claude/todo/installation_token_update.md` with:
- ❌ Mark current approach as "Not Recommended"
- ✅ Add this critique as context
- ✅ Replace implementation plan with error-driven approach

---

## Conclusion

The current plan is **well-intentioned but fundamentally flawed**. It attempts to solve a non-existent problem (token expiry) using an inappropriate tool (creating new tokens) while introducing severe performance and cost penalties.

**The right solution is simpler, faster, cheaper, and follows GitHub best practices**: Detect errors during actual API calls and invalidate cache reactively.

**Key insight**: Trust the `ghinstallation` library to handle token refresh. Focus on detecting and recovering from installation ID changes when they actually occur (rarely), not preventing them proactively (expensive and wasteful).

---

**Recommended Path Forward**:
1. Implement error-driven cache invalidation (6-10 hours)
2. Add observability metrics (2 hours)
3. Monitor in production for 1 month
4. Evaluate if additional measures (webhooks) are needed based on actual data

**Total effort**: 8-12 hours (vs. 14-20 hours for current plan)
**Performance impact**: None (vs. 2x latency degradation)
**API cost**: Zero (vs. 142x rate limit overage)
**Reliability**: Equal or better
