# Phase 7: Migration and Rollout

## Phase Overview
**Priority**: MEDIUM - Safe deployment
**Estimated Time**: 2-3 hours
**Purpose**: Plan and execute a safe migration from current configuration to the new middleware-based routing system with proper rollback procedures

## Prerequisites
- [ ] Phases 1-6 completed successfully
- [ ] All tests passing
- [ ] Staging environment available
- [ ] Access to production configuration
- [ ] Rollback procedure documented
- [ ] Team notification prepared

## Context
We need to migrate from:
- Single GitHub config to separate enterprise/cloud configs
- Direct dispatcher registration to middleware-based routing
- Support both old and new configurations during transition
- Provide clear migration path for operators

## Tasks

### Task 1: Create Configuration Migration Script
- [ ] Create `/Users/dannytrevino/development/policy-bot/scripts/migrate_config.go`:
  ```go
  package main

  import (
      "encoding/json"
      "flag"
      "fmt"
      "io/ioutil"
      "os"

      "gopkg.in/yaml.v2"
      "github.com/palantir/policy-bot/server"
  )

  func main() {
      var (
          inputFile  = flag.String("input", "", "Input config file")
          outputFile = flag.String("output", "", "Output config file")
          format     = flag.String("format", "yaml", "Output format (yaml/json)")
          dryRun     = flag.Bool("dry-run", false, "Perform dry run")
      )
      flag.Parse()

      if *inputFile == "" {
          fmt.Fprintf(os.Stderr, "Error: input file required\n")
          os.Exit(1)
      }

      // Read existing config
      data, err := ioutil.ReadFile(*inputFile)
      if err != nil {
          fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
          os.Exit(1)
      }

      var config server.Config
      if err := yaml.Unmarshal(data, &config); err != nil {
          fmt.Fprintf(os.Stderr, "Error parsing config: %v\n", err)
          os.Exit(1)
      }

      // Perform migration
      migrated := migrateConfig(&config)

      // Output migrated config
      var output []byte
      if *format == "json" {
          output, err = json.MarshalIndent(migrated, "", "  ")
      } else {
          output, err = yaml.Marshal(migrated)
      }

      if err != nil {
          fmt.Fprintf(os.Stderr, "Error marshaling output: %v\n", err)
          os.Exit(1)
      }

      if *dryRun {
          fmt.Println("=== DRY RUN - Migrated Configuration ===")
          fmt.Println(string(output))
          fmt.Println("=== Migration Summary ===")
          printMigrationSummary(&config, migrated)
      } else {
          if *outputFile != "" {
              err = ioutil.WriteFile(*outputFile, output, 0644)
              if err != nil {
                  fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
                  os.Exit(1)
              }
              fmt.Printf("Configuration migrated to %s\n", *outputFile)
          } else {
              fmt.Println(string(output))
          }
      }
  }

  func migrateConfig(old *server.Config) *server.Config {
      new := *old // Copy existing config

      // If only legacy Github config exists, migrate it
      if old.Github.App.ID != 0 {
          if new.GithubEnterprise.App.ID == 0 {
              new.GithubEnterprise.Config = old.Github
              new.GithubEnterprise.WebhookRoute = "/api/github/hook"
              fmt.Println("Migrated Github config to GithubEnterprise")
          }

          if new.GithubCloud.App.ID == 0 {
              new.GithubCloud.Config = old.Github
              new.GithubCloud.WebhookRoute = "/api/github/hook"
              fmt.Println("Migrated Github config to GithubCloud")
          }

          // Clear legacy field in new config
          new.Github = githubapp.Config{}
          fmt.Println("Cleared legacy Github config field")
      }

      // Set middleware defaults if not present
      if new.Middleware.DefaultRoute == "" {
          new.Middleware.DefaultRoute = "cloud"
          new.Middleware.EnableCaching = true
          new.Middleware.CacheTTL = 5 * time.Minute
          fmt.Println("Added middleware configuration defaults")
      }

      return &new
  }

  func printMigrationSummary(old, new *server.Config) {
      fmt.Println("\nChanges:")
      if old.Github.App.ID != 0 && new.GithubEnterprise.App.ID != 0 {
          fmt.Printf("- Migrated Github (ID: %d) -> GithubEnterprise & GithubCloud\n",
              old.Github.App.ID)
      }
      if new.Middleware.DefaultRoute != "" {
          fmt.Printf("- Added middleware config (default: %s)\n",
              new.Middleware.DefaultRoute)
      }
  }
  ```
