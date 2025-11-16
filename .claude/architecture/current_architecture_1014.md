# Policy Bot Architecture Document (v10.14.2025)
*AI-Optimized Documentation for Claude/LLM Context*

## 🎯 System Overview

### Core Purpose
Policy Bot is a GitHub App that enforces custom approval policies on pull requests using configurable YAML rules. It operates as a long-running HTTP server that:
- Receives GitHub webhook events (directly or via SQS)
- Evaluates pull request state against defined policies
- Posts commit statuses and manages reviewer assignments
- Provides UI for detailed policy evaluation insights

### Key Capabilities
1. **Multi-tenant support**: Handles both GitHub Enterprise Server (GHES) and GitHub Enterprise Cloud (GHEC)
2. **Dual ingestion paths**: HTTP webhooks and/or AWS SQS message queues
3. **Policy engine**: Complex approval rules with predicates, conditions, and logical operators
4. **Auto-reviewer assignment**: Automatically requests reviewers based on rules
5. **Cross-organization support**: Evaluates membership across multiple GitHub organizations

## 🏗️ Architecture Components

### 1. Entry Points & Configuration

#### Main Entry (`main.go` → `cmd/server.go`)
- Uses Cobra CLI framework
- Loads YAML configuration from file
- Environment variable overrides with `POLICYBOT_` prefix
- Bootstraps HTTP server and optionally SQS consumers

#### Configuration Structure (`server/config.go`)
```yaml
Key configuration sections:
├── server:           # baseapp.HTTPConfig (port, TLS, etc.)
├── logging:          # Level (debug/info/warn/error), text/json format
├── cache:            # HTTP cache size, pushed_at cache size
├── github_enterprise: # GHES app config, credentials, URLs
├── github_cloud:     # GHEC app config, credentials, URLs
├── sessions:         # Cookie encryption key, lifetime
├── options:          # Policy evaluation options per environment
├── workers:          # Async worker pool sizing
├── sqs:             # AWS SQS configuration (if enabled)
└── prometheus/datadog: # Metrics & monitoring configs
```

### 2. Server Bootstrap (`server/server.go`)

#### Initialization Flow
1. **Logger creation** - zerolog with configured level/format
2. **Session manager** - SCS cookie-based sessions
3. **HTTP cache setup** - LRU cache per GitHub client (default 50MB)
4. **GitHub client creators** - Separate for enterprise/cloud with:
   - User agent: `policy-bot/<version>`
   - Configurable timeout (default 10s)
   - Request logging at debug level
   - Prometheus metrics middleware
   - HTTP caching via httpcache/lrucache

5. **GitHub App verification** - Validates app installation and permissions
6. **Global caches** - Shared commit timestamp cache (LRU, default 100K entries)
7. **Policy loaders** - Fetches .policy.yml from repos via appconfig.Loader
8. **Handler creation** - Event handlers for both enterprise/cloud stacks
9. **Async schedulers** - QueueAsyncScheduler with configurable workers/queue
10. **Event dispatchers** - Maps GitHub events to handlers with webhook validation
11. **SQS consumer** (optional) - Parallel message processing from queues
12. **HTTP router** - Goji mux with middleware chain
13. **Template loading** - Go templates for UI rendering

### 3. Request Routing Architecture

#### Dual-Stack Architecture
Policy Bot maintains separate handler stacks for Enterprise and Cloud to:
- Use different GitHub API endpoints
- Apply different policy configurations
- Maintain separate caches and metrics
- Support different authentication credentials

#### Header-Based Routing (`server/middleware/header_check.go`)

**Detection Priority Order:**
1. `X-GitHub-Enterprise-Host` header → Routes to Enterprise
2. `x-dcp-destination-host` header → Routes to Cloud
3. `source` query parameter (`?source=enterprise` or `?source=cloud`)
4. Default → Routes to Cloud

**Metrics Tracked:**
- `policy_bot_routing_decisions_total{route, detection_method, handler_type}`
- `policy_bot_routing_latency_seconds{route, handler_type}`

