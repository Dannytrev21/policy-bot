# Policy Bot Dashboards

This directory contains observability dashboards for Policy Bot production monitoring.

## Contents

- **operational-dashboard.md** - Complete NRQL query reference for all dashboard panels
- **new-relic-dashboard.json** - Importable New Relic dashboard configuration
- **README.md** - This file

## Quick Start

### Import Dashboard to New Relic

**Option 1: Via New Relic UI**
1. Log in to New Relic
2. Navigate to **Dashboards** → **Import dashboard**
3. Upload `new-relic-dashboard.json`
4. Select your Policy Bot application
5. Click **Import**

**Option 2: Via New Relic CLI**
```bash
newrelic dashboard import \
  --file .claude/dashboards/new-relic-dashboard.json \
  --account-id YOUR_ACCOUNT_ID
```

**Option 3: Via Terraform**
```hcl
resource "newrelic_one_dashboard" "policy_bot" {
  name = "Policy Bot - Operational"
  dashboard_json = file("${path.module}/.claude/dashboards/new-relic-dashboard.json")
}
```

## Dashboard Structure

The dashboard is organized into 5 pages:

1. **System Health** - Overall status, success rates, circuit breaker state
2. **Performance** - Latency metrics for webhooks, SQS, and end-to-end traces
3. **Capacity** - Queue depth, worker utilization, cache efficiency
4. **Errors & Failures** - Authentication failures, dropped events, DLQ messages
5. **Distributed Tracing** - Request flows, error traces, bottleneck analysis

## Prerequisites

### Required Configuration

1. **OpenTelemetry Export** - Ensure OTEL collector is configured to send to New Relic:
   ```bash
   export OTEL_EXPORTER_OTLP_ENDPOINT=https://otlp.nr-data.net:4317
   export OTEL_EXPORTER_OTLP_HEADERS=api-key=YOUR_NEW_RELIC_LICENSE_KEY
   export NEW_RELIC_APP_NAME=policy-bot
   ```

2. **Metrics Collection** - Verify metrics are being exported:
   ```bash
   # Check metrics in New Relic NRQL console
   SELECT * FROM Metric WHERE appName = 'policy-bot' SINCE 5 minutes ago LIMIT 10
   ```

3. **Tracing** - Verify traces are being collected:
   ```bash
   # Check traces in New Relic NRQL console
   SELECT * FROM Span WHERE appName = 'policy-bot' SINCE 5 minutes ago LIMIT 10
   ```

## Key Metrics Overview

### Authentication Metrics
- `installation.client.success` / `installation.client.failure` - V3 REST API client creation
- `installation.v4client.success` / `installation.v4client.failure` - V4 GraphQL client creation

### Circuit Breaker Metrics
- `installation.circuit_breaker.state` - Current state (0=closed, 1=open, 2=half-open)
- `installation.circuit_breaker.opened_total` - Times circuit opened
- `installation.circuit_breaker.closed_total` - Times circuit closed

### Cache Metrics
- `installation.registry.cache_hits_total` - Cache hits
- `installation.registry.cache_misses_total` - Cache misses
- `installation.registry.cache_size` - Total cached entries
- `installation.registry.api_calls_total` - API calls made

### Webhook Processing Metrics
- `github.event.queued` - Queue depth
- `github.event.workers` - Active workers
- `github.event.age` - Event processing latency
- `github.event.dropped` - Dropped events (critical!)

### SQS Processing Metrics (per event_type)
- `sqs.messages.processed` - Successfully processed
- `sqs.messages.failed` - Failed messages
- `sqs.processing.time.mean_ms` - Mean processing time
- `sqs.dlq.messages` - Messages in DLQ
- `sqs.worker_pool.utilization` - Worker pool utilization %

### Tracing
- Span attributes: `installation.id`, `repository`, `pull_request.number`, `circuit_breaker.state`
- Span names: `InstallationManager.GetClients`, `Base.NewEvalContext`

## Alert Thresholds

Recommended alert conditions (implement in Phase 5.3):

| Metric | Condition | Severity | Response |
|--------|-----------|----------|----------|
| Success Rate | < 99% for 5 min | Warning | Investigate |
| Success Rate | < 95% for 5 min | Critical | Page on-call |
| Circuit Breaker | state = 1 (open) | Critical | GitHub API issue |
| Dropped Events | > 0 | Critical | Scale or reduce traffic |
| Queue Length | > 50 for 5 min | Warning | Performance issue |
| DLQ Messages | > 10 | Critical | Investigate errors |
| Worker Utilization | > 90% for 10 min | Warning | Scale workers |

## Maintenance

### Updating Dashboards

1. **Make changes** - Edit `operational-dashboard.md` with new NRQL queries
2. **Update JSON** - Regenerate `new-relic-dashboard.json` (or manually edit)
3. **Test** - Import to staging New Relic account first
4. **Commit** - Version control all changes
5. **Deploy** - Import to production New Relic

### Dashboard Version Control

All dashboard changes must be:
- Documented in `operational-dashboard.md`
- Reflected in `new-relic-dashboard.json`
- Committed to this repository
- Reviewed in pull requests

### Troubleshooting

**Problem: No data in dashboard**
```bash
# Verify metrics are being exported
curl -H "Api-Key: YOUR_KEY" \
  "https://api.newrelic.com/v2/applications/YOUR_APP_ID/metrics.json"

# Check OTEL collector logs
docker logs otel-collector | grep error
```

**Problem: Dashboard import fails**
- Verify JSON syntax: `jq . new-relic-dashboard.json`
- Check New Relic API permissions
- Ensure account ID is correct

**Problem: Missing metrics**
- Check `server/metrics/otel_bridge.go` - Are metrics registered?
- Verify `go-metrics` registry contains metrics
- Check OTEL export configuration

## Related Documentation

- **Metrics Implementation**: `server/metrics/otel_bridge.go`
- **Tracing Implementation**: `server/handler/installation_manager.go`, `server/handler/base.go`
- **Optimization Plan**: `.claude/todo/github_app_optimization.md`
- **Architecture**: `.claude/architecture/current_architecture_1014.md`

## Support

For dashboard issues or questions:
1. Check this README and `operational-dashboard.md`
2. Review Phase 5 completion notes in `.claude/todo/github_app_optimization.md`
3. Inspect metric definitions in `server/metrics/otel_bridge.go`
4. Contact platform team for New Relic access issues