- [ ] Make script executable: `chmod +x scripts/migrate_config.go`
- [ ] Test with sample configs
- [ ] Save the file

### Task 2: Create Feature Flag System
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/feature_flags.go`:
  ```go
  package server

  import (
      "os"
      "strconv"
      "sync"
      "time"

      "github.com/rs/zerolog/log"
  )

  // FeatureFlags controls rollout of new features
  type FeatureFlags struct {
      mu                     sync.RWMutex
      UseMiddlewareRouting   bool
      UseEnhancedDetection   bool
      EnablePayloadInspection bool
      LogRoutingDecisions    bool
      RoutingPercentage      int // Percentage of requests to route via middleware
  }

  var (
      features     *FeatureFlags
      featuresOnce sync.Once
  )

  // GetFeatureFlags returns the singleton feature flags instance
  func GetFeatureFlags() *FeatureFlags {
      featuresOnce.Do(func() {
          features = &FeatureFlags{}
          features.LoadFromEnvironment()
          features.startReloadTimer()
      })
      return features
  }

  // LoadFromEnvironment loads feature flags from environment variables
  func (f *FeatureFlags) LoadFromEnvironment() {
      f.mu.Lock()
      defer f.mu.Unlock()

      f.UseMiddlewareRouting = getBoolEnv("FEATURE_MIDDLEWARE_ROUTING", true)
      f.UseEnhancedDetection = getBoolEnv("FEATURE_ENHANCED_DETECTION", false)
      f.EnablePayloadInspection = getBoolEnv("FEATURE_PAYLOAD_INSPECTION", false)
      f.LogRoutingDecisions = getBoolEnv("FEATURE_LOG_ROUTING", true)
      f.RoutingPercentage = getIntEnv("FEATURE_ROUTING_PERCENTAGE", 100)

      log.Info().
          Bool("middleware_routing", f.UseMiddlewareRouting).
          Bool("enhanced_detection", f.UseEnhancedDetection).
          Bool("payload_inspection", f.EnablePayloadInspection).
          Int("routing_percentage", f.RoutingPercentage).
          Msg("Loaded feature flags")
  }

  // IsMiddlewareRoutingEnabled checks if middleware routing is enabled
  func (f *FeatureFlags) IsMiddlewareRoutingEnabled() bool {
      f.mu.RLock()
      defer f.mu.RUnlock()
      return f.UseMiddlewareRouting
  }

  // ShouldUseMiddleware determines if a request should use middleware
  func (f *FeatureFlags) ShouldUseMiddleware(requestID string) bool {
      f.mu.RLock()
      defer f.mu.RUnlock()

      if !f.UseMiddlewareRouting {
          return false
      }

      if f.RoutingPercentage >= 100 {
          return true
      }

      // Use request ID for consistent routing
      hash := hashString(requestID)
      return (hash % 100) < f.RoutingPercentage
  }

  // startReloadTimer periodically reloads feature flags
  func (f *FeatureFlags) startReloadTimer() {
      ticker := time.NewTicker(1 * time.Minute)
      go func() {
          for range ticker.C {
              f.LoadFromEnvironment()
          }
      }()
  }

  func getBoolEnv(key string, defaultValue bool) bool {
      value := os.Getenv(key)
      if value == "" {
          return defaultValue
      }
      b, err := strconv.ParseBool(value)
      if err != nil {
          return defaultValue
      }
      return b
  }

  func getIntEnv(key string, defaultValue int) int {
      value := os.Getenv(key)
      if value == "" {
          return defaultValue
      }
      i, err := strconv.Atoi(value)
      if err != nil {
          return defaultValue
      }
      return i
  }

  func hashString(s string) int {
      h := 0
      for _, c := range s {
          h = h*31 + int(c)
      }
      if h < 0 {
          h = -h
      }
      return h
  }
  ```
- [ ] Save the file

### Task 3: Update Server to Support Feature Flags
- [ ] Modify `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Add feature flag check for webhook routing:
  ```go
  // Around line 340, replace webhook route registration with:

  features := GetFeatureFlags()

  if features.IsMiddlewareRoutingEnabled() {
      // New middleware-based routing
      mux.Handle(pat.Post(githubapp.DefaultWebhookRoute),
          middleware.SelectWebhookDispatcher(enterpriseDispatcher, cloudDispatcher))
      log.Info().Msg("Using middleware-based webhook routing")
  } else {
      // Legacy direct registration (for rollback)
      // Register cloud as default (legacy behavior)
      mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), cloudDispatcher)
      log.Warn().Msg("Using legacy webhook routing (middleware disabled)")
  }
  ```