#### Route Map
```
Path-based routes:
├── /api/github/hook       [POST]  → Header-based routing to dispatcher
├── /api/simulate/*/*/*/*  [POST]  → Header-based routing for simulation
├── /api/validate          [PUT]   → Shared policy validation
├── /api/health            [GET]   → Combined health check
├── /api/metrics           [GET]   → Prometheus metrics endpoint
├── /details/ghes/*        [GET]   → Enterprise-specific UI
├── /details/ghec/*        [GET]   → Cloud-specific UI
├── /static/*              [GET]   → Static assets
├── /oauth/callback        [GET]   → OAuth2 callback
└── /                      [GET]   → Index page (header-routed)
```

### 4. Event Processing Pipeline

#### 4.1 HTTP Webhook Flow

```mermaid
sequenceDiagram
    GitHub->>Middleware: POST /api/github/hook
    Middleware->>Middleware: Check headers/params
    Middleware->>Dispatcher: Route to enterprise/cloud
    Dispatcher->>Dispatcher: Verify webhook signature
    Dispatcher->>Scheduler: Enqueue event
    Scheduler->>Handler: Execute in worker goroutine
    Handler->>GitHub: Fetch PR data (REST+GraphQL)
    Handler->>PolicyEngine: Evaluate rules
    Handler->>GitHub: Post status check
```

#### 4.2 SQS Message Flow

```mermaid
sequenceDiagram
    SQS->>Consumer: Long poll (20s default)
    Consumer->>Processor: Parse message
    Processor->>Processor: Detect source from headers
    Processor->>Scheduler: Route to appropriate scheduler
    Scheduler->>Handler: Execute in worker goroutine
    Handler->>GitHub: Process event
    Handler->>Consumer: Success/failure
    Consumer->>SQS: Delete message or retry
```

#### 4.3 Event Handlers (`server/handler/`)

**Core Handlers:**
- `Installation`: GitHub App installation lifecycle
- `MergeGroup`: GitHub merge queue events
- `PullRequest`: PR opened/synchronized/edited/labeled
- `PullRequestReview`: Review submitted/edited/dismissed
- `IssueComment`: Comments on PRs
- `Status`: Commit status updates
- `CheckRun`: Check suite/run completion
- `WorkflowRun`: GitHub Actions workflow completion

**API Handlers:**
- `Simulate`: Test policy evaluation without side effects
- `Validate`: Validate policy YAML syntax
- `Details`: Render PR policy evaluation UI
- `Health`: Health check endpoint
- `Metrics`: Prometheus metrics export

### 5. SQS Integration (`server/sqsconsumer/`)

#### Configuration
```yaml
sqs:
  enabled: true
  region: us-east-1
  endpoint_url: "" # Optional for LocalStack

  # Event type to queue URL mapping
  queues:
    pull_request: "https://sqs.region.amazonaws.com/.../pr-queue"
    pull_request_review: "https://sqs.region.amazonaws.com/.../review-queue"
    issue_comment: "https://sqs.region.amazonaws.com/.../comment-queue"

  # Event routing strategy (per event type)
  event_routing:
    pull_request: "both"     # Process via both HTTP and SQS
    status: "sqs"           # Process only via SQS
    issue_comment: "http"   # Process only via HTTP

  # Worker allocation
  workers_per_queue: 5      # Default workers per queue
  queue_workers:            # Override per event type
    installation: 2         # Low volume
    pull_request: 10        # High volume
    status: 15             # Very high volume

  # SQS settings
  max_messages: 10          # Messages per receive (1-10)
  visibility_timeout: 30    # Seconds before retry
  wait_time_seconds: 20     # Long polling duration
  shutdown_timeout: 30s     # Graceful shutdown timeout

  # Retry configuration
  enable_retry: true
  max_retries: 3
```

#### Message Format
**Structured format (preferred):**
```json
{
  "event_type": "pull_request",
  "delivery_id": "12345678-1234-1234-1234-123456789012",
  "headers": {
    "X-GitHub-Enterprise-Host": "github.company.com",
    "X-GitHub-Event": "pull_request",
    "X-GitHub-Delivery": "12345678-1234-1234-1234-123456789012"
  },
  "payload": { /* GitHub webhook payload */ },
  "source": "sqs",
  "retry_count": 0
}
```

#### Source Detection Logic
1. Check `headers["X-GitHub-Enterprise-Host"]` → Enterprise
2. Check `headers["Host"]` for known enterprise domains → Enterprise
3. Check legacy `source` field → Enterprise/Cloud
4. Default → Cloud

