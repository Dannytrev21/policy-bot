# Policy Bot Solutions

## Methodology: Tree of Thought

For each critical issue, I apply the Tree of Thought (ToT) approach:
1. **Problem Definition**: Clearly state the issue
2. **Solution Branches**: Generate 3+ alternative solutions
3. **Evaluation**: Score each on complexity, risk, effectiveness
4. **Selection**: Choose the optimal path
5. **Implementation**: Detail the chosen solution

---

## Solution 1: Per-Installation Circuit Breakers

### Problem Definition
A single circuit breaker is shared across all installations. When one installation fails 5 times (e.g., deleted app), ALL installations are blocked for 60 seconds.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to isolate failures    │
                    │  per installation?          │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Per-Install  │        │  Per-Org      │        │   Hybrid      │
│  Circuit Brk  │        │  Circuit Brk  │        │  (Tiered)     │
└───────────────┘        └───────────────┘        └───────────────┘
```

#### Branch A: Per-Installation Circuit Breaker

**Implementation**:
```go
type InstallationManager struct {
    clientCreator   githubapp.ClientCreator
    circuitBreakers sync.Map  // map[int64]*CircuitBreaker (key = installationID)
    clientCache     *ClientCache
}

func (m *InstallationManager) getCircuitBreaker(installationID int64) *CircuitBreaker {
    if cb, ok := m.circuitBreakers.Load(installationID); ok {
        return cb.(*CircuitBreaker)
    }
    newCB := NewCircuitBreaker()
    actual, _ := m.circuitBreakers.LoadOrStore(installationID, newCB)
    return actual.(*CircuitBreaker)
}
```

**Evaluation**:
- ✅ Perfect isolation: One installation's failures don't affect others
- ✅ Simple implementation: Just change from single to map
- ⚠️ Memory: One circuit breaker per active installation
- ⚠️ No global protection: GitHub API issues won't trip all breakers

**Score**: 8/10

#### Branch B: Per-Org Circuit Breaker

**Implementation**:
```go
type InstallationManager struct {
    circuitBreakers sync.Map  // map[string]*CircuitBreaker (key = org name)
}
```

**Evaluation**:
- ✅ Semantically correct for GHEC (one installation = one org)
- ✅ Works well for org-level permissions
- ⚠️ Requires passing org name to GetClients
- ⚠️ Inconsistent with installation ID-based caching

**Score**: 7/10

#### Branch C: Hybrid Tiered Circuit Breakers

**Implementation**:
```go
type InstallationManager struct {
    globalCircuitBreaker *CircuitBreaker       // For GitHub API outages
    installationBreakers sync.Map              // Per-installation issues
}

func (m *InstallationManager) GetClients(...) (*InstallationClients, error) {
    // Check global first (service-level issues)
    if !m.globalCircuitBreaker.Allow() {
        return nil, errors.New("GitHub API unavailable")
    }

    // Check installation-specific breaker
    instCB := m.getInstallationBreaker(installationID)
    if !instCB.Allow() {
        return nil, fmt.Errorf("installation %d circuit open", installationID)
    }
    // ...
}
```

**Evaluation**:
- ✅ Best of both worlds: isolates installation issues + detects global outages
- ✅ Global breaker uses higher threshold (e.g., 50 failures across all)
- ⚠️ Most complex implementation
- ⚠️ Need to carefully tune thresholds

**Score**: 9/10

### Selected Solution: Branch C (Hybrid)

**Rationale**: Provides both isolation for individual installation issues AND protection against GitHub-wide outages.

### Detailed Implementation

```go
// File: server/handler/installation_manager.go

const (
    // Per-installation circuit breaker
    instCircuitBreakerThreshold = 5
    instCircuitBreakerTimeout   = 60 * time.Second

    // Global circuit breaker (higher threshold for true outages)
    globalCircuitBreakerThreshold = 50
    globalCircuitBreakerTimeout   = 120 * time.Second
)

type InstallationManager struct {
    clientCreator        githubapp.ClientCreator
    metricsRegistry      gometrics.Registry
    globalCircuitBreaker *CircuitBreaker
    installationBreakers sync.Map // map[int64]*CircuitBreaker
    clientCache          *ClientCache
}

func NewInstallationManager(...) *InstallationManager {
    return &InstallationManager{
        clientCreator:        clientCreator,
        metricsRegistry:      metricsRegistry,
        globalCircuitBreaker: NewCircuitBreakerWithConfig(
            globalCircuitBreakerThreshold,
            globalCircuitBreakerTimeout,
        ),
        clientCache: NewClientCache(defaultClientCacheTTL, defaultClientCacheMaxSize),
    }
}

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

