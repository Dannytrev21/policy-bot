# Policy Bot Application Notes

## Overview

Policy Bot is a GitHub App that enforces custom approval policies on pull requests using YAML-based rules. It operates as an HTTP server receiving webhooks and SQS messages, interacting with GitHub via REST (v3) and GraphQL (v4) APIs.

The application supports dual environments:
- **GHES** (GitHub Enterprise Server) - Self-hosted GitHub instances
- **GHEC** (GitHub Enterprise Cloud) - GitHub's cloud offering

The focus is on **org-level installation for GHEC**, where there is ONE installation per organization.

---

## Architecture Overview

```
                    ┌─────────────────────┐
                    │   GitHub Webhooks   │
                    │   (Push, PR, etc.)  │
                    └──────────┬──────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
          ▼                    ▼                    ▼
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│  HTTP Webhook   │  │   SQS Consumer  │  │  Web UI (OAuth) │
│    Handler      │  │    (Async)      │  │   /details/*    │
└────────┬────────┘  └────────┬────────┘  └────────┬────────┘
         │                    │                    │
         └────────────────────┼────────────────────┘
                              │
                    ┌─────────▼─────────┐
                    │   handler.Base    │
                    │  (Core Handler)   │
                    └─────────┬─────────┘
                              │
              ┌───────────────┼───────────────┐
              │               │               │
              ▼               ▼               ▼
    ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
    │ InstallationMgr │ │  ClientCreator  │ │   ConfigFetcher │
    │  (Circuit Brk)  │ │  (Rate Limited) │ │   (AppConfig)   │
    └────────┬────────┘ └────────┬────────┘ └─────────────────┘
             │                   │
             ▼                   ▼
    ┌─────────────────┐ ┌─────────────────┐
    │  ClientCache    │ │  GitHub API     │
    │  (TTL, LRU)     │ │  (v3/v4)        │
    └─────────────────┘ └─────────────────┘
```

---

## Key Components

### 1. Server Initialization (`server/server.go`)

The server creates two separate environments with their own:
- `ClientCreator` instances (enterprise vs cloud)
- `Base` handlers with environment-specific configuration
- Event dispatchers for webhooks
- SQS consumers with rate-limited client creators

**Important Design Decision**: Rate limiting is only applied to SQS processing, NOT webhooks.

```go
// From server.go:203-239
var sqsEnterpriseClientCreator githubapp.ClientCreator = enterpriseClientCreator
var sqsCloudClientCreator githubapp.ClientCreator = cloudClientCreator

if c.RateLimit.Enabled {
    sqsEnterpriseClientCreator = handler.NewRateLimitedClientCreator(...)
    sqsCloudClientCreator = handler.NewRateLimitedClientCreator(...)
}
```

### 2. Client Creator (`go-githubapp/githubapp/client_creator.go`)

The `ClientCreator` interface from the vendor library handles GitHub authentication:

```go
type ClientCreator interface {
    NewAppClient() (*github.Client, error)
    NewAppV4Client() (*githubv4.Client, error)
    NewInstallationClient(installationID int64) (*github.Client, error)
    NewInstallationV4Client(installationID int64) (*githubv4.Client, error)
    NewTokenClient(token string) (*github.Client, error)
    NewTokenV4Client(token string) (*githubv4.Client, error)
}
```

**Token Management**: Uses `ghinstallation` package internally:
- JWT signing for app authentication
- Automatic installation token refresh (tokens valid for 1 hour)
- Token caching handled transparently by `ghinstallation`

### 3. Installation Manager (`server/handler/installation_manager.go`)

Central component for managing GitHub installation clients with resilience patterns:

**Key Features**:
- **Client Caching**: Reuses existing clients to reduce token creation overhead
- **Circuit Breaker**: Prevents cascading failures (threshold=5, timeout=60s)
- **Retry Logic**: Exponential backoff with jitter (max 3 attempts, 1-8s delay)
- **OpenTelemetry Tracing**: Full observability

```go
type InstallationManager struct {
    clientCreator   githubapp.ClientCreator
    metricsRegistry gometrics.Registry
    circuitBreaker  *CircuitBreaker
    clientCache     *ClientCache
}
```

**Circuit Breaker States**:
```
Closed → (5 failures) → Open → (60s timeout) → Half-Open → (1 success) → Closed
                                    ↓
                               (1 failure)
                                    ↓
                                  Open
```

### 4. Client Cache (`server/handler/client_cache.go`)

TTL-based client caching with negative caching support:

**Configuration**:
- Positive TTL: 10 minutes (clients)
- Negative TTL: 2 minutes (not found results)
- Max Size: 1000 entries
- Cleanup Interval: 1 minute

**Key Design**:
- Uses `sync.Map` for lock-free reads (optimized for read-heavy workloads)
- LRU eviction when cache exceeds max size (evicts oldest 10%)
- Negative caching prevents repeated lookups for non-existent installations
- Keys are owner IDs (int64) for GHEC org-level installations

```go
type CachedClients struct {
    Clients        *InstallationClients
    InstallationID int64
    ExpiresAt      time.Time
    CreatedAt      time.Time
    IsNegative     bool  // True if caching "not found" result
}
```