- [ ] Add similar checks for API routes
- [ ] Save the file

### Task 4: Create Rollback Procedure Document
- [ ] Create `/Users/dannytrevino/development/policy-bot/docs/rollback_procedure.md`:
  ```markdown
  # Middleware Rollback Procedure

  ## Quick Rollback (< 1 minute)

  If issues are detected after deployment, perform immediate rollback:

  1. **Disable middleware routing via environment variable:**
     ```bash
     export FEATURE_MIDDLEWARE_ROUTING=false
     # Restart application
     ```

  2. **Verify rollback:**
     - Check logs for "Using legacy webhook routing"
     - Monitor error rates return to normal
     - Verify webhooks are being processed

  ## Configuration Rollback

  If configuration issues are detected:

  1. **Revert to legacy configuration:**
     ```yaml
     # Restore original github section
     github:
       app:
         id: <original_id>
         webhook_secret: <original_secret>

     # Comment out new sections
     # github_enterprise:
     # github_cloud:
     ```

  2. **Restart application**

  3. **Verify functionality**

  ## Full Code Rollback

  If critical issues persist:

  1. **Git revert to previous release:**
     ```bash
     git revert --no-commit <middleware_commit_range>
     git commit -m "Revert: Middleware routing implementation"
     git push
     ```

  2. **Deploy previous version:**
     ```bash
     ./deploy.sh <previous_version>
     ```

  ## Monitoring During Rollback

  Watch these metrics:
  - Webhook processing rate
  - Error rates
  - Response times
  - Queue depths

  ## Post-Rollback Actions

  1. Document issue that triggered rollback
  2. Create incident report
  3. Update test suite to catch issue
  4. Plan fix and re-deployment
  ```
- [ ] Save the file

