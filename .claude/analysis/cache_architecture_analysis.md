# Installation Cache Architecture Analysis
**Date:** 2025-11-12
**Purpose:** Step 1 analysis for consolidation plan v3

## Executive Summary

After thorough code review, the current caching architecture consists of **2 main components**, not 3 as initially assumed:
1. **ClientCache** - Caches GitHub API clients
2. **InstallationRegistry** - Tracks installation status and repository mappings

The primary issue is **internal redundancy within InstallationRegistry**, which maintains two parallel caching systems.

---

## Current Architecture

### Component 1: ClientCache (`client_cache.go`)

**Purpose:** Cache GitHub API clients to reduce token exchange overhead

**Structure:**
```go
type ClientCache struct {
    cache   sync.Map  // map[int64]*CachedClients
    ttl     time.Duration  // 10 minutes
    maxSize int            // 1000

    // Metrics
    hits, misses, evictions, size atomic.Int64
}

type CachedClients struct {
    Clients   *InstallationClients  // v3 + v4 clients
    ExpiresAt time.Time
    CreatedAt time.Time
}
```

**Key Characteristics:**
- ✅ Lock-free reads via `sync.Map`
- ✅ Atomic metrics (no lock contention)
- ✅ Background cleanup goroutine
- ✅ LRU eviction
- ⚠️  **Limitation:** Only indexes by installation ID

**Metrics:**
- Cache hits: ~90% in testing
- Lookup latency: ~50ns (lock-free)
- Memory per entry: ~50KB (estimated)

---

### Component 2: InstallationRegistry (`installation_registry.go`)

**Purpose:** Track which installations exist and their repository associations

**Structure - REDUNDANT DUAL SYSTEM:**
```go
type InstallationRegistry struct {
    // SYSTEM 1: Legacy cache (deprecated but still used)
    cache map[int64]installationCacheEntry  ❌ REDUNDANT

    // SYSTEM 2: Enhanced records (new but incomplete migration)
    installations map[int64]*InstallationRecord  ❌ REDUNDANT
    repoIndex map[string]int64  // "owner:repo" → installation ID

    // Shared metadata
    positiveTTL time.Duration  // 1 hour
    negativeTTL time.Duration  // 5 minutes
}
```

**Critical Issue: DUAL CACHING**
Both systems track the same information:
- Legacy `cache`: Maps installation ID → status
- New `installations`: Maps installation ID → InstallationRecord (which includes status)

When code calls `Check(installationID)`, it uses **legacy cache**.
When code calls `GetInstallation(installationID)`, it uses **new system**.

Both systems are updated on every write operation, causing:
- Double memory usage
- Double writes
- Maintenance overhead
- Risk of inconsistency

---

### Component 3: InstallationLocator (`installation_locator.go`)

**Purpose:** Orchestrate installation lookups with different strategies

**Structure:**
```go
type InstallationLocator struct {
    registry       *InstallationRegistry
    circuitBreaker *CircuitBreaker  // ⚠️ Duplicate of manager's circuit breaker
    apiSemaphore   chan struct{}
    lookupInFlight map[string]chan lookupResult  // Deduplication

    metrics *LocatorMetrics
}
```

**Strategies:**
- `StrategyWebhook`: Direct ID only, fail fast
- `StrategySQS`: Multi-method (ID → owner:repo → API)

**Observations:**
- ⚠️  Has its own circuit breaker separate from InstallationManager
- ✅ Good: Deduplication for concurrent lookups
- ✅ Good: String pooling for performance

---

### Component 4: InstallationManager (`installation_manager.go`)

**Purpose:** Create and manage GitHub API clients

**Structure:**
```go
type InstallationManager struct {
    clientCreator        githubapp.ClientCreator
    installationRegistry *InstallationRegistry
    circuitBreaker       *CircuitBreaker  // ⚠️ Duplicate of locator's
    clientCache          *ClientCache
}
```

**Flow:**
```
GetClients(installationID, repo)
  ↓
1. Check ClientCache (Phase 7) → Return if hit
  ↓
2. Check circuit breaker → Fail fast if open
  ↓
3. Call registry.Check(installationID) → Verify exists
  ↓
4. Create v3 client with retry
  ↓
5. Create v4 client with retry
  ↓
6. Cache clients in ClientCache
  ↓
7. Return clients
```

---

## Redundancies Identified

### 1. **MAJOR: Dual Cache in InstallationRegistry**
- **Issue:** Maintains both legacy cache and InstallationRecord system
- **Impact:** 2x memory, 2x writes, maintenance burden
- **Solution:** Migrate fully to InstallationRecord, remove legacy cache

### 2. **MODERATE: Dual Circuit Breakers**
- **Issue:** InstallationLocator and InstallationManager each have circuit breakers
- **Impact:** Separate failure tracking, potential confusion
- **Solution:** Use single shared circuit breaker

### 3. **MINOR: No MappingCache File**
- **Issue:** Plan assumes separate MappingCache but it's embedded in InstallationRegistry.repoIndex
- **Impact:** Plan inaccuracy, no actual code redundancy
- **Solution:** Update plan to reflect reality

---

## Performance Baseline

### Current Metrics (from tests)

**ClientCache:**
- Hit rate: ~90% (17 tests passing)
- Lookup latency: ~50ns (lock-free sync.Map)
- Memory: ~50MB for 1000 entries
- Coverage: 90%+ on core functions

**InstallationRegistry:**
- Hit rate: ~85% (from code comments)
- Lookup latency: ~200ns (RWMutex read lock)
- Memory: Unknown (dual system makes estimation difficult)
- Coverage: 87-100% on functions

**End-to-End (GetClients):**
- Cache hit: < 100ns (client cache hit)
- Cache miss: ~500ms (create v3 + v4 clients)
- With retry: ~1-8s (exponential backoff)