### 5. Rate Limiter (`server/handler/rate_limiter.go`)

Proactive rate limiting to prevent GitHub API 429 errors:

**Two-Level Rate Limiting**:
1. **Per-Org Rate Limit**: 3 req/sec with burst of 10 (per organization)
2. **Global Rate Limit**: 100 req/sec with burst of 50 (across all orgs)

**Token Bucket Algorithm**: Uses `golang.org/x/time/rate.Limiter`

**Adaptive Rate Limiting** (Optional):
- Reads GitHub `X-RateLimit-*` headers
- Calculates safe rate based on remaining quota
- Uses EMA smoothing (factor 0.3)
- Bounds: min 1 req/sec, max 4 req/sec

```go
type RateLimitedClientCreator struct {
    base           githubapp.ClientCreator
    config         *RateLimitConfig
    orgLimiters    sync.Map  // map[string]*rate.Limiter
    globalLimiter  *rate.Limiter
    adaptiveStates sync.Map  // map[string]*adaptiveRateState
}
```

**Preferred Methods for GHEC**:
```go
func (r *RateLimitedClientCreator) NewOrgClient(ctx, owner, installationID) (*github.Client, error)
func (r *RateLimitedClientCreator) NewOrgV4Client(ctx, owner, installationID) (*githubv4.Client, error)
```

### 6. Error Handling (`server/handler/errors.go`)

Centralized error classification:

**Error Categories**:
- **Retryable Errors**: Network errors, 5xx, 429 (rate limit), timeouts
- **Non-Retryable Errors**: 404 (not found), 401/403 (auth), 422 (bad request)

**Key Functions**:
```go
func IsRetryableError(err error) bool
func IsInstallationNotFoundError(err error) bool
func IsAuthenticationError(err error) bool
func classifyGitHubError(err error) (status int, isRateLimit bool, isAuthRelated bool)
```

---

## Authentication Flow

### GitHub App Authentication Hierarchy

```
┌─────────────────────────────────────────────────────────────────┐
│                     GITHUB APP AUTHENTICATION                    │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. App-Level Authentication (JWT)                              │
│     ├── Used for: App metadata, listing installations          │
│     ├── Token: Self-signed JWT using private key                │
│     └── Scope: Global app operations                            │
│                                                                  │
│  2. Installation-Level Authentication (Installation Token)      │
│     ├── Used for: API calls on behalf of an org/repo           │
│     ├── Token: Obtained via App JWT + Installation ID          │
│     ├── Validity: 1 hour (auto-refreshed by ghinstallation)    │
│     └── Scope: Permissions granted during app installation     │
│                                                                  │
│  3. User-Level Authentication (OAuth)                           │
│     ├── Used for: Web UI (/details/*)                          │
│     ├── Token: OAuth2 access token                              │
│     └── Scope: User's repository access                         │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Client Creation Flow

```
GetClients(ctx, installationID, repoFullName)
    │
    ├── 1. Check ClientCache for existing clients
    │       ├── Cache Hit → Return cached clients
    │       └── Cache Miss → Continue
    │
    ├── 2. Check Circuit Breaker
    │       ├── Open → Fail fast with error
    │       └── Closed/Half-Open → Continue
    │
    ├── 3. Create V3 Client (with retry)
    │       ├── Call clientCreator.NewInstallationClient(installationID)
    │       ├── ghinstallation creates JWT → requests token → caches it
    │       └── Retry on transient failures (max 3 attempts)
    │
    ├── 4. Create V4 Client (with retry)
    │       ├── Call clientCreator.NewInstallationV4Client(installationID)
    │       └── Same token management as V3
    │
    ├── 5. Record success/failure with Circuit Breaker
    │
    └── 6. Cache clients for future requests
```

---

## Rate Limiting Strategy

### GHEC-Specific Considerations

For GHEC with **org-level installation** (ONE installation per org):
- Rate limit is effectively per-org
- Installation ID == Org's unique identifier
- 15,000 requests/hour per installation = ~4.16 req/sec theoretical max

### Rate Limit Flow

```
NewOrgClient(ctx, owner, installationID)
    │
    ├── 1. Wait for Global Rate Limit Token
    │       └── Prevents overwhelming GitHub API overall
    │
    ├── 2. Wait for Per-Org Rate Limit Token
    │       └── Ensures fair distribution across orgs
    │
    ├── 3. Create Actual Client
    │       └── base.NewInstallationClient(installationID)
    │
    └── 4. (Optional) Wrap with Adaptive Transport
            └── Reads X-RateLimit headers for dynamic adjustment
