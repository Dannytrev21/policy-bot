# Installation ID Lookup Enhancement for GHEC with Smart Caching

**Date**: 2025-01-28 (Revised)
**Author**: Platform Engineering Team
**Status**: Planning - Redesigned Caching Strategy
**Priority**: Critical

---

## Executive Summary

For GitHub Enterprise Cloud (GHEC) deployments with organizations like `cof-sandbox` and `cof-primary`, we need a robust installation ID lookup strategy that handles various webhook event types and edge cases. This plan enhances the current implementation with a **hybrid multi-method lookup strategy** and **smart positive/negative caching** that correctly identifies valid installations regardless of lookup method.

### Key Change: Smart Cache Management 🔑

**Current Problem**: When direct installation ID extraction fails (ID=0 or missing), but we successfully find the installation via repository or organization lookup, the system might incorrectly treat this as a "negative" (not installed) case.

**Solution**: **If ANY lookup method succeeds → Installation EXISTS → Positive Cache**
- Direct ID found → ✅ Positive cache
- Repo lookup succeeds → ✅ Positive cache
- Org lookup succeeds → ✅ Positive cache
- ALL methods fail → ❌ Negative indicators

This ensures installations found via ANY method are correctly marked as existing, preventing false negatives in the cache.

---

## Problem Analysis

### Current Challenges
1. **Installation ID Issues**: Events from SNS → SQS sometimes have:
   - Missing installation ID (`installation: null`)
   - Invalid installation ID (`installation.id: 0`)
   - Malformed installation field

2. **Event Type Variations**:
   - **Repository events**: Have `repository.owner.login` and `repository.name`
   - **Organization events**: Only have `organization.login` (no repository)
   - **Installation events**: Have `installation.account.login`
   - **Team events**: Have `organization.login` but different structure

3. **GHEC Specifics**:
   - Apps installed at organization level (`cof-sandbox`, `cof-primary`)
   - Single installation per organization (typical for enterprise)
   - Need to handle cross-repository events within same org

### Available GitHub API Methods

```go
// InstallationsService interface methods:
GetByRepository(ctx, owner, repo string) (Installation, error)  // Most specific
GetByOwner(ctx, owner string) (Installation, error)             // Organization-level
ListAll(ctx) ([]Installation, error)                           // All installations
```

### Current Caching Problem ⚠️

The existing implementation has a critical flaw in cache management:

**Problem Scenario**:
1. Event arrives with `installation.id = 0` (invalid)
2. System might mark this as "negative" (not installed)
3. BUT: Repository lookup succeeds → Installation ID = 12345
4. **Issue**: Installation 12345 exists but might be in negative cache!

**Root Cause**: Conflating "can't extract ID directly" with "installation doesn't exist"

**Required Fix**:
- ✅ If ANY lookup method succeeds → **Positive cache** (installation exists)
- ❌ Only if ALL lookup methods fail → **Negative cache** (not installed)

---

## Recommended Solution: Smart Caching with Hybrid Lookup Strategy

### Lookup Priority Order

```
1. Direct Installation ID (if valid)
   ↓ (fallback if invalid/missing)
2. Repository-based lookup (if repo info available)
   ↓ (fallback if no repo)
3. Organization-based lookup (if org info available)
   ↓ (fallback if all fail)
4. Pass through to handler
```

### Why This Order?

| Method | Pros | Cons | Best For |
|--------|------|------|----------|
| **Direct ID** | Fastest, no API call | Sometimes missing/invalid | 80% of events |
| **Repository Lookup** | Most specific, accurate | Requires repo info | Pull requests, issues |
| **Organization Lookup** | Works without repo | Less specific | Org-level events |
| **Pass Through** | Safety net | No filtering | Edge cases |

### Smart Caching Strategy 🧠

