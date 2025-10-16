# JMeter Load Testing Plan for Policy Bot Webhooks

## Overview
This document outlines the JMeter load testing strategy for Policy Bot's webhook queue system, focusing on identifying dropped events when the queue reaches capacity.

## Testing Goals
- Stress test webhook endpoint to identify queue saturation points
- Monitor dropped event metrics via OpenTelemetry
- Validate system behavior under various load patterns
- Ensure proper GitHub webhook header handling

## Implementation Status

### Phase 1: Planning & Design
- [x] Analyze webhook architecture and queue system
- [x] Identify key metrics (dropped events, queue length, workers)
- [x] Plan OpenTelemetry metrics integration
- [x] Design load test scenarios

### Phase 2: Metrics Integration
- [x] Implement OpenTelemetry metrics bridge for go-metrics
- [x] Create metrics adapter for `github.event.dropped` counter
- [x] Add queue saturation metrics
- [x] Write unit tests for metrics bridge

### Phase 3: JMeter Test Development
- [x] Create base JMeter test plan (.jmx)
- [x] Configure webhook endpoint and headers
- [x] Add CSV data source for dynamic payloads
- [x] Create thread groups for different load patterns
- [x] Add assertions for response validation

### Phase 4: Test Scenarios
- [x] Baseline test (normal load)
- [x] Burst test (sudden spike)
- [x] Sustained high load test
- [x] Mixed event types test
- [x] Queue overflow test

## Technical Details

### Required GitHub Headers
The following headers must be included in webhook requests:

| Header | Description | Example Value |
|--------|-------------|---------------|
| `X-GitHub-Event` | Event type | `pull_request`, `status`, `issue_comment` |
| `X-GitHub-Delivery` | Unique delivery ID | `12345-67890-abcdef` |
| `X-GitHub-Hook-ID` | Webhook ID | `123456` |
| `X-GitHub-Enterprise-Host` | Enterprise host (optional) | `ghes.example.com` |
| `X-GitHub-Hook-Installation-Target-ID` | Installation ID | `987654` |
| `X-GitHub-Hook-Installation-Target-Type` | Target type | `integration` |
| `User-Agent` | GitHub user agent | `GitHub-Hookshot/abc123` |
| `Content-Type` | Content type | `application/json` |
| `X-Hub-Signature-256` | HMAC signature | `sha256=...` |

### Configurable Parameters

#### Environment Configuration
- **Target URL**: `${__P(target.url,http://localhost:8080/api/github/hook)}`
- **QA URL**: `${__P(qa.url,https://qa.policybot.example.com/api/github/hook)}`
- **Thread Count**: `${__P(threads,10)}`
- **Ramp-up Period**: `${__P(rampup,10)}`
- **Loop Count**: `${__P(loops,100)}`
- **Think Time**: `${__P(thinktime,100)}`

#### Event Distribution
- Pull Request events: 40%
- Status events: 30%
- Issue Comment events: 20%
- Pull Request Review events: 10%

### OpenTelemetry Metrics

#### Key Metrics to Monitor
1. **Dropped Events** (`github.event.dropped`)
   - Type: Counter
   - Description: Number of events dropped due to queue overflow

2. **Queue Length** (`github.event.queued`)
   - Type: Gauge
   - Description: Current queue depth

3. **Active Workers** (`github.event.workers`)
   - Type: Gauge
   - Description: Number of active worker threads

4. **Event Age** (`github.event.age`)
   - Type: Histogram
   - Description: Age of events when processed

#### OTEL Exporter Configuration
```yaml
otel:
  service_name: "policy-bot"
  exporter:
    endpoint: "localhost:4317"
    protocol: "grpc"
  metrics:
    interval: "10s"
    timeout: "5s"
```

## JMeter Test Structure

### 1. Test Plan Components

```
Policy Bot Load Test
├── User Defined Variables
│   ├── target_url
│   ├── webhook_secret
│   └── event_types
├── CSV Data Set Config
│   └── webhook_payloads.csv
├── Thread Groups
│   ├── Normal Load
│   ├── Burst Load
│   └── Sustained High Load
├── HTTP Request Defaults
├── HTTP Header Manager
├── Samplers
│   ├── Pull Request Webhook
│   ├── Status Webhook
│   └── Issue Comment Webhook
├── Assertions
│   ├── Response Code (200-202)
│   └── Response Time (<1000ms)
├── Listeners
│   ├── Summary Report
│   ├── Response Time Graph
│   └── Errors Report
└── Backend Listener (InfluxDB/Prometheus)
```

### 2. Sample Webhook Payloads