#### Processing Flow
1. **Consumer** (`consumer.go`):
   - Spawns configurable workers per queue
   - Long-polls SQS with wait time
   - Handles graceful shutdown with timeout
   - Monitors queue health and DLQ

2. **Processor** (`processor.go`):
   - Parses SQS message body
   - Detects enterprise vs cloud source
   - Maps event type to appropriate handler
   - Uses same scheduler as HTTP path
   - Records metrics per event type
   - Handles retry with exponential backoff

3. **Context Enrichment**:
   - Adds SQS metadata to context
   - Preserves delivery ID for tracing
   - Tracks processing latency
   - Maintains idempotency via delivery ID

### 6. Policy Engine (`policy/` package)

#### Policy Structure
```yaml
# .policy.yml in repository root
policy:
  approval:
    - or:
      - rule1
      - and:
        - rule2
        - rule3
  disapproval:
    requires:
      organizations: ["org1"]

approval_rules:
  - name: "rule1"
    if:                    # Predicates (when rule applies)
      changed_files:
        paths: ["src/.*"]
    options:               # Behavior modifiers
      invalidate_on_push: true
      request_review:
        enabled: true
    requires:              # What satisfies the rule
      count: 2
      teams: ["org/team"]
```

#### Evaluation Flow (`handler/eval_context.go`)
1. **ParseConfig**: Load and validate YAML policy
2. **Trigger matching**: Skip evaluation if trigger doesn't match
3. **EvaluatePolicy**:
   - Build rule evaluators from definitions
   - Evaluate predicates (if conditions)
   - Check requirements (approvals/conditions)
   - Aggregate results (approved/pending/disapproved)
4. **Post status**: Format as `<context>: <branch>`
5. **Post-evaluation actions**:
   - Auto-assign reviewers if configured
   - Dismiss stale reviews
   - Update UI data

#### Rule Components
- **Predicates** (`policy/predicate/`): Conditions for rule activation
- **Requirements**: Approval counts, users, teams, permissions
- **Options**: Behavioral modifiers (allow_author, invalidate_on_push, etc.)
- **Logical operators**: `and`, `or` for combining rules (max depth: 5)

### 7. GitHub Integration (`pull/` package)

#### Client Architecture
- **REST Client**: Standard GitHub API v3 operations
- **GraphQL Client**: Complex queries, v4-only features
- **Caching**:
  - HTTP response cache (LRU, configurable size)
  - Commit pushed-at times (global LRU cache)
  - Collaborator permissions (per-request cache)

#### Key Components
- **GitHubContext** (`github.go`): Central context for PR data
  - Lazy loads and caches: files, commits, comments, reviews
  - Handles pagination automatically
  - Provides unified interface for policy evaluation

- **Membership** (`github_membership.go`):
  - Organization membership checks
  - Team membership resolution
  - Cross-organization support

- **Error Handling**:
  - Temporary errors trigger retries
  - Rate limit handling with backoff
  - GraphQL vs REST fallback strategies

### 8. UI Layer

#### Template System
- **Templates** (`server/templates/`): Go templates
- **Static assets** (`server/assets/`): CSS, JS via Webpack/Tailwind
- **Dynamic loading**: Templates reference GitHub URLs dynamically

#### Key Pages
- **Index**: Landing page with app overview
- **Details**: Per-PR policy evaluation tree
  - Shows rule hierarchy
  - Indicates approval status per rule
  - Lists required/actual approvers
  - Displays evaluation timeline

#### Authentication
- OAuth2 flow via GitHub App
- SCS cookie sessions
- RequireLogin middleware for protected routes

### 9. Observability

#### Metrics (Prometheus)
**HTTP Metrics:**
- Request rate, latency, errors
- GitHub API call metrics
- Cache hit rates

**SQS Metrics:**
- `sqs.messages.processed{event_type}`
- `sqs.messages.failed{event_type}`
- `sqs.processing.time{event_type}`
- `sqs.queue.depth{queue_name}`
- `sqs.dlq.messages{queue_name}`

**Routing Metrics:**
- `policy_bot_routing_decisions_total`
- `policy_bot_routing_latency_seconds`

#### Logging (Zerolog)
- Structured JSON or text format
- Context propagation with request IDs
- Level-based filtering
- Enhanced for SQS with queue metadata

