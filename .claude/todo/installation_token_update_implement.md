# Installation Token Error-Driven Cache Invalidation - Implementation Plan

**Date**: November 2025
**Approach**: Reactive error-driven cache invalidation with singleflight protection
**Status**: Not Started

---

## 📋 Plan Status Checklist

### Phase 1: Error Detection & Classification ⬜ Not Started
- [ ] 1.1: Add error classification helper (1-2 hours)
- [ ] 1.2: Implement error type detection (401/403/404/410/422) (1 hour)
- [ ] 1.3: Add unit tests for error detection (1 hour)
- [ ] **Phase 1 Total**: 3-4 hours

### Phase 2: Singleflight Client Creation ⬜ Not Started
- [ ] 2.1: Add singleflight wrapper to Base handler (2 hours)
- [ ] 2.2: Integrate singleflight into retrieveClientAndInstallationId (1 hour)
- [ ] 2.3: Add concurrency tests (2 hours)
- [ ] **Phase 2 Total**: 5 hours

### Phase 3: Error-Driven Cache Invalidation ⬜ Not Started
- [ ] 3.1: Implement cache invalidation on auth errors (2 hours)
- [ ] 3.2: Add retry logic with error recovery (2-3 hours)
- [ ] 3.3: Handle negative caching for 404/410 (1 hour)
- [ ] 3.4: Integration tests for error recovery (2 hours)
- [ ] **Phase 3 Total**: 7-8 hours

### Phase 4: Optimize Cache TTL ⬜ Not Started
- [ ] 4.1: Increase positive cache TTL to 45 minutes (30 min)
- [ ] 4.2: Add TTL configuration options (30 min)
- [ ] 4.3: Update cache metrics and tests (1 hour)
- [ ] **Phase 4 Total**: 2 hours

### Phase 5: Metrics & Observability ⬜ Not Started
- [ ] 5.1: Add error-driven metrics (1 hour)
- [ ] 5.2: Update dashboards (1 hour)
- [ ] 5.3: Add alerting rules (30 min)
- [ ] **Phase 5 Total**: 2-3 hours

### Phase 6: (Optional) Installation Webhook Handlers ⬜ Not Started
- [ ] 6.1: Add webhook handlers for installation events (2 hours)
- [ ] 6.2: Implement cache invalidation by installation ID (1 hour)
- [ ] 6.3: Test webhook integration (1 hour)
- [ ] **Phase 6 Total**: 4 hours

---

**Total Estimated Effort**: 19-25 hours (core phases: 19-21 hours, optional: +4 hours)

---

## 📚 Context

### Problem Statement
- **Primary Issue**: When GitHub App installations are deleted/reinstalled, cached clients use stale installation IDs
- **Frequency**: Rare (~1-2 times/month per org)
- **Current Behavior**: Cached clients fail silently until cache expires (10 min)
- **Desired Behavior**: Detect installation errors and automatically recover

### Why NOT Proactive Validation
1. **ghinstallation already handles token refresh** automatically (1-min before 1-hour expiry)
2. **Proactive validation creates NEW tokens** - would cause 142x rate limit overage
3. **Performance penalty**: +50ms on every cache hit (99% of requests)
4. **Violates GitHub best practices**: "Cache and reuse tokens"

### Why Error-Driven Approach
1. **Zero API cost**: No additional token creation
2. **Zero latency impact**: Only activates on actual errors (rare)
3. **Follows GitHub best practices**: Reactive error handling
4. **Simpler**: 19-21 hours vs 14-20 hours for proactive approach
5. **Self-healing**: Automatically recovers from installation changes

---

## 🔒 Constraints

1. **Must not increase API call volume** (currently 99% cache hit rate reduces API calls to 1%)
2. **Must not add latency to successful requests** (P95: 50ms)
3. **Must handle GHEC and GHES differently** (GHEC: per-org, GHES: per-installation)
4. **Must prevent cache stampedes** (multiple goroutines recreating same client)
5. **Must maintain backward compatibility** (existing callers should work unchanged)
6. **Must respect GitHub rate limits** (5,000 requests/hour per installation)
7. **Must handle concurrent client creation** safely

---

## 📖 References

### Key Files
- **`server/handler/base.go`**: Core client creation logic (lines 518-755)
- **`server/handler/client_cache.go`**: ClientCache implementation
- **`server/handler/rate_limiter.go`**: Rate limiting logic
- **`vendor/github.com/bradleyfalzon/ghinstallation/v2/transport.go`**: Token auto-refresh (lines 136-149)
- **`vendor/github.com/palantir/go-githubapp/githubapp/client_creator.go`**: ClientCreator interface

### Key Concepts
- **ghinstallation Transport**: Automatically refreshes tokens 1 minute before expiry
- **Token Lifespan**: 1 hour (ghinstallation caches and auto-refreshes)
- **ClientCache TTL**: Currently 10 minutes (will increase to 45 minutes)
- **Installation ID Changes**: Happens when app is uninstalled/reinstalled
- **Singleflight**: Prevents multiple goroutines from making duplicate requests

### GitHub Error Codes
- **401 Unauthorized**: Token invalid/expired or installation suspended
- **403 Forbidden**: Permissions issue OR rate limit (need to distinguish)
- **404 Not Found**: Installation deleted or resource doesn't exist
- **410 Gone**: Installation permanently deleted
- **422 Unprocessable**: Installation suspended or permissions revoked

### Documentation References
- GitHub Apps Authentication: https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app
- Installation Token Best Practices: https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app
- Rate Limiting: https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api

---

## ⚠️ Things to Keep in Mind

1. **Don't fight ghinstallation**: Trust its token caching and refresh logic
2. **Error classification matters**: 403 rate limit ≠ 403 suspended installation
3. **Singleflight is critical**: Prevents token creation stampedes under load
4. **Negative caching**: 404/410 should be cached to avoid repeated lookups
5. **V3 and V4 clients share tokens**: ghinstallation handles both automatically
6. **Thread safety**: All cache operations must be thread-safe
7. **Metrics are essential**: Track error rates to validate approach
8. **Testing with mocks**: Can't easily test actual GitHub installation changes

---

## 🔧 Implementation Phases

---

## Phase 1: Error Detection & Classification

### Overview
Add helpers to detect and classify GitHub API errors that indicate installation issues. This is the foundation for error-driven cache invalidation.

---

### Step 1.1: Add Error Classification Helper

**Information & References**:
- Location: `server/handler/base.go`
- Reference: GitHub error response structure in `vendor/github.com/google/go-github/v47/github/github.go`
- Error codes reference: https://docs.github.com/en/rest/using-the-rest-api/troubleshooting-the-rest-api

**Implementation**:

```go
// File: server/handler/base.go

// installationErrorType represents different types of installation errors
type installationErrorType int

const (
    errorTypeNone installationErrorType = iota
    errorTypeUnauthorized     // 401 - Token invalid or installation suspended
    errorTypeNotFound         // 404 - Installation deleted
    errorTypeGone             // 410 - Installation permanently deleted
    errorTypeUnprocessable    // 422 - Installation suspended or permissions revoked
    errorTypeForbidden        // 403 - Permissions issue (not rate limit)
    errorTypeRateLimit        // 403 - Rate limit (should not invalidate cache)
)

// classifyInstallationError examines an error and determines if it indicates
// an installation-level problem that requires cache invalidation.
//
// Returns:
//   - errorType: The type of error detected
//   - shouldInvalidate: Whether cache should be invalidated
//   - shouldNegativeCache: Whether a negative cache entry should be created
func (b *Base) classifyInstallationError(err error) (errorType installationErrorType, shouldInvalidate bool, shouldNegativeCache bool) {
    if err == nil {
        return errorTypeNone, false, false
    }

    // Check for GitHub API error response
    if ghErr, ok := err.(*github.ErrorResponse); ok {
        statusCode := ghErr.Response.StatusCode

        switch statusCode {
        case 401:
            // Unauthorized - token invalid or installation suspended
            // Check error message for suspension vs token issue
            errMsg := strings.ToLower(ghErr.Message)
            if strings.Contains(errMsg, "suspend") {
                return errorTypeUnauthorized, true, false
            }
            // Token issue - let ghinstallation handle it, but invalidate cache
            return errorTypeUnauthorized, true, false

        case 403:
            // Forbidden - could be rate limit OR permissions issue
            // Check if this is a rate limit error (should NOT invalidate cache)
            if ghErr.Rate.Remaining == 0 || strings.Contains(strings.ToLower(ghErr.Message), "rate limit") {
                return errorTypeRateLimit, false, false
            }
            // Not rate limit - permissions issue, invalidate cache
            return errorTypeForbidden, true, false

        case 404:
            // Not Found - installation or resource deleted
            // Check if error message indicates installation issue
            errMsg := strings.ToLower(ghErr.Message)
            if strings.Contains(errMsg, "installation") {
                return errorTypeNotFound, true, true // Negative cache
            }
            // Resource not found, not installation issue
            return errorTypeNone, false, false

        case 410:
            // Gone - installation permanently deleted
            return errorTypeGone, true, true // Negative cache

        case 422:
            // Unprocessable - installation suspended or permissions revoked
            errMsg := strings.ToLower(ghErr.Message)
            if strings.Contains(errMsg, "suspend") || strings.Contains(errMsg, "installation") {
                return errorTypeUnprocessable, true, false
            }
            return errorTypeNone, false, false
        }
    }

    // Check for ghinstallation-specific errors
    errMsg := strings.ToLower(err.Error())
    if strings.Contains(errMsg, "could not refresh installation") ||
       strings.Contains(errMsg, "installation not found") {
        return errorTypeUnauthorized, true, true
    }

    return errorTypeNone, false, false
}

// isInstallationError returns true if the error should trigger cache invalidation
func (b *Base) isInstallationError(err error) bool {
    _, shouldInvalidate, _ := b.classifyInstallationError(err)
    return shouldInvalidate
}
```

**Testing Plan**:

```go
// File: server/handler/base_error_classification_test.go

func TestClassifyInstallationError(t *testing.T) {
    tests := []struct {
        name                 string
        err                  error
        expectedType         installationErrorType
        expectedInvalidate   bool
        expectedNegativeCache bool
    }{
        {
            name:                 "nil error",
            err:                  nil,
            expectedType:         errorTypeNone,
            expectedInvalidate:   false,
            expectedNegativeCache: false,
        },
        {
            name: "401 unauthorized - token invalid",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 401},
                Message:  "Bad credentials",
            },
            expectedType:         errorTypeUnauthorized,
            expectedInvalidate:   true,
            expectedNegativeCache: false,
        },
        {
            name: "401 unauthorized - installation suspended",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 401},
                Message:  "Installation is suspended",
            },
            expectedType:         errorTypeUnauthorized,
            expectedInvalidate:   true,
            expectedNegativeCache: false,
        },
        {
            name: "403 forbidden - rate limit",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 403},
                Message:  "API rate limit exceeded",
                Rate:     github.Rate{Remaining: 0},
            },
            expectedType:         errorTypeRateLimit,
            expectedInvalidate:   false,
            expectedNegativeCache: false,
        },
        {
            name: "403 forbidden - permissions",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 403},
                Message:  "Resource not accessible by integration",
            },
            expectedType:         errorTypeForbidden,
            expectedInvalidate:   true,
            expectedNegativeCache: false,
        },
        {
            name: "404 not found - installation deleted",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 404},
                Message:  "Installation not found",
            },
            expectedType:         errorTypeNotFound,
            expectedInvalidate:   true,
            expectedNegativeCache: true,
        },
        {
            name: "404 not found - resource not found",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 404},
                Message:  "Repository not found",
            },
            expectedType:         errorTypeNone,
            expectedInvalidate:   false,
            expectedNegativeCache: false,
        },
        {
            name: "410 gone - installation deleted",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 410},
                Message:  "Installation has been deleted",
            },
            expectedType:         errorTypeGone,
            expectedInvalidate:   true,
            expectedNegativeCache: true,
        },
        {
            name: "422 unprocessable - installation suspended",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 422},
                Message:  "Installation suspended",
            },
            expectedType:         errorTypeUnprocessable,
            expectedInvalidate:   true,
            expectedNegativeCache: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            base := &Base{}
            errType, shouldInvalidate, shouldNegativeCache := base.classifyInstallationError(tt.err)

            if errType != tt.expectedType {
                t.Errorf("expected error type %v, got %v", tt.expectedType, errType)
            }
            if shouldInvalidate != tt.expectedInvalidate {
                t.Errorf("expected shouldInvalidate %v, got %v", tt.expectedInvalidate, shouldInvalidate)
            }
            if shouldNegativeCache != tt.expectedNegativeCache {
                t.Errorf("expected shouldNegativeCache %v, got %v", tt.expectedNegativeCache, shouldNegativeCache)
            }
        })
    }
}
```