### Task 5: Create Deployment Checklist
- [ ] Create `/Users/dannytrevino/development/policy-bot/docs/deployment_checklist.md`:
  ```markdown
  # Middleware Deployment Checklist

  ## Pre-Deployment

  - [ ] All tests passing
  - [ ] Configuration migrated and validated
  - [ ] Feature flags configured (disabled by default)
  - [ ] Rollback procedure reviewed
  - [ ] Team notified of deployment window
  - [ ] Metrics dashboards ready
  - [ ] Log aggregation configured

  ## Staging Deployment

  ### Stage 1: Deploy with Middleware Disabled
  - [ ] Deploy new code with `FEATURE_MIDDLEWARE_ROUTING=false`
  - [ ] Verify existing functionality works
  - [ ] Monitor for 15 minutes
  - [ ] Check no regression in metrics

  ### Stage 2: Enable for 10% Traffic
  - [ ] Set `FEATURE_ROUTING_PERCENTAGE=10`
  - [ ] Set `FEATURE_MIDDLEWARE_ROUTING=true`
  - [ ] Monitor routing decisions in logs
  - [ ] Verify both enterprise and cloud routes work
  - [ ] Monitor for 30 minutes

  ### Stage 3: Increase to 50% Traffic
  - [ ] Set `FEATURE_ROUTING_PERCENTAGE=50`
  - [ ] Monitor error rates
  - [ ] Check routing distribution metrics
  - [ ] Verify cache hit rates
  - [ ] Monitor for 1 hour

  ### Stage 4: Full Rollout in Staging
  - [ ] Set `FEATURE_ROUTING_PERCENTAGE=100`
  - [ ] Enable enhanced detection: `FEATURE_ENHANCED_DETECTION=true`
  - [ ] Run integration test suite
  - [ ] Perform load testing
  - [ ] Monitor for 24 hours

  ## Production Deployment

  ### Stage 1: Canary Deployment (Single Instance)
  - [ ] Deploy to single instance with middleware disabled
  - [ ] Verify instance health
  - [ ] Enable middleware for that instance only
  - [ ] Monitor for 1 hour

  ### Stage 2: Gradual Rollout
  - [ ] Deploy to 25% of instances
  - [ ] Monitor for 2 hours
  - [ ] Deploy to 50% of instances
  - [ ] Monitor for 2 hours
  - [ ] Deploy to 100% of instances

  ### Stage 3: Enable Enhanced Features
  - [ ] Enable payload inspection
  - [ ] Enable enhanced detection
  - [ ] Monitor cache performance
  - [ ] Verify no memory leaks

  ## Post-Deployment

  - [ ] Remove legacy configuration sections
  - [ ] Update documentation
  - [ ] Close deployment ticket
  - [ ] Schedule retrospective
  - [ ] Update runbooks

  ## Success Criteria

  - Error rate < 0.1%
  - p99 latency < 100ms increase
  - Successful routing of both enterprise and cloud webhooks
  - No memory leaks detected
  - Cache hit rate > 80%
  ```
- [ ] Save the file

### Task 6: Create Monitoring Configuration
- [ ] Create `/Users/dannytrevino/development/policy-bot/monitoring/alerts.yaml`:
  ```yaml
  alerts:
    - name: middleware_routing_errors
      expression: rate(policy_bot_routing_failures_total[5m]) > 0.01
      severity: warning
      description: "Middleware routing failures exceeding 1%"

    - name: routing_latency_high
      expression: histogram_quantile(0.99, policy_bot_routing_latency_seconds) > 0.1
      severity: warning
      description: "Routing decision latency exceeding 100ms at p99"

    - name: cache_hit_rate_low
      expression: |
        rate(policy_bot_routing_cache_hits_total{result="hit"}[5m]) /
        rate(policy_bot_routing_cache_hits_total[5m]) < 0.5
      severity: info
      description: "Routing cache hit rate below 50%"

    - name: dispatcher_imbalance
      expression: |
        abs(
          rate(policy_bot_routing_decisions_total{route="enterprise"}[5m]) -
          rate(policy_bot_routing_decisions_total{route="cloud"}[5m])
        ) > 100
      severity: info
      description: "Significant imbalance in routing distribution"

  dashboards:
    - name: middleware_routing
      panels:
        - title: "Routing Decisions"
          query: sum by (route, detection_method) (rate(policy_bot_routing_decisions_total[5m]))

        - title: "Routing Latency"
          query: histogram_quantile(0.99, policy_bot_routing_latency_seconds)

        - title: "Cache Performance"
          query: rate(policy_bot_routing_cache_hits_total[5m])

        - title: "Detection Methods"
          query: sum by (method) (increase(policy_bot_routing_decisions_total[1h]))
  ```
- [ ] Configure alerts in monitoring system
- [ ] Create Grafana dashboards
- [ ] Save the file