#### Multi-Level Cache Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Installation Registry               │
│                                                      │
│  ┌──────────────────────────────────────────────┐  │
│  │  Installation ID Cache (Primary)             │  │
│  │  - Key: Installation ID (int64)              │  │
│  │  - Value: Exists/NotFound status             │  │
│  │  - TTL: 1 hour (positive), 5 min (negative) │  │
│  └──────────────────────────────────────────────┘  │
│                                                      │
│  ┌──────────────────────────────────────────────┐  │
│  │  Repository → Installation Mapping           │  │
│  │  - Key: "org/repo" (string)                  │  │
│  │  - Value: Installation ID (int64)            │  │
│  │  - TTL: 1 hour                               │  │
│  └──────────────────────────────────────────────┘  │
│                                                      │
│  ┌──────────────────────────────────────────────┐  │
│  │  Organization → Installation Mapping         │  │
│  │  - Key: "org:name" (string)                  │  │
│  │  - Value: Installation ID (int64)            │  │
│  │  - TTL: 1 hour                               │  │
│  └──────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

#### Cache Update Rules

**RULE 1: Successful Lookup = Positive Cache**
```go
// If ANY method finds installation → Mark as POSITIVE
if installationID, err := lookupByAnyMethod(); err == nil {
    registry.MarkInstalled(installationID)      // ✅ Positive cache
    mappingCache.Set(lookupKey, installationID) // Cache the mapping
    return installationID, nil
}
```

**RULE 2: All Methods Fail = Negative Cache**
```go
// Only if ALL methods fail → Mark as NEGATIVE
if directLookupFailed && repoLookupFailed && orgLookupFailed {
    // Can't mark negative without an ID, but we can cache the failure
    mappingCache.SetNotFound(lookupKey)  // ❌ Negative mapping cache
    return 0, ErrNoInstallation
}
```

**RULE 3: Cache Invalidation on Uninstall**
```go
// When app is uninstalled from repository
func handleUninstallEvent(installationID int64, repos []string) {
    registry.Remove(installationID)
    for _, repo := range repos {
        repoCache.Remove(repo)
    }
}
```

---

## Implementation Plan

### Phase 1: Enhanced Field Extraction

**Goal**: Extract all possible identifiers from webhook payloads

```go
// ExtractedIdentifiers holds all possible installation lookup keys
type ExtractedIdentifiers struct {
    InstallationID   int64  // Direct installation.id
    OwnerLogin      string // organization.login or repository.owner.login
    OwnerID         int64  // organization.id or repository.owner.id
    RepoName        string // repository.name
    RepoID          int64  // repository.id
    AccountLogin    string // installation.account.login (for installation events)
    AccountID       int64  // installation.account.id
}

// extractIdentifiers extracts all possible identifiers from payload
func extractIdentifiers(payload []byte) (*ExtractedIdentifiers, error) {
    ids := &ExtractedIdentifiers{}

    // Try to extract installation.id
    var installationEvent struct {
        Installation *struct {
            ID      int64 `json:"id"`
            Account *struct {
                Login string `json:"login"`
                ID    int64  `json:"id"`
            } `json:"account"`
        } `json:"installation"`
    }
    json.Unmarshal(payload, &installationEvent)
    if installationEvent.Installation != nil {
        ids.InstallationID = installationEvent.Installation.ID
        if installationEvent.Installation.Account != nil {
            ids.AccountLogin = installationEvent.Installation.Account.Login
            ids.AccountID = installationEvent.Installation.Account.ID
        }
    }

    // Try to extract repository info
    var repoEvent struct {
        Repository *struct {
            ID    int64  `json:"id"`
            Name  string `json:"name"`
            Owner *struct {
                Login string `json:"login"`
                ID    int64  `json:"id"`
            } `json:"owner"`
        } `json:"repository"`
    }
    json.Unmarshal(payload, &repoEvent)
    if repoEvent.Repository != nil {
        ids.RepoID = repoEvent.Repository.ID
        ids.RepoName = repoEvent.Repository.Name
        if repoEvent.Repository.Owner != nil {
            ids.OwnerLogin = repoEvent.Repository.Owner.Login
            ids.OwnerID = repoEvent.Repository.Owner.ID
        }
    }

    // Try to extract organization info (for org-level events)
    var orgEvent struct {
        Organization *struct {
            Login string `json:"login"`
            ID    int64  `json:"id"`
        } `json:"organization"`
    }
    json.Unmarshal(payload, &orgEvent)
    if orgEvent.Organization != nil {
        if ids.OwnerLogin == "" {
            ids.OwnerLogin = orgEvent.Organization.Login
            ids.OwnerID = orgEvent.Organization.ID
        }
    }

    return ids, nil
}
```

