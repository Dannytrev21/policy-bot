# Policy-Bot Middleware Enhancement Plan

## Executive Summary

This plan addresses the implementation of header-based routing middleware to properly separate GitHub Enterprise Server (GHES) and GitHub Enterprise Cloud (GHEC) webhook processing. The analysis revealed several critical issues in the current implementation that must be fixed alongside the middleware enhancement.

## Current State Analysis

### Critical Issues Identified

1. **Empty Middleware File**: `/server/middleware/header_check.go` exists but is empty (0 bytes)
2. **Dispatcher Bug**: Enterprise dispatcher incorrectly uses cloud handlers (server.go:290)
3. **Route Collision**: Both dispatchers registered on same webhook path (server.go:339-341)
4. **Config Structure**: Missing separate `GithubEnterprise` and `GithubCloud` config fields
5. **Import Missing**: `server.go` references middleware package without importing it
6. **Compilation Broken**: Code references non-existent `middleware.SelectIndexHandler`

### Working Components

- Dual handler setup (enterprise/cloud) with separate Base instances
- Dual client creators with caching and metrics
- Independent async schedulers for each environment
- SQS consumer with proper message routing by source field
- Policy evaluation and status posting logic
- Separate details page routes (`/ghes/` and `/ghec/`)

## Implementation Phases

### Phase 1: Fix Critical Bugs and Compilation Issues
**Priority: URGENT - Blocks all other work**
**Estimated Time: 1-2 hours**

#### 1.1 Fix Dispatcher Handler Assignment
- **File**: `server/server.go:290`
- **Change**: `enterpriseDispatcher` should use `enterpriseHandlers` not `cloudHandlers`
- **Impact**: Enterprise webhooks currently processed with wrong handlers

#### 1.2 Add Middleware Import
- **File**: `server/server.go`
- **Add**: Import statement for middleware package
- **Path**: `"github.com/palantir/policy-bot/server/middleware"`

#### 1.3 Temporarily Fix Compilation
- **Option A**: Comment out line 373 (middleware.SelectIndexHandler call)
- **Option B**: Create minimal stub implementation in middleware package
- **Goal**: Allow code to compile while developing full middleware

### Phase 2: Configuration Structure Enhancement
**Priority: HIGH - Foundation for middleware**
**Estimated Time: 2-3 hours**

#### 2.1 Extend Config Structure
```go
// server/config.go
type Config struct {
    // ... existing fields ...

    // Deprecated - will be removed after migration
    Github githubapp.Config `yaml:"github"`

    // New separate configurations
    GithubEnterprise GithubAppConfig `yaml:"github_enterprise"`
    GithubCloud      GithubAppConfig `yaml:"github_cloud"`
}

type GithubAppConfig struct {
    githubapp.Config `yaml:",inline"`

    // Additional app-specific settings
    WebhookRoute string `yaml:"webhook_route" json:"webhook_route"`
    EventRouting map[string]string `yaml:"event_routing" json:"event_routing"`
}
```

#### 2.2 Configuration Loading Logic
- Support both old (`Github`) and new (`GithubEnterprise`/`GithubCloud`) formats
- Migration path: If only `Github` is set, duplicate to both new fields
- Validation: Ensure at least one configuration is present
- Add deprecation warning for old format

#### 2.3 Update Server Initialization
- Use `c.GithubEnterprise` for enterprise components
- Use `c.GithubCloud` for cloud components
- Update webhook secrets to use respective configs
- Update App ID and private key references

### Phase 3: Implement Core Middleware Functions
**Priority: HIGH - Core functionality**
**Estimated Time: 3-4 hours**

#### 3.1 Header Detection Middleware
```go
// server/middleware/header_check.go
package middleware

import (
    "net/http"
    "github.com/rs/zerolog"
)

// Header constants for routing
const (
    HeaderGitHubEnterpriseHost = "X-GitHub-Enterprise-Host"
    HeaderDCPDestinationHost   = "x-dcp-destination-host"
    HeaderGitHubEvent          = "X-GitHub-Event"
    HeaderGitHubDelivery       = "X-GitHub-Delivery"
)

// SelectWebhookDispatcher routes webhooks to appropriate dispatcher based on headers
func SelectWebhookDispatcher(enterprise, cloud http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        logger := zerolog.Ctx(ctx)

        // Log routing decision for debugging
        deliveryID := r.Header.Get(HeaderGitHubDelivery)
        eventType := r.Header.Get(HeaderGitHubEvent)

        // Route based on headers
        if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
            logger.Info().
                Str("delivery_id", deliveryID).
                Str("event_type", eventType).
                Str("route", "enterprise").
                Msg("Routing webhook to enterprise dispatcher")
            enterprise.ServeHTTP(w, r)
            return
        }

        if r.Header.Get(HeaderDCPDestinationHost) != "" {
            logger.Info().
                Str("delivery_id", deliveryID).
                Str("event_type", eventType).
                Str("route", "cloud").
                Msg("Routing webhook to cloud dispatcher")
            cloud.ServeHTTP(w, r)
            return
        }

        // Default routing (configurable)
        logger.Warn().
            Str("delivery_id", deliveryID).
            Str("event_type", eventType).
            Msg("No routing header found, using default cloud dispatcher")
        cloud.ServeHTTP(w, r)
    })
}
```