#### Health Checks
**HTTP Health** (`/api/health`):
- Basic liveness check
- Returns 200 OK if server running

**SQS Health** (internal):
- Queue connectivity
- Message processing status
- DLQ monitoring
- Worker pool status

### 10. Testing Infrastructure

#### Test Types
1. **Unit tests**: Package-level testing
2. **Integration tests** (`test/integration_test.go`):
   - LocalStack for SQS simulation
   - Mock GitHub API responses
   - End-to-end event processing

3. **Performance tests** (`test/performance_test.go`):
   - Queue throughput testing
   - Concurrent event handling
   - Memory/CPU profiling

#### Test Helpers
- `localstack_helpers.go`: LocalStack container management
- `test_helpers.go`: GitHub API mocking
- `scripts/test-event-processing.go`: Manual event replay

## 🔄 Event Processing Workflows

### Workflow 1: Pull Request Update via HTTP
1. Developer pushes commit to PR branch
2. GitHub sends webhook to `/api/github/hook`
3. Middleware detects enterprise/cloud from headers
4. Dispatcher verifies webhook signature
5. Event enqueued to async scheduler
6. Worker pulls from queue, creates handler context
7. Handler fetches PR data via REST+GraphQL
8. Policy engine evaluates all rules
9. Status posted to GitHub commit
10. Reviewers auto-assigned if configured

### Workflow 2: Review Submitted via SQS
1. Developer submits PR review
2. GitHub sends event to infrastructure
3. Infrastructure routes to SQS queue
4. Consumer long-polls queue
5. Message parsed, source detected
6. Event scheduled via same async scheduler
7. Handler processes review event
8. Policy re-evaluated with new review
9. Status updated on GitHub
10. Message deleted from SQS

### Workflow 3: Cross-Organization Approval
1. PR requires approval from external org team
2. Policy bot installed on both orgs
3. Handler checks team membership via GraphQL
4. Caches membership for request duration
5. Evaluates against requirement
6. Updates status based on cross-org approval

## 🔧 Key Design Patterns

### 1. Dual-Stack Architecture
- Separate handlers for enterprise/cloud
- Shared interfaces and base implementations
- Independent configuration and credentials
- Unified metrics and logging

### 2. Scheduler Sharing
- Single scheduler per environment
- Used by both HTTP and SQS paths
- Prevents duplicate processing
- Consistent concurrency control

### 3. Lazy Loading with Caching
- Data fetched only when needed
- Aggressive caching at multiple levels
- Per-request and global caches
- TTL-based and LRU eviction

### 4. Context Propagation
- Request context flows through stack
- Enriched with metadata at each layer
- Used for logging, tracing, cancellation
- SQS adds queue-specific context

### 5. Graceful Degradation
- SQS optional, falls back to HTTP-only
- GraphQL failures fall back to REST
- Missing policy files don't break processing
- Partial failures logged but don't stop evaluation

## 🛠️ Configuration Best Practices

### Multi-Tenant Setup
```yaml
github_enterprise:
  v3_api_url: "https://github.company.com/api/v3"
  v4_api_url: "https://github.company.com/api/graphql"
  web_url: "https://github.company.com"
  app:
    integration_id: 12345
    private_key: "-----BEGIN RSA PRIVATE KEY-----"
    webhook_secret: "enterprise-secret"

github_cloud:
  v3_api_url: "https://api.github.com"
  v4_api_url: "https://api.github.com/graphql"
  web_url: "https://github.com"
  app:
    integration_id: 67890
    private_key: "-----BEGIN RSA PRIVATE KEY-----"
    webhook_secret: "cloud-secret"
```

### SQS Configuration for Scale
```yaml
sqs:
  enabled: true
  queue_workers:
    installation: 2      # Rare events
    pull_request: 10     # Core functionality
    pull_request_review: 8
    issue_comment: 6
    status: 20          # Highest volume
    check_run: 15       # CI/CD events
    workflow_run: 12

  event_routing:
    pull_request: "both"  # Redundancy during migration
    status: "sqs"        # High volume to SQS only
    installation: "http" # Low volume to HTTP only
```

### Performance Tuning
```yaml
workers:
  workers: 10        # Concurrent handlers
  queue_size: 100    # Buffered events
  github_timeout: 10s # API call timeout

cache:
  max_size: 100MB    # HTTP cache per client
  pushed_at_size: 200000 # Commit cache entries

sqs:
  max_messages: 10   # Batch size
  wait_time_seconds: 20 # Long polling
```

