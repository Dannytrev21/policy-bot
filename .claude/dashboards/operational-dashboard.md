# Policy Bot Operational Dashboard

## Overview

This dashboard provides comprehensive observability for Policy Bot across both webhook and SQS event processing paths. Metrics are exported via OpenTelemetry to New Relic.

**Dashboard Organization:**
1. **System Health Overview** - Overall success rates and system status
2. **Performance Metrics** - Latency and throughput
3. **Capacity & Utilization** - Resource usage and limits
4. **Errors & Failures** - What's failing and why
5. **Distributed Tracing** - End-to-end request visibility

---

## 1. SYSTEM HEALTH OVERVIEW

### 1.1 Overall Success Rate

**Description:** Combined success rate across all event processing (webhooks + SQS)

```nrql
SELECT
  (sum(installation.client.success) + sum(installation.v4client.success)) /
  (sum(installation.client.success) + sum(installation.v4client.success) +
   sum(installation.client.failure) + sum(installation.v4client.failure)) * 100
AS 'Success Rate %'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart
**Threshold:** Alert if < 99%

### 1.2 Events Processed Per Minute

**Description:** Total event throughput (webhooks + SQS combined)

```nrql
SELECT
  rate(sum(installation.client.success) + sum(installation.v4client.success), 1 minute)
AS 'Events/min'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Area chart
**Expected:** 50-200 events/min during peak hours

### 1.3 Circuit Breaker Status

**Description:** Current circuit breaker state (0=closed/healthy, 1=open/degraded, 2=half-open/recovering)

```nrql
SELECT latest(installation.circuit_breaker.state) AS 'State'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 5 minutes ago
```

**Chart Type:** Billboard
**Values:**
- 0 = 🟢 Closed (Healthy)
- 1 = 🔴 Open (GitHub API Unavailable)
- 2 = 🟡 Half-Open (Recovering)

---

## 2. PERFORMANCE METRICS

### 2.1 Webhook Event Age (Latency)

**Description:** How long events wait in the webhook queue before processing

```nrql
SELECT
  average(github.event.age.mean_ms) AS 'Mean (ms)',
  percentile(github.event.age, 95) AS 'P95 (ms)',
  max(github.event.age.max_ms) AS 'Max (ms)'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart (multi-series)
**Target:** P95 < 500ms

### 2.2 SQS Processing Time by Event Type

**Description:** Processing latency for SQS events, broken down by event type

```nrql
SELECT
  average(sqs.processing.time.mean_ms) AS 'Mean (ms)',
  average(sqs.processing.time.p95_ms) AS 'P95 (ms)'
FROM Metric
WHERE appName = 'policy-bot'
FACET event_type
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart faceted by event_type
**Target:** Mean < 200ms, P95 < 500ms

### 2.3 End-to-End Trace Latency

**Description:** Full request duration from webhook/SQS to policy evaluation completion

```nrql
SELECT
  average(duration.ms) AS 'Mean',
  percentile(duration.ms, 95) AS 'P95',
  percentile(duration.ms, 99) AS 'P99'
FROM Span
WHERE appName = 'policy-bot'
  AND name = 'Base.NewEvalContext'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart
**Target:** P95 < 1000ms

---

## 3. CAPACITY & UTILIZATION

### 3.1 Webhook Queue Depth

**Description:** Number of events waiting for processing (internal scheduler queue)

```nrql
SELECT
  latest(github.event.queued) AS 'Queue Length',
  latest(github.event.workers) AS 'Active Workers'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 15 minutes ago
TIMESERIES AUTO
```

**Chart Type:** Area chart (stacked)
**Alert:** Queue > 50 indicates backlog

### 3.2 SQS Worker Pool Utilization

**Description:** SQS worker pool capacity and utilization by event type

```nrql
SELECT
  latest(sqs.worker_pool.active_workers) AS 'Active',
  latest(sqs.worker_pool.capacity) AS 'Capacity',
  latest(sqs.worker_pool.utilization) AS 'Utilization %'
