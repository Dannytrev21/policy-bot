# Current Architecture (Codex Overview)

## System Purpose
- `policy-bot` runs as a GitHub App that enforces repository-specific approval policies on pull requests.
- The binary exposes a long-lived server (`policy-bot server`) that accepts GitHub webhooks, renders a UI for policy insights, and provides APIs for simulation and validation.
- Policies are expressed as YAML and evaluated against live GitHub pull request state to post commit statuses, request/dismiss reviewers, and expose detail pages.

## Runtime Composition
- **Entry Point**: `main.go` delegates to Cobra commands under `cmd`. `cmd/server.go` loads YAML configuration, optionally runs in test mode, and starts the HTTP server.
- **Configuration Model** (`server/config.go`): wraps Palantir `baseapp` HTTP settings, logging options, shared session config, GitHub App credentials for both enterprise and cloud instances, worker sizing, caching, Datadog/Prometheus emitters, and SQS ingestion settings. Values can be overridden via `POLICYBOT_`-prefixed environment variables.
- **Server Bootstrap** (`server/server.go`): constructs logging, session management (SCS cookies), HTTP clients with LRU caching, and GitHub App installations for both GitHub Enterprise Server (GHES) and GitHub Enterprise Cloud (GHEC). It builds two `Base` handler stacks (enterprise/cloud) backed by shared global caches (`pull.NewLRUGlobalCache`) and per-environment policy fetchers (`appconfig.Loader`).
- **Async Schedulers**: separate `githubapp.QueueAsyncScheduler` instances buffer work for enterprise and cloud event streams. Both HTTP webhooks and SQS consumers enqueue onto these schedulers for concurrent processing.

## Request Routing & Middleware
- **Goji Router**: the server uses `goji` muxes layered on top of `baseapp`. A configurable `PublicURL` base path is trimmed and reused for OAuth callbacks and asset routes.
- **Header-Aware Routing** (`server/middleware/header_check.go`): middleware inspects incoming requests to decide whether to serve via enterprise or cloud stacks. Priority order: `X-GitHub-Enterprise-Host` → `x-dcp-destination-host` → `source` query param → default to cloud. Metrics (`policy_bot_routing_decisions_total`, `policy_bot_routing_latency_seconds`) capture routing outcomes.
- **Key Routes**:
  - `POST /api/github/hook`: GitHub webhook endpoint; delegates to either enterprise or cloud dispatcher.
  - `POST /api/simulate/:owner/:repo/:number`: runs policy evaluation against live data using user-provided OAuth token.
  - `PUT /api/validate`: validates uploaded policy YAML without executing it.
  - `GET /api/health`, `/api/metrics`: health checks and Prometheus metrics.
  - Details UI under `/details/ghes/...` and `/details/ghec/...` requires authenticated session and renders Go templates loaded from `server/templates`.
  - Static assets under `/static/` and root index page served through header-driven selection.

## Event Handling Pipeline
- **Dispatchers**: `githubapp.NewEventDispatcher` instances (enterprise/cloud) wrap handler collections for installation, merge group, pull request, review, issue comment, status, check run, and workflow_run events.
- **Handlers** (`server/handler`):
  - Specialized structs embed `handler.Base`, providing event-specific parsing and trigger selection (e.g., `PullRequest`, `PullRequestReview`, `IssueComment`, `WorkflowRun`, `Status`, `CheckRun`).
  - `handler.Installation` reconciles GitHub App installation lifecycle.
  - `handler.MergeGroup` handles GitHub merge queue events.
  - HTTP responders (e.g., `Simulate`, `Validate`, `Index`, `Details`, `Health`) reuse the same `Base` infrastructure for policy access and GitHub clients.
- **Scheduling Flow**:
  1. Webhook request reaches dispatcher via header-aware middleware.
  2. Dispatcher verifies signature, parses payload, and enqueues a `githubapp.Dispatch` on the appropriate scheduler.
  3. Scheduler executes the registered handler in worker goroutines sized by `workers` and `queue_size` from config.

## SQS Integration
- **Optional Consumer** (`server/sqsconsumer`):
  - Enabled by `sqs.enabled`. Loads AWS config (region + optional endpoint for LocalStack) and starts `WorkersPerQueue` goroutines per configured event type queue.
  - `consumer.consumeQueue` long-polls SQS (wait time configurable) and hands messages to the `Processor`.