func (m *InstallationManager) GetClients(ctx context.Context, installationID int64, repoFullName string) (*InstallationClients, error) {
    // ... existing cache check ...

    // Check global circuit breaker first
    if !m.globalCircuitBreaker.Allow() {
        return nil, fmt.Errorf("global circuit breaker open: GitHub API may be unavailable")
    }

    // Check per-installation circuit breaker
    instCB := m.getInstallationBreaker(installationID)
    if !instCB.Allow() {
        return nil, fmt.Errorf("circuit breaker open for installation %d", installationID)
    }

    // ... client creation ...

    // On failure, record to both breakers
    if isRetryableError(err) {
        m.globalCircuitBreaker.RecordFailure()
        instCB.RecordFailure()
    }

    // On success, record to both
    m.globalCircuitBreaker.RecordSuccess()
    instCB.RecordSuccess()
}

// NewCircuitBreakerWithConfig allows configurable thresholds
func NewCircuitBreakerWithConfig(threshold int, timeout time.Duration) *CircuitBreaker {
    return &CircuitBreaker{
        state:     CircuitBreakerClosed,
        threshold: threshold,
        timeout:   timeout,
    }
}
```

**Migration Path**:
1. Add `threshold` and `timeout` fields to `CircuitBreaker`
2. Add `NewCircuitBreakerWithConfig` constructor
3. Add `installationBreakers sync.Map` to `InstallationManager`
4. Update `GetClients` to check both breakers
5. Add metrics for per-installation breaker states

---

## Solution 2: Rate Limiting for Webhooks

### Problem Definition
Webhooks are not rate-limited, only SQS processing is. This can cause 429 errors during webhook storms.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  Should webhooks be rate    │
                    │  limited? How?              │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│ Rate-limit    │        │ Shared rate   │        │ Document as   │
│ webhooks too  │        │ limit pool    │        │ intentional   │
└───────────────┘        └───────────────┘        └───────────────┘
```

#### Branch A: Rate-limit Webhooks Independently

**Implementation**: Wrap webhook client creators with rate limiter

**Evaluation**:
- ✅ Prevents webhook 429s
- ⚠️ Delays webhook processing (bad UX)
- ⚠️ GitHub expects fast webhook responses (< 10s)
- ❌ Could cause webhook timeouts

**Score**: 4/10

#### Branch B: Shared Rate Limit Pool

**Implementation**: Single rate limiter shared between webhooks and SQS

```go
// In server.go
sharedRateLimiter := handler.NewRateLimitedClientCreator(...)

// Use for BOTH webhook and SQS handlers
enterpriseBasePolicyHandler := handler.Base{
    ClientCreator: sharedRateLimiter,  // Same limiter
}
sqsEnterpriseBasePolicyHandler := handler.Base{
    ClientCreator: sharedRateLimiter,  // Same limiter
}
```

**Evaluation**:
- ✅ Unified rate limiting
- ✅ SQS and webhooks share the same quota fairly
- ⚠️ Webhook latency increases under load
- ⚠️ Need careful tuning to avoid webhook timeouts

**Score**: 6/10

#### Branch C: Document as Intentional Design

**Implementation**: Document why webhooks aren't rate-limited

**Rationale**:
- Webhooks are time-sensitive (GitHub expects quick response)
- SQS is asynchronous (can wait)
- Webhooks typically process single events
- SQS processes bulk events (e.g., batch re-evaluation)

**Evaluation**:
- ✅ No code changes
- ✅ Maintains webhook responsiveness
- ⚠️ Doesn't solve 429 during storms
- ⚠️ Relies on GitHub's own rate limiting

**Score**: 5/10

### Selected Solution: Branch B with Priority Queuing

**Hybrid Approach**: Shared rate limiter with webhook priority

```go
type PriorityRateLimitedClientCreator struct {
    base          githubapp.ClientCreator
    webhookPool   *rate.Limiter  // Higher burst for webhooks
    sqsPool       *rate.Limiter  // Standard rate for SQS
    globalLimiter *rate.Limiter  // Overall cap
}

func (r *PriorityRateLimitedClientCreator) NewWebhookClient(ctx context.Context, ...) (*github.Client, error) {
    // Wait on global + webhook pool (faster)
    r.globalLimiter.Wait(ctx)
    r.webhookPool.Wait(ctx)  // Higher burst, same rate
    return r.base.NewInstallationClient(installationID)
}

func (r *PriorityRateLimitedClientCreator) NewSQSClient(ctx context.Context, ...) (*github.Client, error) {
    // Wait on global + SQS pool (may wait longer)
    r.globalLimiter.Wait(ctx)
    r.sqsPool.Wait(ctx)
    return r.base.NewInstallationClient(installationID)
}
```

**Configuration**:
- Webhook pool: Same rate (3 req/s) but higher burst (30)
- SQS pool: Same rate (3 req/s) but lower burst (10)
- Global: 100 req/s (unchanged)