FROM Metric
WHERE appName = 'policy-bot'
FACET event_type
SINCE 15 minutes ago
TIMESERIES AUTO
```

**Chart Type:** Line chart faceted by event_type
**Alert:** Utilization > 90% indicates need to scale

### 3.3 Installation Cache Hit Rate

**Description:** Efficiency of installation verification caching

```nrql
SELECT
  sum(installation.registry.cache_hits_total) /
  (sum(installation.registry.cache_hits_total) + sum(installation.registry.cache_misses_total)) * 100
AS 'Hit Rate %'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart
**Target:** > 80% (indicates caching is effective)

### 3.4 Cache Size and Composition

**Description:** Breakdown of cached installation entries

```nrql
SELECT
  latest(installation.registry.cache_size) AS 'Total',
  latest(installation.registry.positive_entries) AS 'Installed',
  latest(installation.registry.negative_entries) AS 'Not Installed'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 15 minutes ago
TIMESERIES AUTO
```

**Chart Type:** Area chart (stacked)

---

## 4. ERRORS & FAILURES

### 4.1 Authentication Failures by Type

**Description:** Failed installation client creations (v3 REST and v4 GraphQL)

```nrql
SELECT
  sum(installation.client.failure) AS 'v3 (REST) Failures',
  sum(installation.v4client.failure) AS 'v4 (GraphQL) Failures'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart
**Alert:** Spike > 10 failures/min

### 4.2 Circuit Breaker Events

**Description:** Circuit breaker state transitions (opens and closes)

```nrql
SELECT
  sum(installation.circuit_breaker.opened_total) AS 'Opened',
  sum(installation.circuit_breaker.closed_total) AS 'Closed'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 4 hours ago
TIMESERIES AUTO
```

**Chart Type:** Line chart
**Alert:** Any opens indicate GitHub API issues

### 4.3 Webhook Dropped Events

**Description:** Events dropped due to queue capacity exceeded

```nrql
SELECT
  sum(github.event.dropped) AS 'Dropped Events'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Area chart
**Alert:** ANY dropped events is critical

### 4.4 SQS Failed Messages and DLQ

**Description:** Failed SQS message processing and DLQ accumulation

```nrql
SELECT
  sum(sqs.messages.failed) AS 'Failed Messages',
  latest(sqs.dlq.messages) AS 'DLQ Count'
FROM Metric
WHERE appName = 'policy-bot'
FACET event_type
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart faceted by event_type
**Alert:** DLQ > 10 messages indicates recurring failures

### 4.5 SQS Worker Pool Issues

**Description:** Worker pool rejections and panics

```nrql
SELECT
  sum(sqs.worker_pool.rejected_total) AS 'Rejections',
  sum(sqs.worker_pool.panics_total) AS 'Panics'
FROM Metric
WHERE appName = 'policy-bot'
FACET event_type
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart faceted by event_type
**Alert:** Any panics require immediate investigation

### 4.6 Cache API Call Rate

**Description:** API calls to GitHub for installation verification (cache misses)

```nrql
SELECT
  rate(sum(installation.registry.api_calls_total), 1 minute) AS 'API Calls/min'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Area chart
**Context:** High rate indicates cache inefficiency or TTL issues

---

## 5. DISTRIBUTED TRACING

### 5.1 Trace Success vs Errors

**Description:** Trace completion status breakdown

```nrql
SELECT count(*)
FROM Span
WHERE appName = 'policy-bot'
  AND name = 'Base.NewEvalContext'
FACET otel.status_code
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Stacked area chart
**Values:**
- `OK` = Success
- `ERROR` = Failed

### 5.2 Slowest Operations

**Description:** P95 slowest spans to identify bottlenecks

```nrql
SELECT percentile(duration.ms, 95) AS 'P95 Duration'
FROM Span
WHERE appName = 'policy-bot'
FACET name
SINCE 1 hour ago
```

**Chart Type:** Bar chart
**Use:** Identify performance bottlenecks

### 5.3 Error Traces

**Description:** Recent failed traces with error details

```nrql
SELECT trace.id, error.message, repository, installation.id
FROM Span
WHERE appName = 'policy-bot'
  AND otel.status_code = 'ERROR'
  AND name IN ('InstallationManager.GetClients', 'Base.NewEvalContext')
SINCE 1 hour ago
LIMIT 100
```

**Chart Type:** Table
**Use:** Click trace ID to see full distributed trace

