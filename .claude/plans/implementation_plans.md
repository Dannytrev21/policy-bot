# Policy Bot Implementation Plans

This document contains detailed step-by-step implementation plans for each solution, ordered by priority. Each plan is designed to be executed by an AI agent.

---

# Master Checklist

## P0 - High Priority (Must Do)
- [ ] **Solution 1**: Per-Installation Circuit Breakers (Critique #3)
- [ ] **Solution 6**: Concurrent Request Limiting (Critique #13)

## P1 - Medium-High Priority (Should Do Soon)
- [ ] **Solution 4**: Graceful Shutdown (Critique #9)
- [ ] **Solution 3**: Consistent Error Classification (Critique #7)
- [ ] **Solution 8**: AppClient Caching (Critique #15)
- [ ] **Solution 10**: VerifyInstallation Caching (Critique #21)

## P2 - Medium Priority (Plan for Later)
- [ ] **Solution 5**: 429 Feedback Loop (Critique #12)
- [ ] **Solution 9**: Owner ID Validation (Critique #20)
- [ ] **Solution 7**: GraphQL Rate Limiting (Critique #14)

## P3 - Lower Priority (Future Work)
- [ ] **Solution 2**: Webhook Rate Limiting (Critique #2)

---

# Plan Context (Common to All)

## Key Files Reference

| File | Purpose |
|------|---------|
| `server/handler/installation_manager.go` | Circuit breaker, client creation, caching |
| `server/handler/client_cache.go` | Client caching with TTL and negative caching |
| `server/handler/rate_limiter.go` | Rate limiting, adaptive rate control |
| `server/handler/errors.go` | Error classification functions |
| `server/handler/base.go` | Base handler with GetClientsByOwner, VerifyInstallation |
| `server/server.go` | Server initialization, shutdown |
| `vendor/github.com/palantir/go-githubapp/githubapp/` | External library interfaces |

## Existing Patterns to Follow

1. **Error Handling**: Use `errors.As` for type checking, `errors.Wrap` for context
2. **Logging**: Use `zerolog` with structured fields
3. **Metrics**: Use `go-metrics` registry pattern
4. **Concurrency**: Use `sync.Map` for concurrent access, `sync.Once` for lazy init
5. **Testing**: Table-driven tests with `testify/assert`

## Dependencies

```go
import (
    "golang.org/x/sync/semaphore"  // For Solution 6
    "golang.org/x/time/rate"       // Already used
)
```

---

# Solution 1: Per-Installation Circuit Breakers

## Checklist
- [ ] Step 1.1: Add configurable CircuitBreaker constructor
- [ ] Step 1.2: Add per-installation breaker map to InstallationManager
- [ ] Step 1.3: Add global circuit breaker to InstallationManager
- [ ] Step 1.4: Update GetClients to check both breakers
- [ ] Step 1.5: Add metrics for per-installation breakers
- [ ] Step 1.6: Write unit tests
- [ ] Step 1.7: Write integration tests
- [ ] Step 1.8: Update documentation

## Context

**Problem**: Single circuit breaker shared across all installations. When one org's installation fails 5 times, ALL orgs are blocked for 60 seconds.

**Solution**: Hybrid approach with:
- Global circuit breaker (threshold=50) for GitHub-wide outages
- Per-installation circuit breakers (threshold=5) for individual issues

**Files to Modify**:
- `server/handler/installation_manager.go` (primary)
- `server/handler/installation_manager_test.go` (tests)

---

### Step 1.1: Add Configurable CircuitBreaker Constructor

**Location**: `server/handler/installation_manager.go`

**What to Find**: Look for `type CircuitBreaker struct` and `func NewCircuitBreaker()`

**Implementation**:
```go
// Add new constants near existing circuit breaker constants
const (
    // Per-installation circuit breaker
    instCircuitBreakerThreshold = 5
    instCircuitBreakerTimeout   = 60 * time.Second

    // Global circuit breaker (higher threshold for true outages)
    globalCircuitBreakerThreshold = 50
    globalCircuitBreakerTimeout   = 120 * time.Second
)

// Add fields to CircuitBreaker struct
type CircuitBreaker struct {
    mu        sync.RWMutex
    state     CircuitBreakerState
    failures  int
    lastStateChange time.Time
    threshold int           // NEW: configurable threshold
    timeout   time.Duration // NEW: configurable timeout
}

// Add new constructor
func NewCircuitBreakerWithConfig(threshold int, timeout time.Duration) *CircuitBreaker {
    return &CircuitBreaker{
        state:     CircuitBreakerClosed,
        threshold: threshold,
        timeout:   timeout,
    }
}

// Update existing NewCircuitBreaker to use defaults
func NewCircuitBreaker() *CircuitBreaker {
    return NewCircuitBreakerWithConfig(
        circuitBreakerThreshold,
        circuitBreakerTimeout,
    )
}
```

**Update Allow() and other methods** to use `cb.threshold` and `cb.timeout` instead of constants.

**Acceptance Criteria**:
- [ ] `NewCircuitBreakerWithConfig` creates breaker with custom threshold/timeout
- [ ] Existing `NewCircuitBreaker` still works with defaults
- [ ] `Allow()` uses configurable threshold
- [ ] `shouldReset()` uses configurable timeout

---

### Step 1.2: Add Per-Installation Breaker Map

**Location**: `server/handler/installation_manager.go`

**What to Find**: `type InstallationManager struct`

**Implementation**:
```go
type InstallationManager struct {
    clientCreator        githubapp.ClientCreator
    metricsRegistry      gometrics.Registry
    circuitBreaker       *CircuitBreaker  // KEEP for backward compat initially
    installationBreakers sync.Map         // NEW: map[int64]*CircuitBreaker
    clientCache          *ClientCache
    logger               zerolog.Logger
}

// Add helper method
func (m *InstallationManager) getInstallationBreaker(installationID int64) *CircuitBreaker {
    if cb, ok := m.installationBreakers.Load(installationID); ok {
        return cb.(*CircuitBreaker)
    }
    newCB := NewCircuitBreakerWithConfig(
        instCircuitBreakerThreshold,
        instCircuitBreakerTimeout,
    )
    actual, _ := m.installationBreakers.LoadOrStore(installationID, newCB)
    return actual.(*CircuitBreaker)
}
```

**Acceptance Criteria**:
- [ ] `installationBreakers` sync.Map added to struct
- [ ] `getInstallationBreaker` returns existing or creates new
- [ ] Uses `LoadOrStore` for atomic creation

---

### Step 1.3: Add Global Circuit Breaker

**Location**: `server/handler/installation_manager.go`

**What to Find**: `NewInstallationManager` function

**Implementation**:
```go
type InstallationManager struct {
    // ... existing fields ...
    globalCircuitBreaker *CircuitBreaker  // NEW: for GitHub-wide outages
    installationBreakers sync.Map
}

func NewInstallationManager(
    clientCreator githubapp.ClientCreator,
    metricsRegistry gometrics.Registry,
    logger zerolog.Logger,
) *InstallationManager {
    return &InstallationManager{
        clientCreator:   clientCreator,
        metricsRegistry: metricsRegistry,
        logger:          logger,
        // Initialize global breaker with higher threshold
        globalCircuitBreaker: NewCircuitBreakerWithConfig(
            globalCircuitBreakerThreshold,
            globalCircuitBreakerTimeout,
        ),
        clientCache: NewClientCache(
            defaultClientCacheTTL,
            defaultClientCacheMaxSize,
            metricsRegistry,
        ),
    }
}
```

**Acceptance Criteria**:
- [ ] `globalCircuitBreaker` initialized in constructor
- [ ] Uses higher threshold (50) and timeout (120s)

---

### Step 1.4: Update GetClients to Check Both Breakers

**Location**: `server/handler/installation_manager.go`

**What to Find**: `func (m *InstallationManager) GetClients(...)`

**Implementation**:
```go
func (m *InstallationManager) GetClients(ctx context.Context, installationID int64, repoFullName string) (*InstallationClients, error) {
    // ... existing cache check ...

    // Check global circuit breaker FIRST
    if !m.globalCircuitBreaker.Allow() {
        m.logger.Warn().
            Int64("installation_id", installationID).
            Msg("Global circuit breaker is open - GitHub API may be unavailable")
        return nil, fmt.Errorf("global circuit breaker open: GitHub API may be unavailable")
    }

    // Check per-installation circuit breaker
    instCB := m.getInstallationBreaker(installationID)
    if !instCB.Allow() {
        m.logger.Warn().
            Int64("installation_id", installationID).
            Msg("Per-installation circuit breaker is open")
        return nil, fmt.Errorf("circuit breaker open for installation %d", installationID)
    }

    // ... existing client creation logic ...

    // On failure, update both breakers for retryable errors
    if err != nil && IsRetryableError(err) {
        m.globalCircuitBreaker.RecordFailure()
        instCB.RecordFailure()

        // Log state changes
        if m.globalCircuitBreaker.State() == CircuitBreakerOpen {
            m.logger.Error().Msg("Global circuit breaker opened")
        }
        if instCB.State() == CircuitBreakerOpen {
            m.logger.Warn().
                Int64("installation_id", installationID).
                Msg("Installation circuit breaker opened")
        }
    }

    // On success, update both breakers
    if err == nil {
        m.globalCircuitBreaker.RecordSuccess()
        instCB.RecordSuccess()
    }

    return clients, err
}
```

**Acceptance Criteria**:
- [ ] Global breaker checked first
- [ ] Per-installation breaker checked second
- [ ] Both breakers updated on success/failure
- [ ] Only retryable errors trip breakers
- [ ] Proper logging for state changes

---

### Step 1.5: Add Metrics for Per-Installation Breakers

**Location**: `server/handler/installation_manager.go`

**Implementation**:
```go
const (
    MetricsKeyGlobalCircuitBreakerState = "installation.global_circuit_breaker.state"
    MetricsKeyInstallationCircuitBreakerOpened = "installation.circuit_breaker.opened"
    MetricsKeyInstallationCircuitBreakerClosed = "installation.circuit_breaker.closed"
    MetricsKeyActiveInstallationBreakers = "installation.circuit_breakers.count"
)

// Add method to count active breakers
func (m *InstallationManager) countActiveBreakers() int {
    count := 0
    m.installationBreakers.Range(func(_, _ interface{}) bool {
        count++
        return true
    })
    return count
}

// Update metrics publishing (add to existing metrics loop if present)
func (m *InstallationManager) publishMetrics() {
    if m.metricsRegistry == nil {
        return
    }

    // Global breaker state
    globalState := 0 // closed
    if m.globalCircuitBreaker.State() == CircuitBreakerOpen {
        globalState = 1
    } else if m.globalCircuitBreaker.State() == CircuitBreakerHalfOpen {
        globalState = 2
    }
    gometrics.GetOrRegisterGauge(MetricsKeyGlobalCircuitBreakerState, m.metricsRegistry).Update(int64(globalState))

    // Active breaker count
    gometrics.GetOrRegisterGauge(MetricsKeyActiveInstallationBreakers, m.metricsRegistry).Update(int64(m.countActiveBreakers()))
}
```

**Acceptance Criteria**:
- [ ] Global breaker state metric (0=closed, 1=open, 2=half-open)
- [ ] Count of active installation breakers
- [ ] Metrics published periodically

---

### Step 1.6: Write Unit Tests

**Location**: `server/handler/installation_manager_test.go`

**Implementation**:
```go
func TestPerInstallationCircuitBreaker_Isolation(t *testing.T) {
    m := NewInstallationManager(mockCreator, nil, zerolog.Nop())

    // Trip circuit for installation 100
    breaker100 := m.getInstallationBreaker(100)
    for i := 0; i < instCircuitBreakerThreshold; i++ {
        breaker100.RecordFailure()
    }

    // Installation 100 should be blocked
    assert.False(t, breaker100.Allow(), "Installation 100 should be blocked")

    // Installation 200 should still work
    breaker200 := m.getInstallationBreaker(200)
    assert.True(t, breaker200.Allow(), "Installation 200 should not be blocked")
}

func TestGlobalCircuitBreaker_BlocksAll(t *testing.T) {
    m := NewInstallationManager(mockCreator, nil, zerolog.Nop())

    // Trip global circuit
    for i := 0; i < globalCircuitBreakerThreshold; i++ {
        m.globalCircuitBreaker.RecordFailure()
    }

    // Global should be open
    assert.False(t, m.globalCircuitBreaker.Allow(), "Global breaker should be open")

    // Even healthy installations can't proceed
    _, err := m.GetClients(context.Background(), 100, "org/repo")
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "global circuit breaker open")
}

func TestCircuitBreaker_Recovery(t *testing.T) {
    cb := NewCircuitBreakerWithConfig(3, 100*time.Millisecond)

    // Trip the breaker
    for i := 0; i < 3; i++ {
        cb.RecordFailure()
    }
    assert.False(t, cb.Allow())

    // Wait for timeout
    time.Sleep(150 * time.Millisecond)

    // Should be half-open now
    assert.True(t, cb.Allow())

    // Success should close it
    cb.RecordSuccess()
    assert.Equal(t, CircuitBreakerClosed, cb.State())
}
```

**Acceptance Criteria**:
- [ ] Test isolation: one installation's failures don't affect others
- [ ] Test global breaker blocks all when open
- [ ] Test recovery after timeout
- [ ] Test half-open state transitions

---

### Step 1.7: Write Integration Tests

**Location**: `server/handler/installation_integration_test.go` (new file or existing)

**Implementation**:
```go
func TestInstallationManager_GetClients_CircuitBreaker_Integration(t *testing.T) {
    // Create mock that returns errors
    mockCreator := &MockClientCreator{
        NewInstallationClientFunc: func(installationID int64) (*github.Client, error) {
            if installationID == 100 {
                return nil, &github.ErrorResponse{
                    Response: &http.Response{StatusCode: 500},
                    Message:  "Internal Server Error",
                }
            }
            return github.NewClient(nil), nil
        },
    }

    m := NewInstallationManager(mockCreator, nil, zerolog.Nop())

    // Make 5 failing requests for installation 100
    for i := 0; i < 5; i++ {
        _, err := m.GetClients(context.Background(), 100, "org/repo")
        assert.Error(t, err)
    }

    // Installation 100 should now be circuit-broken
    _, err := m.GetClients(context.Background(), 100, "org/repo")
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "circuit breaker open")

    // Installation 200 should still work
    _, err = m.GetClients(context.Background(), 200, "org/repo")
    assert.NoError(t, err)
}
```

**Acceptance Criteria**:
- [ ] Integration test with mock client creator
- [ ] Verifies circuit opens after threshold failures
- [ ] Verifies other installations unaffected

---

### Step 1.8: Update Documentation

**Location**: `.claude/analysis/application_notes.md` and code comments

**Updates**:
1. Document the hybrid circuit breaker approach
2. Update architecture diagram to show tiered breakers
3. Add operational notes about monitoring breaker states
4. Document configuration constants

**Acceptance Criteria**:
- [ ] Application notes updated with new architecture
- [ ] Code comments explain the hybrid approach
- [ ] Operational guidance for monitoring

---

# Solution 6: Concurrent Request Limiting

## Checklist
- [ ] Step 6.1: Add golang.org/x/sync/semaphore dependency
- [ ] Step 6.2: Add concurrency config to RateLimitConfig
- [ ] Step 6.3: Add semaphore fields to RateLimitedClientCreator
- [ ] Step 6.4: Implement acquire/release slot methods
- [ ] Step 6.5: Update NewOrgClient with concurrency limiting
- [ ] Step 6.6: Create concurrency tracking transport
- [ ] Step 6.7: Add concurrency metrics
- [ ] Step 6.8: Write unit tests
- [ ] Step 6.9: Update documentation

## Context

**Problem**: GitHub enforces secondary rate limits including max 100 concurrent requests per installation. Current rate limiter only tracks request count, not concurrent in-flight requests.

**Solution**: Use `golang.org/x/sync/semaphore` weighted semaphore for:
- Global concurrency limit (100)
- Per-org concurrency limit (50)

**Files to Modify**:
- `server/handler/rate_limiter.go` (primary)
- `server/handler/rate_limiter_test.go` (tests)
- `go.mod` (dependency)
- `vendor/` (vendored dependency)

---

### Step 6.1: Add Dependency

**Location**: Project root

**Commands**:
```bash
go get golang.org/x/sync/semaphore
go mod tidy
go mod vendor
```

**Acceptance Criteria**:
- [ ] `golang.org/x/sync` in go.mod
- [ ] Vendored files present

---

### Step 6.2: Add Concurrency Config

**Location**: `server/handler/rate_limiter.go`

**What to Find**: `type RateLimitConfig struct`

**Implementation**:
```go
const (
    // GitHub allows 100 concurrent requests per installation
    DefaultGlobalConcurrencyLimit = 100
    DefaultOrgConcurrencyLimit    = 50  // Conservative per-org limit

    // Weights for different operations
    ConcurrencyWeightStandard     int64 = 1
    ConcurrencyWeightGraphQL      int64 = 2   // GraphQL more expensive
    ConcurrencyWeightContentWrite int64 = 3   // Creating content uses more resources
)

type RateLimitConfig struct {
    // ... existing fields ...

    // Concurrency limiting
    GlobalConcurrencyLimit int `yaml:"global_concurrency_limit" json:"global_concurrency_limit"`
    OrgConcurrencyLimit    int `yaml:"org_concurrency_limit" json:"org_concurrency_limit"`
    ConcurrencyEnabled     bool `yaml:"concurrency_enabled" json:"concurrency_enabled"`
}

// Update DefaultRateLimitConfig
func DefaultRateLimitConfig() *RateLimitConfig {
    return &RateLimitConfig{
        // ... existing defaults ...
        GlobalConcurrencyLimit: DefaultGlobalConcurrencyLimit,
        OrgConcurrencyLimit:    DefaultOrgConcurrencyLimit,
        ConcurrencyEnabled:     true,
    }
}
```

**Acceptance Criteria**:
- [ ] Config struct has concurrency fields
- [ ] Defaults set appropriately
- [ ] YAML/JSON tags for config parsing

---

### Step 6.3: Add Semaphore Fields

**Location**: `server/handler/rate_limiter.go`

**What to Find**: `type RateLimitedClientCreator struct`

**Implementation**:
```go
import "golang.org/x/sync/semaphore"

type RateLimitedClientCreator struct {
    base githubapp.ClientCreator
    config *RateLimitConfig

    // Existing rate limiters
    orgLimiters   sync.Map
    globalLimiter *rate.Limiter
    adaptiveStates sync.Map

    // NEW: Concurrent request limiters
    globalConcurrencySem *semaphore.Weighted
    orgConcurrencySems   sync.Map  // map[string]*semaphore.Weighted

    logger   zerolog.Logger
    registry metrics.Registry

    // ... existing fields ...
}
```

**Update constructor**:
```go
func NewRateLimitedClientCreator(
    base githubapp.ClientCreator,
    config *RateLimitConfig,
    logger zerolog.Logger,
    registry metrics.Registry,
) *RateLimitedClientCreator {
    if config == nil {
        config = DefaultRateLimitConfig()
    }

    // ... existing initialization ...

    rlcc := &RateLimitedClientCreator{
        // ... existing fields ...
    }

    // Initialize concurrency semaphore if enabled
    if config.ConcurrencyEnabled {
        rlcc.globalConcurrencySem = semaphore.NewWeighted(int64(config.GlobalConcurrencyLimit))
    }

    return rlcc
}
```

**Acceptance Criteria**:
- [ ] Semaphore fields added to struct
- [ ] Constructor initializes global semaphore
- [ ] Only initialized if enabled in config

---

### Step 6.4: Implement Acquire/Release Methods

**Location**: `server/handler/rate_limiter.go`

**Implementation**:
```go
// getOrgConcurrencySem returns or creates a per-org concurrency semaphore
func (r *RateLimitedClientCreator) getOrgConcurrencySem(owner string) *semaphore.Weighted {
    if sem, ok := r.orgConcurrencySems.Load(owner); ok {
        return sem.(*semaphore.Weighted)
    }
    newSem := semaphore.NewWeighted(int64(r.config.OrgConcurrencyLimit))
    actual, _ := r.orgConcurrencySems.LoadOrStore(owner, newSem)
    return actual.(*semaphore.Weighted)
}

// acquireConcurrencySlot acquires both global and per-org concurrency slots
func (r *RateLimitedClientCreator) acquireConcurrencySlot(ctx context.Context, owner string, weight int64) error {
    if r.globalConcurrencySem == nil {
        return nil // Concurrency limiting disabled
    }

    // Try to acquire global slot first
    if err := r.globalConcurrencySem.Acquire(ctx, weight); err != nil {
        if r.registry != nil {
            metrics.GetOrRegisterCounter(MetricsKeyConcurrencyRejected, r.registry).Inc(1)
        }
        return fmt.Errorf("global concurrency limit reached: %w", err)
    }

    // Try to acquire org slot
    orgSem := r.getOrgConcurrencySem(owner)
    if err := orgSem.Acquire(ctx, weight); err != nil {
        // Release global slot if org acquisition fails
        r.globalConcurrencySem.Release(weight)
        if r.registry != nil {
            metrics.GetOrRegisterCounter(MetricsKeyConcurrencyRejected, r.registry).Inc(1)
        }
        return fmt.Errorf("org %s concurrency limit reached: %w", owner, err)
    }

    if r.registry != nil {
        metrics.GetOrRegisterCounter(MetricsKeyConcurrencyAcquired, r.registry).Inc(1)
    }

    return nil
}

// releaseConcurrencySlot releases both global and per-org concurrency slots
func (r *RateLimitedClientCreator) releaseConcurrencySlot(owner string, weight int64) {
    if r.globalConcurrencySem == nil {
        return // Concurrency limiting disabled
    }

    orgSem := r.getOrgConcurrencySem(owner)
    orgSem.Release(weight)
    r.globalConcurrencySem.Release(weight)
}
```

**Acceptance Criteria**:
- [ ] `getOrgConcurrencySem` creates or returns existing semaphore
- [ ] `acquireConcurrencySlot` acquires both global and org slots
- [ ] On org failure, global slot is released
- [ ] `releaseConcurrencySlot` releases both
- [ ] No-op if concurrency disabled

---

### Step 6.5: Update NewOrgClient

**Location**: `server/handler/rate_limiter.go`

**What to Find**: `func (r *RateLimitedClientCreator) NewOrgClient(...)`

**Implementation**:
```go
func (r *RateLimitedClientCreator) NewOrgClient(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
    startTime := time.Now()

    // Wait for rate limit token (existing)
    if err := r.waitForOrgRateLimit(ctx, owner); err != nil {
        return nil, err
    }

    // NEW: Acquire concurrency slot
    if err := r.acquireConcurrencySlot(ctx, owner, ConcurrencyWeightStandard); err != nil {
        return nil, err
    }

    // Create the base client
    client, err := r.base.NewInstallationClient(installationID)
    if err != nil {
        r.releaseConcurrencySlot(owner, ConcurrencyWeightStandard)
        return nil, err
    }

    // Wrap transport to track concurrency and release slot on response
    originalTransport := client.Client().Transport
    client.Client().Transport = &concurrencyTrackingTransport{
        base:    originalTransport,
        creator: r,
        owner:   owner,
        weight:  ConcurrencyWeightStandard,
    }

    // Record wait time metric
    if r.registry != nil {
        waitTime := time.Since(startTime)
        if timer := r.registry.Get(MetricsKeyRateLimitWaitTime); timer != nil {
            if t, ok := timer.(metrics.Timer); ok {
                t.Update(waitTime)
            }
        }
    }

    return client, nil
}
```

**Also update NewOrgV4Client** similarly with `ConcurrencyWeightGraphQL`.

**Acceptance Criteria**:
- [ ] Concurrency slot acquired before client creation
- [ ] Slot released on error
- [ ] Transport wrapped to release on response

---

### Step 6.6: Create Concurrency Tracking Transport

**Location**: `server/handler/rate_limiter.go`

**Implementation**:
```go
// concurrencyTrackingTransport wraps http.RoundTripper to release concurrency slot after request
type concurrencyTrackingTransport struct {
    base    http.RoundTripper
    creator *RateLimitedClientCreator
    owner   string
    weight  int64
    released bool
    mu       sync.Mutex
}

func (t *concurrencyTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    resp, err := t.base.RoundTrip(req)

    // Release concurrency slot when response is received (only once)
    t.mu.Lock()
    if !t.released {
        t.creator.releaseConcurrencySlot(t.owner, t.weight)
        t.released = true
    }
    t.mu.Unlock()

    return resp, err
}
```

**Acceptance Criteria**:
- [ ] Transport releases slot after RoundTrip completes
- [ ] Only releases once (idempotent)
- [ ] Thread-safe release

---

### Step 6.7: Add Concurrency Metrics

**Location**: `server/handler/rate_limiter.go`

**Implementation**:
```go
const (
    MetricsKeyConcurrencyAcquired  = "handler.concurrency.acquired"
    MetricsKeyConcurrencyRejected  = "handler.concurrency.rejected"
    MetricsKeyConcurrencyWaitTime  = "handler.concurrency.wait_time"
    MetricsKeyConcurrencyInFlight  = "handler.concurrency.in_flight"
)

// Add method to get current in-flight count
func (r *RateLimitedClientCreator) GetGlobalConcurrencyInFlight() int64 {
    if r.globalConcurrencySem == nil {
        return 0
    }
    // This is an estimate - semaphore doesn't expose current count directly
    // Would need to track separately if needed
    return 0
}
```

**Acceptance Criteria**:
- [ ] Metric keys defined
- [ ] Metrics recorded on acquire/reject
- [ ] Metrics registered with registry

---

### Step 6.8: Write Unit Tests

**Location**: `server/handler/rate_limiter_test.go`

**Implementation**:
```go
func TestConcurrencyLimiting_AcquireRelease(t *testing.T) {
    config := &RateLimitConfig{
        GlobalConcurrencyLimit: 10,
        OrgConcurrencyLimit:    5,
        ConcurrencyEnabled:     true,
    }
    rlcc := NewRateLimitedClientCreator(mockBase, config, zerolog.Nop(), nil)

    ctx := context.Background()

    // Should be able to acquire
    err := rlcc.acquireConcurrencySlot(ctx, "test-org", 1)
    assert.NoError(t, err)

    // Release
    rlcc.releaseConcurrencySlot("test-org", 1)
}

func TestConcurrencyLimiting_OrgLimit(t *testing.T) {
    config := &RateLimitConfig{
        GlobalConcurrencyLimit: 100,
        OrgConcurrencyLimit:    2,
        ConcurrencyEnabled:     true,
    }
    rlcc := NewRateLimitedClientCreator(mockBase, config, zerolog.Nop(), nil)

    ctx := context.Background()

    // Acquire 2 slots (at limit)
    assert.NoError(t, rlcc.acquireConcurrencySlot(ctx, "test-org", 1))
    assert.NoError(t, rlcc.acquireConcurrencySlot(ctx, "test-org", 1))

    // Third should block/timeout
    ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
    defer cancel()
    err := rlcc.acquireConcurrencySlot(ctxTimeout, "test-org", 1)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "concurrency limit")

    // Different org should work
    assert.NoError(t, rlcc.acquireConcurrencySlot(ctx, "other-org", 1))
}

func TestConcurrencyLimiting_GlobalLimit(t *testing.T) {
    config := &RateLimitConfig{
        GlobalConcurrencyLimit: 2,
        OrgConcurrencyLimit:    10,
        ConcurrencyEnabled:     true,
    }
    rlcc := NewRateLimitedClientCreator(mockBase, config, zerolog.Nop(), nil)

    ctx := context.Background()

    // Acquire 2 global slots across different orgs
    assert.NoError(t, rlcc.acquireConcurrencySlot(ctx, "org-1", 1))
    assert.NoError(t, rlcc.acquireConcurrencySlot(ctx, "org-2", 1))

    // Third should fail even for new org (global limit)
    ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
    defer cancel()
    err := rlcc.acquireConcurrencySlot(ctxTimeout, "org-3", 1)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "global concurrency limit")
}
```

**Acceptance Criteria**:
- [ ] Test basic acquire/release
- [ ] Test per-org limit blocking
- [ ] Test global limit blocking
- [ ] Test different orgs don't affect each other's org limit

---

### Step 6.9: Update Documentation

**Location**: `.claude/analysis/application_notes.md`

**Updates**:
1. Document the secondary rate limit handling
2. Add operational notes about concurrency limits
3. Document configuration options

**Acceptance Criteria**:
- [ ] Documentation updated with concurrency limiting explanation
- [ ] Configuration options documented

---

# Solution 4: Graceful Shutdown

## Checklist
- [ ] Step 4.1: Add StopInstallationManager method to Base
- [ ] Step 4.2: Update server shutdown to call cleanup
- [ ] Step 4.3: Ensure RateLimitedClientCreator.Close is called
- [ ] Step 4.4: Write tests for graceful shutdown
- [ ] Step 4.5: Update documentation

## Context

**Problem**: `ClientCache` background goroutines aren't stopped during server shutdown, causing goroutine leaks.

**Solution**: Add shutdown hooks to server that call cleanup methods on all handlers.

**Files to Modify**:
- `server/handler/base.go`
- `server/server.go`

---

### Step 4.1: Add StopInstallationManager to Base

**Location**: `server/handler/base.go`

**Implementation**:
```go
// StopInstallationManager stops the installation manager's background goroutines
func (b *Base) StopInstallationManager() {
    if b.installationManager != nil {
        b.installationManager.StopClientCache()
    }
}

// Stop stops all background goroutines in the Base handler
func (b *Base) Stop() {
    b.StopInstallationManager()

    // Stop rate limiter if it's a RateLimitedClientCreator
    if closer, ok := b.ClientCreator.(interface{ Close() }); ok {
        closer.Close()
    }
}
```

**Acceptance Criteria**:
- [ ] `StopInstallationManager` method added
- [ ] `Stop` method calls all cleanup functions
- [ ] Checks for Close interface before calling

---

### Step 4.2: Update Server Shutdown

**Location**: `server/server.go`

**What to Find**: Server `Start()` method or shutdown handling

**Implementation**:
```go
func (s *Server) Start() error {
    // ... existing setup ...

    // Setup graceful shutdown
    shutdown := make(chan os.Signal, 1)
    signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-shutdown
        s.logger.Info().Msg("Shutdown signal received, cleaning up...")

        // Stop all base handlers
        s.enterpriseBasePolicyHandler.Stop()
        s.cloudBasePolicyHandler.Stop()
        s.sqsEnterpriseBasePolicyHandler.Stop()
        s.sqsCloudBasePolicyHandler.Stop()

        s.logger.Info().Msg("Handlers stopped")
    }()

    return s.base.Start()
}
```

**Or if using defer pattern**:
```go
func (s *Server) Start() error {
    // ... existing setup ...

    // Ensure cleanup on exit
    defer func() {
        s.logger.Info().Msg("Stopping handlers...")
        enterpriseBasePolicyHandler.Stop()
        cloudBasePolicyHandler.Stop()
        sqsEnterpriseBasePolicyHandler.Stop()
        sqsCloudBasePolicyHandler.Stop()
        s.logger.Info().Msg("Handlers stopped")
    }()

    return s.base.Start()
}
```

**Acceptance Criteria**:
- [ ] Shutdown hooks call Stop on all handlers
- [ ] Logging indicates cleanup happening
- [ ] Both webhook and SQS handlers cleaned up

---

### Step 4.3: Ensure RateLimitedClientCreator.Close is Called

**Location**: `server/handler/rate_limiter.go`

**Verify existing Close method**:
```go
func (r *RateLimitedClientCreator) Close() {
    if r.cancel != nil {
        r.cancel()
    }
}
```

**Acceptance Criteria**:
- [ ] `Close()` method exists and stops background goroutines
- [ ] Called via interface check in Base.Stop()

---

### Step 4.4: Write Tests

**Location**: `server/handler/base_test.go`

**Implementation**:
```go
func TestBase_Stop_CallsCleanup(t *testing.T) {
    // Create mock with Stop tracking
    mockManager := &MockInstallationManager{
        StopCalled: false,
    }

    b := &Base{
        installationManager: mockManager,
    }

    b.Stop()

    assert.True(t, mockManager.StopCalled, "Stop should call StopClientCache")
}

func TestBase_Stop_HandlesNilManager(t *testing.T) {
    b := &Base{
        installationManager: nil,
    }

    // Should not panic
    assert.NotPanics(t, func() {
        b.Stop()
    })
}
```

**Acceptance Criteria**:
- [ ] Test Stop calls cleanup methods
- [ ] Test handles nil gracefully

---

### Step 4.5: Update Documentation

**Location**: Code comments and operations playbook

**Acceptance Criteria**:
- [ ] Document shutdown sequence
- [ ] Note about graceful shutdown timeout

---

# Solution 3: Consistent Error Classification

## Checklist
- [ ] Step 3.1: Update IsRetryableError to use type assertions first
- [ ] Step 3.2: Reduce string matching to minimum fallback
- [ ] Step 3.3: Update IsInstallationNotFoundError similarly
- [ ] Step 3.4: Update IsAuthenticationError similarly
- [ ] Step 3.5: Write comprehensive tests
- [ ] Step 3.6: Update call sites if needed

## Context

**Problem**: `IsRetryableError` uses fragile string matching ("500", "429") which can cause false positives.

**Solution**: Use `errors.As` for type checking first, fallback to string matching only for truly unknown errors.

**Files to Modify**:
- `server/handler/errors.go`
- `server/handler/errors_test.go`

---

### Step 3.1: Update IsRetryableError

**Location**: `server/handler/errors.go`

**Implementation**:
```go
func IsRetryableError(err error) bool {
    if err == nil {
        return false
    }

    // 1. Check for GitHub library errors (most reliable)
    var rlErr *github.RateLimitError
    if errors.As(err, &rlErr) {
        return true  // Rate limit is retryable
    }

    var abuseErr *github.AbuseRateLimitError
    if errors.As(err, &abuseErr) {
        return true  // Abuse rate limit is retryable (after waiting)
    }

    var errResp *github.ErrorResponse
    if errors.As(err, &errResp) && errResp.Response != nil {
        switch errResp.Response.StatusCode {
        case 500, 502, 503, 504, 429:
            return true
        case 401, 403, 404, 422:
            return false
        }
    }

    // 2. Check for network errors
    var netErr net.Error
    if errors.As(err, &netErr) {
        return netErr.Timeout() || netErr.Temporary()
    }

    var urlErr *url.Error
    if errors.As(err, &urlErr) {
        return true  // Connection issues are retryable
    }

    // 3. Check for context errors
    if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
        return false  // Context errors are not retryable
    }

    // 4. Only fallback to string matching for truly unknown errors
    // Keep this list minimal - only patterns that can't be caught by type
    errMsg := strings.ToLower(err.Error())
    unknownRetryable := []string{
        "connection reset by peer",
        "tls handshake timeout",
        "no such host",
        "broken pipe",
    }
    for _, pattern := range unknownRetryable {
        if strings.Contains(errMsg, pattern) {
            return true
        }
    }

    return false  // Default: don't retry unknown errors
}
```

**Acceptance Criteria**:
- [ ] Type assertions checked before string matching
- [ ] String patterns reduced to only network-level patterns
- [ ] No status codes in string matching (use ErrorResponse instead)

---

### Step 3.2-3.4: Update Other Error Functions

**Apply similar pattern to**:
- `IsInstallationNotFoundError`
- `IsAuthenticationError`
- `classifyGitHubError`

**Acceptance Criteria**:
- [ ] All error functions use consistent pattern
- [ ] Type assertions first, string matching last

---

### Step 3.5: Write Comprehensive Tests

**Location**: `server/handler/errors_test.go`

**Implementation**:
```go
func TestIsRetryableError_GitHubErrors(t *testing.T) {
    tests := []struct {
        name     string
        err      error
        expected bool
    }{
        {
            name: "RateLimitError is retryable",
            err: &github.RateLimitError{
                Response: &http.Response{StatusCode: 403},
            },
            expected: true,
        },
        {
            name: "500 ErrorResponse is retryable",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 500},
            },
            expected: true,
        },
        {
            name: "404 ErrorResponse is not retryable",
            err: &github.ErrorResponse{
                Response: &http.Response{StatusCode: 404},
            },
            expected: false,
        },
        {
            name: "wrapped error still works",
            err: errors.Wrap(&github.ErrorResponse{
                Response: &http.Response{StatusCode: 502},
            }, "wrapper message"),
            expected: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := IsRetryableError(tt.err)
            assert.Equal(t, tt.expected, result)
        })
    }
}

func TestIsRetryableError_NoFalsePositives(t *testing.T) {
    // Test that string matching doesn't cause false positives
    tests := []struct {
        name     string
        errMsg   string
        expected bool
    }{
        {
            name:     "repo name with 500 is not false positive",
            errMsg:   "repository 'org/api-500-service' not found",
            expected: false,
        },
        {
            name:     "message with 429 in path is not false positive",
            errMsg:   "failed to access /api/v429/resource",
            expected: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := errors.New(tt.errMsg)
            result := IsRetryableError(err)
            assert.Equal(t, tt.expected, result)
        })
    }
}
```

**Acceptance Criteria**:
- [ ] Tests for each GitHub error type
- [ ] Tests for wrapped errors
- [ ] Tests confirming no false positives
- [ ] Tests for network errors

---

# Solution 8: AppClient Caching

## Checklist
- [ ] Step 8.1: Add appClient fields to Base struct
- [ ] Step 8.2: Implement GetAppClient with sync.Once
- [ ] Step 8.3: Update VerifyInstallation to use GetAppClient
- [ ] Step 8.4: Update any other NewAppClient calls
- [ ] Step 8.5: Write tests
- [ ] Step 8.6: Update documentation

## Context

**Problem**: Every `VerifyInstallation()` call creates a new App-level client, regenerating the JWT each time. JWT signing is CPU-intensive.

**Solution**: Use `sync.Once` for thread-safe lazy initialization of the app client.

**Files to Modify**:
- `server/handler/base.go`
- `server/handler/base_test.go`

---

### Step 8.1: Add Fields to Base

**Location**: `server/handler/base.go`

**What to Find**: `type Base struct`

**Implementation**:
```go
type Base struct {
    // ... existing fields ...

    // Cached app client (lazy initialized)
    appClientOnce sync.Once
    appClient     *github.Client
    appClientErr  error
}
```

**Acceptance Criteria**:
- [ ] Fields added to struct
- [ ] sync.Once for thread-safe init

---

### Step 8.2: Implement GetAppClient

**Location**: `server/handler/base.go`

**Implementation**:
```go
// GetAppClient returns a cached app-level client.
// The client is lazily initialized on first call and reused thereafter.
// Thread-safe via sync.Once.
func (b *Base) GetAppClient() (*github.Client, error) {
    b.appClientOnce.Do(func() {
        b.appClient, b.appClientErr = b.ClientCreator.NewAppClient()
        if b.appClientErr != nil {
            b.Logger.Error().Err(b.appClientErr).
                Msg("Failed to create app client")
        } else {
            b.Logger.Debug().Msg("App client created and cached")
        }
    })
    return b.appClient, b.appClientErr
}
```

**Acceptance Criteria**:
- [ ] Uses sync.Once for single initialization
- [ ] Caches both success and error
- [ ] Logs appropriately

---

### Step 8.3: Update VerifyInstallation

**Location**: `server/handler/base.go`

**What to Find**: `func (b *Base) VerifyInstallation(...)`

**Implementation**:
```go
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
    logger := zerolog.Ctx(ctx)

    // Use cached app client
    appClient, err := b.GetAppClient()
    if err != nil {
        logger.Warn().Err(err).
            Int64("installation_id", installationID).
            Msg("Failed to get app client for installation verification")
        return false
    }

    // ... rest of method unchanged ...
}
```

**Acceptance Criteria**:
- [ ] Uses GetAppClient instead of NewAppClient
- [ ] No other logic changes

---

### Step 8.4: Update Other Calls

**Search for**: `b.NewAppClient()` or `b.ClientCreator.NewAppClient()`

**Replace with**: `b.GetAppClient()` where appropriate

**Note**: Some calls may need new client (e.g., if requiring fresh token). Evaluate each case.

**Acceptance Criteria**:
- [ ] All appropriate calls updated
- [ ] Cases needing fresh client documented

---

### Step 8.5: Write Tests

**Location**: `server/handler/base_test.go`

**Implementation**:
```go
func TestGetAppClient_CachesClient(t *testing.T) {
    callCount := 0
    mockCreator := &MockClientCreator{
        NewAppClientFunc: func() (*github.Client, error) {
            callCount++
            return github.NewClient(nil), nil
        },
    }

    b := &Base{ClientCreator: mockCreator}

    // First call
    client1, err := b.GetAppClient()
    assert.NoError(t, err)
    assert.NotNil(t, client1)
    assert.Equal(t, 1, callCount)

    // Second call should return cached
    client2, err := b.GetAppClient()
    assert.NoError(t, err)
    assert.Same(t, client1, client2)
    assert.Equal(t, 1, callCount, "Should not call NewAppClient again")
}

func TestGetAppClient_CachesError(t *testing.T) {
    expectedErr := errors.New("app client error")
    callCount := 0
    mockCreator := &MockClientCreator{
        NewAppClientFunc: func() (*github.Client, error) {
            callCount++
            return nil, expectedErr
        },
    }

    b := &Base{ClientCreator: mockCreator, Logger: zerolog.Nop()}

    // First call
    _, err := b.GetAppClient()
    assert.Error(t, err)

    // Second call should return cached error
    _, err = b.GetAppClient()
    assert.Error(t, err)
    assert.Equal(t, 1, callCount, "Should not retry on cached error")
}

func TestGetAppClient_ThreadSafe(t *testing.T) {
    callCount := int64(0)
    mockCreator := &MockClientCreator{
        NewAppClientFunc: func() (*github.Client, error) {
            atomic.AddInt64(&callCount, 1)
            time.Sleep(10 * time.Millisecond) // Simulate slow creation
            return github.NewClient(nil), nil
        },
    }

    b := &Base{ClientCreator: mockCreator}

    // Call concurrently
    var wg sync.WaitGroup
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            b.GetAppClient()
        }()
    }
    wg.Wait()

    assert.Equal(t, int64(1), callCount, "Should only create once even with concurrent calls")
}
```

**Acceptance Criteria**:
- [ ] Test caching works
- [ ] Test error caching
- [ ] Test thread safety

---

# Solution 10: VerifyInstallation Caching

## Checklist
- [ ] Step 10.1: Add IsNegativelyCached method to ClientCache
- [ ] Step 10.2: Update VerifyInstallation to check cache first
- [ ] Step 10.3: Add negative caching on 404
- [ ] Step 10.4: Write tests
- [ ] Step 10.5: Update documentation

## Context

**Problem**: `VerifyInstallation` makes a direct API call every time, bypassing caching. This is inefficient for GHES workloads.

**Solution**: Check ClientCache first. If clients exist, installation is verified. Also use GetAppClient (Solution 8).

**Files to Modify**:
- `server/handler/client_cache.go`
- `server/handler/base.go`
- Tests

**Depends on**: Solution 8 (AppClient Caching)

---

### Step 10.1: Add IsNegativelyCached Method

**Location**: `server/handler/client_cache.go`

**Implementation**:
```go
// IsNegativelyCached checks if an installation ID is in the negative cache
// (i.e., previously determined to not exist)
func (c *ClientCache) IsNegativelyCached(installationID int64) bool {
    value, ok := c.cache.Load(installationID)
    if !ok {
        return false
    }

    entry := value.(*CachedClients)

    // Check expiration
    if time.Now().After(entry.ExpiresAt) {
        c.cache.Delete(installationID)
        return false
    }

    return entry.IsNegative
}
```

**Acceptance Criteria**:
- [ ] Method returns true only for non-expired negative cache entries
- [ ] Cleans up expired entries

---

### Step 10.2: Update VerifyInstallation

**Location**: `server/handler/base.go`

**Implementation**:
```go
// VerifyInstallation checks if the GitHub App is installed for the given installation ID.
// OPTIMIZED: Uses ClientCache first before making API call.
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
    logger := zerolog.Ctx(ctx)

    // OPTIMIZATION 1: Check if we have cached clients for this installation
    // If we do, the installation is verified (clients require valid installation)
    if b.ClientCache != nil {
        if clients := b.ClientCache.Get(installationID); clients != nil {
            logger.Debug().
                Int64("installation_id", installationID).
                Msg("Installation verified via client cache hit")
            return true
        }

        // Check negative cache - if negatively cached, installation doesn't exist
        if b.ClientCache.IsNegativelyCached(installationID) {
            logger.Debug().
                Int64("installation_id", installationID).
                Msg("Installation not found (negatively cached)")
            return false
        }
    }

    // OPTIMIZATION 2: Use cached app client
    appClient, err := b.GetAppClient()
    if err != nil {
        logger.Warn().Err(err).
            Int64("installation_id", installationID).
            Msg("Failed to get app client for installation verification")
        return false
    }

    // Call GitHub API to verify installation exists
    _, _, err = appClient.Apps.GetInstallation(ctx, installationID)
    exists := err == nil

    if err != nil {
        logger.Debug().Err(err).
            Int64("installation_id", installationID).
            Msg("Installation verification failed or not found")

        // OPTIMIZATION 3: Cache negative result to avoid repeated calls
        if b.ClientCache != nil && IsInstallationNotFoundError(err) {
            b.ClientCache.PutNegative(installationID)
            logger.Debug().
                Int64("installation_id", installationID).
                Msg("Installation negatively cached")
        }
    } else {
        logger.Debug().
            Int64("installation_id", installationID).
            Msg("Installation verified via API call")
    }

    // Update legacy cache for backwards compatibility
    if exists {
        b.mu.Lock()
        b.InstallationIdMap[installationID] = installationID
        b.mu.Unlock()
    }

    return exists
}
```

**Acceptance Criteria**:
- [ ] Checks positive cache first
- [ ] Checks negative cache second
- [ ] Uses GetAppClient (cached)
- [ ] Adds to negative cache on 404

---

### Step 10.3: Ensure PutNegative Exists

**Location**: `server/handler/client_cache.go`

**Verify or add**:
```go
// PutNegative stores a negative cache entry for an installation ID
func (c *ClientCache) PutNegative(installationID int64) {
    entry := &CachedClients{
        Clients:        nil,
        InstallationID: installationID,
        ExpiresAt:      time.Now().Add(c.negativeTTL),
        CreatedAt:      time.Now(),
        IsNegative:     true,
    }
    c.cache.Store(installationID, entry)
}
```

**Acceptance Criteria**:
- [ ] PutNegative method exists
- [ ] Uses negative TTL

---

### Step 10.4: Write Tests

**Location**: `server/handler/base_test.go`

**Implementation**:
```go
func TestVerifyInstallation_UsesCacheHit(t *testing.T) {
    // Setup cache with existing clients
    cache := NewClientCache(10*time.Minute, 100, nil)
    cache.Put(100, &InstallationClients{})

    b := &Base{
        ClientCache: cache,
        Logger:      zerolog.Nop(),
    }

    // Should return true from cache without API call
    result := b.VerifyInstallation(context.Background(), 100)
    assert.True(t, result)
}

func TestVerifyInstallation_UsesNegativeCache(t *testing.T) {
    cache := NewClientCache(10*time.Minute, 100, nil)
    cache.PutNegative(100)

    b := &Base{
        ClientCache: cache,
        Logger:      zerolog.Nop(),
    }

    // Should return false from negative cache
    result := b.VerifyInstallation(context.Background(), 100)
    assert.False(t, result)
}

func TestVerifyInstallation_CachesNegativeResult(t *testing.T) {
    cache := NewClientCache(10*time.Minute, 100, nil)

    mockCreator := &MockClientCreator{
        NewAppClientFunc: func() (*github.Client, error) {
            return mockClientThatReturns404(), nil
        },
    }

    b := &Base{
        ClientCache:   cache,
        ClientCreator: mockCreator,
        Logger:        zerolog.Nop(),
    }

    // First call should cache negative result
    result := b.VerifyInstallation(context.Background(), 100)
    assert.False(t, result)

    // Should be negatively cached now
    assert.True(t, cache.IsNegativelyCached(100))
}
```

**Acceptance Criteria**:
- [ ] Test cache hit path
- [ ] Test negative cache hit path
- [ ] Test negative caching on 404

---

# Remaining Solutions (P2 and P3)

## Solution 5: 429 Feedback Loop (P2)
See solution_application.md for detailed implementation.

**Key Changes**:
- Update `adaptiveTransport.RoundTrip` to handle 429 responses
- Immediately reduce rate on 429
- Parse Retry-After header
- Add metrics for rate limit exceeded events

## Solution 9: Owner ID Validation (P2)
See solution_application.md for detailed implementation.

**Key Changes**:
- Add `OwnerName` field to `CachedClients`
- Add `GetWithValidation` method
- Update `GetClientsByOwner` to validate

## Solution 7: GraphQL Rate Limiting (P2)
See solution_application.md for detailed implementation.

**Key Changes**:
- Create new `graphql_rate_limiter.go`
- Point-based rate limiting
- Query cost estimation
- Response parsing for adaptive adjustment

## Solution 2: Webhook Rate Limiting (P3)
See solution_application.md for detailed implementation.

**Key Changes**:
- Create priority rate limiting
- Different burst settings for webhooks vs SQS
- Careful tuning to avoid webhook timeouts

---

# Testing Strategy

## Unit Test Coverage Goals
- Each new function should have >80% coverage
- Edge cases explicitly tested
- Error paths tested

## Integration Test Scenarios
1. Circuit breaker isolation between installations
2. Concurrency limiting under load
3. Graceful shutdown with active requests
4. Cache hit/miss scenarios

## Load Test Scenarios
1. 100+ concurrent requests to verify concurrency limiting
2. Rapid installation failures to verify circuit breaker isolation
3. Mixed webhook + SQS load to verify rate limiting

---

# Rollback Plan

For each solution, ensure:
1. Feature flags where possible
2. Backward-compatible changes
3. Metrics to detect issues
4. Easy config to disable new behavior

---

# Success Metrics

| Solution | Key Metric | Target |
|----------|-----------|--------|
| 1. Circuit Breakers | Installation isolation | 0 cross-installation blocks |
| 6. Concurrency | 429 secondary rate limit errors | <1/hour |
| 4. Shutdown | Goroutine leaks | 0 after shutdown |
| 3. Error Classification | False positive retries | <1% |
| 8. AppClient Caching | JWT regenerations | 1 per app lifetime |
| 10. VerifyInstallation | API calls saved | >90% cache hit |