- **Processing** (`Processor`):
  - Parses message bodies into `SQSMessage` with headers/payload. Automatically detects enterprise vs cloud origin using webhook headers (Host, legacy `source`).
  - Maps GitHub event types to handler instances collected from enterprise/cloud handler slices. Uses the same schedulers as HTTP path to avoid duplication.
  - Records metrics: processed/failed counters and processing latency per event type. Optional retry flow requeues messages with exponential backoff (`EnableRetry`, `MaxRetries`).
  - On success, deletes message; on failure, respects visibility timeout and optionally requeues.
- **Lifecycle**: `Server.Start` launches SQS consumer asynchronously, ensures graceful shutdown with configurable timeout, and logs health errors separately from HTTP.

## Policy Evaluation Flow
- **Handler.Base**:
  - Wraps `githubapp.ClientCreator`, installation services, config fetchers, and global caches.
  - `PreparePRContext` seeds contextual logging; `NewEvalContext` builds `pull.Context` via REST + GraphQL clients with caching.
  - Fetches repo policy using `ConfigFetcher` (retries transient GitHub errors, supports shared `.github/policy.yml` fallback).
- **EvalContext** (`handler/eval_context.go`):
  1. `ParseConfig` validates YAML load/parse results, enforces trigger matching (to skip redundant evaluations), and handles status posting when configs fail.
  2. `EvaluatePolicy` executes `policy.ParsePolicy`, deriving aggregated `common.Result` statuses (`approved`, `pending`, `disapproved`, `skipped`) and posts GitHub commit statuses using templated context (`<StatusCheckContext>: <base branch>`).
  3. `RunPostEvaluateActions` coordinates reviewer auto-assignment and stale review dismissal by consulting rule metadata (`policy/reviewer` utilities).
- **Policy Engine** (`policy` package):
  - `policy.ParsePolicy` composes approval and disapproval evaluators from rule definitions, applying global options (e.g., ignore edited comments).
  - Rule parsing resides in subpackages (`policy/approval`, `policy/disapproval`, `policy/predicate`) that evaluate GitHub state obtained via `pull.Context`.
  - Evaluation outputs hierarchical results consumed by UI templates and status descriptions.

## GitHub & Session Integration
- **Client Management**: `githubapp.NewDefaultCachingClientCreator` builds REST & GraphQL clients for enterprise/cloud endpoints with shared LRU caches (`lrucache`), request logging, and Prometheus metrics.
- **Installations**: `githubapp.NewInstallationsService` caches installation metadata; handler base maps installation IDs to tokens and uses `pull.GlobalCache` for commit timestamps.
- **OAuth2 Flow**: Shared `/oauth/callback` leverages `githubapp/oauth2` with SCS cookie sessions to support UI logins; session configuration is derived from server config (lifetime, force TLS).

## UI Layer
- Templates are loaded through `handler.LoadTemplates` combining server-side assets with GitHub URLs. The UI surfaces approval rule trees, reviewer assignments, and audit data.
- `handler.Details` and `handler.Index` render pages based on enterprise/cloud selection. Access to details pages is gated by login middleware (`handler.RequireLogin`).
- Static assets compiled via Webpack/Tailwind live under `server/assets` and `server/templates`.

## Observability & Metrics
- Prometheus metrics endpoint wraps `base.Registry()`. Middleware and SQS consumers register custom counters/timers. Datadog emission is optional (`datadog.StartEmitter`).
- Structured logging uses `zerolog`; context-aware logging attaches delivery IDs and request metadata.

## Testing & Tooling
- Integration tests (`test/integration_test.go`) target LocalStack-backed SQS + GitHub simulation helpers.
- Performance tests and helpers under `test/performance_test.go` and `test/localstack_helpers.go` exercise queue throughput.
- Scripts in `scripts/` (e.g., `test-event-processing.go`) provide local event replay against the configured handlers.

## Key External Dependencies
- Palantir `go-baseapp` for HTTP server lifecycle, logging, and metrics.
- Palantir `go-githubapp` for GitHub App auth, webhook dispatch, schedulers, OAuth, and config loading.
- AWS SDK v2 for SQS interactions.
- Goji router (`goji.io`) plus Hatpear error handling for HTTP flows.
- Zerolog, Prometheus client, and SCS session manager for infrastructure concerns.