## 📊 Metrics & Monitoring

### Key Metrics to Monitor

#### System Health
- `up` - Is the service running
- `http_request_duration_seconds` - Request latency
- `http_requests_total` - Request rate
- `go_goroutines` - Active goroutines
- `go_memstats_heap_inuse_bytes` - Memory usage

#### GitHub Integration
- `github_request_duration_seconds` - API latency
- `github_requests_total` - API call rate
- `github_rate_limit_remaining` - Rate limit status

#### SQS Processing
- `sqs.messages.processed` - Success rate
- `sqs.messages.failed` - Failure rate
- `sqs.processing.time` - Processing latency
- `sqs.queue.depth` - Queue backlog

#### Policy Evaluation
- `policy_evaluation_duration_seconds` - Evaluation time
- `policy_evaluation_total` - Evaluations per result
- `policy_rules_evaluated` - Rules per evaluation

### Alert Conditions

#### Critical
- Service down (up == 0)
- GitHub rate limit < 100
- SQS processing failures > 10/min
- Memory usage > 90%

#### Warning
- Request latency p99 > 5s
- Queue depth > 1000 messages
- Cache hit rate < 50%
- Error rate > 1%

## 🔒 Security Considerations

### Authentication & Authorization
- GitHub App private key authentication
- Webhook signature verification (SHA-256 HMAC)
- OAuth2 for user sessions
- Repository-level permission checks

### Data Protection
- Encrypted session cookies
- TLS for all external communication
- No persistent storage of PR data
- Credentials in environment/config only

### Input Validation
- Webhook payload signature verification
- Policy YAML schema validation
- Path traversal prevention in file operations
- SQL injection N/A (no database)

## 🚀 Deployment Considerations

### Requirements
- **Go 1.25+** for building
- **GitHub App** installation with permissions:
  - Repository contents (read)
  - Pull requests (read/write)
  - Commit statuses (read/write)
  - Organization members (read)
  - Actions, Checks, Issues (read)

### AWS Permissions (if using SQS)
- `sqs:ReceiveMessage`
- `sqs:DeleteMessage`
- `sqs:GetQueueAttributes`
- `sqs:SendMessage` (if retry enabled)

### Resource Requirements
- **Memory**: 512MB minimum, 2GB recommended
- **CPU**: 2 cores minimum, 4+ for high volume
- **Network**: Low latency to GitHub API
- **Storage**: Minimal, just for caching

### High Availability
- Stateless design enables horizontal scaling
- Multiple instances can run concurrently
- SQS provides queue-based load distribution
- No coordination required between instances

## 🔍 Troubleshooting Guide

### Common Issues

#### 1. Events not processing
- Check webhook configuration in GitHub
- Verify webhook secret matches config
- Check SQS queue permissions
- Review routing headers in requests

#### 2. Policy not found
- Verify .policy.yml exists in default branch
- Check file permissions in repository
- Try shared repository fallback
- Validate YAML syntax

#### 3. High latency
- Monitor GitHub API rate limits
- Check cache configuration
- Review worker pool sizing
- Analyze queue depth for bottlenecks

#### 4. Memory growth
- Check cache size limits
- Monitor goroutine leaks
- Review request context cleanup
- Analyze heap profiles

### Debug Techniques
1. Enable debug logging: `level: debug`
2. Use `/api/simulate` for testing
3. Check `/api/metrics` for internals
4. Review delivery IDs in logs
5. Test with `scripts/test-event-processing.go`

## 📝 Summary

Policy Bot implements a sophisticated event-driven architecture for GitHub pull request policy enforcement. Key architectural decisions include:

1. **Dual ingestion paths** (HTTP/SQS) for reliability and scale
2. **Multi-tenant support** with separate enterprise/cloud stacks
3. **Header-based routing** for transparent environment selection
4. **Shared schedulers** between ingestion paths
5. **Aggressive caching** at multiple levels
6. **Stateless design** for horizontal scaling
7. **Comprehensive observability** via metrics and structured logging

The architecture prioritizes flexibility, performance, and reliability while maintaining clean separation of concerns and allowing for gradual migration between deployment models.