### Phase 2: Smart Multi-Method Lookup with Proper Cache Management

**Goal**: Try multiple lookup methods and correctly manage positive/negative cache

```go
// lookupInstallationWithSmartCache performs multi-method lookup with proper cache management
// CRITICAL: If ANY method succeeds → Installation exists → Positive cache
//           Only if ALL methods fail → Installation doesn't exist → Negative indicators
func (h *InstallationFilterHandler) lookupInstallationWithSmartCache(
    ctx context.Context,
    ids *ExtractedIdentifiers,
) (int64, error) {
    logger := zerolog.Ctx(ctx)

    // Track what we've tried for comprehensive logging
    var attemptedMethods []string
    var installationID int64
    var lookupErr error

    // Method 1: Direct installation ID from payload
    if ids.InstallationID > 0 {
        // Check if this ID is in positive cache
        if status, cached := h.registry.Check(ids.InstallationID); cached {
            if status == InstallationExists {
                logger.Debug().
                    Int64("installation_id", ids.InstallationID).
                    Msg("Using direct installation ID (cache hit)")
                return ids.InstallationID, nil
            }
            // If negative cache, continue to other methods
            // (maybe it was installed since negative cache entry)
        }

        // Not cached, validate with API
        if h.verifyInstallationExists(ctx, ids.InstallationID) {
            h.registry.MarkInstalled(ids.InstallationID) // ✅ POSITIVE CACHE
            return ids.InstallationID, nil
        }
        attemptedMethods = append(attemptedMethods, "direct_id")
    }

    // Method 2: Check mapping caches first (fast path)
    if ids.OwnerLogin != "" && ids.RepoName != "" {
        cacheKey := fmt.Sprintf("%s/%s", ids.OwnerLogin, ids.RepoName)
        if cachedID, found := h.repoCache.Get(cacheKey); found && cachedID > 0 {
            logger.Debug().
                Str("cache_key", cacheKey).
                Int64("installation_id", cachedID).
                Msg("Repository cache hit")
            h.registry.MarkInstalled(cachedID) // Ensure positive cache
            return cachedID, nil
        }
    }

    if ids.OwnerLogin != "" {
        cacheKey := fmt.Sprintf("org:%s", ids.OwnerLogin)
        if cachedID, found := h.orgCache.Get(cacheKey); found && cachedID > 0 {
            logger.Debug().
                Str("cache_key", cacheKey).
                Int64("installation_id", cachedID).
                Msg("Organization cache hit")
            h.registry.MarkInstalled(cachedID) // Ensure positive cache
            return cachedID, nil
        }
    }

    // Method 3: Repository-based API lookup
    if ids.OwnerLogin != "" && ids.RepoName != "" {
        logger.Debug().
            Str("owner", ids.OwnerLogin).
            Str("repo", ids.RepoName).
            Msg("Attempting repository-based installation lookup")

        installationID, lookupErr = h.lookupByRepository(ctx, ids.OwnerLogin, ids.RepoName)
        if lookupErr == nil {
            // SUCCESS! Mark as positive in ALL caches
            h.registry.MarkInstalled(installationID) // ✅ POSITIVE CACHE
            h.repoCache.Set(fmt.Sprintf("%s/%s", ids.OwnerLogin, ids.RepoName), installationID)
            // Also cache at org level if single installation per org
            h.orgCache.Set(fmt.Sprintf("org:%s", ids.OwnerLogin), installationID)

            logger.Info().
                Int64("installation_id", installationID).
                Str("method", "repository_lookup").
                Msg("Found installation via repository lookup")
            return installationID, nil
        }
        attemptedMethods = append(attemptedMethods, "repo_lookup")
    }

    // Method 4: Organization-based API lookup
    if ids.OwnerLogin != "" {
        logger.Debug().
            Str("org", ids.OwnerLogin).
            Msg("Attempting organization-based installation lookup")

        installationID, lookupErr = h.lookupByOrganization(ctx, ids.OwnerLogin)
        if lookupErr == nil {
            // SUCCESS! Mark as positive
            h.registry.MarkInstalled(installationID) // ✅ POSITIVE CACHE
            h.orgCache.Set(fmt.Sprintf("org:%s", ids.OwnerLogin), installationID)

            logger.Info().
                Int64("installation_id", installationID).
                Str("method", "organization_lookup").
                Msg("Found installation via organization lookup")
            return installationID, nil
        }
        attemptedMethods = append(attemptedMethods, "org_lookup")
    }

    // Method 5: Account-based lookup (for installation events)
    if ids.AccountLogin != "" && ids.AccountLogin != ids.OwnerLogin {
        logger.Debug().
            Str("account", ids.AccountLogin).
            Msg("Attempting account-based installation lookup")

        installationID, lookupErr = h.lookupByOrganization(ctx, ids.AccountLogin)
        if lookupErr == nil {
            // SUCCESS! Mark as positive
            h.registry.MarkInstalled(installationID) // ✅ POSITIVE CACHE
            h.orgCache.Set(fmt.Sprintf("org:%s", ids.AccountLogin), installationID)

            logger.Info().
                Int64("installation_id", installationID).
                Str("method", "account_lookup").
                Msg("Found installation via account lookup")
            return installationID, nil
        }
        attemptedMethods = append(attemptedMethods, "account_lookup")
    }

    // ALL METHODS FAILED - Installation genuinely doesn't exist
    // We can't mark negative in the main registry without an ID,
    // but we can cache negative results in mapping caches
    if ids.OwnerLogin != "" && ids.RepoName != "" {
        h.repoCache.SetNotFound(fmt.Sprintf("%s/%s", ids.OwnerLogin, ids.RepoName))
    }
    if ids.OwnerLogin != "" {
        h.orgCache.SetNotFound(fmt.Sprintf("org:%s", ids.OwnerLogin))
    }

    logger.Warn().
        Interface("identifiers", ids).
        Strs("attempted_methods", attemptedMethods).
        Msg("All installation lookup methods failed - app not installed")

    return 0, ErrNoInstallation
}

// verifyInstallationExists checks if an installation ID is valid
func (h *InstallationFilterHandler) verifyInstallationExists(ctx context.Context, installationID int64) bool {
    // Try to create a client for this installation
    // If successful, the installation exists
    _, err := h.clientCreator.NewInstallationClient(installationID)
    return err == nil
}

// lookupByRepository queries GitHub API for installation by repository
func (h *InstallationFilterHandler) lookupByRepository(
    ctx context.Context,
    owner, repo string,
) (int64, error) {
    installation, err := h.installationsService.GetByRepository(ctx, owner, repo)
    if err != nil {
        if IsInstallationNotFoundError(err) {
            // App not installed on this repository
            return 0, ErrNoInstallation
        }
        // Other errors (network, auth, etc.)
        return 0, err
    }

    return installation.ID, nil
}

// lookupByOrganization queries GitHub API for installation by organization
func (h *InstallationFilterHandler) lookupByOrganization(
    ctx context.Context,
    org string,
) (int64, error) {
    installation, err := h.installationsService.GetByOwner(ctx, org)
    if err != nil {
        if IsInstallationNotFoundError(err) {
            // App not installed for this organization
            return 0, ErrNoInstallation
        }
        // Other errors (network, auth, etc.)
        return 0, err
    }

    return installation.ID, nil
}
```

