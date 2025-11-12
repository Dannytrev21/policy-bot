# Installation Registry & Lookup Enhancement Plan

## Plan Status Overview

- [ ] **Step 1**: Extend InstallationRegistry with InstallationRecord
- [ ] **Step 2**: Create InstallationLocator Service
- [ ] **Step 3**: Update Filtering Logic for Webhook vs SQS
- [ ] **Step 4**: Implement Event-Type Gating (check_run exclusion)
- [ ] **Step 5**: Integration & Testing

**Current Progress**: Planning Phase
**Estimated Effort**: 3-4 days
**Priority**: Critical

---

## Context

The Policy Bot needs an enhanced installation registry that can:
- Track installations by multiple keys (ID, owner, repo)
- Support alternate lookup methods when installation ID is missing
- Filter events appropriately for webhook vs SQS sources
- Prevent cache poisoning from certain event types (e.g., check_run)
- Handle 200 events/sec with minimal lock contention

### Existing Implementation
- **Phase 1-4 Complete**: ExtractedIdentifiers, MappingCache, smart lookup, metrics
- **Performance Optimized**: Atomic counters, sync.Pool, cache key helpers
- **Current Gap**: Registry only tracks installation ID, not full metadata

---

## Constraints & Requirements

### Technical Constraints
- Must maintain backward compatibility with existing code
- Thread-safe for 200 events/sec burst traffic
- Minimize allocations in hot paths
- Reuse existing components where possible (KISS principle)
- Work with both GHEC and GHES environments

### Functional Requirements
- Webhook events: Filter only by installation ID (pass through if 0)
- SQS events: Filter by installation ID, use smart lookup if 0
- Cache management: Certain events (check_run) must not mutate caches
- Multiple lookup strategies: Direct ID → Repo → Org → API fallback

---

## Solution Design (Tree of Thought Analysis)

### Evaluated Approaches

| Hypothesis | Approach | KISS Score | Decision |
|------------|----------|------------|----------|
| H1: Enhanced InstallationRecord | Extend registry with metadata | 6/10 | ❌ Complex refactoring |
| H2: Separate Locator Service | New orchestration service | 7/10 | ❌ Another abstraction |
| H3: Extend Current Implementation | Minimal changes to existing | 9/10 | ❌ Limited flexibility |
| H4: External Store (Redis/DB) | Centralized persistence | 3/10 | ❌ Violates KISS |
| **H5: Hybrid (Registry + Locator)** | **Enhanced registry + facade** | **8/10** | **✅ SELECTED** |

### Selected Solution: Hybrid Approach

Combines the best aspects:
- **InstallationRecord**: Rich metadata in registry
- **InstallationLocator**: Clean facade for lookups
- **Backward Compatible**: Existing code continues to work
- **SOLID Principles**: Single responsibility, clean interfaces

---

## Detailed Implementation Steps

### Step 1: Extend InstallationRegistry with InstallationRecord

**Location**: `server/handler/installation_registry.go`

**Information**:
- Current registry only tracks installation ID → status mapping
- Need to store owner, repo, and other metadata per installation
- Must maintain thread safety with atomic operations

**Implementation**:

```go
// InstallationRecord holds comprehensive installation metadata
type InstallationRecord struct {
    // Core identification
    InstallationID int64
    Status        InstallationStatus

    // Owner/Organization info
    OwnerLogin    string
    OwnerID       int64
    OwnerType     string // "User" or "Organization"

    // Repository associations (if installation-specific)
    Repositories  []RepositoryInfo // Can be empty for org-wide installations

    // Metadata
    CreatedAt     time.Time
    UpdatedAt     time.Time
    ExpiresAt     time.Time

    // Flags
    IsOrgWide     bool // True if installed on all org repos
}

type RepositoryInfo struct {
    Name     string
    ID       int64
    FullName string // owner/repo format
}

// Enhanced InstallationRegistry
type InstallationRegistry struct {
    mu sync.RWMutex

    // Primary index: Installation ID → Record
    records map[int64]*InstallationRecord

    // Secondary indexes for fast lookup
    ownerIndex map[string][]int64  // owner login → installation IDs
    repoIndex  map[string]int64    // "owner/repo" → installation ID

    // TTLs
    positiveTTL time.Duration
    negativeTTL time.Duration

    // Metrics (atomic for lock-free updates)
    cacheHits   atomic.Int64
    cacheMisses atomic.Int64
    apiCalls    atomic.Int64

    metricsRegistry gometrics.Registry
}

// New methods to add
func (r *InstallationRegistry) UpsertRecord(record *InstallationRecord)
func (r *InstallationRegistry) LookupByOwner(owner string) ([]int64, bool)
func (r *InstallationRegistry) LookupByRepo(owner, repo string) (int64, bool)
func (r *InstallationRegistry) GetRecord(installationID int64) (*InstallationRecord, bool)
```