**Acceptance Criteria**:
- ✅ Function correctly classifies all GitHub error types (401/403/404/410/422)
- ✅ Distinguishes between 403 rate limit (don't invalidate) and 403 permissions (invalidate)
- ✅ Identifies installation-specific 404 errors vs resource 404 errors
- ✅ Returns correct invalidation and negative cache flags
- ✅ All tests pass with 100% coverage
- ✅ No false positives (doesn't invalidate on rate limits or resource 404s)
- ✅ No false negatives (catches all installation-related errors)

---

### Step 1.2: Implement Error Type Detection

**Information & References**:
- Location: `server/handler/base.go`
- Reference: Existing `recordInstallationClientMetric` function for metrics integration

**Implementation**:

```go
// File: server/handler/base.go

// Add new metric keys
const (
    // Existing metrics...
    MetricsKeyInstallationClientSuccess          = "installation.client.success"
    MetricsKeyInstallationClientFailure          = "installation.client.failure"

    // New metrics for error detection
    MetricsKeyInstallationErrorDetected          = "installation.error.detected"
    MetricsKeyInstallationErrorUnauthorized      = "installation.error.unauthorized"
    MetricsKeyInstallationErrorNotFound          = "installation.error.not_found"
    MetricsKeyInstallationErrorGone              = "installation.error.gone"
    MetricsKeyInstallationErrorUnprocessable     = "installation.error.unprocessable"
    MetricsKeyInstallationErrorForbidden         = "installation.error.forbidden"
    MetricsKeyInstallationErrorRateLimit         = "installation.error.rate_limit"
)

// detectAndRecordInstallationError classifies an error and records metrics
func (b *Base) detectAndRecordInstallationError(ctx context.Context, err error, owner string, installationID int64) (shouldInvalidate bool, shouldNegativeCache bool) {
    if err == nil {
        return false, false
    }

    logger := zerolog.Ctx(ctx)
    errType, shouldInvalidate, shouldNegativeCache := b.classifyInstallationError(err)

    if errType == errorTypeNone {
        return false, false
    }

    // Record metrics based on error type
    b.recordInstallationClientMetric(MetricsKeyInstallationErrorDetected)

    switch errType {
    case errorTypeUnauthorized:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorUnauthorized)
        logger.Warn().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Unauthorized error detected - token invalid or installation suspended")

    case errorTypeNotFound:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorNotFound)
        logger.Warn().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Not found error detected - installation may be deleted")

    case errorTypeGone:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorGone)
        logger.Warn().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Gone error detected - installation permanently deleted")

    case errorTypeUnprocessable:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorUnprocessable)
        logger.Warn().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Unprocessable error detected - installation suspended or permissions revoked")

    case errorTypeForbidden:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorForbidden)
        logger.Warn().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Forbidden error detected - permissions issue")

    case errorTypeRateLimit:
        b.recordInstallationClientMetric(MetricsKeyInstallationErrorRateLimit)
        logger.Debug().
            Err(err).
            Str("owner", owner).
            Int64("installation_id", installationID).
            Msg("Rate limit error detected - not invalidating cache")
    }

    return shouldInvalidate, shouldNegativeCache
}
```

**Testing Plan**:

```go
// File: server/handler/base_error_detection_test.go

func TestDetectAndRecordInstallationError(t *testing.T) {
    // Use mock metrics registry
    registry := metrics.NewRegistry()

    tests := []struct {
        name                  string
        err                   error
        expectedInvalidate    bool
        expectedNegativeCache bool
        expectedMetric        string
    }{
        {
            name:                  "401 error",
            err:                   &github.ErrorResponse{Response: &http.Response{StatusCode: 401}},
            expectedInvalidate:    true,
            expectedNegativeCache: false,
            expectedMetric:        MetricsKeyInstallationErrorUnauthorized,
        },
        {
            name:                  "403 rate limit - should not invalidate",
            err:                   &github.ErrorResponse{Response: &http.Response{StatusCode: 403}, Message: "rate limit", Rate: github.Rate{Remaining: 0}},
            expectedInvalidate:    false,
            expectedNegativeCache: false,
            expectedMetric:        MetricsKeyInstallationErrorRateLimit,
        },
        {
            name:                  "404 installation not found",
            err:                   &github.ErrorResponse{Response: &http.Response{StatusCode: 404}, Message: "installation not found"},
            expectedInvalidate:    true,
            expectedNegativeCache: true,
            expectedMetric:        MetricsKeyInstallationErrorNotFound,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            base := &Base{
                metricsRegistry: registry,
            }

            ctx := context.Background()
            shouldInvalidate, shouldNegativeCache := base.detectAndRecordInstallationError(ctx, tt.err, "test-org", 12345)

            if shouldInvalidate != tt.expectedInvalidate {
                t.Errorf("expected shouldInvalidate %v, got %v", tt.expectedInvalidate, shouldInvalidate)
            }
            if shouldNegativeCache != tt.expectedNegativeCache {
                t.Errorf("expected shouldNegativeCache %v, got %v", tt.expectedNegativeCache, shouldNegativeCache)
            }

            // Verify metric was recorded
            counter := registry.Get(tt.expectedMetric)
            if counter == nil {
                t.Errorf("expected metric %s to be recorded", tt.expectedMetric)
            }
        })
    }
}
```

**Acceptance Criteria**:
- ✅ Correctly detects all error types and returns appropriate flags
- ✅ Records metrics for each error type
- ✅ Logs appropriate warning/debug messages
- ✅ Rate limit errors don't trigger cache invalidation
- ✅ All tests pass with 100% coverage
- ✅ Metrics can be queried in New Relic dashboard

---

### Step 1.3: Add Unit Tests for Error Detection

**Information & References**:
- Location: `server/handler/base_error_classification_test.go` (new file)
- Reference: Existing test patterns in `server/handler/base_test.go`

**Implementation**:

See testing plans in Steps 1.1 and 1.2 above.

**Additional edge case tests**:

```go
// File: server/handler/base_error_classification_test.go

func TestErrorClassificationEdgeCases(t *testing.T) {
    tests := []struct {
        name         string
        err          error
        expectPanic  bool
    }{
        {
            name: "non-github error",
            err:  errors.New("generic error"),
        },
        {
            name: "wrapped github error",
            err:  fmt.Errorf("wrapped: %w", &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}),
        },
        {
            name: "nil response in error",
            err:  &github.ErrorResponse{Response: nil},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            base := &Base{}

            // Should not panic
            errType, shouldInvalidate, shouldNegativeCache := base.classifyInstallationError(tt.err)

            // Verify sensible defaults
            t.Logf("errType=%v, invalidate=%v, negativeCache=%v", errType, shouldInvalidate, shouldNegativeCache)
        })
    }
}
```

**Acceptance Criteria**:
- ✅ All unit tests pass
- ✅ Edge cases handled gracefully (no panics)
- ✅ Code coverage > 95% for error classification functions
- ✅ Tests run in < 1 second
- ✅ Can run tests in parallel (`go test -race`)

---

## Phase 2: Singleflight Client Creation

### Overview
Implement singleflight pattern to prevent multiple goroutines from creating duplicate clients/tokens simultaneously. This is critical for preventing cache stampedes under load.

---

### Step 2.1: Add Singleflight Wrapper to Base Handler

**Information & References**:
- Location: `server/handler/base.go`
- Reference: `golang.org/x/sync/singleflight` or `vendor/github.com/aws/smithy-go/internal/sync/singleflight`
- Pattern: Group.Do(key, fn) ensures fn runs once per key

**Implementation**:

```go
// File: server/handler/base.go

import (
    "golang.org/x/sync/singleflight"
    // ... existing imports
)

// Base is the base handler with client creation and caching
type Base struct {
    // ... existing fields ...

    // clientCreationGroup ensures only one goroutine creates a client for a given owner
    // Key format: "owner:{ownerID}" or "installation:{installationID}"
    clientCreationGroup singleflight.Group
}

// singleflightClientCreation wraps client creation with singleflight to prevent stampedes
func (b *Base) singleflightClientCreation(ctx context.Context, key string, fn func() (*InstallationClients, int64, error)) (*InstallationClients, int64, error) {
    logger := zerolog.Ctx(ctx)

    logger.Debug().
        Str("key", key).
        Msg("Entering singleflight client creation")

    // Use singleflight to ensure only one goroutine creates the client
    result, err, shared := b.clientCreationGroup.Do(key, func() (interface{}, error) {
        clients, installationID, err := fn()
        if err != nil {
            return nil, err
        }
        return &clientCreationResult{
            Clients:        clients,
            InstallationID: installationID,
        }, nil
    })

    if err != nil {
        logger.Error().
            Err(err).
            Str("key", key).
            Bool("shared", shared).
            Msg("Singleflight client creation failed")
        return nil, 0, err
    }

    res := result.(*clientCreationResult)

    logger.Debug().
        Str("key", key).
        Bool("shared", shared).
        Int64("installation_id", res.InstallationID).
        Msg("Singleflight client creation completed")

    return res.Clients, res.InstallationID, nil
}

// clientCreationResult holds the result of client creation for singleflight
type clientCreationResult struct {
    Clients        *InstallationClients
    InstallationID int64
}
```

**Testing Plan**:

```go
// File: server/handler/base_singleflight_test.go

func TestSingleflightClientCreation(t *testing.T) {
    t.Run("single caller", func(t *testing.T) {
        base := &Base{}
        ctx := context.Background()

        callCount := atomic.Int32{}
        fn := func() (*InstallationClients, int64, error) {
            callCount.Add(1)
            time.Sleep(10 * time.Millisecond) // Simulate work
            return &InstallationClients{}, int64(123), nil
        }

        clients, installID, err := base.singleflightClientCreation(ctx, "test-key", fn)

        require.NoError(t, err)
        require.NotNil(t, clients)
        require.Equal(t, int64(123), installID)
        require.Equal(t, int32(1), callCount.Load())
    })

    t.Run("concurrent callers - should only call once", func(t *testing.T) {
        base := &Base{}
        ctx := context.Background()

        callCount := atomic.Int32{}
        fn := func() (*InstallationClients, int64, error) {
            callCount.Add(1)
            time.Sleep(50 * time.Millisecond) // Simulate slower work
            return &InstallationClients{}, int64(123), nil
        }

        // Launch 10 concurrent requests for same key
        var wg sync.WaitGroup
        errors := make([]error, 10)
        clients := make([]*InstallationClients, 10)

        for i := 0; i < 10; i++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                c, _, err := base.singleflightClientCreation(ctx, "test-key", fn)
                clients[idx] = c
                errors[idx] = err
            }(i)
        }

        wg.Wait()

        // Verify only called once
        require.Equal(t, int32(1), callCount.Load(), "function should only be called once")

        // Verify all callers got the same result
        for i := 0; i < 10; i++ {
            require.NoError(t, errors[i])
            require.NotNil(t, clients[i])
            require.Same(t, clients[0], clients[i], "all callers should get same client instance")
        }
    })

    t.Run("different keys - should call separately", func(t *testing.T) {
        base := &Base{}
        ctx := context.Background()

        callCount := atomic.Int32{}
        fn := func() (*InstallationClients, int64, error) {
            callCount.Add(1)
            return &InstallationClients{}, int64(123), nil
        }

        // Call with different keys
        _, _, err1 := base.singleflightClientCreation(ctx, "key1", fn)
        _, _, err2 := base.singleflightClientCreation(ctx, "key2", fn)

        require.NoError(t, err1)
        require.NoError(t, err2)
        require.Equal(t, int32(2), callCount.Load(), "should call twice for different keys")
    })

    t.Run("error propagation", func(t *testing.T) {
        base := &Base{}
        ctx := context.Background()

        expectedErr := errors.New("creation failed")
        fn := func() (*InstallationClients, int64, error) {
            return nil, 0, expectedErr
        }

        _, _, err := base.singleflightClientCreation(ctx, "test-key", fn)

        require.Error(t, err)
        require.Equal(t, expectedErr, err)
    })
}
```

**Acceptance Criteria**:
- ✅ Singleflight correctly deduplicates concurrent requests for same key
- ✅ Different keys result in separate executions
- ✅ All callers receive the same result for same key
- ✅ Errors propagate correctly to all callers
- ✅ No deadlocks under concurrent load
- ✅ Tests pass with race detector (`go test -race`)
- ✅ Metrics track singleflight hits/misses

---

### Step 2.2: Integrate Singleflight into retrieveClientAndInstallationId

**Information & References**:
- Location: `server/handler/base.go` (lines 531-669)
- Reference: Existing `retrieveClientAndInstallationId` function

**Implementation**:

```go
// File: server/handler/base.go

// retrieveClientAndInstallationId retrieves both the installation client and installation ID
// using a cache-first approach with singleflight protection against cache stampedes.
//
// Enhanced with singleflight (November 2025):
//   - Prevents multiple goroutines from creating duplicate clients
//   - Reduces token creation load during cache misses
//
// Flow:
// 1. Check cache by owner ID (fast path)
// 2. If cache miss, use singleflight to ensure only one goroutine creates client
// 3. Lookup installation ID and create clients
// 4. Cache and return
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
            return clients, cachedInstallationID, nil
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
        Msg("Cache miss - using singleflight for client creation")

    // Step 2: Use singleflight to prevent stampede on cache miss
    // Key format: "owner:{ownerID}" for GHEC, "installation:{installationID}" for GHES
    singleflightKey := fmt.Sprintf("owner:%d", ownerID)
    if !b.GithubCloud && installationID > 0 {
        singleflightKey = fmt.Sprintf("installation:%d", installationID)
    }

    return b.singleflightClientCreation(ctx, singleflightKey, func() (*InstallationClients, int64, error) {
        // Double-check cache inside singleflight (another goroutine may have populated it)
        if b.ClientCache != nil && ownerID > 0 {
            if clients, cachedInstallationID := b.ClientCache.GetWithInstallationID(ownerID); clients != nil {
                logger.Debug().
                    Str("owner", ownerName).
                    Int64("owner_id", ownerID).
                    Msg("Cache hit on second check (populated by concurrent request)")
                return clients, cachedInstallationID, nil
            }
        }

        // Perform actual client creation (existing logic)
        return b.createClientWithLookup(ctx, installationID, ownerID, ownerName, repo)
    })
}

// createClientWithLookup handles the actual client creation logic
// (extracted from retrieveClientAndInstallationId for singleflight wrapping)
func (b *Base) createClientWithLookup(ctx context.Context, installationID int64, ownerID int64, ownerName string, repo string) (*InstallationClients, int64, error) {
    logger := zerolog.Ctx(ctx)

    // Get installation ID from API if not provided
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

    // Fallback logic
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

    // Create installation clients
    clients, err := b.createClientsForOwner(ctx, ownerName, finalInstallationID)
    if err != nil {
        return nil, 0, errors.Wrapf(err, "failed to create clients for owner %s", ownerName)
    }

    // Cache clients with installation ID
    if b.ClientCache != nil && ownerID > 0 {
        b.ClientCache.PutWithInstallationID(ownerID, clients, finalInstallationID)
        logger.Debug().
            Str("owner", ownerName).
            Int64("owner_id", ownerID).
            Int64("installation_id", finalInstallationID).
            Msg("Cached clients and installation ID")
    }

    return clients, finalInstallationID, nil
}
```

**Testing Plan**:

```go
// File: server/handler/base_singleflight_integration_test.go

func TestRetrieveClientAndInstallationId_Singleflight(t *testing.T) {
    t.Run("concurrent cache misses should only create client once", func(t *testing.T) {
        // Setup mock installations
        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                time.Sleep(50 * time.Millisecond) // Simulate slow API call
                return &github.Installation{ID: github.Int64(12345)}, nil
            },
        }

        clientCreationCount := atomic.Int32{}
        mockClientCreator := &mockClientCreator{
            newOrgClientFunc: func(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
                clientCreationCount.Add(1)
                return github.NewClient(nil), nil
            },
        }

        base := &Base{
            Installations:   mockInstallations,
            ClientCreator:   mockClientCreator,
            GithubCloud:     true,
            ClientCache:     NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil),
        }

        ctx := context.Background()
        ownerID := int64(999)
        ownerName := "test-org"

        // Launch 10 concurrent requests
        var wg sync.WaitGroup
        errors := make([]error, 10)
        clients := make([]*InstallationClients, 10)

        for i := 0; i < 10; i++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                c, _, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
                clients[idx] = c
                errors[idx] = err
            }(i)
        }

        wg.Wait()

        // Verify only created client once
        require.Equal(t, int32(1), clientCreationCount.Load(), "should only create client once")

        // Verify all requests succeeded
        for i := 0; i < 10; i++ {
            require.NoError(t, errors[i])
            require.NotNil(t, clients[i])
        }

        // Verify result was cached
        cachedClients, cachedID := base.ClientCache.GetWithInstallationID(ownerID)
        require.NotNil(t, cachedClients)
        require.Equal(t, int64(12345), cachedID)
    })
}
```

**Acceptance Criteria**:
- ✅ Singleflight prevents duplicate client creation on cache misses
- ✅ Cache double-check inside singleflight avoids redundant work
- ✅ Concurrent requests for same owner only create one client
- ✅ Different owners create separate clients
- ✅ Cache hit path bypasses singleflight (performance)
- ✅ All existing tests continue to pass
- ✅ No performance regression on cache hits
- ✅ Tests pass with race detector

---

### Step 2.3: Add Concurrency Tests

**Information & References**:
- Location: `server/handler/base_concurrency_test.go` (new file)
- Reference: Load test patterns in `test/load/rate_limiting_load_test.go`

**Implementation**:

```go
// File: server/handler/base_concurrency_test.go

func TestConcurrentClientCreation(t *testing.T) {
    t.Run("high concurrency - 100 goroutines, 10 unique owners", func(t *testing.T) {
        // Track API calls per owner
        apiCallCount := sync.Map{}

        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                // Track calls per owner
                val, _ := apiCallCount.LoadOrStore(owner, &atomic.Int32{})
                counter := val.(*atomic.Int32)
                counter.Add(1)

                time.Sleep(10 * time.Millisecond) // Simulate API latency
                return &github.Installation{ID: github.Int64(hash(owner))}, nil
            },
        }

        base := &Base{
            Installations: mockInstallations,
            ClientCreator: &mockClientCreator{},
            GithubCloud:   true,
            ClientCache:   NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil),
        }

        ctx := context.Background()
        ownerNames := []string{"org1", "org2", "org3", "org4", "org5", "org6", "org7", "org8", "org9", "org10"}

        // Launch 100 goroutines (10 per owner)
        var wg sync.WaitGroup
        errCount := atomic.Int32{}

        for i := 0; i < 100; i++ {
            wg.Add(1)
            ownerName := ownerNames[i%len(ownerNames)]
            ownerID := int64(i % len(ownerNames))

            go func(owner string, id int64) {
                defer wg.Done()
                _, _, err := base.retrieveClientAndInstallationId(ctx, 0, id, owner, "")
                if err != nil {
                    errCount.Add(1)
                }
            }(ownerName, ownerID)
        }

        wg.Wait()

        // Verify no errors
        require.Equal(t, int32(0), errCount.Load(), "should have no errors")

        // Verify each owner only had API called once (singleflight working)
        for _, ownerName := range ownerNames {
            val, ok := apiCallCount.Load(ownerName)
            require.True(t, ok, "owner %s should have API call", ownerName)
            counter := val.(*atomic.Int32)
            require.Equal(t, int32(1), counter.Load(), "owner %s should only have 1 API call", ownerName)
        }
    })

    t.Run("stress test - 1000 goroutines", func(t *testing.T) {
        base := setupTestBase(t)
        ctx := context.Background()

        start := time.Now()
        var wg sync.WaitGroup
        successCount := atomic.Int32{}

        for i := 0; i < 1000; i++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                ownerID := int64(idx % 50) // 50 unique owners
                ownerName := fmt.Sprintf("org-%d", ownerID)
                _, _, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
                if err == nil {
                    successCount.Add(1)
                }
            }(i)
        }

        wg.Wait()
        duration := time.Since(start)

        t.Logf("Completed 1000 requests in %v", duration)
        t.Logf("Success rate: %d/1000", successCount.Load())

        require.Greater(t, successCount.Load(), int32(900), "success rate should be > 90%")
        require.Less(t, duration, 5*time.Second, "should complete in < 5 seconds")
    })
}

func hash(s string) int64 {
    h := fnv.New64a()
    h.Write([]byte(s))
    return int64(h.Sum64())
}
```

**Acceptance Criteria**:
- ✅ 100 concurrent requests complete successfully
- ✅ Singleflight reduces API calls to 1 per unique owner
- ✅ No deadlocks under high concurrency
- ✅ No race conditions (verified with `-race`)
- ✅ Performance scales linearly with number of unique owners
- ✅ 1000 goroutines complete in < 5 seconds
- ✅ Success rate > 90%

---

## Phase 3: Error-Driven Cache Invalidation

### Overview
Implement reactive cache invalidation when GitHub API returns installation-related errors. This is the core of the error-driven approach.

---

### Step 3.1: Implement Cache Invalidation on Auth Errors

**Information & References**:
- Location: `server/handler/base.go`
- Reference: Error classification from Phase 1
- Pattern: Detect error → Invalidate cache → Retry

**Implementation**:

```go
// File: server/handler/base.go

// handleInstallationError handles installation-related errors by invalidating cache
// and attempting to recover with fresh installation lookup and client creation.
//
// This implements error-driven cache invalidation:
//   - Reactive, not proactive (only runs when errors occur)
//   - Zero API cost for successful requests
//   - Automatically recovers from installation changes
//
// Flow:
// 1. Detect installation error (Phase 1 classification)
// 2. Invalidate cache entry (or negative cache)
// 3. Re-lookup installation ID
// 4. Create new clients
// 5. Cache and return
//
// Returns: new InstallationClients, error
func (b *Base) handleInstallationError(ctx context.Context, originalErr error, ownerID int64, ownerName string, repo string, currentInstallationID int64) (*InstallationClients, error) {
    logger := zerolog.Ctx(ctx)

    // Classify the error
    shouldInvalidate, shouldNegativeCache := b.detectAndRecordInstallationError(ctx, originalErr, ownerName, currentInstallationID)

    if !shouldInvalidate {
        // Not an installation error, return original error
        return nil, originalErr
    }

    logger.Warn().
        Err(originalErr).
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Int64("installation_id", currentInstallationID).
        Bool("negative_cache", shouldNegativeCache).
        Msg("Installation error detected - invalidating cache and attempting recovery")

    // Record metric
    b.recordInstallationClientMetric(MetricsKeyInstallationCacheInvalidation)

    // Step 1: Invalidate cache entry
    if b.ClientCache != nil && ownerID > 0 {
        if shouldNegativeCache {
            // Mark as not found (404/410)
            b.ClientCache.PutNegative(ownerID)
            logger.Info().
                Str("owner", ownerName).
                Int64("owner_id", ownerID).
                Msg("Cached negative entry - installation not found")
            return nil, fmt.Errorf("installation not found for owner %s: %w", ownerName, originalErr)
        }

        // Remove cache entry
        b.ClientCache.Delete(ownerID)
        logger.Debug().
            Str("owner", ownerName).
            Int64("owner_id", ownerID).
            Msg("Invalidated cache entry for owner")
    }

    // Step 2: Attempt recovery by re-creating clients
    // Use singleflight to prevent stampede if multiple requests hit same error
    singleflightKey := fmt.Sprintf("recovery:owner:%d", ownerID)

    clients, _, err := b.singleflightClientCreation(ctx, singleflightKey, func() (*InstallationClients, int64, error) {
        logger.Info().
            Str("owner", ownerName).
            Int64("owner_id", ownerID).
            Msg("Attempting to recover from installation error")

        // Re-fetch installation and create new clients
        return b.createClientWithLookup(ctx, 0, ownerID, ownerName, repo)
    })

    if err != nil {
        b.recordInstallationClientMetric(MetricsKeyInstallationRecoveryFailure)
        logger.Error().
            Err(err).
            Str("owner", ownerName).
            Int64("owner_id", ownerID).
            Msg("Failed to recover from installation error")
        return nil, errors.Wrapf(err, "failed to recover from installation error for owner %s: original error: %v", ownerName, originalErr)
    }

    b.recordInstallationClientMetric(MetricsKeyInstallationRecoverySuccess)
    logger.Info().
        Str("owner", ownerName).
        Int64("owner_id", ownerID).
        Msg("Successfully recovered from installation error")

    return clients, nil
}

// Add new metric keys
const (
    MetricsKeyInstallationCacheInvalidation  = "installation.cache.invalidation"
    MetricsKeyInstallationRecoverySuccess    = "installation.recovery.success"
    MetricsKeyInstallationRecoveryFailure    = "installation.recovery.failure"
    MetricsKeyInstallationRecoveryLatencyMs  = "installation.recovery.latency_ms"
)
```

**Testing Plan**:

```go
// File: server/handler/base_error_recovery_test.go

func TestHandleInstallationError(t *testing.T) {
    t.Run("401 error - should invalidate and recover", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)

        // Pre-populate cache with old client
        oldClient := &InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, oldClient, 999)

        // Setup mock that returns new client
        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                return &github.Installation{ID: github.Int64(1000)}, nil // New installation ID
            },
        }

        base := &Base{
            Installations: mockInstallations,
            ClientCreator: &mockClientCreator{},
            ClientCache:   cache,
            GithubCloud:   true,
        }

        ctx := context.Background()
        originalErr := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 401},
            Message:  "Bad credentials",
        }

        // Attempt recovery
        newClients, err := base.handleInstallationError(ctx, originalErr, 123, "test-org", "", 999)

        // Should succeed
        require.NoError(t, err)
        require.NotNil(t, newClients)
        require.NotEqual(t, oldClient, newClients, "should return new client")

        // Verify cache was updated
        cachedClients, cachedID := cache.GetWithInstallationID(123)
        require.NotNil(t, cachedClients)
        require.Equal(t, int64(1000), cachedID)
    })

    t.Run("404 error - should negative cache", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)
        base := &Base{
            ClientCache: cache,
        }

        ctx := context.Background()
        originalErr := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 404},
            Message:  "Installation not found",
        }

        // Attempt recovery (should fail)
        clients, err := base.handleInstallationError(ctx, originalErr, 123, "test-org", "", 999)

        require.Error(t, err)
        require.Nil(t, clients)

        // Verify negative cache
        require.True(t, cache.IsNegativelyCached(123))
    })

    t.Run("403 rate limit - should not invalidate", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)

        // Pre-populate cache
        oldClient := &InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, oldClient, 999)

        base := &Base{
            ClientCache: cache,
        }

        ctx := context.Background()
        originalErr := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 403},
            Message:  "API rate limit exceeded",
            Rate:     github.Rate{Remaining: 0},
        }

        // Attempt recovery (should not invalidate)
        clients, err := base.handleInstallationError(ctx, originalErr, 123, "test-org", "", 999)

        require.Error(t, err)
        require.Equal(t, originalErr, err, "should return original error")
        require.Nil(t, clients)

        // Verify cache NOT invalidated
        cachedClients, _ := cache.GetWithInstallationID(123)
        require.NotNil(t, cachedClients)
        require.Equal(t, oldClient, cachedClients, "cache should be unchanged")
    })
}
```

**Acceptance Criteria**:
- ✅ 401/403/422 errors invalidate cache and attempt recovery
- ✅ 404/410 errors create negative cache entries
- ✅ Rate limit errors (403) do NOT invalidate cache
- ✅ Successful recovery returns new clients and updates cache
- ✅ Failed recovery returns error without caching
- ✅ Singleflight prevents stampede during recovery
- ✅ Metrics track invalidation and recovery success/failure
- ✅ All tests pass

---

### Step 3.2: Add Retry Logic with Error Recovery

**Information & References**:
- Location: `server/handler/base.go`
- Pattern: Try → Detect error → Recover → Retry once
- Reference: Existing retry patterns in rate limiter

**Implementation**:

```go
// File: server/handler/base.go

// GetClientsByOwnerWithRetry is a wrapper around GetClientsByOwner that
// implements error-driven cache invalidation with automatic retry.
//
// This is the recommended method for handlers to use when obtaining clients.
//
// Flow:
// 1. Get clients from cache or create new
// 2. Return clients to caller
// 3. If caller's API call fails with installation error:
//    a. Call handleInstallationError to recover
//    b. Return new clients (or error)
//
// Note: This function doesn't perform the retry itself - it provides the
// recovery mechanism that callers can use after detecting errors.
func (b *Base) GetClientsByOwnerWithRetry(ctx context.Context, owner string, ownerID int64) (*InstallationClients, error) {
    // This is just GetClientsByOwner - the "retry" happens at the call site
    // when the caller detects an error and calls handleInstallationError
    return b.GetClientsByOwner(ctx, owner, ownerID)
}

// Example usage pattern for handlers:
/*
func (h *MyHandler) handlePullRequest(ctx context.Context, event *github.PullRequestEvent) error {
    owner := event.Repo.Owner.GetLogin()
    ownerID := event.Repo.Owner.GetID()

    clients, err := h.Base.GetClientsByOwner(ctx, owner, ownerID)
    if err != nil {
        return err
    }

    // Make API call
    pr, resp, err := clients.V3Client.PullRequests.Get(ctx, owner, repo, prNum)

    // Check for installation error
    if h.Base.isInstallationError(err) {
        // Attempt recovery
        newClients, recoveryErr := h.Base.handleInstallationError(ctx, err, ownerID, owner, repo, installationID)
        if recoveryErr != nil {
            return recoveryErr
        }

        // Retry with new client
        pr, resp, err = newClients.V3Client.PullRequests.Get(ctx, owner, repo, prNum)
    }

    if err != nil {
        return err
    }

    // Process pull request...
}
*/
```

**Alternative: Automatic Retry Wrapper** (more advanced):

```go
// File: server/handler/github_retry.go (NEW)

// RetryableGitHubClient wraps a GitHub client with automatic retry on installation errors
type RetryableGitHubClient struct {
    *github.Client
    base      *Base
    ownerID   int64
    ownerName string
    repo      string
    installationID int64
}

// NewRetryableGitHubClient creates a client that automatically retries on installation errors
func (b *Base) NewRetryableGitHubClient(clients *InstallationClients, ownerID int64, ownerName string, repo string, installationID int64) *RetryableGitHubClient {
    return &RetryableGitHubClient{
        Client:         clients.V3Client,
        base:           b,
        ownerID:        ownerID,
        ownerName:      ownerName,
        repo:           repo,
        installationID: installationID,
    }
}

// withRetry wraps an API call with automatic retry on installation errors
func (r *RetryableGitHubClient) withRetry(ctx context.Context, fn func(*github.Client) error) error {
    // First attempt
    err := fn(r.Client)
    if err == nil {
        return nil
    }

    // Check if this is an installation error
    if !r.base.isInstallationError(err) {
        return err
    }

    // Attempt recovery
    newClients, recoveryErr := r.base.handleInstallationError(ctx, err, r.ownerID, r.ownerName, r.repo, r.installationID)
    if recoveryErr != nil {
        return recoveryErr
    }

    // Retry with new client
    r.Client = newClients.V3Client
    return fn(r.Client)
}

// Example: Wrap common operations
func (r *RetryableGitHubClient) GetRepository(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
    var repository *github.Repository
    var resp *github.Response

    err := r.withRetry(ctx, func(client *github.Client) error {
        var err error
        repository, resp, err = client.Repositories.Get(ctx, owner, repo)
        return err
    })

    return repository, resp, err
}
```

**Testing Plan**:

```go
// File: server/handler/github_retry_test.go

func TestRetryableGitHubClient(t *testing.T) {
    t.Run("successful call - no retry", func(t *testing.T) {
        // Mock successful call
        mockClient := &mockGitHubClient{
            getRepoFunc: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
                return &github.Repository{Name: github.String("test")}, nil, nil
            },
        }

        callCount := atomic.Int32{}
        retryClient := &RetryableGitHubClient{
            Client: mockClient,
            base:   &Base{},
        }

        err := retryClient.withRetry(context.Background(), func(client *github.Client) error {
            callCount.Add(1)
            _, _, err := mockClient.getRepoFunc(context.Background(), "owner", "repo")
            return err
        })

        require.NoError(t, err)
        require.Equal(t, int32(1), callCount.Load(), "should only call once")
    })

    t.Run("installation error - retry succeeds", func(t *testing.T) {
        callCount := atomic.Int32{}

        // First call fails, second succeeds
        mockClient := &mockGitHubClient{
            getRepoFunc: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
                if callCount.Load() == 0 {
                    return nil, nil, &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}
                }
                return &github.Repository{Name: github.String("test")}, nil, nil
            },
        }

        // Mock recovery
        mockBase := &Base{
            handleInstallationErrorFunc: func(ctx context.Context, err error, ownerID int64, ownerName, repo string, installationID int64) (*InstallationClients, error) {
                return &InstallationClients{V3Client: github.NewClient(nil)}, nil
            },
        }

        retryClient := &RetryableGitHubClient{
            Client: mockClient,
            base:   mockBase,
        }

        err := retryClient.withRetry(context.Background(), func(client *github.Client) error {
            callCount.Add(1)
            _, _, err := mockClient.getRepoFunc(context.Background(), "owner", "repo")
            return err
        })

        require.NoError(t, err)
        require.Equal(t, int32(2), callCount.Load(), "should retry once")
    })

    t.Run("installation error - recovery fails", func(t *testing.T) {
        mockClient := &mockGitHubClient{
            getRepoFunc: func(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
                return nil, nil, &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}
            },
        }

        expectedErr := errors.New("recovery failed")
        mockBase := &Base{
            handleInstallationErrorFunc: func(ctx context.Context, err error, ownerID int64, ownerName, repo string, installationID int64) (*InstallationClients, error) {
                return nil, expectedErr
            },
        }

        retryClient := &RetryableGitHubClient{
            Client: mockClient,
            base:   mockBase,
        }

        err := retryClient.withRetry(context.Background(), func(client *github.Client) error {
            _, _, err := mockClient.getRepoFunc(context.Background(), "owner", "repo")
            return err
        })

        require.Error(t, err)
        require.Equal(t, expectedErr, err)
    })
}
```

**Acceptance Criteria**:
- ✅ Successful API calls don't trigger retry logic
- ✅ Installation errors trigger recovery and retry exactly once
- ✅ Recovery failure returns error without further retries
- ✅ Non-installation errors don't trigger retry
- ✅ Retry updates client with new instance
- ✅ All tests pass
- ✅ No infinite retry loops

---

### Step 3.3: Handle Negative Caching for 404/410

**Information & References**:
- Location: `server/handler/client_cache.go` (existing negative cache implementation)
- Reference: Lines 246-270 for PutNegative and IsNegativelyCached

**Implementation**:

The negative caching is already implemented in ClientCache. We just need to ensure it's used correctly in error handling:

```go
// File: server/handler/base.go

// Ensure handleInstallationError uses negative caching for 404/410
// (Already implemented in Step 3.1, but verify here)

// Add method to clear negative cache (useful for webhooks)
func (b *Base) ClearNegativeCache(ownerID int64) {
    if b.ClientCache != nil && ownerID > 0 {
        // Remove negative cache entry by deleting it
        b.ClientCache.Delete(ownerID)
    }
}

// Add method to check if owner is negatively cached
func (b *Base) IsOwnerNegativelyCached(ownerID int64) bool {
    if b.ClientCache == nil || ownerID == 0 {
        return false
    }
    return b.ClientCache.IsNegativelyCached(ownerID)
}
```

**Testing Plan**:

```go
// File: server/handler/base_negative_cache_test.go

func TestNegativeCaching(t *testing.T) {
    t.Run("404 creates negative cache entry", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)
        base := &Base{
            ClientCache: cache,
        }

        ctx := context.Background()
        err := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 404},
            Message:  "Installation not found",
        }

        // Handle error
        _, recoveryErr := base.handleInstallationError(ctx, err, 123, "test-org", "", 999)

        require.Error(t, recoveryErr)

        // Verify negative cache
        require.True(t, base.IsOwnerNegativelyCached(123))
    })

    t.Run("negative cache prevents repeated lookups", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)

        apiCallCount := atomic.Int32{}
        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                apiCallCount.Add(1)
                return nil, &github.ErrorResponse{Response: &http.Response{StatusCode: 404}}
            },
        }

        base := &Base{
            Installations: mockInstallations,
            ClientCache:   cache,
            GithubCloud:   true,
        }

        ctx := context.Background()

        // First call - should hit API
        _, err1 := base.retrieveClientAndInstallationId(ctx, 0, 123, "test-org", "")
        require.Error(t, err1)
        require.Equal(t, int32(1), apiCallCount.Load())

        // Second call - should use negative cache, not hit API
        _, err2 := base.retrieveClientAndInstallationId(ctx, 0, 123, "test-org", "")
        require.Error(t, err2)
        require.Equal(t, int32(1), apiCallCount.Load(), "should not call API again")
        require.Contains(t, err2.Error(), "negatively cached")
    })

    t.Run("clear negative cache", func(t *testing.T) {
        cache := NewClientCache(10*time.Minute, 2*time.Minute, 1000, nil)
        cache.PutNegative(123)

        base := &Base{
            ClientCache: cache,
        }

        require.True(t, base.IsOwnerNegativelyCached(123))

        // Clear negative cache
        base.ClearNegativeCache(123)

        require.False(t, base.IsOwnerNegativelyCached(123))
    })
}
```

**Acceptance Criteria**:
- ✅ 404/410 errors create negative cache entries
- ✅ Negative cache entries prevent repeated API calls
- ✅ Negative cache respects TTL (2 minutes)
- ✅ Can clear negative cache programmatically
- ✅ Negative cache doesn't affect other owner IDs
- ✅ All tests pass

---

### Step 3.4: Integration Tests for Error Recovery

**Information & References**:
- Location: `test/installation_error_recovery_test.go` (NEW)
- Reference: Existing integration test patterns

**Implementation**:

```go
// File: test/installation_error_recovery_integration_test.go

func TestInstallationErrorRecovery_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    t.Run("full error recovery flow", func(t *testing.T) {
        // Setup: Create base handler with real cache
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Mock installations service that simulates app reinstall
        installationIDSequence := atomic.Int64{}
        installationIDSequence.Store(1000) // Start with installation ID 1000

        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                currentID := installationIDSequence.Load()
                return &github.Installation{ID: github.Int64(currentID)}, nil
            },
        }

        // Mock client creator that returns clients
        clientCreationCount := atomic.Int32{}
        mockClientCreator := &mockClientCreator{
            newOrgClientFunc: func(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
                clientCreationCount.Add(1)
                client := github.NewClient(nil)
                // Attach metadata to track which installation ID this client uses
                return client, nil
            },
        }

        base := &handler.Base{
            Installations: mockInstallations,
            ClientCreator: mockClientCreator,
            ClientCache:   cache,
            GithubCloud:   true,
        }

        ctx := context.Background()
        ownerID := int64(123)
        ownerName := "test-org"

        // Step 1: Get initial clients
        clients1, installID1, err := base.RetrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
        require.NoError(t, err)
        require.NotNil(t, clients1)
        require.Equal(t, int64(1000), installID1)
        require.Equal(t, int32(1), clientCreationCount.Load())

        // Step 2: Simulate app reinstall (installation ID changes)
        installationIDSequence.Store(2000) // App was reinstalled with new ID

        // Step 3: Simulate API call failure with 401
        apiError := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 401},
            Message:  "Bad credentials",
        }

        // Step 4: Handle installation error (should invalidate cache and recover)
        clients2, err := base.HandleInstallationError(ctx, apiError, ownerID, ownerName, "", installID1)
        require.NoError(t, err)
        require.NotNil(t, clients2)
        require.NotEqual(t, clients1, clients2, "should return new clients")
        require.Equal(t, int32(2), clientCreationCount.Load(), "should create new client")

        // Step 5: Verify cache was updated with new installation ID
        cachedClients, cachedID := cache.GetWithInstallationID(ownerID)
        require.NotNil(t, cachedClients)
        require.Equal(t, int64(2000), cachedID, "cache should have new installation ID")

        // Step 6: Next request should use new cached clients (no new creation)
        clients3, installID3, err := base.RetrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
        require.NoError(t, err)
        require.Equal(t, clients2, clients3, "should return cached clients")
        require.Equal(t, int64(2000), installID3)
        require.Equal(t, int32(2), clientCreationCount.Load(), "should not create another client")
    })

    t.Run("concurrent error recovery", func(t *testing.T) {
        // Test multiple goroutines hitting same error simultaneously
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Pre-populate with old installation
        oldClients := &handler.InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, oldClients, 1000)

        clientCreationCount := atomic.Int32{}
        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                time.Sleep(50 * time.Millisecond) // Simulate slow API
                return &github.Installation{ID: github.Int64(2000)}, nil
            },
        }

        mockClientCreator := &mockClientCreator{
            newOrgClientFunc: func(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
                clientCreationCount.Add(1)
                return github.NewClient(nil), nil
            },
        }

        base := &handler.Base{
            Installations: mockInstallations,
            ClientCreator: mockClientCreator,
            ClientCache:   cache,
            GithubCloud:   true,
        }

        ctx := context.Background()
        apiError := &github.ErrorResponse{
            Response: &http.Response{StatusCode: 401},
            Message:  "Bad credentials",
        }

        // Launch 10 concurrent error recovery attempts
        var wg sync.WaitGroup
        errors := make([]error, 10)
        clients := make([]*handler.InstallationClients, 10)

        for i := 0; i < 10; i++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                c, err := base.HandleInstallationError(ctx, apiError, 123, "test-org", "", 1000)
                clients[idx] = c
                errors[idx] = err
            }(i)
        }

        wg.Wait()

        // Verify all succeeded
        for i := 0; i < 10; i++ {
            require.NoError(t, errors[i])
            require.NotNil(t, clients[i])
        }

        // Verify only created client once (singleflight working)
        require.Equal(t, int32(1), clientCreationCount.Load(), "should only create client once despite 10 concurrent errors")
    })
}
```

**Acceptance Criteria**:
- ✅ Full error recovery flow works end-to-end
- ✅ Cache invalidation and recovery detected installation ID changes
- ✅ Concurrent error recovery uses singleflight (only creates client once)
- ✅ Cache updated with new installation ID after recovery
- ✅ Subsequent requests use new cached clients
- ✅ All integration tests pass
- ✅ Tests complete in reasonable time (< 10 seconds)

---

## Phase 4: Optimize Cache TTL

### Overview
Increase positive cache TTL from 10 minutes to 45 minutes to better align with 1-hour token lifespan, reducing token creation frequency.

---

### Step 4.1: Increase Positive Cache TTL to 45 Minutes

**Information & References**:
- Location: `server/handler/client_cache.go` (line 46)
- Current TTL: 10 minutes
- Token lifespan: 60 minutes (ghinstallation refreshes at 59 minutes)
- Recommended TTL: 45 minutes (leaves 15-minute buffer)

**Implementation**:

```go
// File: server/handler/client_cache.go

const (
    // Default cache configuration
    // UPDATED: Increased from 10min to 45min to better align with 1-hour token lifespan
    // Tokens are valid for 60 minutes, ghinstallation refreshes at 59 minutes
    // 45-minute cache TTL provides 15-minute safety buffer
    defaultClientCacheTTL         = 45 * time.Minute // Previously: 10 * time.Minute
    defaultClientCacheNegativeTTL = 2 * time.Minute  // Unchanged
    defaultClientCacheMaxSize     = 1000             // Unchanged
    defaultCleanupInterval        = 1 * time.Minute  // Unchanged
    metricsPublishInterval        = 10 * time.Second // Unchanged
)
```

**Testing Plan**:

```go
// File: server/handler/client_cache_test.go

func TestClientCache_IncreasedTTL(t *testing.T) {
    t.Run("clients remain cached for 45 minutes", func(t *testing.T) {
        cache := NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        clients := &InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, clients, 999)

        // Verify cached immediately
        cached, id := cache.GetWithInstallationID(123)
        require.NotNil(t, cached)
        require.Equal(t, int64(999), id)

        // Advance time by 44 minutes (should still be cached)
        // Note: In real test, use time mocking library or just check ExpiresAt
        // For now, verify ExpiresAt is set correctly
        value, _ := cache.cache.Load(int64(123))
        cachedEntry := value.(*CachedClients)
        expectedExpiry := cachedEntry.CreatedAt.Add(45 * time.Minute)
        require.Equal(t, expectedExpiry, cachedEntry.ExpiresAt)
    })

    t.Run("negative cache still expires after 2 minutes", func(t *testing.T) {
        cache := NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        cache.PutNegative(123)

        // Verify negative cache ExpiresAt is 2 minutes
        value, _ := cache.cache.Load(int64(123))
        cachedEntry := value.(*CachedClients)
        expectedExpiry := cachedEntry.CreatedAt.Add(2 * time.Minute)
        require.Equal(t, expectedExpiry, cachedEntry.ExpiresAt)
        require.True(t, cachedEntry.IsNegative)
    })
}
```

**Acceptance Criteria**:
- ✅ Positive cache TTL is 45 minutes
- ✅ Negative cache TTL remains 2 minutes (unchanged)
- ✅ Existing cache tests still pass
- ✅ Cache hit rate remains high (>95%)
- ✅ Token creation frequency decreases by ~4.5x (10min → 45min)

---

### Step 4.2: Add TTL Configuration Options

**Information & References**:
- Location: `config/policy-bot.example.yml`
- Allow operators to tune TTL based on their environment

**Implementation**:

```go
// File: server/handler/config.go

type Config struct {
    // ... existing fields ...

    // ClientCache configuration
    ClientCacheTTL         time.Duration `yaml:"client_cache_ttl" json:"client_cache_ttl"`
    ClientCacheNegativeTTL time.Duration `yaml:"client_cache_negative_ttl" json:"client_cache_negative_ttl"`
    ClientCacheMaxSize     int           `yaml:"client_cache_max_size" json:"client_cache_max_size"`
}

// ApplyDefaults sets default values for optional configuration
func (c *Config) ApplyDefaults() {
    if c.ClientCacheTTL == 0 {
        c.ClientCacheTTL = 45 * time.Minute
    }
    if c.ClientCacheNegativeTTL == 0 {
        c.ClientCacheNegativeTTL = 2 * time.Minute
    }
    if c.ClientCacheMaxSize == 0 {
        c.ClientCacheMaxSize = 1000
    }
}
```

```yaml
# File: config/policy-bot.example.yml

github:
  app:
    # Client cache configuration
    # Positive cache TTL (how long to cache valid clients)
    # Default: 45m (matches token lifespan of 60m with 15m safety buffer)
    # Range: 1m - 50m (must be less than token lifespan)
    client_cache_ttl: 45m

    # Negative cache TTL (how long to cache "not found" results)
    # Default: 2m
    # Range: 30s - 10m
    client_cache_negative_ttl: 2m

    # Maximum number of cached clients
    # Default: 1000
    client_cache_max_size: 1000
```

**Testing Plan**:

```go
// File: server/handler/config_test.go

func TestConfigDefaults(t *testing.T) {
    t.Run("apply defaults", func(t *testing.T) {
        config := &Config{}
        config.ApplyDefaults()

        require.Equal(t, 45*time.Minute, config.ClientCacheTTL)
        require.Equal(t, 2*time.Minute, config.ClientCacheNegativeTTL)
        require.Equal(t, 1000, config.ClientCacheMaxSize)
    })

    t.Run("custom values preserved", func(t *testing.T) {
        config := &Config{
            ClientCacheTTL:         30 * time.Minute,
            ClientCacheNegativeTTL: 5 * time.Minute,
            ClientCacheMaxSize:     500,
        }
        config.ApplyDefaults()

        require.Equal(t, 30*time.Minute, config.ClientCacheTTL)
        require.Equal(t, 5*time.Minute, config.ClientCacheNegativeTTL)
        require.Equal(t, 500, config.ClientCacheMaxSize)
    })
}
```

**Acceptance Criteria**:
- ✅ Configuration loads from YAML
- ✅ Default values applied when not specified
- ✅ Custom values preserved
- ✅ Cache uses configured TTL values
- ✅ Documentation updated in example config

---

### Step 4.3: Update Cache Metrics and Tests

**Information & References**:
- Location: `server/handler/client_cache.go`
- Update tests to work with new TTL values

**Implementation**:

```go
// File: server/handler/client_cache_test.go

// Update existing tests that hardcode 10-minute TTL

func TestClientCache_TTL(t *testing.T) {
    // Use configurable TTL in tests instead of hardcoded value
    ttl := 45 * time.Minute
    cache := NewClientCache(ttl, 2*time.Minute, 1000, nil)

    // Test rest remains same...
}

// Update any tests that check specific expiry times
func TestClientCache_Expiration(t *testing.T) {
    ttl := 45 * time.Minute
    cache := NewClientCache(ttl, 2*time.Minute, 1000, nil)

    clients := &InstallationClients{V3Client: github.NewClient(nil)}
    cache.PutWithInstallationID(123, clients, 999)

    // Verify expiry is set correctly
    value, _ := cache.cache.Load(int64(123))
    cached := value.(*CachedClients)

    expectedExpiry := cached.CreatedAt.Add(ttl)
    require.WithinDuration(t, expectedExpiry, cached.ExpiresAt, time.Second)
}
```

**Acceptance Criteria**:
- ✅ All existing cache tests pass with new TTL
- ✅ No hardcoded 10-minute TTL assumptions in tests
- ✅ Cache metrics show increased hit rate
- ✅ Token creation rate decreases by ~4.5x

---

## Phase 5: Metrics & Observability

### Overview
Add comprehensive metrics and dashboards to monitor error-driven cache invalidation and track system health.

---

### Step 5.1: Add Error-Driven Metrics

**Information & References**:
- Location: `server/handler/base.go`
- Reference: Existing `recordInstallationClientMetric` function
- Integration with New Relic via go-metrics

**Implementation**:

```go
// File: server/handler/base.go

// All new metrics (already defined in previous steps, consolidate here)
const (
    // Existing metrics
    MetricsKeyInstallationClientSuccess          = "installation.client.success"
    MetricsKeyInstallationClientFailure          = "installation.client.failure"

    // Error detection metrics
    MetricsKeyInstallationErrorDetected          = "installation.error.detected"
    MetricsKeyInstallationErrorUnauthorized      = "installation.error.unauthorized"
    MetricsKeyInstallationErrorNotFound          = "installation.error.not_found"
    MetricsKeyInstallationErrorGone              = "installation.error.gone"
    MetricsKeyInstallationErrorUnprocessable     = "installation.error.unprocessable"
    MetricsKeyInstallationErrorForbidden         = "installation.error.forbidden"
    MetricsKeyInstallationErrorRateLimit         = "installation.error.rate_limit"

    // Cache invalidation metrics
    MetricsKeyInstallationCacheInvalidation      = "installation.cache.invalidation"
    MetricsKeyInstallationCacheInvalidationRate  = "installation.cache.invalidation_rate"

    // Recovery metrics
    MetricsKeyInstallationRecoverySuccess        = "installation.recovery.success"
    MetricsKeyInstallationRecoveryFailure        = "installation.recovery.failure"
    MetricsKeyInstallationRecoveryLatencyMs      = "installation.recovery.latency_ms"
    MetricsKeyInstallationRecoveryAttempts       = "installation.recovery.attempts"

    // Singleflight metrics
    MetricsKeySingleflightHits                   = "installation.singleflight.hits"
    MetricsKeySingleflightMisses                 = "installation.singleflight.misses"
)

// recordInstallationRecoveryLatency records recovery latency in milliseconds
func (b *Base) recordInstallationRecoveryLatency(duration time.Duration) {
    if b.metricsRegistry != nil {
        if histogram := b.metricsRegistry.GetOrRegisterHistogram(
            MetricsKeyInstallationRecoveryLatencyMs,
            metrics.NewExpDecaySample(1028, 0.015),
        ); histogram != nil {
            histogram.Update(duration.Milliseconds())
        }
    }
}
```

**Testing Plan**:

```go
// File: server/handler/metrics_test.go

func TestErrorDrivenMetrics(t *testing.T) {
    registry := metrics.NewRegistry()

    base := &Base{
        metricsRegistry: registry,
    }

    // Record various metrics
    base.recordInstallationClientMetric(MetricsKeyInstallationErrorDetected)
    base.recordInstallationClientMetric(MetricsKeyInstallationRecoverySuccess)
    base.recordInstallationRecoveryLatency(100 * time.Millisecond)

    // Verify metrics were recorded
    errorCounter := registry.Get(MetricsKeyInstallationErrorDetected)
    require.NotNil(t, errorCounter)

    recoveryCounter := registry.Get(MetricsKeyInstallationRecoverySuccess)
    require.NotNil(t, recoveryCounter)

    latencyHistogram := registry.Get(MetricsKeyInstallationRecoveryLatencyMs)
    require.NotNil(t, latencyHistogram)
}
```

**Acceptance Criteria**:
- ✅ All metrics are registered in go-metrics registry
- ✅ Metrics are exported to New Relic
- ✅ Counter metrics increment correctly
- ✅ Histogram metrics record latency values
- ✅ Metrics visible in New Relic dashboard
- ✅ No performance impact from metric recording

---

### Step 5.2: Update Dashboards

**Information & References**:
- Location: `.claude/dashboards/operational-dashboard.md`
- Reference: Existing New Relic dashboard panels

**Implementation**:

```markdown
# File: .claude/dashboards/operational-dashboard.md

## Installation Error Detection & Recovery Dashboard

### Panel 1: Installation Error Rate
**Metric**: `installation.error.detected`
**Query**:
\`\`\`nrql
SELECT rate(count(*), 1 minute) AS 'Error Rate'
FROM Metric
WHERE metricName = 'installation.error.detected'
TIMESERIES AUTO
\`\`\`
**Alert**: Error rate > 5/minute (indicates systemic issue)

### Panel 2: Error Types Breakdown
**Metrics**: `installation.error.*`
**Query**:
\`\`\`nrql
SELECT count(*) AS 'Count'
FROM Metric
WHERE metricName LIKE 'installation.error.%'
FACET metricName
TIMESERIES AUTO
\`\`\`

### Panel 3: Cache Invalidation Rate
**Metric**: `installation.cache.invalidation`
**Query**:
\`\`\`nrql
SELECT rate(count(*), 1 hour) AS 'Invalidations/Hour'
FROM Metric
WHERE metricName = 'installation.cache.invalidation'
TIMESERIES AUTO
\`\`\`
**Expected**: < 10/hour (1-2 per org per month)

### Panel 4: Recovery Success Rate
**Metrics**: `installation.recovery.success`, `installation.recovery.failure`
**Query**:
\`\`\`nrql
SELECT
  (filter(count(*), WHERE metricName = 'installation.recovery.success') /
   (filter(count(*), WHERE metricName = 'installation.recovery.success') +
    filter(count(*), WHERE metricName = 'installation.recovery.failure'))) * 100
  AS 'Success Rate %'
FROM Metric
WHERE metricName IN ('installation.recovery.success', 'installation.recovery.failure')
TIMESERIES AUTO
\`\`\`
**Expected**: > 95%

### Panel 5: Recovery Latency
**Metric**: `installation.recovery.latency_ms`
**Query**:
\`\`\`nrql
SELECT
  percentile(value, 50) AS 'P50',
  percentile(value, 95) AS 'P95',
  percentile(value, 99) AS 'P99'
FROM Metric
WHERE metricName = 'installation.recovery.latency_ms'
TIMESERIES AUTO
\`\`\`
**Expected**: P95 < 500ms

### Panel 6: Singleflight Effectiveness
**Metrics**: `installation.singleflight.hits`, `installation.singleflight.misses`
**Query**:
\`\`\`nrql
SELECT
  filter(count(*), WHERE metricName = 'installation.singleflight.hits') AS 'Deduplicated',
  filter(count(*), WHERE metricName = 'installation.singleflight.misses') AS 'Unique'
FROM Metric
WHERE metricName LIKE 'installation.singleflight.%'
TIMESERIES AUTO
\`\`\`

### Panel 7: Cache Hit Rate (Existing - Updated)
**Updated to show 45-min TTL impact**
\`\`\`nrql
SELECT
  (filter(count(*), WHERE metricName = 'installation.client_cache.hits') /
   (filter(count(*), WHERE metricName = 'installation.client_cache.hits') +
    filter(count(*), WHERE metricName = 'installation.client_cache.misses'))) * 100
  AS 'Cache Hit Rate %'
FROM Metric
WHERE metricName LIKE 'installation.client_cache.%'
TIMESERIES AUTO
\`\`\`
**Expected**: > 99% (increased from > 95% with longer TTL)
```

**Acceptance Criteria**:
- ✅ Dashboard panels created in New Relic
- ✅ All queries return data
- ✅ Panels visualize trends over time
- ✅ Dashboard accessible to team

---

### Step 5.3: Add Alerting Rules

**Information & References**:
- New Relic alerting policy configuration

**Implementation**:

```yaml
# File: .claude/monitoring/alert-policies.yml

alert_policies:
  - name: "Policy Bot - Installation Errors"
    incident_preference: "PER_CONDITION"

    conditions:
      - name: "High Installation Error Rate"
        type: "NRQL"
        enabled: true
        query: |
          SELECT rate(count(*), 1 minute)
          FROM Metric
          WHERE metricName = 'installation.error.detected'
        threshold:
          critical: 10  # > 10 errors/minute
          warning: 5    # > 5 errors/minute
        duration: 5     # For 5 minutes
        operator: "above"

      - name: "Installation Recovery Failure"
        type: "NRQL"
        enabled: true
        query: |
          SELECT count(*)
          FROM Metric
          WHERE metricName = 'installation.recovery.failure'
        threshold:
          critical: 1   # Any recovery failure
        duration: 1     # Immediate alert
        operator: "above"

      - name: "High Cache Invalidation Rate"
        type: "NRQL"
        enabled: true
        query: |
          SELECT rate(count(*), 1 hour)
          FROM Metric
          WHERE metricName = 'installation.cache.invalidation'
        threshold:
          critical: 50  # > 50 invalidations/hour (indicates problem)
          warning: 20   # > 20 invalidations/hour
        duration: 10
        operator: "above"

      - name: "Low Recovery Success Rate"
        type: "NRQL"
        enabled: true
        query: |
          SELECT
            (filter(count(*), WHERE metricName = 'installation.recovery.success') /
             (filter(count(*), WHERE metricName = 'installation.recovery.success') +
              filter(count(*), WHERE metricName = 'installation.recovery.failure'))) * 100
          FROM Metric
        threshold:
          critical: 90  # < 90% success rate
          warning: 95   # < 95% success rate
        duration: 10
        operator: "below"

      - name: "High Recovery Latency"
        type: "NRQL"
        enabled: true
        query: |
          SELECT percentile(value, 95)
          FROM Metric
          WHERE metricName = 'installation.recovery.latency_ms'
        threshold:
          critical: 1000  # P95 > 1 second
          warning: 500    # P95 > 500ms
        duration: 10
        operator: "above"

notification_channels:
  - type: "slack"
    channel: "#policy-bot-alerts"
  - type: "pagerduty"
    service_key: "PAGERDUTY_SERVICE_KEY"
```

**Acceptance Criteria**:
- ✅ Alert policies created in New Relic
- ✅ Alerts trigger on threshold breaches
- ✅ Notifications sent to Slack/PagerDuty
- ✅ Alert thresholds tuned based on baseline metrics
- ✅ No false positive alerts during normal operation

---

## Phase 6: (Optional) Installation Webhook Handlers

### Overview
Listen to GitHub installation webhooks for proactive cache management. This is optional but provides zero-cost proactive detection of installation changes.

---

### Step 6.1: Add Webhook Handlers for Installation Events

**Information & References**:
- Location: `server/handler/installation_webhook.go` (NEW)
- Reference: Existing webhook handlers in `server/handler/*.go`
- GitHub webhook events: installation.deleted, installation.created, installation.suspend, installation.unsuspend

**Implementation**:

```go
// File: server/handler/installation_webhook.go

import (
    "context"

    "github.com/google/go-github/v47/github"
    "github.com/rs/zerolog"
)

// InstallationWebhookHandler handles GitHub installation webhook events
// for proactive cache management.
type InstallationWebhookHandler struct {
    Base *Base
}

// HandleInstallationEvent handles installation webhook events
func (h *InstallationWebhookHandler) HandleInstallationEvent(ctx context.Context, event *github.InstallationEvent) error {
    logger := zerolog.Ctx(ctx)
    action := event.GetAction()
    installation := event.GetInstallation()
    installationID := installation.GetID()

    // Get account (org/user) info
    account := installation.GetAccount()
    accountID := account.GetID()
    accountLogin := account.GetLogin()

    logger.Info().
        Str("action", action).
        Int64("installation_id", installationID).
        Int64("account_id", accountID).
        Str("account_login", accountLogin).
        Msg("Received installation webhook event")

    switch action {
    case "deleted":
        return h.handleInstallationDeleted(ctx, accountID, accountLogin, installationID)

    case "suspend":
        return h.handleInstallationSuspended(ctx, accountID, accountLogin, installationID)

    case "unsuspend":
        return h.handleInstallationUnsuspended(ctx, accountID, accountLogin, installationID)

    case "created":
        return h.handleInstallationCreated(ctx, accountID, accountLogin, installationID)

    case "new_permissions_accepted":
        // Permissions changed - invalidate cache to force new token with updated permissions
        return h.handlePermissionsChanged(ctx, accountID, accountLogin, installationID)
    }

    logger.Debug().
        Str("action", action).
        Msg("Ignoring installation webhook action")

    return nil
}

// handleInstallationDeleted invalidates cache when installation is deleted
func (h *InstallationWebhookHandler) handleInstallationDeleted(ctx context.Context, accountID int64, accountLogin string, installationID int64) error {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Int64("account_id", accountID).
        Str("account", accountLogin).
        Int64("installation_id", installationID).
        Msg("Installation deleted - invalidating cache")

    if h.Base.ClientCache != nil && accountID > 0 {
        // Create negative cache entry to prevent repeated lookups
        h.Base.ClientCache.PutNegative(accountID)

        h.Base.recordInstallationClientMetric(MetricsKeyInstallationWebhookInvalidation)

        logger.Info().
            Int64("account_id", accountID).
            Msg("Created negative cache entry for deleted installation")
    }

    return nil
}

// handleInstallationSuspended invalidates cache when installation is suspended
func (h *InstallationWebhookHandler) handleInstallationSuspended(ctx context.Context, accountID int64, accountLogin string, installationID int64) error {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Int64("account_id", accountID).
        Str("account", accountLogin).
        Int64("installation_id", installationID).
        Msg("Installation suspended - invalidating cache")

    if h.Base.ClientCache != nil && accountID > 0 {
        // Remove cache entry - next request will fail and create negative cache
        h.Base.ClientCache.Delete(accountID)

        h.Base.recordInstallationClientMetric(MetricsKeyInstallationWebhookInvalidation)

        logger.Info().
            Int64("account_id", accountID).
            Msg("Invalidated cache for suspended installation")
    }

    return nil
}

// handleInstallationUnsuspended clears negative cache when installation is restored
func (h *InstallationWebhookHandler) handleInstallationUnsuspended(ctx context.Context, accountID int64, accountLogin string, installationID int64) error {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Int64("account_id", accountID).
        Str("account", accountLogin).
        Int64("installation_id", installationID).
        Msg("Installation unsuspended - clearing negative cache")

    if h.Base.ClientCache != nil && accountID > 0 {
        // Clear negative cache to allow new lookups
        h.Base.ClearNegativeCache(accountID)

        h.Base.recordInstallationClientMetric(MetricsKeyInstallationWebhookCacheClear)

        logger.Info().
            Int64("account_id", accountID).
            Msg("Cleared negative cache for unsuspended installation")
    }

    return nil
}

// handleInstallationCreated clears negative cache for newly created installations
func (h *InstallationWebhookHandler) handleInstallationCreated(ctx context.Context, accountID int64, accountLogin string, installationID int64) error {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Int64("account_id", accountID).
        Str("account", accountLogin).
        Int64("installation_id", installationID).
        Msg("Installation created - clearing negative cache if present")

    if h.Base.ClientCache != nil && accountID > 0 {
        // Clear negative cache (if present) to allow new installation to be used
        if h.Base.IsOwnerNegativelyCached(accountID) {
            h.Base.ClearNegativeCache(accountID)

            h.Base.recordInstallationClientMetric(MetricsKeyInstallationWebhookCacheClear)

            logger.Info().
                Int64("account_id", accountID).
                Msg("Cleared negative cache for newly created installation")
        }
    }

    return nil
}

// handlePermissionsChanged invalidates cache when permissions are updated
func (h *InstallationWebhookHandler) handlePermissionsChanged(ctx context.Context, accountID int64, accountLogin string, installationID int64) error {
    logger := zerolog.Ctx(ctx)

    logger.Info().
        Int64("account_id", accountID).
        Str("account", accountLogin).
        Int64("installation_id", installationID).
        Msg("Installation permissions changed - invalidating cache")

    if h.Base.ClientCache != nil && accountID > 0 {
        // Invalidate cache to force new token with updated permissions
        h.Base.ClientCache.Delete(accountID)

        h.Base.recordInstallationClientMetric(MetricsKeyInstallationWebhookInvalidation)

        logger.Info().
            Int64("account_id", accountID).
            Msg("Invalidated cache for permissions change")
    }

    return nil
}

// New metric keys for webhook-driven invalidation
const (
    MetricsKeyInstallationWebhookInvalidation = "installation.webhook.invalidation"
    MetricsKeyInstallationWebhookCacheClear   = "installation.webhook.cache_clear"
)
```

**Testing Plan**:

```go
// File: server/handler/installation_webhook_test.go

func TestInstallationWebhookHandler(t *testing.T) {
    t.Run("installation deleted - creates negative cache", func(t *testing.T) {
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Pre-populate cache
        clients := &handler.InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, clients, 999)

        base := &handler.Base{
            ClientCache: cache,
        }

        webhookHandler := &handler.InstallationWebhookHandler{
            Base: base,
        }

        ctx := context.Background()
        event := &github.InstallationEvent{
            Action: github.String("deleted"),
            Installation: &github.Installation{
                ID: github.Int64(999),
                Account: &github.User{
                    ID:    github.Int64(123),
                    Login: github.String("test-org"),
                },
            },
        }

        err := webhookHandler.HandleInstallationEvent(ctx, event)
        require.NoError(t, err)

        // Verify negative cache
        require.True(t, cache.IsNegativelyCached(123))
    })

    t.Run("installation created - clears negative cache", func(t *testing.T) {
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Create negative cache entry
        cache.PutNegative(123)

        base := &handler.Base{
            ClientCache: cache,
        }

        webhookHandler := &handler.InstallationWebhookHandler{
            Base: base,
        }

        ctx := context.Background()
        event := &github.InstallationEvent{
            Action: github.String("created"),
            Installation: &github.Installation{
                ID: github.Int64(1000),
                Account: &github.User{
                    ID:    github.Int64(123),
                    Login: github.String("test-org"),
                },
            },
        }

        err := webhookHandler.HandleInstallationEvent(ctx, event)
        require.NoError(t, err)

        // Verify negative cache cleared
        require.False(t, cache.IsNegativelyCached(123))
    })

    t.Run("installation suspended - invalidates cache", func(t *testing.T) {
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Pre-populate cache
        clients := &handler.InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(123, clients, 999)

        base := &handler.Base{
            ClientCache: cache,
        }

        webhookHandler := &handler.InstallationWebhookHandler{
            Base: base,
        }

        ctx := context.Background()
        event := &github.InstallationEvent{
            Action: github.String("suspend"),
            Installation: &github.Installation{
                ID: github.Int64(999),
                Account: &github.User{
                    ID:    github.Int64(123),
                    Login: github.String("test-org"),
                },
            },
        }

        err := webhookHandler.HandleInstallationEvent(ctx, event)
        require.NoError(t, err)

        // Verify cache invalidated
        cached, _ := cache.GetWithInstallationID(123)
        require.Nil(t, cached)
    })
}
```

**Acceptance Criteria**:
- ✅ Webhook handler processes all installation event types
- ✅ Deleted installations create negative cache entries
- ✅ Created installations clear negative cache
- ✅ Suspended installations invalidate cache
- ✅ Unsuspended installations clear negative cache
- ✅ Permission changes invalidate cache
- ✅ All tests pass
- ✅ Metrics recorded for webhook-driven invalidations

---

### Step 6.2: Implement Cache Invalidation by Installation ID

**Information & References**:
- Location: `server/handler/client_cache.go`
- Requirement: Invalidate cache by installation ID (not just owner ID)

**Implementation**:

```go
// File: server/handler/client_cache.go

// InvalidateByInstallation removes all cache entries for a specific installation ID
// This is useful for webhook-driven cache management when we know the installation ID
// but may not know all owner IDs that map to it.
func (c *ClientCache) InvalidateByInstallation(installationID int64) int {
    if installationID == 0 {
        return 0
    }

    invalidatedCount := 0

    // Iterate over cache and remove entries matching installation ID
    c.cache.Range(func(key, value interface{}) bool {
        cached := value.(*CachedClients)

        // Check if this entry is for the target installation
        if cached.InstallationID == installationID {
            // Remove this entry
            c.cache.Delete(key)
            c.size.Add(-1)
            c.evictions.Add(1)
            invalidatedCount++
        }

        return true // Continue iteration
    })

    return invalidatedCount
}

// GetInstallationID returns the installation ID for a cached owner (if present)
// Returns 0 if not found or negatively cached.
func (c *ClientCache) GetInstallationID(ownerID int64) int64 {
    if ownerID == 0 {
        return 0
    }

    value, ok := c.cache.Load(ownerID)
    if !ok {
        return 0
    }

    cached := value.(*CachedClients)
    if cached.IsNegative || cached.IsExpired() {
        return 0
    }

    return cached.InstallationID
}
```

**Testing Plan**:

```go
// File: server/handler/client_cache_test.go

func TestClientCache_InvalidateByInstallation(t *testing.T) {
    t.Run("invalidates all owners with installation ID", func(t *testing.T) {
        cache := NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        // Cache multiple owners with same installation ID
        clients := &InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(100, clients, 999) // Owner 100
        cache.PutWithInstallationID(101, clients, 999) // Owner 101
        cache.PutWithInstallationID(102, clients, 888) // Owner 102 (different installation)

        // Invalidate installation 999
        count := cache.InvalidateByInstallation(999)

        require.Equal(t, 2, count, "should invalidate 2 owners")

        // Verify owners 100 and 101 are invalidated
        cached100, _ := cache.GetWithInstallationID(100)
        require.Nil(t, cached100)

        cached101, _ := cache.GetWithInstallationID(101)
        require.Nil(t, cached101)

        // Verify owner 102 is still cached (different installation)
        cached102, id102 := cache.GetWithInstallationID(102)
        require.NotNil(t, cached102)
        require.Equal(t, int64(888), id102)
    })

    t.Run("get installation ID from cache", func(t *testing.T) {
        cache := NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        clients := &InstallationClients{V3Client: github.NewClient(nil)}
        cache.PutWithInstallationID(100, clients, 999)

        installID := cache.GetInstallationID(100)
        require.Equal(t, int64(999), installID)

        // Not found
        installID2 := cache.GetInstallationID(999)
        require.Equal(t, int64(0), installID2)
    })
}
```

**Acceptance Criteria**:
- ✅ Can invalidate cache by installation ID
- ✅ Invalidates all owners mapped to that installation
- ✅ Doesn't affect other installations
- ✅ Returns count of invalidated entries
- ✅ Can query installation ID by owner ID
- ✅ All tests pass

---

### Step 6.3: Test Webhook Integration

**Information & References**:
- Location: `test/installation_webhook_integration_test.go` (NEW)
- Simulate full webhook flow

**Implementation**:

```go
// File: test/installation_webhook_integration_test.go

func TestInstallationWebhookIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    t.Run("full webhook flow - installation reinstall", func(t *testing.T) {
        // Setup
        cache := handler.NewClientCache(45*time.Minute, 2*time.Minute, 1000, nil)

        installationIDSequence := atomic.Int64{}
        installationIDSequence.Store(1000)

        mockInstallations := &mockInstallationsService{
            getByOwnerFunc: func(ctx context.Context, owner string) (*github.Installation, error) {
                return &github.Installation{
                    ID: github.Int64(installationIDSequence.Load()),
                }, nil
            },
        }

        base := &handler.Base{
            Installations: mockInstallations,
            ClientCreator: &mockClientCreator{},
            ClientCache:   cache,
            GithubCloud:   true,
        }

        webhookHandler := &handler.InstallationWebhookHandler{
            Base: base,
        }

        ctx := context.Background()
        ownerID := int64(123)
        ownerName := "test-org"

        // Step 1: Normal operation - cache client
        clients1, installID1, err := base.RetrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
        require.NoError(t, err)
        require.Equal(t, int64(1000), installID1)

        // Step 2: Simulate app uninstall (webhook)
        deleteEvent := &github.InstallationEvent{
            Action: github.String("deleted"),
            Installation: &github.Installation{
                ID: github.Int64(1000),
                Account: &github.User{
                    ID:    github.Int64(ownerID),
                    Login: github.String(ownerName),
                },
            },
        }

        err = webhookHandler.HandleInstallationEvent(ctx, deleteEvent)
        require.NoError(t, err)

        // Verify negative cache
        require.True(t, cache.IsNegativelyCached(ownerID))

        // Step 3: App reinstalled with new installation ID
        installationIDSequence.Store(2000)

        // Webhook for new installation
        createEvent := &github.InstallationEvent{
            Action: github.String("created"),
            Installation: &github.Installation{
                ID: github.Int64(2000),
                Account: &github.User{
                    ID:    github.Int64(ownerID),
                    Login: github.String(ownerName),
                },
            },
        }

        err = webhookHandler.HandleInstallationEvent(ctx, createEvent)
        require.NoError(t, err)

        // Verify negative cache cleared
        require.False(t, cache.IsNegativelyCached(ownerID))

        // Step 4: Next request should use new installation ID
        clients2, installID2, err := base.RetrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")
        require.NoError(t, err)
        require.NotEqual(t, clients1, clients2)
        require.Equal(t, int64(2000), installID2)

        // Verify cached with new installation ID
        cachedInstallID := cache.GetInstallationID(ownerID)
        require.Equal(t, int64(2000), cachedInstallID)
    })
}
```

**Acceptance Criteria**:
- ✅ Full webhook flow works end-to-end
- ✅ Installation deletion creates negative cache
- ✅ Installation creation clears negative cache
- ✅ New installation ID used after reinstall
- ✅ Metrics recorded for webhook events
- ✅ All integration tests pass

---

## 🎯 Final Acceptance Criteria (All Phases)

### Functional Requirements
- ✅ Error-driven cache invalidation detects all installation errors (401/403/404/410/422)
- ✅ Rate limit errors (403) do NOT trigger cache invalidation
- ✅ Singleflight prevents cache stampedes during concurrent requests
- ✅ Recovery attempts succeed > 95% of time
- ✅ Negative caching prevents repeated lookups for deleted installations
- ✅ Webhook handlers proactively manage cache (Phase 6)

### Performance Requirements
- ✅ Zero latency impact on successful requests (no proactive validation)
- ✅ Zero additional API calls during normal operation
- ✅ P95 latency < 100ms (2x better than proactive approach)
- ✅ Cache hit rate > 99% (improved with 45-min TTL)
- ✅ Token creation rate reduced by 4.5x (10min → 45min TTL)

### Reliability Requirements
- ✅ No race conditions (verified with `go test -race`)
- ✅ No deadlocks under high concurrency
- ✅ Graceful degradation on API errors
- ✅ Self-healing (automatic recovery from installation changes)

### Observability Requirements
- ✅ Metrics track all error types and recovery attempts
- ✅ Dashboards visualize error rates and recovery success
- ✅ Alerts trigger on abnormal error rates or recovery failures
- ✅ Logs provide debugging context

### Testing Requirements
- ✅ Unit test coverage > 95%
- ✅ All unit tests pass
- ✅ Integration tests validate full error recovery flow
- ✅ Concurrency tests verify no stampedes
- ✅ All tests pass with race detector

### Documentation Requirements
- ✅ Code comments explain error-driven approach
- ✅ Configuration options documented
- ✅ Runbook updated with error recovery procedures
- ✅ Metrics and dashboards documented

---

## 📝 Post-Implementation Checklist

### Week 1: Development
- [ ] Complete Phase 1 (Error Detection)
- [ ] Complete Phase 2 (Singleflight)
- [ ] Complete Phase 3 (Error Recovery)
- [ ] Complete Phase 4 (TTL Optimization)
- [ ] Complete Phase 5 (Metrics)
- [ ] All tests passing

### Week 2: Testing & Rollout
- [ ] Code review completed
- [ ] Integration tests passing
- [ ] Load tests passing (1000+ concurrent requests)
- [ ] Deploy to staging
- [ ] Monitor metrics for 3 days
- [ ] Gradual production rollout (10% → 50% → 100%)

### Week 3: Monitoring & Validation
- [ ] Validate zero API cost increase
- [ ] Validate zero latency impact
- [ ] Confirm cache hit rate > 99%
- [ ] Confirm error recovery success rate > 95%
- [ ] Document baseline metrics
- [ ] Update runbooks

### Optional: Phase 6 (Webhooks)
- [ ] Implement webhook handlers
- [ ] Test webhook integration
- [ ] Deploy webhook handlers
- [ ] Monitor webhook-driven invalidations

---

## 📊 Success Metrics (30 Days Post-Deployment)

| Metric | Target | Actual | Status |
|--------|--------|--------|--------|
| API call volume increase | 0% | ___ | ___ |
| P95 latency impact | 0ms | ___ | ___ |
| Cache hit rate | > 99% | ___ | ___ |
| Error detection rate | < 10/day | ___ | ___ |
| Recovery success rate | > 95% | ___ | ___ |
| Token creation frequency | 4.5x reduction | ___ | ___ |
| Production incidents | 0 | ___ | ___ |

---

## 📞 Support & Contact

**Primary Contacts:**
- **Implementation Lead**: [Name]
- **SRE Contact**: [Name]
- **On-Call**: [Rotation]

**Resources:**
- **Documentation**: `.claude/analysis/installation_token_critique.md`
- **Runbook**: `docs/runbooks/installation_error_recovery.md`
- **Dashboard**: [New Relic Dashboard URL]
- **Slack**: `#policy-bot-alerts`

---

**Document Version**: 1.0
**Last Updated**: November 2025
**Status**: Ready for Implementation