### Phase 3: Enhanced Caching Strategy with Negative Cache Support

**Goal**: Multi-level caching with proper positive/negative distinction

```go
// Enhanced InstallationFilterHandler with multiple smart caches
type InstallationFilterHandler struct {
    wrapped              githubapp.EventHandler
    registry             *InstallationRegistry      // Installation ID cache (positive/negative)
    repoCache           *MappingCache              // Repository → Installation ID mapping
    orgCache            *MappingCache              // Organization → Installation ID mapping
    installationsService githubapp.InstallationsService
    clientCreator       githubapp.ClientCreator    // For verifying installation IDs
    metrics             *InstallationFilterMetrics
    metricsRegistry     gometrics.Registry
}

// MappingCache handles both positive and negative cache entries for mappings
type MappingCache struct {
    mu          sync.RWMutex
    entries     map[string]mappingEntry
    positiveTTL time.Duration  // TTL for successful lookups (1 hour)
    negativeTTL time.Duration  // TTL for failed lookups (5 minutes)
}

type mappingEntry struct {
    installationID int64     // 0 means "not found"
    isNotFound     bool      // Explicitly tracks negative cache
    expiresAt      time.Time
}

func NewMappingCache(positiveTTL, negativeTTL time.Duration) *MappingCache {
    return &MappingCache{
        entries:     make(map[string]mappingEntry),
        positiveTTL: positiveTTL,
        negativeTTL: negativeTTL,
    }
}

// Get returns installation ID and whether it was found in cache
func (c *MappingCache) Get(key string) (installationID int64, found bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    entry, exists := c.entries[key]
    if !exists || time.Now().After(entry.expiresAt) {
        return 0, false
    }

    // Check if this is a negative cache entry
    if entry.isNotFound {
        return 0, true  // Found in cache, but installation doesn't exist
    }

    return entry.installationID, true
}

// Set caches a successful lookup (positive cache)
func (c *MappingCache) Set(key string, installationID int64) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.entries[key] = mappingEntry{
        installationID: installationID,
        isNotFound:     false,
        expiresAt:      time.Now().Add(c.positiveTTL),
    }
}

// SetNotFound caches a failed lookup (negative cache)
func (c *MappingCache) SetNotFound(key string) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.entries[key] = mappingEntry{
        installationID: 0,
        isNotFound:     true,
        expiresAt:      time.Now().Add(c.negativeTTL),
    }
}

// Remove invalidates a cache entry
func (c *MappingCache) Remove(key string) {
    c.mu.Lock()
    defer c.mu.Unlock()

    delete(c.entries, key)
}

// Clear removes all entries
func (c *MappingCache) Clear() {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.entries = make(map[string]mappingEntry)
}

// GetSize returns the current number of cached entries
func (c *MappingCache) GetSize() int {
    c.mu.RLock()
    defer c.mu.RUnlock()

    return len(c.entries)
}

// GetStats returns cache statistics
func (c *MappingCache) GetStats() (positive, negative, total int) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    total = len(c.entries)
    for _, entry := range c.entries {
        if entry.isNotFound {
            negative++
        } else {
            positive++
        }
    }
    return
}
```