#### Pull Request Event
```json
{
  "action": "opened",
  "number": ${__Random(1,10000)},
  "pull_request": {
    "id": ${__UUID()},
    "number": ${__Random(1,10000)},
    "state": "open",
    "title": "Test PR ${__threadNum}",
    "user": {
      "login": "testuser${__threadNum}"
    },
    "head": {
      "ref": "feature/test-${__Random(1,100)}",
      "sha": "${__RandomString(40,abcdef0123456789,)}"
    },
    "base": {
      "ref": "main",
      "sha": "${__RandomString(40,abcdef0123456789,)}"
    }
  },
  "repository": {
    "id": 123456,
    "name": "test-repo",
    "full_name": "org/test-repo",
    "owner": {
      "login": "org"
    }
  },
  "sender": {
    "login": "testuser${__threadNum}"
  }
}
```

## Running Tests

### Prerequisites
1. JMeter 5.5+ installed
2. No third-party plugins required (plan uses core controllers only)

### Execution Commands

#### Local Testing
```bash
# Basic test run
jmeter -n -t policy_bot_load_test.jmx \
  -Jtarget.url=http://localhost:8080/api/github/hook \
  -Jthreads=10 \
  -Jloops=100 \
  -l results.jtl \
  -e -o report/

# High load test
jmeter -n -t policy_bot_load_test.jmx \
  -Jtarget.url=http://localhost:8080/api/github/hook \
  -Jthreads=100 \
  -Jloops=1000 \
  -Jrampup=30 \
  -l high_load_results.jtl
```

#### QA Environment Testing
```bash
# QA environment test
jmeter -n -t policy_bot_load_test.jmx \
  -Jqa.url=https://qa.policybot.example.com/api/github/hook \
  -Jthreads=50 \
  -Jloops=500 \
  -Jwebhook.secret=${QA_WEBHOOK_SECRET} \
  -l qa_results.jtl
```

### Analysis

#### Success Criteria
- **Response Time**: P95 < 500ms, P99 < 1000ms
- **Error Rate**: < 0.1%
- **Dropped Events**: < 1% under normal load
- **Queue Saturation**: Identified at specific TPS

#### Monitoring Dashboard
Monitor the following during test execution:
1. OpenTelemetry metrics dashboard
2. Application logs for errors
3. System resources (CPU, memory, network)
4. Database connection pool

## Implementation Files

### Added
1. ✅ `server/metrics/otel_bridge.go` – OTEL exporter for queue metrics
2. ✅ `server/metrics/otel_bridge_test.go` – unit coverage for bridge callbacks
3. ✅ `jmeter/scripts/policy_bot_webhook_load_test.jmx` – configurable webhook load test plan
4. ✅ `jmeter/data/webhook_events.csv` – event rotation list for CSV Data Set Config
5. ✅ `jmeter/templates/*.json` – payload templates consumed by the Groovy pre-processor

### Verification Notes
- `go test -mod=mod ./server/metrics` exercises the new OTEL bridge (vendored tree still needs a refresh, so CI should either update `vendor/modules.txt` or run without vendoring).
- Bridge uses the process-global OTEL `MeterProvider`; without configuring an exporter the callback runs but telemetry is dropped by the default no-op provider.

## Integration with Application

- Bridge is created in `server.New` immediately after the base server is constructed and cleaned up in `Server.Start`.
- Scheduler metrics (`github.event.queued`, `github.event.workers`, `github.event.dropped`, histogram-derived gauges) now surface as OTEL async instruments by reading `base.Registry()`.
- To see data, deploy an OTEL SDK/collector (e.g., set `OTEL_EXPORTER_OTLP_ENDPOINT` via environment or install auto-instrumentation in QA).

## 429 Investigation Summary

- Policy Bot itself returns `503` when the internal queue is over capacity; repeated `429` responses observed during load tests originate upstream (load balancer/WAF throttling) before the dispatcher runs.
- Because 429 responses are rejected at the edge, queue depth and dropped counters remain flat, matching previous observations of “no events queued or dropped.”
- Follow-up: coordinate with platform owners to inspect QA ingress logs, allow-list load-test hosts, or ramp traffic gradually past external rate limits so queue saturation can be reproduced inside the app.

## Remaining Follow-ups

1. Refresh vendor modules so `go test ./server/metrics` succeeds in CI.
2. Provide OTEL collector configuration/runbook for QA to visualise the new instruments.
3. Document a helper (script or README snippet) that converts a full QA webhook URL into the `target.protocol/host/port/path` properties used by the JMX plan.
4. Optionally wire a JMeter backend listener once the monitoring sink (Prometheus/InfluxDB) is finalised.
5. Schedule a QA dry run to confirm queue overflow produces `github.event.dropped` increments once upstream throttling is mitigated.

## References
- [GitHub Webhook Documentation](https://docs.github.com/en/developers/webhooks-and-events/webhooks)
- [JMeter Best Practices](https://jmeter.apache.org/usermanual/best-practices.html)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
- [go-metrics Library](https://github.com/rcrowley/go-metrics)