### Task 7: Create Migration Communication Plan
- [ ] Create `/Users/dannytrevino/development/policy-bot/docs/migration_communication.md`:
  ```markdown
  # Migration Communication Plan

  ## Stakeholder Notification

  ### 2 Weeks Before
  **To: All Teams Using Policy Bot**
  Subject: Upcoming Policy Bot Routing Enhancement

  We will be deploying an enhancement to Policy Bot's webhook routing system
  to better support GitHub Enterprise Server and Cloud environments.

  **What's Changing:**
  - Improved routing for enterprise vs cloud webhooks
  - Better detection of webhook sources
  - No changes to API or functionality

  **Impact:**
  - No expected downtime
  - No action required from users
  - Gradual rollout over 1 week period

  ### 1 Day Before
  **To: On-Call Team**
  Subject: Policy Bot Middleware Deployment Tomorrow

  Deployment window: [DATE] 10:00-14:00 UTC

  Key points:
  - Feature flag controlled rollout
  - Rollback procedure in docs/rollback_procedure.md
  - Monitor dashboard: [LINK]
  - Escalation: [CONTACT]

  ### During Deployment
  **Slack #policy-bot Channel:**
  ```
  🚀 Starting Policy Bot routing enhancement deployment
  Stage 1/4: Deploying with feature disabled...
  ✅ Stage 1 complete, monitoring...
  ```

  ### Post-Deployment
  **To: All Stakeholders**
  Subject: Policy Bot Routing Enhancement Complete

  The routing enhancement has been successfully deployed.

  **Results:**
  - Zero downtime achieved
  - [X]% improvement in routing accuracy
  - No user action required

  Please report any issues to [CONTACT].
  ```
- [ ] Schedule communications
- [ ] Prepare Slack notifications
- [ ] Save the file

### Task 8: Create Validation Script
- [ ] Create `/Users/dannytrevino/development/policy-bot/scripts/validate_deployment.sh`:
  ```bash
  #!/bin/bash

  set -e

  echo "=== Policy Bot Middleware Deployment Validation ==="

  # Colors for output
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  NC='\033[0m'

  # Configuration
  POLICY_BOT_URL=${POLICY_BOT_URL:-"http://localhost:8080"}
  WEBHOOK_SECRET=${WEBHOOK_SECRET:-"test-secret"}

  # Validation functions
  check_health() {
      echo -n "Checking health endpoint... "
      response=$(curl -s -o /dev/null -w "%{http_code}" ${POLICY_BOT_URL}/api/health)
      if [ "$response" = "200" ]; then
          echo -e "${GREEN}✓${NC}"
          return 0
      else
          echo -e "${RED}✗ (HTTP $response)${NC}"
          return 1
      fi
  }

  check_enterprise_routing() {
      echo -n "Testing enterprise webhook routing... "

      response=$(curl -s -X POST ${POLICY_BOT_URL}/api/github/hook \
          -H "X-GitHub-Enterprise-Host: ghes.example.com" \
          -H "X-GitHub-Event: ping" \
          -H "X-GitHub-Delivery: test-enterprise-123" \
          -H "Content-Type: application/json" \
          -d '{"zen": "Design for failure."}' \
          -o /dev/null -w "%{http_code}")

      if [ "$response" = "200" ] || [ "$response" = "202" ]; then
          echo -e "${GREEN}✓${NC}"
          return 0
      else
          echo -e "${RED}✗ (HTTP $response)${NC}"
          return 1
      fi
  }

  check_cloud_routing() {
      echo -n "Testing cloud webhook routing... "

      response=$(curl -s -X POST ${POLICY_BOT_URL}/api/github/hook \
          -H "x-dcp-destination-host: github.com" \
          -H "X-GitHub-Event: ping" \
          -H "X-GitHub-Delivery: test-cloud-456" \
          -H "Content-Type: application/json" \
          -d '{"zen": "Avoid administrative distraction."}' \
          -o /dev/null -w "%{http_code}")

      if [ "$response" = "200" ] || [ "$response" = "202" ]; then
          echo -e "${GREEN}✓${NC}"
          return 0
      else
          echo -e "${RED}✗ (HTTP $response)${NC}"
          return 1
      fi
  }

  check_metrics() {
      echo -n "Checking metrics endpoint... "
      response=$(curl -s ${POLICY_BOT_URL}/api/metrics | grep -c "policy_bot_routing")
      if [ "$response" -gt "0" ]; then
          echo -e "${GREEN}✓ (routing metrics present)${NC}"
          return 0
      else
          echo -e "${YELLOW}⚠ (no routing metrics found)${NC}"
          return 0  # Warning only
      fi
  }

  check_feature_flags() {
      echo -n "Checking feature flags... "

      if [ -n "$FEATURE_MIDDLEWARE_ROUTING" ]; then
          echo -e "${GREEN}✓ (FEATURE_MIDDLEWARE_ROUTING=$FEATURE_MIDDLEWARE_ROUTING)${NC}"
      else
          echo -e "${YELLOW}⚠ (FEATURE_MIDDLEWARE_ROUTING not set)${NC}"
      fi
  }

  # Main validation
  echo ""
  failures=0

  check_health || ((failures++))
  check_enterprise_routing || ((failures++))
  check_cloud_routing || ((failures++))
  check_metrics || ((failures++))
  check_feature_flags

  echo ""
  echo "=== Validation Summary ==="
  if [ "$failures" -eq 0 ]; then
      echo -e "${GREEN}All checks passed!${NC}"
      exit 0
  else
      echo -e "${RED}$failures check(s) failed${NC}"
      exit 1
  fi
  ```