**Testing Plan**:
- Unit test concurrent UpsertRecord operations
- Test secondary index consistency
- Verify TTL enforcement per record
- Benchmark lookup performance

**Acceptance Criteria**:
- ✅ Registry supports multi-key lookups without API calls
- ✅ Secondary indexes stay synchronized with primary
- ✅ Thread-safe under 200 events/sec load
- ✅ Backward compatible with existing Check() method

---

### Step 2: Create InstallationLocator Service

**Location**: `server/handler/installation_locator.go` (new file)

**Information**:
- Orchestrates lookups across registry, mapping caches, and API
- Configurable strategies for webhook vs SQS
- Centralized location for lookup logic

**Implementation**:

```go
// InstallationLocator provides a unified interface for installation lookups
type InstallationLocator struct {
    registry             *InstallationRegistry
    repoCache           *MappingCache
    orgCache            *MappingCache
    installationsService githubapp.InstallationsService
    logger              zerolog.Logger
    metricsRegistry     gometrics.Registry
}

// LookupStrategy defines how to search for installations
type LookupStrategy int

const (
    StrategyDirectOnly LookupStrategy = iota // Webhooks: ID only
    StrategySmartLookup                      // SQS: Multi-method
)

// LookupOptions configures the lookup behavior
type LookupOptions struct {
    Strategy        LookupStrategy
    AllowAPIFallback bool
    RecordMetrics    bool
}

// LookupResult contains the installation ID and how it was found
type LookupResult struct {
    InstallationID int64
    Method         string // "direct", "repo_cache", "org_cache", "repo_api", "org_api"
    Cached         bool
}

// Core lookup method
func (l *InstallationLocator) Locate(
    ctx context.Context,
    ids *ExtractedIdentifiers,
    opts LookupOptions,
) (*LookupResult, error) {
    // Implementation follows existing lookupInstallationWithSmartCache logic
    // but with cleaner structure and configurable strategy

    if opts.Strategy == StrategyDirectOnly {
        // Webhook path: only use direct ID
        if ids.InstallationID > 0 {
            if record, found := l.registry.GetRecord(ids.InstallationID); found {
                return &LookupResult{
                    InstallationID: ids.InstallationID,
                    Method: "direct",
                    Cached: true,
                }, nil
            }
        }
        return nil, ErrNoInstallation
    }

    // SQS path: try all methods
    // 1. Direct ID
    // 2. Registry secondary indexes
    // 3. Mapping caches
    // 4. API fallbacks
}

// Helper to determine if event should mutate cache
func (l *InstallationLocator) ShouldCacheEvent(eventType string) bool {
    // Events that shouldn't affect cache
    noCacheEvents := map[string]bool{
        "check_run": true,
        "check_suite": true,  // Often missing context
        "workflow_run": true, // GitHub Actions events
    }
    return !noCacheEvents[eventType]
}
```

**Testing Plan**:
- Test webhook vs SQS strategy selection
- Verify cache mutation gating for check_run
- Test all lookup methods in priority order
- Mock API failures and verify fallback behavior

**Acceptance Criteria**:
- ✅ Single entry point for all installation lookups
- ✅ Configurable strategies work correctly
- ✅ Metrics recorded for each lookup method
- ✅ check_run events don't pollute cache

---

### Step 3: Update Filtering Logic for Webhook vs SQS

**Location**: `server/handler/installation_filter.go`

**Information**:
- Current implementation already detects SQS via context
- Need to ensure webhook events only filter by installation ID
- SQS events should use full smart lookup

**Implementation**:

```go
func (h *InstallationFilterHandler) Handle(
    ctx context.Context,
    eventType, deliveryID string,
    payload []byte,
) error {
    logger := zerolog.Ctx(ctx)

    // Extract identifiers
    ids, err := extractIdentifiers(payload)
    if err != nil {
        logger.Debug().Err(err).Msg("Failed to extract identifiers")
        // Pass through on extraction failure
        return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
    }
    defer putIdentifiers(ids) // Return to pool

    // Determine lookup strategy based on event source
    opts := LookupOptions{
        Strategy: StrategyDirectOnly,    // Default for webhooks
        AllowAPIFallback: false,
        RecordMetrics: false,
    }

    // Check if SQS event
    if eventSource, ok := ctx.Value(SQSEventSourceKey).(string); ok && eventSource == "sqs" {
        opts.Strategy = StrategySmartLookup
        opts.AllowAPIFallback = true
        opts.RecordMetrics = true
    }

    // Check if event type should affect cache
    if !h.locator.ShouldCacheEvent(eventType) {
        logger.Debug().
            Str("event_type", eventType).
            Msg("Event type excluded from cache mutations")
        opts.AllowAPIFallback = false // Still lookup but don't cache results
    }

    // Perform lookup
    result, err := h.locator.Locate(ctx, ids, opts)
    if err != nil {
        if errors.Is(err, ErrInstallationNotInstalled) {
            // Definitive negative - filter the event
            logger.Info().
                Str("event_type", eventType).
                Msg("Event filtered - installation not found")
            h.recordFilteredEvent()
            return nil
        }

        // Unknown error or no installation ID - pass through
        logger.Debug().
            Err(err).
            Msg("Could not determine installation, passing to handler")
        h.recordPassedEvent()
        return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
    }

    // Installation found - pass to handler
    logger.Debug().
        Int64("installation_id", result.InstallationID).
        Str("lookup_method", result.Method).
        Bool("cached", result.Cached).
        Msg("Installation found, passing to handler")

    h.recordPassedEvent()
    return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
}
```