#### 3.2 Index Handler Selection
```go
// SelectIndexHandler routes index page requests based on headers
func SelectIndexHandler(
    enterpriseBase, cloudBase handler.Base,
    enterpriseConfig, cloudConfig *GithubAppConfig,
    templates templatetree.HTMLTree,
) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()

        // Determine which handler to use
        var base handler.Base
        var config *GithubAppConfig

        if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
            base = enterpriseBase
            config = enterpriseConfig
        } else {
            base = cloudBase
            config = cloudConfig
        }

        // Create and serve the index handler
        indexHandler := &handler.Index{
            Base:         base,
            GithubConfig: &config.Config,
            Templates:    templates,
        }

        indexHandler.ServeHTTP(w, r)
    }
}
```

#### 3.3 API Route Selection Middleware
```go
// SelectAPIHandler routes API requests to appropriate handler
func SelectAPIHandler(enterprise, cloud http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Check for enterprise indicator in request
        // Could be header, query param, or path segment
        if isEnterpriseRequest(r) {
            enterprise.ServeHTTP(w, r)
        } else {
            cloud.ServeHTTP(w, r)
        }
    })
}
```

### Phase 4: Update Routing Configuration
**Priority: HIGH - Integrate middleware**
**Estimated Time: 2-3 hours**

#### 4.1 Webhook Route Updates
```go
// server/server.go - Replace lines 339-341
mux.Handle(
    pat.Post(githubapp.DefaultWebhookRoute),
    middleware.SelectWebhookDispatcher(enterpriseDispatcher, cloudDispatcher),
)
```

#### 4.2 Index Route Updates
```go
// server/server.go - Fix line 373
mux.Handle(pat.Get("/"), middleware.SelectIndexHandler(
    enterpriseBasePolicyHandler,
    cloudBasePolicyHandler,
    &c.GithubEnterprise,
    &c.GithubCloud,
    templates,
))
```

#### 4.3 API Routes Updates
```go
// Validation endpoint
mux.Handle(pat.Post("/api/validate"), middleware.SelectAPIHandler(
    &handler.Validate{Base: enterpriseBasePolicyHandler},
    &handler.Validate{Base: cloudBasePolicyHandler},
))

// Simulation endpoint
mux.Handle(pat.Post("/api/simulate/:owner/:repo/:number"), middleware.SelectAPIHandler(
    &handler.Simulate{Base: enterpriseBasePolicyHandler},
    &handler.Simulate{Base: cloudBasePolicyHandler},
))
```

#### 4.4 Details Routes (Already Separate)
- Keep existing `/details/ghes/` and `/details/ghec/` routes
- These already have proper separation

### Phase 5: Enhanced Header Detection and Fallback Logic
**Priority: MEDIUM - Improve reliability**
**Estimated Time: 2-3 hours**

#### 5.1 Advanced Header Detection
```go
// middleware/detection.go
package middleware

import (
    "encoding/json"
    "io"
    "strings"
)

// DetectGitHubSource determines if request is from GHES or GHEC
func DetectGitHubSource(r *http.Request) string {
    // Priority 1: Explicit headers
    if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
        return "enterprise"
    }
    if r.Header.Get(HeaderDCPDestinationHost) != "" {
        return "cloud"
    }

    // Priority 2: URL analysis
    if strings.Contains(r.Host, "github.com") {
        return "cloud"
    }

    // Priority 3: Payload inspection (if needed)
    if r.Method == "POST" && r.Body != nil {
        source := detectFromPayload(r)
        if source != "" {
            return source
        }
    }

    // Default
    return "cloud"
}

func detectFromPayload(r *http.Request) string {
    // Read body (careful to restore it)
    body, _ := io.ReadAll(r.Body)
    r.Body = io.NopCloser(bytes.NewBuffer(body))

    var payload map[string]interface{}
    if json.Unmarshal(body, &payload) == nil {
        // Check repository URL or installation URL
        if repo, ok := payload["repository"].(map[string]interface{}); ok {
            if url, ok := repo["html_url"].(string); ok {
                if strings.Contains(url, "github.com") {
                    return "cloud"
                }
                return "enterprise"
            }
        }
    }

    return ""
}
```

#### 5.2 Metrics and Monitoring
```go
// Add metrics for routing decisions
var (
    routingDecisions = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "policy_bot_routing_decisions_total",
            Help: "Total number of routing decisions made",
        },
        []string{"source", "detection_method"},
    )
)
```

### Phase 6: Testing Strategy
**Priority: HIGH - Ensure reliability**
**Estimated Time: 4-5 hours**

#### 6.1 Unit Tests
- **Middleware Functions**: Test header detection logic
- **Routing Logic**: Test all routing combinations
- **Configuration**: Test loading and validation
- **Fallback Logic**: Test default routing behavior