- [ ] Make script executable: `chmod +x scripts/validate_deployment.sh`
- [ ] Test script in local environment
- [ ] Save the file

### Task 9: Document Configuration Examples
- [ ] Create `/Users/dannytrevino/development/policy-bot/config/examples/`:
- [ ] Create `config/examples/legacy.yml`:
  ```yaml
  # Legacy configuration (deprecated)
  github:
    app:
      id: 12345
      webhook_secret: "shared-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ...
        -----END RSA PRIVATE KEY-----
  ```
- [ ] Create `config/examples/enterprise_only.yml`:
  ```yaml
  # GitHub Enterprise Server only
  github_enterprise:
    app:
      id: 11111
      webhook_secret: "enterprise-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ...
        -----END RSA PRIVATE KEY-----
    base_url: "https://github.enterprise.company.com"
    webhook_route: "/api/github/hook"

  middleware:
    default_route: "enterprise"
    enable_caching: true
    cache_ttl: 5m
  ```
- [ ] Create `config/examples/dual_environment.yml`:
  ```yaml
  # Both Enterprise Server and Cloud
  github_enterprise:
    app:
      id: 11111
      webhook_secret: "enterprise-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ...
        -----END RSA PRIVATE KEY-----
    base_url: "https://github.enterprise.company.com"

  github_cloud:
    app:
      id: 22222
      webhook_secret: "cloud-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ...
        -----END RSA PRIVATE KEY-----

  middleware:
    default_route: "cloud"
    enable_caching: true
    cache_ttl: 5m
    payload_inspection: true
  ```
- [ ] Save all example files

## Acceptance Criteria
- [ ] Migration script successfully converts legacy configs
- [ ] Feature flags control middleware activation
- [ ] Rollback procedure documented and tested
- [ ] Deployment checklist comprehensive
- [ ] Monitoring alerts configured
- [ ] Communication plan ready
- [ ] Validation script works correctly
- [ ] Example configurations provided
- [ ] Zero-downtime deployment possible
- [ ] Gradual rollout supported

## Testing Checklist
- [ ] Test migration script with various configs
- [ ] Test feature flag toggles
- [ ] Test rollback procedure
- [ ] Test validation script
- [ ] Test gradual rollout (10%, 50%, 100%)
- [ ] Test monitoring alerts trigger correctly
- [ ] Load test during migration

## Risk Mitigation
- Feature flags allow instant rollback
- Gradual rollout limits blast radius
- Monitoring alerts detect issues early
- Validation script confirms functionality
- Legacy code path preserved for rollback

## Notes for Next Phase
- Phase 8 will focus on documentation and long-term monitoring
- Collect metrics during rollout for analysis
- Schedule post-deployment review
- Plan for legacy code removal timeline