### 5.4 Circuit Breaker Correlation

**Description:** Traces that triggered circuit breaker events

```nrql
SELECT count(*)
FROM Span
WHERE appName = 'policy-bot'
  AND circuit_breaker.state IS NOT NULL
FACET circuit_breaker.state
SINCE 4 hours ago
TIMESERIES AUTO
```

**Chart Type:** Area chart
**Use:** Correlate circuit breaker state with request patterns

---

## 6. COMPOSITE VIEWS

### 6.1 System Health Score

**Description:** Composite health metric combining multiple signals

```nrql
SELECT
  (
    -- Success rate (40% weight)
    ((sum(installation.client.success) + sum(installation.v4client.success)) /
     (sum(installation.client.success) + sum(installation.v4client.success) +
      sum(installation.client.failure) + sum(installation.v4client.failure))) * 40 +

    -- Circuit breaker closed (30% weight) - state 0 = closed
    (1 - latest(installation.circuit_breaker.state) / 2) * 30 +

    -- No dropped events (20% weight)
    (1 - min(1, sum(github.event.dropped) / 100)) * 20 +

    -- Cache hit rate (10% weight)
    (sum(installation.registry.cache_hits_total) /
     (sum(installation.registry.cache_hits_total) + sum(installation.registry.cache_misses_total))) * 10
  ) AS 'Health Score (0-100)'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 5 minutes ago
```

**Chart Type:** Billboard with threshold colors
- 🟢 Green: 90-100 (Healthy)
- 🟡 Yellow: 75-89 (Degraded)
- 🔴 Red: < 75 (Critical)

### 6.2 Throughput Comparison: Webhooks vs SQS

**Description:** Event processing volume by source

```nrql
SELECT
  rate(sum(github.event.queued), 1 minute) AS 'Webhook Events/min',
  rate(sum(sqs.messages.processed), 1 minute) AS 'SQS Messages/min'
FROM Metric
WHERE appName = 'policy-bot'
SINCE 1 hour ago
TIMESERIES AUTO
```

**Chart Type:** Line chart (multi-series)
**Use:** Understand event distribution across processing paths

---

## Dashboard Import Instructions

### Using New Relic UI

1. Navigate to New Relic → Dashboards → Import dashboard
2. Upload the `new-relic-dashboard.json` file from this directory
3. Select your Policy Bot application
4. Dashboard will be created with all panels configured

### Using Terraform (Infrastructure as Code)

```hcl
resource "newrelic_one_dashboard" "policy_bot_operational" {
  name = "Policy Bot - Operational Dashboard"

  # Import from JSON
  dashboard_json = file("${path.module}/new-relic-dashboard.json")
}
```

### Using New Relic CLI

```bash
newrelic dashboard import \
  --file new-relic-dashboard.json \
  --account-id YOUR_ACCOUNT_ID
```

---

## Alert Thresholds Summary

| Metric | Threshold | Severity | Action |
|--------|-----------|----------|--------|
| Success Rate | < 99% | Warning | Check authentication failures |
| Success Rate | < 95% | Critical | Page on-call |
| Circuit Breaker | State = 1 (Open) | Critical | GitHub API issue |
| Webhook Dropped Events | > 0 | Critical | Scale workers or reduce traffic |
| Webhook Queue Length | > 50 | Warning | Check processing speed |
| SQS DLQ Messages | > 10 | Critical | Investigate recurring failures |
| Worker Pool Utilization | > 90% | Warning | Scale worker pools |
| Cache Hit Rate | < 80% | Warning | Review TTL settings |
| Trace Error Rate | > 5% | Warning | Investigate failed traces |

---

## Maintenance Notes

### Metric Retention
- **Real-time metrics:** 1 hour (high resolution)
- **Hourly rollups:** 8 days
- **Daily rollups:** 90 days

### Dashboard Updates
- Version control: All dashboard changes committed to this repository
- Review frequency: Monthly during sprint planning
- Ownership: Platform team

### Related Documentation
- Alert runbooks: `.claude/dashboards/runbooks/`
- Metric definitions: `server/metrics/otel_bridge.go`
- Tracing instrumentation: `server/handler/installation_manager.go`, `server/handler/base.go`