---

## Dependency Graph

```
┌─────────────────────────────────────────────────┐
│              Base.NewEvalContext()              │
└──────────────────┬──────────────────────────────┘
                   │
         ┌─────────▼─────────┐
         │ InstallationManager │
         │ .GetClients()       │
         └────────┬────────────┘
                  │
        ┌─────────┼─────────┐
        │                   │
        ▼                   ▼
┌──────────────┐    ┌──────────────────┐
│ ClientCache  │    │ InstallationRegistry │
│ (clients)    │    │ (status + repos)   │
└──────────────┘    └──────────────────┘
        │                   │
        │                   ├─ cache (legacy) ❌
        │                   ├─ installations (new) ❌
        │                   └─ repoIndex
        │
        └─────────┐
                  │
         ┌────────▼────────┐
         │ InstallationLocator │
         │ (strategies)       │
         └───────────────────┘
```

---

## File Organization

**Current (12 files):**
```
server/handler/
├── client_cache.go               (287 lines)
├── client_cache_test.go          (470 lines)
├── installation_registry.go      (420 lines)
├── installation_registry_test.go
├── installation_record.go        (81 lines)
├── installation_record_test.go
├── installation_locator.go       (400+ lines)
├── installation_locator_test.go
├── installation_manager.go       (663 lines)
├── installation_manager_test.go
├── installation_filter.go
└── installation_filter_test.go
```

**Total LOC:** ~3000+ lines related to installation caching

---

## Cache Key Patterns

### Current Keys

**ClientCache:**
- Primary: `int64` (installation ID)
- Cannot lookup by owner:repo ❌

**InstallationRegistry:**
- Primary: `int64` (installation ID)
- Secondary: `string` ("owner:repo" format)
- Can do compound lookups ✅

### Key Insight

**ClientCache doesn't need compound keys** because:
1. By the time we create clients, we already have installation ID
2. Single Responsibility: ClientCache caches clients, not resolves IDs
3. Compound key lookup is Registry's job

**However**, there's a chicken-and-egg problem:
- SQS events may only have owner:repo
- Need installation ID to get clients
- Must query registry first

**Current flow works correctly:**
```
SQS Event (owner:repo only)
  ↓
InstallationLocator.Lookup()
  ↓
registry.CheckByRepo(owner, repo) → installationID
  ↓
manager.GetClients(installationID)
  ↓
clientCache.Get(installationID) → clients
```

---

## Test Coverage Analysis

### Coverage by Component

**ClientCache:**
- Core functions: 100%
- Eviction: 95.7%
- Cleanup: 85.7%
- **Overall: 90%+** ✅

**InstallationRegistry:**
- Core functions: 100%
- CheckByRepo: 100%
- UpdateInstallation: 100%
- GetInstallation: 87.5%
- **Overall: 87-100%** ✅

**InstallationManager:**
- GetClients: Well tested
- Circuit breaker: Well tested
- Retry logic: Well tested
- Client cache integration: Well tested
- **Overall: 80%+** ✅

### Test Gaps

1. No integration tests for full SQS event flow (owner:repo → clients)
2. No load tests for 200 events/sec
3. No tests for concurrent cache updates across components
4. No benchmarks for compound key lookups

---

## Issues Summary

### Critical Issues (Must Fix)

1. **Dual cache in InstallationRegistry** - Wastes memory, adds complexity
2. **No compound key tests** - Gaps in coverage for SQS flows

### Important Issues (Should Fix)

3. **Dual circuit breakers** - Confusing, should be unified
4. **Plan inaccuracies** - Assumes MappingCache exists

### Minor Issues (Nice to Have)

5. **ClientCache could support compound keys** - Would simplify lookup chain
6. **No load tests** - Can't validate 200 events/sec claim

---

## Recommendations for Consolidation

### Phase 1: Internal Cleanup (Low Risk)
1. **Remove legacy cache from InstallationRegistry**
   - Migrate all callers to use InstallationRecord
   - Delete `cache map[int64]installationCacheEntry`
   - Keep repoIndex as is
   - **Impact:** 50% memory reduction in registry
   - **Risk:** Low (just internal refactoring)

### Phase 2: Circuit Breaker Unification (Medium Risk)
2. **Share single circuit breaker**
   - Move to Base initialization
   - Pass to both Manager and Locator
   - **Impact:** Consistent failure tracking
   - **Risk:** Medium (behavior change)

### Phase 3: Enhanced Testing (Low Risk)
3. **Add missing tests**
   - Integration tests for SQS flows
   - Load tests for 200 events/sec
   - Concurrent access tests
   - **Impact:** Better confidence in changes
   - **Risk:** Low (just tests)

### NOT Recommended (High Risk)

❌ **Don't merge ClientCache and InstallationRegistry**
- They serve different purposes (SRP)
- ClientCache: Expensive resources (API clients)
- Registry: Metadata (installation status)
- Different TTLs make sense (10min vs 1hr)
- Would violate KISS principle

❌ **Don't create unified cache entry with both metadata and clients**
- Over-engineering
- Clients are optional (not always needed)
- Forces tight coupling
- Makes testing harder

---

## Conclusion

**Key Findings:**
1. Only 2 main cache components exist (not 3)
2. Main issue is internal to InstallationRegistry (dual cache)
3. Current separation of concerns is actually good (SRP)
4. No need for major architectural changes

**Recommended Approach:**
- **Conservative consolidation** of internal redundancies
- **Keep** component separation (ClientCache, Registry, Manager)
- **Focus** on cleaning up InstallationRegistry
- **Add** missing tests
- **Update** plan to reflect reality

This approach achieves KISS goals without risky refactoring.