---

## Solution 3: Consistent Error Classification

### Problem Definition
`IsRetryableError` uses string matching which is fragile. Better to use `classifyGitHubError` consistently.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to improve error       │
                    │  classification?            │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Type switch  │        │  Error types  │        │  Wrapper      │
│  on known err │        │  with codes   │        │  function     │
└───────────────┘        └───────────────┘        └───────────────┘
```

### Selected Solution: Branch A (Type Switch)

**Rationale**: Use `errors.As` to check for known error types first, fallback to string matching only as last resort.

```go
// File: server/handler/errors.go

func IsRetryableError(err error) bool {
    if err == nil {
        return false
    }

    // 1. Check for GitHub library errors (most reliable)
    var rlErr *github.RateLimitError
    if errors.As(err, &rlErr) {
        return true  // Rate limit is retryable
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

    // 3. Only fallback to string matching for truly unknown errors
    errMsg := strings.ToLower(err.Error())
    unknownRetryable := []string{
        "connection reset",
        "tls handshake timeout",
        "no such host",
    }
    for _, pattern := range unknownRetryable {
        if strings.Contains(errMsg, pattern) {
            return true
        }
    }

    return false  // Default: don't retry unknown errors
}
```

---

## Solution 4: Graceful Shutdown for ClientCache

### Problem Definition
`ClientCache` background goroutines aren't stopped during server shutdown.

### Selected Solution

**Implementation**: Add shutdown hooks to server

```go
// File: server/server.go

func (s *Server) Start() error {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // ... existing code ...

    // Add cleanup for InstallationManagers
    defer func() {
        // Stop client caches (added handlers)
        enterpriseBasePolicyHandler.StopInstallationManager()
        cloudBasePolicyHandler.StopInstallationManager()
        sqsEnterpriseBasePolicyHandler.StopInstallationManager()
        sqsCloudBasePolicyHandler.StopInstallationManager()
    }()

    return s.base.Start()
}

// File: server/handler/base.go
func (b *Base) StopInstallationManager() {
    if b.installationManager != nil {
        b.installationManager.StopClientCache()
    }
}
```

---

## Solution 5: 429 Response Feedback Loop

### Problem Definition
When GitHub returns 429, the adaptive rate limiter doesn't immediately reduce rate.

### Selected Solution

**Implementation**: Handle 429 in adaptive transport

```go
// File: server/handler/rate_limiter.go

func (t *adaptiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    resp, err := t.base.RoundTrip(req)
    if err != nil {
        return resp, err
    }

    // Handle 429 responses immediately
    if resp.StatusCode == http.StatusTooManyRequests {
        t.handleRateLimitExceeded(resp)
    }

    // Also process headers on successful responses
    if remaining, limit, reset, ok := parseGitHubRateLimitHeaders(resp); ok {
        go t.creator.updateAdaptiveRate(t.owner, remaining, limit, reset)
    }

    return resp, nil
}

func (t *adaptiveTransport) handleRateLimitExceeded(resp *http.Response) {
    // Get Retry-After header
    retryAfter := resp.Header.Get("Retry-After")
    if retryAfter == "" {
        // Default to 60 seconds if no header
        retryAfter = "60"
    }

    seconds, err := strconv.Atoi(retryAfter)
    if err != nil {
        seconds = 60
    }

    // Immediately reduce rate to minimum
    t.creator.updateOrgLimiter(t.owner, t.creator.config.Adaptive.MinRate)

    t.creator.logger.Warn().
        Str("owner", t.owner).
        Int("retry_after", seconds).
        Msg("Rate limit exceeded, reducing rate")

    // Record metric
    if t.creator.registry != nil {
        if counter := t.creator.registry.Get("handler.rate_limit.exceeded"); counter != nil {
            if c, ok := counter.(metrics.Counter); ok {
                c.Inc(1)
            }
        }
    }
}
```

---

## Implementation Priority Matrix

| Solution | Effort | Impact | Priority |
|----------|--------|--------|----------|
| 1. Per-Installation Circuit Breakers | Medium | High | P0 |
| 4. Graceful Shutdown | Low | Medium | P1 |
| 3. Consistent Error Classification | Low | Medium | P1 |
| 5. 429 Feedback Loop | Medium | Medium | P2 |
| 2. Webhook Rate Limiting | High | Medium | P3 |

---

## Migration Path

### Phase 1: Quick Wins (1-2 days)
1. Fix error classification (Solution 3)
2. Add graceful shutdown (Solution 4)
3. Update documentation for cache key naming

### Phase 2: Core Improvements (3-5 days)
1. Implement hybrid circuit breakers (Solution 1)
2. Add 429 feedback loop (Solution 5)

### Phase 3: Architecture (1 week)
1. Evaluate webhook rate limiting needs
2. Implement priority queuing if needed (Solution 2)

---

## Testing Strategy

### Unit Tests
```go
func TestPerInstallationCircuitBreaker(t *testing.T) {
    m := NewInstallationManager(...)

    // Trip circuit for installation A
    for i := 0; i < 5; i++ {
        m.getInstallationBreaker(100).RecordFailure()
    }

    // Installation A should be blocked
    assert.False(t, m.getInstallationBreaker(100).Allow())

    // Installation B should still work
    assert.True(t, m.getInstallationBreaker(200).Allow())
}

func TestGlobalCircuitBreaker(t *testing.T) {
    m := NewInstallationManager(...)

    // Trip global circuit with many failures across installations
    for i := 0; i < 50; i++ {
        m.globalCircuitBreaker.RecordFailure()
    }

    // All installations should be blocked
    assert.False(t, m.globalCircuitBreaker.Allow())
}
```

### Integration Tests
- Simulate GitHub 429 response and verify rate reduction
- Test graceful shutdown with active requests
- Verify circuit breaker recovery timing

---

---

# New Solutions (Additional Analysis)

---

## Solution 6: Concurrent Request Limiting (Critique 13)

### Problem Definition
GitHub enforces secondary rate limits including a maximum of 100 concurrent requests per installation. The current rate limiter only tracks request count per second, not concurrent in-flight requests.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to limit concurrent    │
                    │  requests?                  │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Semaphore    │        │  Channel-     │        │  Weighted     │
│  (counting)   │        │  based pool   │        │  semaphore    │
└───────────────┘        └───────────────┘        └───────────────┘
```

#### Branch A: Counting Semaphore

**Implementation**:
```go
type RateLimitedClientCreator struct {
    // ... existing fields ...

    // Concurrent request limiting
    globalConcurrencySem chan struct{}  // Global semaphore (100 capacity)
    orgConcurrencySems   sync.Map       // map[string]chan struct{} per-org
}
```

**Evaluation**:
- ✅ Simple channel-based semaphore
- ✅ Natural Go idiom
- ⚠️ Fixed capacity per org
- ⚠️ No weighting for expensive operations

**Score**: 7/10

#### Branch B: Channel-based Worker Pool

**Implementation**:
```go
type requestTicket struct {
    owner string
    done  chan struct{}
}

type ConcurrencyManager struct {
    tickets chan requestTicket
}
```

**Evaluation**:
- ✅ Centralized management
- ⚠️ More complex implementation
- ⚠️ Potential bottleneck

**Score**: 5/10

#### Branch C: Weighted Semaphore

**Implementation**:
```go
import "golang.org/x/sync/semaphore"

type RateLimitedClientCreator struct {
    globalConcurrencySem *semaphore.Weighted  // 100 weight capacity
    orgConcurrencySems   sync.Map             // map[string]*semaphore.Weighted
}
```

**Evaluation**:
- ✅ Standard library support
- ✅ Allows different weights for operations
- ✅ Context-aware (can timeout)
- ⚠️ Slightly more complex API

**Score**: 9/10

### Selected Solution: Branch C (Weighted Semaphore)

**Rationale**: `golang.org/x/sync/semaphore` provides context-aware acquisition with timeout support, preventing deadlocks and integrating well with Go patterns.

### Detailed Implementation

```go
// File: server/handler/rate_limiter.go

import "golang.org/x/sync/semaphore"

const (
    // GitHub allows 100 concurrent requests per installation
    DefaultGlobalConcurrencyLimit = 100
    DefaultOrgConcurrencyLimit    = 50  // Conservative per-org limit

    // Weights for different operations
    ConcurrencyWeightStandard     = 1
    ConcurrencyWeightGraphQL      = 2   // GraphQL queries more expensive
    ConcurrencyWeightContentWrite = 3   // Creating content uses more resources
)

type RateLimitedClientCreator struct {
    base githubapp.ClientCreator
    config *RateLimitConfig

    // Existing rate limiters
    orgLimiters   sync.Map
    globalLimiter *rate.Limiter

    // NEW: Concurrent request limiters
    globalConcurrencySem *semaphore.Weighted
    orgConcurrencySems   sync.Map  // map[string]*semaphore.Weighted
}

func NewRateLimitedClientCreator(...) *RateLimitedClientCreator {
    // ... existing code ...

    rlcc := &RateLimitedClientCreator{
        // ... existing fields ...

        // Initialize global concurrency semaphore
        globalConcurrencySem: semaphore.NewWeighted(DefaultGlobalConcurrencyLimit),
    }

    return rlcc
}

func (r *RateLimitedClientCreator) getOrgConcurrencySem(owner string) *semaphore.Weighted {
    if sem, ok := r.orgConcurrencySems.Load(owner); ok {
        return sem.(*semaphore.Weighted)
    }
    newSem := semaphore.NewWeighted(int64(DefaultOrgConcurrencyLimit))
    actual, _ := r.orgConcurrencySems.LoadOrStore(owner, newSem)
    return actual.(*semaphore.Weighted)
}

// acquireConcurrencySlot acquires both global and per-org concurrency slots
func (r *RateLimitedClientCreator) acquireConcurrencySlot(ctx context.Context, owner string, weight int64) error {
    // Try to acquire global slot first
    if err := r.globalConcurrencySem.Acquire(ctx, weight); err != nil {
        return fmt.Errorf("global concurrency limit reached: %w", err)
    }

    // Try to acquire org slot
    orgSem := r.getOrgConcurrencySem(owner)
    if err := orgSem.Acquire(ctx, weight); err != nil {
        // Release global slot if org acquisition fails
        r.globalConcurrencySem.Release(weight)
        return fmt.Errorf("org %s concurrency limit reached: %w", owner, err)
    }

    return nil
}

// releaseConcurrencySlot releases both global and per-org concurrency slots
func (r *RateLimitedClientCreator) releaseConcurrencySlot(owner string, weight int64) {
    orgSem := r.getOrgConcurrencySem(owner)
    orgSem.Release(weight)
    r.globalConcurrencySem.Release(weight)
}

// Updated NewOrgClient with concurrency limiting
func (r *RateLimitedClientCreator) NewOrgClient(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
    // Wait for rate limit token
    if err := r.waitForOrgRateLimit(ctx, owner); err != nil {
        return nil, err
    }

    // Acquire concurrency slot
    if err := r.acquireConcurrencySlot(ctx, owner, ConcurrencyWeightStandard); err != nil {
        return nil, err
    }

    // Create client with transport that releases slot on response
    client, err := r.base.NewInstallationClient(installationID)
    if err != nil {
        r.releaseConcurrencySlot(owner, ConcurrencyWeightStandard)
        return nil, err
    }

    // Wrap transport to release slot after request completes
    client.Client().Transport = &concurrencyTrackingTransport{
        base:    client.Client().Transport,
        creator: r,
        owner:   owner,
        weight:  ConcurrencyWeightStandard,
    }

    return client, nil
}

type concurrencyTrackingTransport struct {
    base    http.RoundTripper
    creator *RateLimitedClientCreator
    owner   string
    weight  int64
}

func (t *concurrencyTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    resp, err := t.base.RoundTrip(req)
    // Release concurrency slot when response is received
    t.creator.releaseConcurrencySlot(t.owner, t.weight)
    return resp, err
}
```

### Metrics

```go
const (
    MetricsKeyConcurrencyAcquired = "handler.concurrency.acquired"
    MetricsKeyConcurrencyRejected = "handler.concurrency.rejected"
    MetricsKeyConcurrencyWaitTime = "handler.concurrency.wait_time"
)
```

---

## Solution 7: GraphQL Rate Limiting (Critique 14)

### Problem Definition
GraphQL uses point-based rate limiting (5,000 points/hour) but the adaptive rate limiter only parses REST API headers.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to handle GraphQL      │
                    │  point-based limits?        │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Parse JSON   │        │  Estimate     │        │  Query cost   │
│  response     │        │  fixed cost   │        │  introspection│
└───────────────┘        └───────────────┘        └───────────────┘
```

#### Branch A: Parse GraphQL Response

**Implementation**: Parse `extensions.rateLimit` from every GraphQL response

**Evaluation**:
- ✅ Accurate real-time data
- ⚠️ Requires response body parsing
- ⚠️ Performance overhead

**Score**: 6/10

#### Branch B: Fixed Cost Estimation

**Implementation**: Assign estimated point cost to each query type

**Evaluation**:
- ✅ Simple, no parsing needed
- ✅ Conservative estimates prevent issues
- ⚠️ May be inaccurate for complex queries
- ⚠️ Requires maintenance as queries change

**Score**: 7/10

#### Branch C: Query Cost Pre-calculation

**Implementation**: Use GitHub's query cost preview to estimate before executing

**Evaluation**:
- ✅ Accurate cost prediction
- ⚠️ Adds extra API call overhead
- ❌ Not practical for every request

**Score**: 3/10

### Selected Solution: Branch B (Fixed Cost Estimation) + Branch A (Async Feedback)

**Hybrid Approach**: Use conservative estimates for proactive limiting, parse responses asynchronously for adaptive adjustment.

### Detailed Implementation

```go
// File: server/handler/graphql_rate_limiter.go

const (
    // Conservative point costs for common operations
    // Actual costs vary based on query complexity and node count
    GraphQLCostSimpleQuery     = 1    // Simple field queries
    GraphQLCostPRReview        = 5    // PR review with basic fields
    GraphQLCostPRWithReviews   = 10   // PR with reviews and comments
    GraphQLCostComplexQuery    = 25   // Complex nested queries
    GraphQLCostMutation        = 5    // Creating/updating content

    // GitHub GraphQL rate limit
    GraphQLPointsPerHour = 5000
    GraphQLPointsPerSec  = float64(GraphQLPointsPerHour) / 3600  // ~1.39 points/sec
)

type GraphQLRateLimiter struct {
    // Point-based rate limiter (using tokens = points)
    pointLimiter *rate.Limiter

    // Tracking actual point usage from responses
    mu              sync.RWMutex
    currentPoints   int64
    remainingPoints int64
    resetTime       time.Time

    logger   zerolog.Logger
    registry metrics.Registry
}

func NewGraphQLRateLimiter(logger zerolog.Logger, registry metrics.Registry) *GraphQLRateLimiter {
    return &GraphQLRateLimiter{
        // Allow ~1.39 points/sec with burst of 50 points
        pointLimiter:    rate.NewLimiter(rate.Limit(GraphQLPointsPerSec), 50),
        remainingPoints: GraphQLPointsPerHour,
        logger:          logger,
        registry:        registry,
    }
}

// ReservePoints reserves points before executing a query
func (g *GraphQLRateLimiter) ReservePoints(ctx context.Context, estimatedCost int) error {
    // Wait for points to be available
    reservation := g.pointLimiter.ReserveN(time.Now(), estimatedCost)
    if !reservation.OK() {
        return fmt.Errorf("GraphQL rate limit: cannot reserve %d points", estimatedCost)
    }

    delay := reservation.Delay()
    if delay > 0 {
        g.logger.Debug().
            Int("cost", estimatedCost).
            Dur("delay", delay).
            Msg("GraphQL rate limit: waiting for points")

        select {
        case <-time.After(delay):
        case <-ctx.Done():
            reservation.Cancel()
            return ctx.Err()
        }
    }

    return nil
}

// UpdateFromResponse updates rate limit state from GraphQL response
func (g *GraphQLRateLimiter) UpdateFromResponse(response map[string]interface{}) {
    extensions, ok := response["extensions"].(map[string]interface{})
    if !ok {
        return
    }

    rateLimit, ok := extensions["rateLimit"].(map[string]interface{})
    if !ok {
        return
    }

    g.mu.Lock()
    defer g.mu.Unlock()

    if cost, ok := rateLimit["cost"].(float64); ok {
        g.currentPoints += int64(cost)
    }

    if remaining, ok := rateLimit["remaining"].(float64); ok {
        g.remainingPoints = int64(remaining)

        // Adjust rate if running low
        if g.remainingPoints < 500 {
            g.pointLimiter.SetLimit(rate.Limit(0.5))  // Slow down
            g.logger.Warn().
                Int64("remaining", g.remainingPoints).
                Msg("GraphQL points running low, reducing rate")
        }
    }

    if resetAt, ok := rateLimit["resetAt"].(string); ok {
        if t, err := time.Parse(time.RFC3339, resetAt); err == nil {
            g.resetTime = t
        }
    }
}

// EstimateCost returns estimated cost for a query type
func EstimateGraphQLCost(queryType string) int {
    switch queryType {
    case "pullRequest":
        return GraphQLCostPRReview
    case "pullRequestWithReviews":
        return GraphQLCostPRWithReviews
    case "repository":
        return GraphQLCostSimpleQuery
    case "viewer":
        return GraphQLCostSimpleQuery
    default:
        return GraphQLCostComplexQuery  // Conservative default
    }
}
```

### Integration with Base Handler

```go
// In base.go or where GraphQL calls are made
func (b *Base) executeGraphQLQuery(ctx context.Context, client *githubv4.Client, query interface{}, variables map[string]interface{}, queryType string) error {
    // Reserve points before query
    cost := EstimateGraphQLCost(queryType)
    if err := b.graphQLLimiter.ReservePoints(ctx, cost); err != nil {
        return err
    }

    // Execute query
    err := client.Query(ctx, query, variables)

    // Update from response (would need custom transport to capture)
    // This is illustrative - actual implementation needs response capture

    return err
}
```

---

## Solution 8: AppClient Caching (Critique 15)

### Problem Definition
Every call to `VerifyInstallation()` creates a new App-level client, regenerating the JWT each time.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to cache AppClient?    │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Field on     │        │  sync.Once    │        │  Lazy init    │
│  Base struct  │        │  pattern      │        │  with TTL     │
└───────────────┘        └───────────────┘        └───────────────┘
```

### Selected Solution: Branch B (sync.Once)

**Rationale**: `sync.Once` ensures thread-safe lazy initialization without repeated work.

### Detailed Implementation

```go
// File: server/handler/base.go

type Base struct {
    // ... existing fields ...

    // Cached app client (lazy initialized)
    appClientOnce sync.Once
    appClient     *github.Client
    appClientErr  error
}

// GetAppClient returns a cached app-level client
func (b *Base) GetAppClient() (*github.Client, error) {
    b.appClientOnce.Do(func() {
        b.appClient, b.appClientErr = b.ClientCreator.NewAppClient()
        if b.appClientErr != nil {
            b.Logger.Error().Err(b.appClientErr).Msg("Failed to create app client")
        }
    })
    return b.appClient, b.appClientErr
}

// Updated VerifyInstallation uses cached client
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
    logger := zerolog.Ctx(ctx)

    // Use cached app client instead of creating new one
    appClient, err := b.GetAppClient()
    if err != nil {
        logger.Warn().Err(err).
            Int64("installation_id", installationID).
            Msg("Failed to get app client for installation verification")
        return false
    }

    // Call GitHub API directly to verify installation exists
    _, _, err = appClient.Apps.GetInstallation(ctx, installationID)
    exists := err == nil

    // ... rest unchanged ...

    return exists
}
```

**Benefits**:
- Single JWT generation for app lifetime
- Thread-safe initialization
- Zero performance overhead after first call
- Handles errors gracefully

---

## Solution 9: Owner ID Validation (Critique 20)

### Problem Definition
`GetClientsByOwner` accepts both owner name and ownerID but never validates they match, risking cache pollution.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to validate owner ID   │
                    │  matches owner name?        │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Validate on  │        │  Store both   │        │  Remove owner │
│  cache miss   │        │  in cache     │        │  name from    │
│               │        │  entry        │        │  lookup       │
└───────────────┘        └───────────────┘        └───────────────┘
```

### Selected Solution: Branch B (Store Both in Cache)

**Rationale**: Store owner name alongside ownerID in cache entry, validate on cache hit.

### Detailed Implementation

```go
// File: server/handler/client_cache.go

type CachedClients struct {
    Clients        *InstallationClients
    InstallationID int64
    OwnerName      string    // NEW: Store owner name for validation
    OwnerID        int64     // NEW: Store owner ID for reference
    ExpiresAt      time.Time
    CreatedAt      time.Time
    IsNegative     bool
}

// Get retrieves cached clients with owner name validation
func (c *ClientCache) GetWithValidation(ownerID int64, ownerName string) (*InstallationClients, bool) {
    value, ok := c.cache.Load(ownerID)
    if !ok {
        c.misses.Add(1)
        return nil, false
    }

    entry := value.(*CachedClients)

    // Check expiration
    if time.Now().After(entry.ExpiresAt) {
        c.cache.Delete(ownerID)
        c.misses.Add(1)
        return nil, false
    }

    // NEW: Validate owner name matches
    if entry.OwnerName != "" && entry.OwnerName != ownerName {
        // Log warning - potential misuse or data inconsistency
        zerolog.Logger{}.Warn().
            Int64("owner_id", ownerID).
            Str("cached_owner", entry.OwnerName).
            Str("requested_owner", ownerName).
            Msg("Owner name mismatch for cached client - returning cache miss")
        c.misses.Add(1)
        return nil, false
    }

    // Check for negative cache
    if entry.IsNegative {
        c.hits.Add(1)
        return nil, true  // Second bool indicates "found but negative"
    }

    c.hits.Add(1)
    return entry.Clients, true
}

// PutWithOwner stores clients with owner information for validation
func (c *ClientCache) PutWithOwner(ownerID int64, ownerName string, clients *InstallationClients, installationID int64) {
    entry := &CachedClients{
        Clients:        clients,
        InstallationID: installationID,
        OwnerName:      ownerName,
        OwnerID:        ownerID,
        ExpiresAt:      time.Now().Add(c.ttl),
        CreatedAt:      time.Now(),
        IsNegative:     false,
    }
    c.cache.Store(ownerID, entry)
}
```

### Updated GetClientsByOwner

```go
func (b *Base) GetClientsByOwner(ctx context.Context, owner string, ownerID ...int64) (*InstallationClients, error) {
    // ... existing validation ...

    // Check cache with validation
    if b.ClientCache != nil && actualOwnerID > 0 {
        clients, found := b.ClientCache.GetWithValidation(actualOwnerID, owner)
        if found {
            if clients != nil {
                return clients, nil
            }
            // Negative cache hit
            return nil, fmt.Errorf("installation not found for owner %s (negatively cached)", owner)
        }
    }

    // ... rest of lookup ...

    // Store with owner name for future validation
    if b.ClientCache != nil && actualOwnerID > 0 {
        b.ClientCache.PutWithOwner(actualOwnerID, owner, clients, installationID)
    }

    return clients, nil
}
```

---

## Solution 10: VerifyInstallation Caching (Critique 21)

### Problem Definition
`VerifyInstallation` makes a direct API call every time, bypassing all caching mechanisms.

### Tree of Thought

```
                    ┌─────────────────────────────┐
                    │  How to cache verification  │
                    │  results?                   │
                    └──────────────┬──────────────┘
                                   │
        ┌──────────────────────────┼──────────────────────────┐
        │                          │                          │
        ▼                          ▼                          ▼
┌───────────────┐        ┌───────────────┐        ┌───────────────┐
│   Branch A    │        │   Branch B    │        │   Branch C    │
│  Short-lived  │        │  Use existing │        │  Remove       │
│  TTL cache    │        │  ClientCache  │        │  verification │
└───────────────┘        └───────────────┘        └───────────────┘
```

### Selected Solution: Branch B (Use Existing ClientCache)

**Rationale**: If an installation has valid clients in cache, it's verified. No need for separate verification.

### Detailed Implementation

```go
// File: server/handler/base.go

// VerifyInstallation checks if the GitHub App is installed for the given installation ID.
// OPTIMIZED: Uses ClientCache first before making API call.
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
    logger := zerolog.Ctx(ctx)

    // OPTIMIZATION 1: Check if we have cached clients for this installation
    // If we do, the installation is verified (clients require valid installation)
    if b.ClientCache != nil {
        // For GHEC, installation ID maps to owner ID
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
    appClient, err := b.GetAppClient()  // Uses sync.Once cached client
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
        }
    } else {
        logger.Debug().
            Int64("installation_id", installationID).
            Msg("Installation verified via API call")
    }

    // Update legacy cache for backwards compatibility if installation exists
    if exists {
        b.mu.Lock()
        b.InstallationIdMap[installationID] = installationID
        b.mu.Unlock()
    }

    return exists
}
```

**Benefits**:
1. Cache-first approach eliminates most API calls
2. Uses cached AppClient (Solution 8)
3. Stores negative results to prevent repeated checks
4. Maintains backwards compatibility

---

## Updated Implementation Priority Matrix

| Solution | Effort | Impact | Priority | Critique |
|----------|--------|--------|----------|----------|
| 1. Per-Installation Circuit Breakers | Medium | High | P0 | #3 |
| 6. Concurrent Request Limiting | Medium | High | P0 | #13 |
| 4. Graceful Shutdown | Low | Medium | P1 | #9 |
| 3. Consistent Error Classification | Low | Medium | P1 | #7 |
| 8. AppClient Caching | Low | Medium | P1 | #15 |
| 10. VerifyInstallation Caching | Low | Medium | P1 | #21 |
| 5. 429 Feedback Loop | Medium | Medium | P2 | #12 |
| 9. Owner ID Validation | Low | Medium | P2 | #20 |
| 7. GraphQL Rate Limiting | High | Medium | P2 | #14 |
| 2. Webhook Rate Limiting | High | Medium | P3 | #2 |

---

## Updated Migration Path

### Phase 1: Quick Wins (1-2 days)
1. Add AppClient caching (Solution 8) - sync.Once pattern
2. Update VerifyInstallation to use caches (Solution 10)
3. Fix error classification (Solution 3)
4. Add graceful shutdown (Solution 4)

### Phase 2: Core Improvements (3-5 days)
1. Implement hybrid circuit breakers (Solution 1)
2. Add concurrent request limiting (Solution 6)
3. Add owner ID validation (Solution 9)

### Phase 3: Advanced Features (1+ week)
1. Add 429 feedback loop (Solution 5)
2. Implement GraphQL rate limiting (Solution 7)
3. Evaluate webhook rate limiting needs (Solution 2)

---

## Summary

This document provides concrete, implementable solutions for the highest-priority issues identified in the critique. The Tree of Thought methodology ensures each solution is well-reasoned with alternatives considered.

Key improvements:
1. **Hybrid Circuit Breakers**: Isolate per-installation failures while detecting global outages
2. **Consistent Error Handling**: Use type assertions over string matching
3. **Graceful Shutdown**: Properly clean up background goroutines
4. **429 Feedback Loop**: Immediately respond to rate limit errors
5. **Priority Webhook Rate Limiting**: Balance responsiveness with protection
6. **Concurrent Request Limiting**: Respect GitHub's 100 concurrent request limit with weighted semaphores
7. **GraphQL Rate Limiting**: Point-based limiting for GraphQL queries
8. **AppClient Caching**: sync.Once pattern for efficient app client reuse
9. **Owner ID Validation**: Prevent cache pollution from mismatched IDs
10. **VerifyInstallation Caching**: Cache-first verification to reduce API calls