**Testing Plan**:
- Test webhook events with installation ID = 0 (should pass through)
- Test SQS events with installation ID = 0 (should use smart lookup)
- Test check_run events don't update cache
- Verify metrics only recorded for SQS events

**Acceptance Criteria**:
- ✅ Webhook filtering only on direct installation ID
- ✅ SQS uses full smart lookup when ID missing
- ✅ Events pass through when installation ID = 0 (webhooks)
- ✅ check_run and similar events don't mutate cache

---

### Step 4: Implement Event-Type Gating

**Location**: `server/handler/installation_locator.go`

**Information**:
- Certain GitHub events lack full context and shouldn't affect cache
- Need configurable list of excluded event types
- Should still process events, just not cache results

**Implementation**:

```go
// Configuration for event-specific behavior
type EventConfig struct {
    // Events that should not mutate cache
    NoCacheMutation []string `yaml:"no_cache_mutation"`

    // Events that should always pass through (no filtering)
    AlwaysPassThrough []string `yaml:"always_pass_through"`
}

// Default configuration
var DefaultEventConfig = EventConfig{
    NoCacheMutation: []string{
        "check_run",      // Often missing repository context
        "check_suite",    // Similar to check_run
        "workflow_run",   // GitHub Actions events
        "workflow_job",   // GitHub Actions job events
        "deployment_status", // Deployment events
    },
    AlwaysPassThrough: []string{
        "installation",             // Installation lifecycle events
        "installation_repositories", // Repo add/remove events
    },
}

// Enhanced ShouldCacheEvent with configuration
func (l *InstallationLocator) ShouldCacheEvent(eventType string, config *EventConfig) bool {
    if config == nil {
        config = &DefaultEventConfig
    }

    for _, noCacheType := range config.NoCacheMutation {
        if eventType == noCacheType {
            l.logger.Debug().
                Str("event_type", eventType).
                Msg("Event type excluded from cache mutations")
            return false
        }
    }
    return true
}

// Check if event should always pass through
func (l *InstallationLocator) ShouldAlwaysPassThrough(eventType string, config *EventConfig) bool {
    if config == nil {
        config = &DefaultEventConfig
    }

    for _, passType := range config.AlwaysPassThrough {
        if eventType == passType {
            return true
        }
    }
    return false
}
```

**Testing Plan**:
- Test check_run events don't update any cache
- Test installation events always pass through
- Test configuration override works
- Verify default configuration is sensible

**Acceptance Criteria**:
- ✅ Configurable list of no-cache events
- ✅ check_run and similar events don't pollute cache
- ✅ Installation events always processed
- ✅ Configuration can be overridden via YAML

---

### Step 5: Integration & Testing

**Location**: Various test files and integration points

**Information**:
- Need comprehensive testing of all components
- Integration with existing Base and server wiring
- Performance validation under load

**Implementation Tasks**:

1. **Update Base initialization** (`server/handler/base.go`):
```go
type Base struct {
    // Existing fields...

    // Enhanced registry and locator
    installationRegistry *InstallationRegistry
    installationLocator  *InstallationLocator

    // Event configuration
    eventConfig *EventConfig
}

func (b *Base) Initialize() {
    // Create enhanced registry
    b.installationRegistry = NewInstallationRegistry(
        1*time.Hour,  // positive TTL
        5*time.Minute, // negative TTL
        b.metricsRegistry,
    )

    // Create locator
    b.installationLocator = NewInstallationLocator(
        b.installationRegistry,
        b.RepoMappingCache,
        b.OrgMappingCache,
        b.installations,
        b.logger,
        b.metricsRegistry,
    )
}
```

2. **Update server wiring** (`server/server.go`):
```go
// Pass locator to filter handlers
filterHandler := handler.NewInstallationFilterHandler(
    baseHandler,
    base.GetInstallationRegistry(),
    base.GetInstallationLocator(), // New parameter
    installations,
    metricsRegistry,
    base.GetRepoMappingCache(),
    base.GetOrgMappingCache(),
)
```