```

### Current Configuration

| Parameter | Value | Description |
|-----------|-------|-------------|
| OrgRate | 3.0 req/sec | Per-org rate limit |
| OrgBurst | 10 | Per-org burst capacity |
| GlobalRate | 100.0 req/sec | Global rate limit |
| GlobalBurst | 50 | Global burst capacity |
| Adaptive Enabled | false | Dynamic rate adjustment |

---

## Caching Strategy

### Multi-Level Caching

```
┌─────────────────────────────────────────────────────────────────┐
│                        CACHING LAYERS                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  1. HTTP Cache (httpcache)                                      │
│     ├── Layer: HTTP response caching                            │
│     ├── Size: 50MB (default)                                    │
│     ├── Type: LRU cache                                         │
│     └── Scope: Per ClientCreator                                │
│                                                                  │
│  2. Client Cache (ClientCache)                                   │
│     ├── Layer: GitHub API clients                               │
│     ├── TTL: 10 min (positive), 2 min (negative)               │
│     ├── Size: 1000 entries max                                  │
│     └── Key: Installation ID (int64)                            │
│                                                                  │
│  3. Installation Token Cache (ghinstallation)                   │
│     ├── Layer: Installation access tokens                       │
│     ├── TTL: Until token expiry (~1 hour)                       │
│     └── Managed by: ghinstallation transport                    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Cache Metrics

The ClientCache publishes metrics every 10 seconds:
- `installation.client_cache.hits`
- `installation.client_cache.misses`
- `installation.client_cache.evictions`
- `installation.client_cache.size`
- `installation.client_cache.hit_rate`

---

## Dual Environment Handling

### Server Configuration

```go
// Two separate ClientCreator instances
enterpriseClientCreator := githubapp.NewDefaultCachingClientCreator(c.GithubEnterprise.Config, ...)
cloudClientCreator := githubapp.NewDefaultCachingClientCreator(c.GithubCloud.Config, ...)

// Two separate Base handlers
enterpriseBasePolicyHandler := handler.Base{
    ClientCreator: enterpriseClientCreator,
    GithubCloud:   false,
    ...
}
cloudBasePolicyHandler := handler.Base{
    ClientCreator: cloudClientCreator,
    GithubCloud:   true,
    ...
}
```

### Request Routing

**Webhook Routing** (header-based):
- `X-GitHub-Enterprise-Host` header → Enterprise dispatcher
- `x-dcp-destination-host` header → Cloud dispatcher
- Default → Cloud dispatcher

**Details Page Routing** (path-based):
- `/details/ghes/*` → Enterprise handler
- `/details/ghec/*` → Cloud handler

---

## Resilience Patterns

### 1. Circuit Breaker

**Configuration**:
```go
const (
    circuitBreakerThreshold   = 5         // Consecutive failures to open
    circuitBreakerTimeout     = 60 * time.Second  // Wait before half-open
    circuitBreakerHalfOpenMax = 1         // Successes needed to close
)
```

**Behavior**:
- Only records failures for retryable errors (not 404, 401, 403)
- Metrics: `installation.circuit_breaker.opened_total`, `installation.circuit_breaker.state`

### 2. Retry with Exponential Backoff

**Configuration**:
```go
const (
    maxRetryAttempts = 3
    baseRetryDelay   = 1 * time.Second
    maxRetryDelay    = 8 * time.Second
    retryDelayJitter = 0.2  // 20% jitter
)
```

**Formula**: `delay = baseDelay * 2^attempt * (1 ± jitter)`

### 3. Negative Caching

Prevents repeated API calls for non-existent installations:
- TTL: 2 minutes (shorter than positive cache)
- Use case: App not installed on a repo/org

---

## Metrics & Observability

### OpenTelemetry Integration

- Tracing spans for `InstallationManager.GetClients`
- Attributes: `installation.id`, `repository`, `client.cached`, error types
- Events: `circuit_breaker_opened`, `circuit_breaker_closed`

### go-metrics Registry

All metrics are registered with `gometrics.Registry` for OTEL export to New Relic:
- Rate limit metrics
- Circuit breaker state
- Client cache statistics
- Retry success/exhaustion counters

---

## File Reference Map

| Component | File | Lines |
|-----------|------|-------|
| Server Initialization | `server/server.go` | 1-692 |
| Installation Manager | `server/handler/installation_manager.go` | 1-630 |
| Client Cache | `server/handler/client_cache.go` | 1-521 |
| Rate Limiter | `server/handler/rate_limiter.go` | 1-921 |
| Error Handling | `server/handler/errors.go` | 1-178 |
| Vendor ClientCreator | `vendor/github.com/palantir/go-githubapp/githubapp/client_creator.go` | 1-439 |
| Vendor Installations | `vendor/github.com/palantir/go-githubapp/githubapp/installations.go` | 1-151 |

---

## Summary

Policy Bot implements a robust GitHub App architecture with:

1. **Proper Authentication Hierarchy**: App JWT → Installation Token → API calls
2. **Multi-Layer Caching**: HTTP responses, API clients, installation tokens
3. **Proactive Rate Limiting**: Per-org and global limits, optional adaptive adjustment
4. **Resilience Patterns**: Circuit breaker, retries with backoff, negative caching
5. **Dual Environment Support**: Separate handlers for GHES and GHEC
6. **Full Observability**: OpenTelemetry tracing, comprehensive metrics

The application correctly uses installation IDs for client caching in GHEC org-level scenarios, where one installation serves an entire organization.
