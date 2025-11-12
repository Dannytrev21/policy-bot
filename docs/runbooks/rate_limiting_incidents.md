# Rate Limiting Incident Runbook

**Owner**: Platform Engineering Team
**Last Updated**: November 2025
**Severity**: P1-P3 depending on symptoms

## Quick Reference

| Symptom | Severity | First Action | Page Link |
|---------|----------|--------------|-----------|
| High rate limit wait times | P2 | Check queue depth | [Section 1](#1-high-rate-limit-wait-times) |
| Excessive throttling | P2 | Validate configuration | [Section 2](#2-excessive-throttling) |
| GitHub 429 errors | P1 | Emergency rate reduction | [Section 3](#3-github-429-errors) |
| Adaptive disabled unexpectedly | P1 | Check feature flag | [Section 4](#4-adaptive-rate-limiting-disabled) |
| Rate oscillations | P2 | Adjust EMA smoothing | [Section 5](#5-rate-limit-oscillations) |
| Per-installation limit breach | P2 | Investigate installation | [Section 6](#6-per-installation-rate-limit-breach) |

## Prerequisites

**Required Access**:
- New Relic dashboard access
- AWS console (SQS queues)
- Server configuration access
- Log aggregation access

**Required Knowledge**:
- Rate limiting basics (static vs adaptive)
- SQS event processing flow
- GitHub API rate limit quotas

---

## 1. High Rate Limit Wait Times

### Symptoms
- Alert: `High Rate Limit Wait Times` (P95 > 2 seconds)
- Dashboard shows increased `handler.rate_limit.wait_time`
- SQS processing latency increasing

### Diagnosis

**Step 1: Check Current Metrics**
```
New Relic Query:
SELECT percentile(handler.rate_limit.wait_time, 95)
FROM Metric
WHERE appName = 'policy-bot'
SINCE 30 minutes ago
TIMESERIES
```

**Step 2: Check SQS Queue Depth**
```bash
aws sqs get-queue-attributes \
  --queue-url $QUEUE_URL \
  --attribute-names ApproximateNumberOfMessages
```

**Step 3: Check Rate Limiter Configuration**
```bash
# View current rate limit config
kubectl get configmap policy-bot-config -o yaml | grep -A 10 "rate_limit"
```

**Step 4: Check Installation Distribution**
```
New Relic Query:
SELECT count(*)
FROM Metric
WHERE metricName = 'handler.rate_limit.installations'
FACET installation_id
SINCE 1 hour ago
```

### Root Causes

| Cause | Indicators | Solution |
|-------|------------|----------|
| Queue backlog | High SQS depth (>100) | Increase worker count or rate limits |
| Hot installation | One installation >> others | Verify legitimate traffic, consider per-installation override |
| Configuration too conservative | All installations waiting | Increase InstallationRate (3.0 → 3.5) |
| Adaptive disabled | Should be enabled but isn't | Enable adaptive feature flag |

### Resolution Steps

#### If Queue Backlog (High SQS Depth):
```bash
# 1. Temporarily increase rate limits
kubectl edit configmap policy-bot-config
# Change:
#   installation_rate: 3.0 → 3.5
#   global_rate: 100.0 → 150.0

# 2. Restart pods to pick up config
kubectl rollout restart deployment policy-bot

# 3. Monitor impact
# Check wait times decrease within 5 minutes
```

#### If Hot Installation:
```bash
# 1. Identify the installation
NEW_RELIC_QUERY="SELECT count(*) FROM Metric FACET installation_id SINCE 1 hour ago"

# 2. Investigate if legitimate
# Check with customer success team
# Review repository activity

# 3. If malicious/misconfigured:
# Add rate limit override for that installation (future enhancement)
# OR temporarily block in firewall rules
```

#### If Configuration Too Conservative:
```yaml
# Update config to be less conservative
rate_limit:
  enabled: true
  installation_rate: 3.5  # Was 3.0
  installation_burst: 15  # Was 10
  global_rate: 120.0      # Was 100.0
  global_burst: 60        # Was 50
```

### Verification

**After Resolution**:
1. Wait 5-10 minutes for metrics to stabilize
2. Check P95 wait time is < 1 second
3. Verify queue depth is decreasing
4. Confirm no new alerts

### Rollback

If changes make things worse:
```bash
# Revert configuration
kubectl rollout undo deployment policy-bot

# OR manually revert config changes
kubectl edit configmap policy-bot-config
# Restore previous values
```

---

## 2. Excessive Throttling

### Symptoms
- Alert: `Excessive Throttling` (>50 throttles/min)
- Dashboard shows spike in `handler.rate_limit.throttled`
- No corresponding queue backlog

### Diagnosis

**Step 1: Check Throttle Rate**
```
New Relic Query:
SELECT rate(sum(handler.rate_limit.throttled), 1 minute)
FROM Metric
SINCE 30 minutes ago
TIMESERIES
```

**Step 2: Check GitHub API Status**
```bash
# Check if GitHub is experiencing issues
curl https://www.githubstatus.com/api/v2/status.json
```

**Step 3: Correlate with Traffic Patterns**
```
New Relic Query:
SELECT count(*)
FROM Log
WHERE message LIKE '%rate limit%'
FACET event_type
SINCE 1 hour ago
```

### Root Causes

| Cause | Indicators | Solution |
|-------|------------|----------|
| Traffic spike | Sudden 2-3x increase | Normal behavior, rate limiting working as designed |
| GitHub API degradation | GitHub status page shows issues | Wait for GitHub recovery, possibly reduce rate |
| Misconfiguration | Throttling immediately after config change | Revert config change |
| Burst traffic | Periodic spikes (every N minutes) | Enable burst configuration |

### Resolution Steps

#### If Traffic Spike (Normal Behavior):
```
# This is expected behavior during high traffic
# Rate limiting is protecting the system
# No action needed if:
# - P99 latency < 5 seconds
# - No events are dropped
# - Queue depth is reasonable

# Monitor for:
# - GitHub 429 errors (should be zero)
# - Processing succeeding (just delayed)
```

#### If GitHub API Issues:
```bash
# 1. Check GitHub status
curl https://www.githubstatus.com/api/v2/status.json | jq '.status.description'

# 2. If degraded, temporarily reduce rate limits
# This reduces pressure on GitHub while they recover
kubectl edit configmap policy-bot-config
# Change:
#   installation_rate: 3.0 → 2.0  (reduce by 33%)

# 3. Monitor GitHub status and restore when resolved
```

#### If Misconfiguration:
```bash
# Revert recent configuration changes
kubectl rollout undo deployment policy-bot

# Review change that caused issue
git log --oneline --all --grep="rate limit" --since="1 day ago"
```

### Verification

1. Throttle rate returns to baseline (<20/min)
2. Processing latency remains acceptable
3. No 429 errors from GitHub API

---

## 3. GitHub 429 Errors

### Symptoms
- Alert: `GitHub API 429 Errors` (critical)
- Logs show "rate limit exceeded" from GitHub
- Processing failures increasing

**Severity**: P1 - This should NEVER happen with rate limiting enabled

### Diagnosis

**Step 1: Confirm 429 Errors**
```
New Relic Query:
SELECT count(*)
FROM Log
WHERE message LIKE '%429%' OR message LIKE '%rate limit exceeded%'
SINCE 15 minutes ago
FACET installation_id
```

**Step 2: Check Rate Limiter Status**
```bash
# Verify rate limiting is enabled
kubectl get configmap policy-bot-config -o yaml | grep "enabled: true"

# Check if pods have the config
kubectl exec -it policy-bot-xxx -- cat /config/config.yaml | grep rate_limit -A 10
```

**Step 3: Check GitHub Rate Limit Remaining**
```
New Relic Query:
SELECT latest(handler.rate_limit.github_remaining)
FROM Metric
FACET installation_id
SINCE 30 minutes ago
```

### Root Causes

| Cause | Indicators | Solution |
|-------|------------|----------|
| Rate limiter disabled | Config shows enabled: false | Emergency enable |
| Rate limiter bypassed | Code path missing rate limiting | Immediate hotfix |
| Severe burst | GitHub headers show 0 remaining | Emergency rate reduction |
| Configuration sync issue | Config file ≠ running config | Force config reload |

### Resolution Steps

#### Emergency Actions (Do Immediately):
```bash
# 1. IMMEDIATE: Reduce rate limits to minimum safe values
kubectl edit configmap policy-bot-config
# Change:
#   installation_rate: 1.0  # Emergency minimum
#   global_rate: 20.0       # Emergency minimum

# 2. Force restart all pods
kubectl delete pods -l app=policy-bot

# 3. Verify rate limiting is active
# Check logs for "rate limiting enabled" message
kubectl logs -l app=policy-bot --tail=100 | grep "rate limit"
```

#### If Rate Limiter Was Disabled:
```yaml
# Enable rate limiting IMMEDIATELY
rate_limit:
  enabled: true  # MUST BE TRUE
  installation_rate: 2.0  # Start conservative
  installation_burst: 5
  global_rate: 30.0
  global_burst: 20
```

#### If Rate Limiter Bypassed (Code Issue):
```bash
# This indicates a critical bug
# 1. Page on-call engineer immediately
# 2. Review recent code changes
git log --oneline --all --since="3 days ago" -- server/handler/

# 3. Identify which code path bypasses rate limiter
# 4. Deploy emergency hotfix
# 5. Post-incident review required
```

### Verification

**Critical Checks**:
1. No new 429 errors in last 10 minutes
2. Rate limiter enabled confirmed in logs
3. `handler.rate_limit.wait_time` shows non-zero values (limiter active)
4. GitHub rate limit remaining is recovering

**Recovery Time**: Should see improvement within 5 minutes

### Post-Incident

1. **Incident Review**: Schedule within 24 hours
2. **Root Cause Analysis**: Why did rate limiter fail?
3. **Prevention**: Add circuit breaker tests for this scenario
4. **Monitoring**: Add alert for "rate limiter inactive"

---

## 4. Adaptive Rate Limiting Disabled

### Symptoms
- Alert: `Adaptive Disabled Unexpectedly`
- Dashboard shows `rate_limit.adaptive.enabled = 0`
- Should be enabled but isn't

### Diagnosis

**Step 1: Check Configuration**
```bash
# Check config file
kubectl get configmap policy-bot-config -o yaml | grep -A 5 "adaptive"

# Check running pods
kubectl exec -it policy-bot-xxx -- env | grep ADAPTIVE
```

**Step 2: Check for Configuration Errors**
```bash
# Review pod logs for configuration errors
kubectl logs -l app=policy-bot --tail=200 | grep -i "adaptive\|config\|error"
```

**Step 3: Check Deployment History**
```bash
# Recent deployments
kubectl rollout history deployment policy-bot

# Compare current vs previous config
kubectl rollout history deployment policy-bot --revision=N
```

### Root Causes

| Cause | Indicators | Solution |
|-------|------------|----------|
| Intentional disable | Recent config change | Verify with team, re-enable if needed |
| Configuration parse error | Logs show YAML errors | Fix YAML syntax |
| Feature flag system issue | Config correct but not applied | Restart pods |
| Rollback to pre-adaptive version | Old code version running | Re-deploy correct version |

### Resolution Steps

#### If Unintentional Disable:
```yaml
# Re-enable adaptive rate limiting
rate_limit:
  adaptive:
    enabled: true
    safety_factor: 0.8
    min_rate: 1.0
    max_rate: 4.0
    smoothing_factor: 0.3
    update_interval: 10s
```

#### If Configuration Error:
```bash
# Validate YAML syntax
kubectl get configmap policy-bot-config -o yaml > /tmp/config.yaml
yamllint /tmp/config.yaml

# Fix errors and reapply
kubectl apply -f /tmp/config.yaml

# Restart pods
kubectl rollout restart deployment policy-bot
```

### Verification

1. Check `rate_limit.adaptive.enabled = 1` in metrics
2. Verify adaptive adjustments happening:
```
SELECT count(*) FROM Metric
WHERE metricName = 'handler.rate_limit.adaptive.adjustments'
SINCE 5 minutes ago
```
3. Confirm rate limits are adjusting dynamically

---

## 5. Rate Limit Oscillations

### Symptoms
- Rate limits changing rapidly (every few seconds)
- Unstable processing latency
- Logs show frequent rate adjustments

### Diagnosis

**Step 1: Check Adjustment Frequency**
```
New Relic Query:
SELECT rate(sum(handler.rate_limit.adaptive.adjustments), 1 minute)
FROM Metric
SINCE 30 minutes ago
TIMESERIES
```

**Step 2: Check Rate Limit Values Over Time**
```
New Relic Query:
SELECT average(handler.rate_limit.adaptive.current_rate)
FROM Metric
SINCE 30 minutes ago
TIMESERIES
FACET installation_id
```

**Step 3: Review Smoothing Configuration**
```bash
kubectl get configmap policy-bot-config -o yaml | grep smoothing_factor
```

### Root Causes

| Cause | Indicators | Solution |
|-------|------------|----------|
| Low smoothing factor | Smoothing < 0.2 | Increase smoothing_factor to 0.3-0.5 |
| Unstable GitHub headers | Headers vary wildly | Increase update_interval |
| Min/max bounds too close | MaxRate - MinRate < 2.0 | Widen bounds |

### Resolution Steps

```yaml
# Adjust adaptive configuration to be more stable
rate_limit:
  adaptive:
    smoothing_factor: 0.5  # Increase from 0.3 (more stable)
    update_interval: 30s   # Increase from 10s (less frequent updates)
    min_rate: 1.0
    max_rate: 4.0          # Ensure adequate range
```

### Verification

1. Rate adjustments < 10 per minute
2. Rate values show smooth trends (not jagged)
3. Processing latency stabilizes

---

## 6. Per-Installation Rate Limit Breach

### Symptoms
- Specific installation hitting rate limits consistently
- Disproportionate traffic from one installation
- Other installations unaffected

### Diagnosis

**Step 1: Identify the Installation**
```
New Relic Query:
SELECT count(*)
FROM Metric
WHERE metricName = 'handler.rate_limit.throttled'
FACET installation_id
SINCE 1 hour ago
```

**Step 2: Analyze Installation Traffic**
```
New Relic Query:
SELECT count(*)
FROM Log
WHERE installation_id = 'XXXX'
FACET event_type
SINCE 6 hours ago
TIMESERIES
```

**Step 3: Check if Legitimate**
```bash
# Query GitHub API for installation details
# (requires appropriate permissions)
curl -H "Authorization: Bearer $TOKEN" \
  https://api.github.com/app/installations/XXXX
```

### Resolution Steps

#### If Legitimate High-Volume Customer:
```bash
# Option 1: Increase global rate to accommodate
# (if within overall capacity)

# Option 2: Request customer reduce webhook frequency
# (contact customer success team)

# Option 3: Per-installation override (future enhancement)
# Add to roadmap if needed frequently
```

#### If Suspicious/Malicious:
```bash
# 1. Document the incident
# 2. Contact security team
# 3. Consider temporary block

# Temporary workaround: Reduce their impact
# (no per-installation blocking exists yet)
# Add to firewall rules if necessary
```

---

## Configuration Reference

### Default Configuration
```yaml
rate_limit:
  enabled: true
  installation_rate: 3.0    # req/sec per installation
  installation_burst: 10    # burst capacity
  global_rate: 100.0        # req/sec global
  global_burst: 50          # burst capacity
  adaptive:
    enabled: false          # Feature flag OFF by default
    safety_factor: 0.8
    min_rate: 1.0
    max_rate: 4.0
    smoothing_factor: 0.3
    update_interval: 10s
```

### Conservative Configuration (During Issues)
```yaml
rate_limit:
  enabled: true
  installation_rate: 2.0    # Reduced 33%
  installation_burst: 5     # Reduced 50%
  global_rate: 50.0         # Reduced 50%
  global_burst: 25          # Reduced 50%
  adaptive:
    enabled: false          # Disable adaptive during incident
```

### Aggressive Configuration (High Load)
```yaml
rate_limit:
  enabled: true
  installation_rate: 3.5    # Increased 17%
  installation_burst: 15    # Increased 50%
  global_rate: 150.0        # Increased 50%
  global_burst: 75          # Increased 50%
  adaptive:
    enabled: true           # Adaptive helps optimize
    safety_factor: 0.8
    min_rate: 1.5           # Slightly higher floor
    max_rate: 4.0
    smoothing_factor: 0.3
    update_interval: 10s
```

---

## Monitoring & Alerts

### Key Metrics to Watch

| Metric | Normal Range | Alert Threshold | Severity |
|--------|--------------|-----------------|----------|
| handler.rate_limit.wait_time (P95) | < 500ms | > 2000ms | P2 |
| handler.rate_limit.throttled (rate) | < 20/min | > 50/min | P2 |
| handler.rate_limit.github_remaining | > 1000 | < 500 | P2 |
| sqs.queue.depth | < 50 | > 100 | P2 |
| github.api.429_errors | 0 | > 0 | P1 |
| rate_limit.adaptive.enabled | 1 (if enabled) | 0 (unexpected) | P1 |

### New Relic Dashboard

Access: [New Relic > Policy Bot > Rate Limiting Dashboard]

**Key Panels**:
1. Rate Limit Wait Times (P50, P95, P99)
2. Throttle Events per Minute
3. Per-Installation Rate Usage
4. Adaptive Adjustments Timeline
5. GitHub API Errors (should be zero)
6. Queue Depth vs Rate Limiting Correlation

---

## Escalation

### Severity Levels

- **P1 (Critical)**: GitHub 429 errors, rate limiter disabled
  - Page: On-call engineer immediately
  - Response time: < 15 minutes

- **P2 (High)**: High wait times, excessive throttling
  - Notify: Team Slack channel
  - Response time: < 1 hour

- **P3 (Medium)**: Minor oscillations, non-urgent issues
  - Ticket: Create Jira ticket
  - Response time: Next business day

### Contacts

- **On-Call**: PagerDuty "Policy Bot" rotation
- **Slack**: #policy-bot-alerts
- **Team Lead**: Platform Engineering Manager

---

## Post-Incident Checklist

After resolving any P1 or P2 incident:

- [ ] Document incident timeline
- [ ] Root cause identified
- [ ] Configuration changes documented
- [ ] Metrics validated back to normal
- [ ] Team notified of resolution
- [ ] Incident review scheduled (within 24-48 hours)
- [ ] Action items created to prevent recurrence
- [ ] Runbook updated if needed

---

## Common Commands Cheat Sheet

```bash
# Check current rate limit config
kubectl get configmap policy-bot-config -o yaml | grep -A 20 "rate_limit"

# View rate limiting logs
kubectl logs -l app=policy-bot --tail=500 | grep -i "rate limit"

# Check SQS queue depth
aws sqs get-queue-attributes --queue-url $QUEUE_URL --attribute-names All

# Restart policy-bot to pick up config changes
kubectl rollout restart deployment policy-bot

# Monitor rollout status
kubectl rollout status deployment policy-bot

# Get recent events
kubectl get events --sort-by='.lastTimestamp' | grep policy-bot

# Emergency: Disable rate limiting (NOT RECOMMENDED)
kubectl set env deployment/policy-bot RATE_LIMIT_ENABLED=false

# Emergency: Force rate limit to minimum
kubectl set env deployment/policy-bot RATE_LIMIT_INSTALLATION_RATE=1.0
```

---

**Last Reviewed**: November 2025
**Next Review**: January 2026
**Maintainer**: Platform Engineering Team