3. **Comprehensive test suite**:
   - Unit tests for InstallationRecord operations
   - Unit tests for InstallationLocator strategies
   - Integration tests for end-to-end flow
   - Load tests at 200 events/sec
   - Race condition tests

**Testing Plan**:
```bash
# Unit tests
go test ./server/handler -run TestInstallationRecord -v
go test ./server/handler -run TestInstallationLocator -v
go test ./server/handler -run TestEventGating -v

# Integration tests
go test ./server/handler -run TestFilterIntegration -v

# Race detection
go test ./server/handler -race -run TestConcurrent

# Benchmarks
go test ./server/handler -bench=BenchmarkLocator -benchmem

# Load test (custom)
go test ./server/handler -run TestLoad200EventsPerSec -v
```

**Acceptance Criteria**:
- ✅ All unit tests passing
- ✅ Integration tests verify end-to-end flow
- ✅ No race conditions detected
- ✅ Handles 200 events/sec sustained load
- ✅ Memory usage remains bounded
- ✅ P99 latency < 5ms for cache lookups

---

## References

### Key Files to Review
- `server/handler/installation_registry.go` - Current registry implementation
- `server/handler/installation_filter.go` - Current filter and smart lookup
- `server/handler/installation_filter_test.go` - Existing test patterns
- `server/handler/base.go` - Base handler with shared resources
- `server/server.go` - Server wiring and initialization
- `.claude/todo/installation_redo.md` - Original requirements and phases
- `.claude/todo/installation_redo_codex_plan.md` - Previous plan iteration

### GitHub API Documentation
- [GitHub Apps - Installations API](https://docs.github.com/en/rest/apps/installations)
- [GitHub Webhooks - Event Types](https://docs.github.com/en/developers/webhooks-and-events/webhooks/webhook-events-and-payloads)

---

## Things to Keep in Mind

### Performance Considerations
- **Hot Path Optimization**: Installation lookup is called for every event
- **Lock Contention**: Use RWMutex for reads, atomics for counters
- **Allocation Reduction**: Reuse objects via sync.Pool where appropriate
- **Cache Key Generation**: Use pre-allocated byte slices, not string concatenation

### Architectural Principles
- **KISS**: Prefer simple solutions that work over complex optimizations
- **SOLID**: Single responsibility - Locator orchestrates, Registry stores, Filter decides
- **Backward Compatibility**: Existing code should continue to work
- **Fail Open**: When in doubt, pass events through rather than dropping

### Operational Concerns
- **Metrics**: Track lookup methods to understand cache effectiveness
- **Logging**: Clear, actionable logs for debugging
- **Circuit Breaking**: Consider adding circuit breaker for GitHub API calls
- **Rate Limiting**: Respect GitHub API rate limits (already implemented)

### Edge Cases
- **Multiple Installations per Org**: Rare but possible in GHEC
- **Repository Transfers**: Installation might change when repo transferred
- **Partial Installations**: App might be installed on subset of org repos
- **Webhook Replay**: Same event might be processed multiple times

---

## Migration & Rollout Strategy

### Phase 1: Development (2 days)
- Implement InstallationRecord and enhanced registry
- Create InstallationLocator with strategies
- Update filtering logic
- Comprehensive unit tests

### Phase 2: Testing (1 day)
- Integration testing with production-like events
- Load testing at 200 events/sec
- Race condition detection
- Memory profiling

### Phase 3: Staged Rollout (1 day)
- Deploy to dev environment
- Monitor metrics and logs
- Enable for 10% of traffic (canary)
- Gradual rollout to 100%

### Rollback Plan
- Feature flag to disable enhanced lookup
- Fallback to existing implementation
- All changes backward compatible

---

## Success Metrics

### Technical Metrics
- **Cache Hit Rate**: > 85% for known installations
- **API Call Reduction**: > 70% compared to baseline
- **P99 Latency**: < 5ms for cache lookups
- **Memory Usage**: < 100MB for registry structures
- **Zero Dropped Events**: No false negatives

### Business Metrics
- **Reduced 404 Errors**: Near zero for legitimate installations
- **Improved Throughput**: Handle 200 events/sec sustained
- **Operational Confidence**: Clear metrics and debugging tools

---

## Risk Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| Cache inconsistency | Medium | TTL-based expiration, lifecycle hooks |
| Memory growth | Low | Bounded cache size, regular cleanup |
| API rate limits | High | Circuit breaker, exponential backoff |
| Complex debugging | Medium | Comprehensive logging and metrics |
| Performance regression | High | Load testing, gradual rollout |

---

## Next Review Checkpoint

After completing Step 1 (InstallationRecord), review:
- Data model supports all lookup scenarios
- Secondary indexes are efficient
- Thread safety is maintained
- Backward compatibility preserved

This checkpoint ensures the foundation is solid before building the locator service.