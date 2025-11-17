# Application Production Readiness & Reliability Enhancements
**Date:** 2026-02-15  
**Goal:** Identify gaps that need to be closed for a production-grade, reliable deployment of Policy Bot across its major components.

---

## HTTP Server & Routing (`server/server.go`)
- **Add explicit readiness endpoints:** Current health check only returns version/status (`server/handler/health.go:19`). Add `/ready` that validates GitHub API token exchange, template load, and (when enabled) SQS connectivity so orchestrators can safely roll pods.
- **Tighten graceful shutdown:** HTTP server relies on `base.Start()` while SQS gets a 30s timeout (`server/server.go:617`). Ensure HTTP listeners also use context-aware shutdown with timeouts and stop accepting webhooks before workers drain.
- **Webhook/SQS routing guardrails:** Event routing is configurable but lacks drift detection. Add a startup validation that cross-checks `Github*.EventRouting` against `SQS.Queues` so an event type cannot silently vanish or be double-processed.
- **Backpressure for HTTP path:** Rate limiting is only wrapped for SQS handlers; HTTP webhooks still use raw client creators (`server/server.go:180`). Apply the same `RateLimitedClientCreator` to HTTP dispatchers or add middleware that enforces org/global limits.

## GitHub Client Management & Installation Handling (`server/handler/base.go`, `installation_manager.go`, `rate_limiter.go`)
- **Unify rate-limit enforcement:** SQS path benefits from per-org + global limiters, but webhook path does not. Production deployments should enable adaptive limits globally and surface remaining quota via metrics and alerts.
- **Fast-fail on missing installations:** `VerifyInstallation` performs a live API call on every GHES lookup (`server/handler/base.go:117`). Cache negative results briefly and emit a distinct metric to avoid noisy retries when repos are off-boarded.
- **Circuit breaker visibility:** Installation client creation uses breakers (`installation_manager.go:53`). Ensure breaker state and trip counts are emitted to Prometheus/OTEL and tied to alerts so operators notice when client creation is blocked.
- **Key/cert rotation plan:** Document and automate rotation of GitHub App private keys and OAuth secrets; ensure reload without restart (currently only loaded at boot).

## Policy Config Loading & Evaluation (`server/handler/fetcher.go`, `eval_context.go`)
- **Deterministic failure mode:** Define whether policy load/parse errors should fail-open or fail-closed per repo/branch and expose that on the status check. Today parse errors only mark the status; add operator override and metrics for persistent offenders.
- **Config change telemetry:** Emit events/metrics whenever a policy file is loaded, changed, or fails YAML strict parsing (`fetcher.go:52`). Hook into the simulation endpoint to log which predicates frequently fail to reduce false negatives.
- **Caching semantics:** Consider short-lived caching of successful policy loads per SHA to avoid repeated GitHub reads during bursty webhook storms, while keeping strict invalidation on new commits.

## SQS Pipeline (`server/sqsconsumer/*`)
- **Distributed idempotency:** Idempotency cache is in-memory (`idempotency.go:44`), so duplicates can slip when running multiple replicas. Move to a shared store (Redis/Dynamo with TTL) or enable SQS content-based dedup plus Delivery-ID keys.
- **Queue health in readiness:** `Consumer.DetailedHealth` is unused by the HTTP health endpoint. Surface per-queue health (visible/delayed/not-visible counts) in `/ready` and alert when DLQ depth grows.
- **Dead-letter and retry policy:** Config exposes DLQ toggles but lacks operational wiring (no alerts/dashboards). Add metrics + alarms for DLQ receive counts and retry exhaustion, and document runbooks for replay.
- **Processing mode safety:** Default `processing_mode` is `scheduler`; `direct` bypasses scheduler without explicit guardrails. Add a startup warning and validation that worker counts/visibility timeouts are set per event when `direct` is chosen.
- **Adaptive polling observability:** Adaptive backoff exists but is not surfaced. Emit metrics for backoff durations and worker saturation so tuning is data-driven.

## Middleware & Event Filtering (`server/middleware/event_filter.go`)
- **Audit for skipped events:** Filtering returns 200 with no audit trail. Persist structured logs/metrics per event type/owner when events are skipped so misconfiguration is detectable.
- **Config drift detection:** Add startup-time assertion that every filtered event has an explicit routing decision (http/sqs/both) to avoid unexpected defaults when new GitHub event types are introduced.

## Observability & Diagnostics
- **Metrics to dashboards/alerts:** Many counters/timers exist (rate limiters, SQS processor, circuit breakers), but there is no documented dashboard or alert set. Publish standard Prometheus/Datadog dashboard definitions and SLOs (e.g., webhook 99p latency, policy evaluation success rate, GitHub 5xx/429 rate).
- **Tracing sampling controls:** OTEL spans are created in evaluation paths (`eval_context.go:101`, `installation_manager.go:48`), but sampling/export config is absent. Add env-configurable sampler, exporter selection, and propagate trace IDs into webhook logs for correlation.
- **Structured request logging:** Ensure webhook requests log `delivery_id`, `event_type`, organization, and installation IDs with a consistent field schema. Add redaction for secrets and policy content where applicable.

## Security, Auth, and Session Handling
- **Session hardening:** `scs` cookies are secure/HTTP-only but lack SameSite setting; set `SameSite=Lax` (or `Strict` if UI tolerates) to reduce token exfil risk. Add idle timeout separate from lifetime.
- **OAuth scopes & CSRF:** Document minimal scopes for the UI OAuth flow and add CSRF protection on the callback endpoints (`/api/github/auth/*`).
- **Secret management:** Move private keys, session keys, and OAuth secrets out of static config into a secret manager (KMS/SM) with rotation alarms and boot-time validation.

## Deployment & Operability
- **Runbooks and autoscaling signals:** Provide runbooks for webhook failures, SQS backlogs, and GitHub rate limits, plus HPA signals (CPU + custom metrics like queue depth or webhook latency).
- **Config validation CLI:** Ship a small CLI/linter that validates `policy-bot` server config (SQS routing, rate limits, webhook secrets, public URL) before rollout to catch misconfigurations earlier.
- **Disaster recovery:** Document backup/restore for any persistent state (none by default) and what needs to be recreated (GitHub App creds, OAuth app, SQS queues, shared policy repos).

--- 

**Next steps:** Prioritize health/readiness improvements and distributed idempotency first, then add observability/alerting packages and security hardening to reach a production-ready posture.