### Phase 4: Metrics and Observability

**Goal**: Track lookup method effectiveness

```go
// New metrics for tracking lookup methods
const (
    MetricsKeyLookupDirect       = "installation.lookup.direct"
    MetricsKeyLookupRepository   = "installation.lookup.repository"
    MetricsKeyLookupOrganization = "installation.lookup.organization"
    MetricsKeyLookupAccount      = "installation.lookup.account"
    MetricsKeyLookupFailed       = "installation.lookup.failed"
    MetricsKeyCacheHitRepo       = "installation.cache.hit.repo"
    MetricsKeyCacheHitOrg        = "installation.cache.hit.org"
)
```

---

## Testing Strategy

### Unit Tests
1. `TestExtractIdentifiers_AllFields` - Verify all fields extracted
2. `TestExtractIdentifiers_RepositoryEvent` - Repository events
3. `TestExtractIdentifiers_OrganizationEvent` - Org-level events
4. `TestExtractIdentifiers_InstallationEvent` - Installation events
5. `TestLookupWithFallbacks_DirectID` - Direct ID works
6. `TestLookupWithFallbacks_RepositoryFallback` - Falls back to repo
7. `TestLookupWithFallbacks_OrganizationFallback` - Falls back to org
8. `TestLookupWithFallbacks_AllMethodsFail` - Graceful failure
9. `TestTTLCache_Expiration` - Cache TTL works
10. `TestConcurrentLookups` - Thread safety