#### 6.2 Integration Tests
```go
// server/middleware/header_check_test.go
func TestSelectWebhookDispatcher(t *testing.T) {
    tests := []struct {
        name        string
        headers     map[string]string
        wantRoute   string
    }{
        {
            name:      "enterprise_header",
            headers:   map[string]string{"X-GitHub-Enterprise-Host": "ghes.example.com"},
            wantRoute: "enterprise",
        },
        {
            name:      "cloud_header",
            headers:   map[string]string{"x-dcp-destination-host": "github.com"},
            wantRoute: "cloud",
        },
        {
            name:      "no_header_default",
            headers:   map[string]string{},
            wantRoute: "cloud",
        },
    }
    // ... test implementation
}
```

#### 6.3 End-to-End Tests
- Deploy to test environment
- Send webhooks with different headers
- Verify correct dispatcher processes events
- Check metrics and logs

### Phase 7: Migration and Rollout
**Priority: MEDIUM - Safe deployment**
**Estimated Time: 2-3 hours**

#### 7.1 Configuration Migration
- Create migration script for existing configs
- Document new configuration format
- Provide examples for both GHES and GHEC

#### 7.2 Rollout Strategy
1. **Stage 1**: Deploy with backward compatibility
   - Support both old and new config formats
   - Log deprecation warnings

2. **Stage 2**: Monitor and validate
   - Check routing metrics
   - Verify both dispatchers receiving events
   - Monitor error rates

3. **Stage 3**: Remove deprecated code
   - Remove old config format support
   - Clean up temporary compatibility code

#### 7.3 Rollback Plan
- Keep feature flag to disable middleware routing
- Ability to revert to direct dispatcher registration
- Document rollback procedures

### Phase 8: Documentation and Monitoring
**Priority: MEDIUM - Operational excellence**
**Estimated Time: 2-3 hours**

#### 8.1 Documentation Updates
- **Configuration Guide**: New GHES/GHEC sections
- **Operations Guide**: How routing works
- **Troubleshooting**: Common header issues
- **Architecture Diagram**: Updated flow

#### 8.2 Monitoring Enhancements
- Dashboard for routing decisions
- Alerts for routing failures
- Metrics for each dispatcher
- Latency comparisons

#### 8.3 Logging Improvements
- Structured logging for routing decisions
- Correlation IDs across dispatchers
- Audit trail for security

## Risk Assessment and Mitigation

### High Risks
1. **Webhook Processing Interruption**
   - Mitigation: Implement gradually with feature flags
   - Rollback: Direct dispatcher registration

2. **Header Detection Failures**
   - Mitigation: Multiple detection methods
   - Fallback: Default to cloud dispatcher

3. **Configuration Complexity**
   - Mitigation: Backward compatibility
   - Tools: Migration scripts and validation

### Medium Risks
1. **Performance Impact**
   - Mitigation: Efficient header checking
   - Monitoring: Latency metrics

2. **Debugging Complexity**
   - Mitigation: Enhanced logging
   - Tools: Routing decision tracking

## Success Criteria

1. **Functional Requirements**
   - [ ] Webhooks routed correctly based on headers
   - [ ] Both dispatchers process their events
   - [ ] No event loss during routing
   - [ ] Backward compatibility maintained

2. **Performance Requirements**
   - [ ] Routing adds <1ms latency
   - [ ] No increase in error rate
   - [ ] Memory usage stable

3. **Operational Requirements**
   - [ ] Clear routing metrics
   - [ ] Debugging capabilities
   - [ ] Documentation complete
   - [ ] Rollback procedure tested

## Timeline Summary

- **Phase 1**: 1-2 hours (Critical fixes)
- **Phase 2**: 2-3 hours (Configuration)
- **Phase 3**: 3-4 hours (Core middleware)
- **Phase 4**: 2-3 hours (Routing updates)
- **Phase 5**: 2-3 hours (Enhanced detection)
- **Phase 6**: 4-5 hours (Testing)
- **Phase 7**: 2-3 hours (Migration)
- **Phase 8**: 2-3 hours (Documentation)

**Total Estimated Time**: 18-26 hours

## Next Steps

1. Fix critical bugs (Phase 1) - **IMMEDIATE**
2. Review and approve this plan
3. Create feature branch for implementation
4. Implement phases sequentially
5. Test in staging environment
6. Plan production rollout

## Appendix: Current Issues Reference

### Files to Modify
- `server/server.go` - Fix bugs, update routing
- `server/config.go` - Add new config fields
- `server/middleware/header_check.go` - Implement middleware
- `server/sqsconsumer/processor.go` - Reference implementation

### Bugs to Fix
- Line 290: `enterpriseDispatcher` using wrong handlers
- Line 339-341: Route collision on webhook endpoint
- Line 373: Missing middleware implementation
- Config: Missing GithubEnterprise/GithubCloud fields

### Patterns to Follow
- SQS consumer's source-based routing
- Existing dual Base handler setup
- Current metrics and logging patterns
- Error handling conventions