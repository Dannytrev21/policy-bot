# Installation Architecture Simplification Plan

## Current State

**Problem**: Over-engineered installation handling with 3,000+ lines of complex code:
- Pre-handler filtering (InstallationFilterHandler)
- Installation-ID-based client caching (wrong for GHEC)
- Installation-ID-based rate limiting (should be per-org)
- Multiple layers: Registry, Locator, Manager, Filter

**For GHEC**: There is ONE installation per org. We should cache and rate limit by org, not installation ID.

---

## Simplification Goals

1. ✅ Remove InstallationFilterHandler (unnecessary pre-handler overhead)
2. ✅ Change ClientCache to key by owner/org (correct for GHEC)
3. ✅ Change RateLimiter to key by owner/org (per-org rate limiting)
4. ✅ Simplify client retrieval (direct org-based lookup)
5. ✅ Remove InstallationRegistry, InstallationLocator, InstallationManager
6. ✅ Remove mapping caches (repo→installation, org→installation)

---

## Step-by-Step Implementation Plan

### Step 1: Remove InstallationFilterHandler from server.go
**File**: `server/server.go`
**Action**: Remove the filter wrapping logic (lines 342-365)
**Change**:
```go
// BEFORE:
handlers = append(handlers, handler.NewInstallationFilterHandler(
    h,
    baseHandler.InstallationRegistry,
    baseHandler.Installations,
    registry,
    baseHandler.RepoMappingCache,
    baseHandler.OrgMappingCache,
    baseHandler.InstallationLocator,
    filterConfig,
    baseHandler.AppID,
    baseHandler.DefaultInstallationID,
    baseHandler.GithubCloud,
))

// AFTER:
handlers = append(handlers, h)
```
**Impact**: Events go directly to handlers without filtering

---

### Step 2: Change ClientCache to key by owner/org
**File**: `server/handler/client_cache.go`
**Changes**:
1. Change cache key from `int64` (installation ID) to `string` (owner)
2. Update `Get()` signature: `Get(owner string) *InstallationClients`
3. Update `Put()` signature: `Put(owner string, clients *InstallationClients)`
4. Update `Invalidate()` signature: `Invalidate(owner string)`

**Example**:
```go
// BEFORE:
func (c *ClientCache) Get(installationID int64) *InstallationClients

// AFTER:
func (c *ClientCache) Get(owner string) *InstallationClients
```

**Benefits**:
- ✅ Correct caching for GHEC (one installation per org)
- ✅ No need to look up installation ID first
- ✅ Direct cache access by owner

---

### Step 3: Update RateLimiter to use owner/org
**File**: `server/handler/rate_limiter.go`
**Changes**:
1. Change `installationLimiters` from `map[int64]*rate.Limiter` to `map[string]*rate.Limiter`
2. Update `NewInstallationClient()` to accept owner string
3. Update `getOrCreateInstallationLimiter()` to use owner string
4. Rename methods for clarity (installation → org)

**Example**:
```go
// BEFORE:
func (r *RateLimitedClientCreator) NewInstallationClient(installationID int64) (*github.Client, error)

// AFTER:
func (r *RateLimitedClientCreator) NewOrgClient(owner string, installationID int64) (*github.Client, error)
```

**Benefits**:
- ✅ Per-org rate limiting (correct for GHEC)
- ✅ Prevents one org from exhausting rate limit
- ✅ Simpler to reason about

---

### Step 4: Simplify Base.GetInstallationClient
**File**: `server/handler/base.go`
**Changes**:
1. Add new method: `GetClientByOwner(ctx, owner string) (*InstallationClients, error)`
2. Simplify logic:
   - Check cache first (by owner)
   - If miss, create client for the owner's installation
   - Cache by owner
3. Remove complex installation ID lookup logic

**Example**:
```go
func (b *Base) GetClientByOwner(ctx context.Context, owner string) (*InstallationClients, error) {
    // Check cache first
    if clients := b.clientCache.Get(owner); clients != nil {
        return clients, nil
    }

    // Look up installation ID for this owner using GitHub API
    installation, err := b.Installations.GetByOwner(ctx, owner)
    if err != nil {
        return nil, err
    }

    // Create clients
    clients, err := b.createInstallationClients(ctx, installation.ID)
    if err != nil {
        return nil, err
    }

    // Cache by owner (not installation ID)
    b.clientCache.Put(owner, clients)

    return clients, nil
}
```

**Benefits**:
- ✅ Direct owner→clients mapping
- ✅ No multi-layer lookup complexity
- ✅ Simple and maintainable

---

### Step 5: Remove InstallationRegistry, InstallationLocator, InstallationManager
**Files to delete**:
- `server/handler/installation_registry.go` (660 lines)
- `server/handler/installation_locator.go` (670 lines)
- `server/handler/installation_manager.go` (655 lines)
- `server/handler/installation_registry_test.go`
- `server/handler/installation_locator_test.go`
- `server/handler/installation_manager_test.go`

**Changes to Base struct**:
Remove fields:
- `InstallationRegistry`
- `InstallationLocator`
- `InstallationManager`
- `CircuitBreaker`
- `RepoMappingCache`
- `OrgMappingCache`

**Benefits**:
- ✅ Removes 2,000+ lines of unnecessary code
- ✅ Simpler mental model
- ✅ Easier to maintain

---

### Step 6: Remove installation filter files
**Files to delete**:
- `server/handler/installation_filter.go` (1,067 lines)
- `server/handler/installation_filter_test.go`
- `server/handler/installation_filter_step5_test.go`
- `server/handler/installation_benchmarks_test.go`
- `server/handler/installation_integration_test.go`

**Benefits**:
- ✅ Removes ~2,000+ lines of filter code and tests
- ✅ Events go directly to handlers

---

### Step 7: Update all handlers
**Files**: Various handler files
**Changes**:
1. Handlers should call `base.GetClientByOwner(ctx, owner)` instead of complex installation lookup
2. Extract owner from payload/context
3. No changes to business logic, only client retrieval

**Example**:
```go
// BEFORE (complex):
ids, err := extractIdentifiers(ctx, payload)
installationID, err := base.ResolveInstallation(ctx, ids)
clients, err := base.GetInstallationClients(ctx, installationID)

// AFTER (simple):
owner := getOwnerFromPayload(payload)
clients, err := base.GetClientByOwner(ctx, owner)
```

---

### Step 8: Run tests and fix breakages
**Action**:
1. Run all handler tests
2. Fix any broken tests that relied on filter logic
3. Update test mocks to use owner-based caching
4. Verify integration tests pass

---

### Step 9: Final cleanup
**Remove unused code**:
- Mapping cache methods from Base
- Extract

Identifiers (no longer needed for filtering)
- Multi-app detection code (AppID, DefaultInstallationID fields)
- Complex metrics for installation tracking

**Benefits**:
- ✅ Codebase reduced by ~3,000 lines
- ✅ Simpler, more maintainable architecture
- ✅ Correct caching and rate limiting for GHEC

---

## Expected Outcome

**Before**:
- 3,000+ lines of installation infrastructure
- Pre-handler filtering
- Installation-ID-based caching (wrong)
- Installation-ID-based rate limiting (wrong)
- Complex multi-layer lookups

**After**:
- Direct owner→clients mapping
- Per-org rate limiting (correct)
- Owner-based client caching (correct)
- ~3,000 lines removed
- Simpler, clearer code

**Key Insight**: For GHEC, there's ONE installation per org. We should cache and rate limit by org, not installation ID. This dramatically simplifies everything.