### Integration Tests
1. Test with real `cof-sandbox` events
2. Test with real `cof-primary` events
3. Test with various GitHub event types
4. Load test with 200 events/sec

---

## Migration Plan

### Stage 1: Development (Day 1-2)
- Implement enhanced extraction
- Add multi-method lookup
- Create comprehensive tests

### Stage 2: Testing (Day 3-4)
- Test with production-like events
- Validate all lookup methods
- Performance testing

### Stage 3: Canary Deployment (Day 5)
- Deploy to handle 10% of `cof-sandbox` traffic
- Monitor metrics and logs
- Validate cache effectiveness

### Stage 4: Full Rollout (Day 6-7)
- Expand to 100% of `cof-sandbox`
- Add `cof-primary`
- Monitor for 24 hours

---

## How Smart Caching Solves Your Problem 🎯

### Scenario: Event from cof-sandbox with missing installation ID

```json
{
  "repository": {
    "owner": {"login": "cof-sandbox"},
    "name": "my-service"
  },
  "installation": {"id": 0}  // Invalid!
}
```

**OLD BEHAVIOR (Problem)**:
1. Extract installation ID → 0 (invalid)
2. Mark as negative cache ❌
3. Later events from same repo might be filtered incorrectly

**NEW BEHAVIOR (Solution)**:
1. Extract installation ID → 0 (invalid)
2. Try repository lookup → `GetByRepository("cof-sandbox", "my-service")`
3. **SUCCESS** → Installation ID = 87654321
4. **CRITICAL**: Mark 87654321 as **POSITIVE** in cache ✅
5. Cache mapping: `"cof-sandbox/my-service" → 87654321`
6. Cache mapping: `"org:cof-sandbox" → 87654321`
7. Next event: Fast cache hit, no API call

### Key Insight

The installation **EXISTS** (we found it via repo lookup), so it goes in the **positive cache**, even though direct extraction failed. This ensures:

- ✅ Installation 87654321 is marked as valid/existing
- ✅ Future events with ID 87654321 are processed correctly
- ✅ Future events from cof-sandbox/my-service use cached mapping
- ❌ Only genuinely non-existent installations get negative cache

### Cache State After Processing

```yaml
Installation Registry:
  87654321: InstallationExists (TTL: 1 hour)  # ✅ Positive

Repository Cache:
  "cof-sandbox/my-service": 87654321 (TTL: 1 hour)  # ✅ Mapping

Organization Cache:
  "org:cof-sandbox": 87654321 (TTL: 1 hour)  # ✅ Mapping
```

---

## Expected Outcomes

### Performance Metrics
- **Direct ID Hit Rate**: 70-80% (fast path, no API call)
- **Repository Cache Hit Rate**: 15-20% (after first lookup)
- **Organization Cache Hit Rate**: 5-10% (org-level events)
- **Overall API Call Reduction**: 85-90%
- **Lookup Success Rate**: >99.5%

### For Your GHEC Setup
- ✅ Handles events from `cof-sandbox` reliably
- ✅ Handles events from `cof-primary` reliably
- ✅ Minimizes GitHub API calls through multi-level caching
- ✅ Works with all GitHub event types
- ✅ Graceful fallback for edge cases

---

## Risk Analysis

### Risks
1. **API Rate Limits**: Mitigated by aggressive caching
2. **Cache Invalidation**: Use reasonable TTLs (1 hour)
3. **Multiple Installations per Org**: Rare in GHEC enterprise setup
4. **Performance Impact**: Minimal with caching

### Mitigation
- Circuit breaker for API calls
- Progressive rollout
- Comprehensive monitoring
- Feature flags for instant rollback

---

## Conclusion

This enhanced implementation provides:
1. **Maximum Reliability**: Multiple lookup methods with fallbacks
2. **Optimal Performance**: Multi-level caching minimizes API calls
3. **GHEC Optimized**: Handles `cof-sandbox` and `cof-primary` perfectly
4. **Future Proof**: Extensible for new lookup methods

The hybrid approach ensures Policy Bot can always find the correct installation, regardless of how the webhook event is structured, while maintaining excellent performance through intelligent caching.