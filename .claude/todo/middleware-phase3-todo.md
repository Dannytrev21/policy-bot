# Phase 3: Core Middleware Implementation

## Phase Overview
**Priority**: HIGH - Core functionality
**Estimated Time**: 3-4 hours
**Purpose**: Implement the actual header-based routing middleware functions to properly route webhooks and web requests between enterprise and cloud dispatchers

## Prerequisites
- [x] Phase 1 completed (code compiles)
- [x] Phase 2 completed (configuration structure ready)
- [x] Access to `/Users/dannytrevino/development/policy-bot/server/middleware/header_check.go`
- [x] Understanding of HTTP middleware patterns
- [x] Understanding of GitHub webhook headers
- [x] Review SQS processor routing logic at `/Users/dannytrevino/development/policy-bot/server/sqsconsumer/processor.go`

## Context
We need to implement three main middleware functions:
1. `SelectWebhookDispatcher` - Routes webhooks based on headers
2. `SelectIndexHandler` - Routes index page requests
3. `SelectAPIHandler` - Routes API requests

Headers to detect:
- `X-GitHub-Enterprise-Host` - Present in GHES webhooks
- `x-dcp-destination-host` - May be present in GHEC webhooks
- `X-GitHub-Event` - Event type for logging
- `X-GitHub-Delivery` - Delivery ID for tracing

## Tasks

### Task 1: Set Up Middleware Package Structure
- [x] Open `/Users/dannytrevino/development/policy-bot/server/middleware/header_check.go`
- [x] Replace the stub implementation with proper package setup:
  ```go
  package middleware

  import (
      "net/http"
      "github.com/rs/zerolog"
      "github.com/bluekeyes/templatetree"
      "github.com/palantir/policy-bot/server/handler"
      "github.com/palantir/policy-bot/server/config"
  )

  // Header constants for routing decisions
  const (
      HeaderGitHubEnterpriseHost = "X-GitHub-Enterprise-Host"
      HeaderDCPDestinationHost   = "x-dcp-destination-host"
      HeaderGitHubEvent          = "X-GitHub-Event"
      HeaderGitHubDelivery       = "X-GitHub-Delivery"
      HeaderGitHubHookID         = "X-GitHub-Hook-ID"
  )
  ```
- [x] Save the file

### Task 2: Implement SelectWebhookDispatcher
- [x] In `header_check.go`, add the webhook dispatcher selection function:
  ```go
  // SelectWebhookDispatcher routes webhooks to appropriate dispatcher based on headers
  func SelectWebhookDispatcher(enterprise, cloud http.Handler) http.Handler {
      return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          ctx := r.Context()
          logger := zerolog.Ctx(ctx)

          // Extract headers for logging
          deliveryID := r.Header.Get(HeaderGitHubDelivery)
          eventType := r.Header.Get(HeaderGitHubEvent)
          hookID := r.Header.Get(HeaderGitHubHookID)
          enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
          dcpHost := r.Header.Get(HeaderDCPDestinationHost)

          // Create sub-logger with request details
          reqLogger := logger.With().
              Str("delivery_id", deliveryID).
              Str("event_type", eventType).
              Str("hook_id", hookID).
              Logger()

          // Route based on headers
          if enterpriseHost != "" {
              reqLogger.Info().
                  Str("enterprise_host", enterpriseHost).
                  Str("route", "enterprise").
                  Msg("Routing webhook to enterprise dispatcher")
              enterprise.ServeHTTP(w, r)
              return
          }

          if dcpHost != "" {
              reqLogger.Info().
                  Str("dcp_host", dcpHost).
                  Str("route", "cloud").
                  Msg("Routing webhook to cloud dispatcher via DCP header")
              cloud.ServeHTTP(w, r)
              return
          }

          // Default routing - use cloud dispatcher
          reqLogger.Warn().
              Msg("No routing header found, using default cloud dispatcher")
          cloud.ServeHTTP(w, r)
      })
  }
  ```
- [x] Save the file

### Task 3: Implement SelectIndexHandler
- [x] Add the index handler selection function:
  ```go
  // SelectIndexHandler routes index page requests based on headers
  func SelectIndexHandler(
      enterpriseBase handler.Base,
      cloudBase handler.Base,
      enterpriseConfig *config.GithubAppConfig,
      cloudConfig *config.GithubAppConfig,
      templates templatetree.HTMLTree,
  ) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          ctx := r.Context()
          logger := zerolog.Ctx(ctx)

          // Determine which handler to use based on headers
          var selectedBase handler.Base
          var selectedConfig *githubapp.Config
          var routeName string

          enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
          dcpHost := r.Header.Get(HeaderDCPDestinationHost)

          if enterpriseHost != "" {
              selectedBase = enterpriseBase
              selectedConfig = &enterpriseConfig.Config
              routeName = "enterprise"
              logger.Debug().
                  Str("enterprise_host", enterpriseHost).
                  Str("route", routeName).
                  Msg("Routing index request to enterprise handler")
          } else if dcpHost != "" {
              selectedBase = cloudBase
              selectedConfig = &cloudConfig.Config
              routeName = "cloud"
              logger.Debug().
                  Str("dcp_host", dcpHost).
                  Str("route", routeName).
                  Msg("Routing index request to cloud handler via DCP header")
          } else {
              // Default to cloud
              selectedBase = cloudBase
              selectedConfig = &cloudConfig.Config
              routeName = "cloud"
              logger.Debug().
                  Str("route", routeName).
                  Msg("Routing index request to cloud handler (default)")
          }

          // Create and serve the index handler
          indexHandler := &handler.Index{
              Base:         selectedBase,
              GithubConfig: selectedConfig,
              Templates:    templates,
          }

          indexHandler.ServeHTTP(w, r)
      })
  }
  ```
- [x] Add import for `"github.com/palantir/go-githubapp/githubapp"` if not already present
- [x] Save the file

### Task 4: Implement SelectAPIHandler
- [x] Add the API handler selection function:
  ```go
  // SelectAPIHandler routes API requests to appropriate handler based on headers or path
  func SelectAPIHandler(enterprise, cloud http.Handler) http.Handler {
      return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          ctx := r.Context()
          logger := zerolog.Ctx(ctx)

          // Check for explicit routing headers
          enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
          dcpHost := r.Header.Get(HeaderDCPDestinationHost)

          // Additional check: look for "source" query parameter
          source := r.URL.Query().Get("source")

          var selectedHandler http.Handler
          var routeName string

          // Priority order: headers first, then query param
          if enterpriseHost != "" {
              selectedHandler = enterprise
              routeName = "enterprise"
              logger.Debug().
                  Str("enterprise_host", enterpriseHost).
                  Str("route", routeName).
                  Str("path", r.URL.Path).
                  Msg("Routing API request to enterprise handler")
          } else if source == "enterprise" || source == "ghes" {
              selectedHandler = enterprise
              routeName = "enterprise"
              logger.Debug().
                  Str("source_param", source).
                  Str("route", routeName).
                  Str("path", r.URL.Path).
                  Msg("Routing API request to enterprise handler via query param")
          } else if dcpHost != "" {
              selectedHandler = cloud
              routeName = "cloud"
              logger.Debug().
                  Str("dcp_host", dcpHost).
                  Str("route", routeName).
                  Str("path", r.URL.Path).
                  Msg("Routing API request to cloud handler via DCP header")
          } else if source == "cloud" || source == "ghec" {
              selectedHandler = cloud
              routeName = "cloud"
              logger.Debug().
                  Str("source_param", source).
                  Str("route", routeName).
                  Str("path", r.URL.Path).
                  Msg("Routing API request to cloud handler via query param")
          } else {
              // Default to cloud
              selectedHandler = cloud
              routeName = "cloud"
              logger.Debug().
                  Str("route", routeName).
                  Str("path", r.URL.Path).
                  Msg("Routing API request to cloud handler (default)")
          }

          selectedHandler.ServeHTTP(w, r)
      })
  }
  ```
- [x] Save the file

### Task 5: Add Helper Functions for Route Detection
- [x] Add helper functions to make the code more maintainable:
  ```go
  // DetectSource determines if a request is from enterprise or cloud
  func DetectSource(r *http.Request) string {
      // Check headers first
      if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
          return "enterprise"
      }
      if r.Header.Get(HeaderDCPDestinationHost) != "" {
          return "cloud"
      }

      // Check query parameters
      source := r.URL.Query().Get("source")
      if source == "enterprise" || source == "ghes" {
          return "enterprise"
      }
      if source == "cloud" || source == "ghec" {
          return "cloud"
      }

      // Default
      return "cloud"
  }

  // IsEnterpriseRequest checks if the request should be routed to enterprise
  func IsEnterpriseRequest(r *http.Request) bool {
      return DetectSource(r) == "enterprise"
  }

  // ExtractGitHubHeaders extracts common GitHub webhook headers for logging
  func ExtractGitHubHeaders(r *http.Request) map[string]string {
      return map[string]string{
          "delivery_id":      r.Header.Get(HeaderGitHubDelivery),
          "event_type":       r.Header.Get(HeaderGitHubEvent),
          "hook_id":          r.Header.Get(HeaderGitHubHookID),
          "enterprise_host":  r.Header.Get(HeaderGitHubEnterpriseHost),
          "dcp_host":         r.Header.Get(HeaderDCPDestinationHost),
      }
  }
  ```
- [x] Save the file

### Task 6: Create Middleware with Metrics Support
- [x] Add metrics tracking to the middleware:
  ```go
  // Import at top of file
  import (
      "github.com/prometheus/client_golang/prometheus"
      "github.com/prometheus/client_golang/prometheus/promauto"
  )

  // Define metrics
  var (
      routingDecisions = promauto.NewCounterVec(
          prometheus.CounterOpts{
              Name: "policy_bot_routing_decisions_total",
              Help: "Total number of routing decisions made by middleware",
          },
          []string{"route", "detection_method", "handler_type"},
      )

      routingLatency = promauto.NewHistogramVec(
          prometheus.HistogramOpts{
              Name: "policy_bot_routing_latency_seconds",
              Help: "Latency of routing decisions in seconds",
              Buckets: prometheus.DefBuckets,
          },
          []string{"route", "handler_type"},
      )
  )
  ```
- [x] Update `SelectWebhookDispatcher` to include metrics:
  ```go
  // Add at the beginning of the handler function
  timer := prometheus.NewTimer(routingLatency.WithLabelValues("", "webhook"))
  defer timer.ObserveDuration()

  // Add after routing decision
  var detectionMethod string
  if enterpriseHost != "" {
      detectionMethod = "enterprise_header"
  } else if dcpHost != "" {
      detectionMethod = "dcp_header"
  } else {
      detectionMethod = "default"
  }
  routingDecisions.WithLabelValues(routeName, detectionMethod, "webhook").Inc()

  // Update timer labels
  routingLatency.WithLabelValues(routeName, "webhook")
  ```
- [x] Save the file

### Task 7: Add Middleware Chain Support
- [x] Create a function to chain multiple middleware:
  ```go
  // Chain combines multiple middleware into a single handler
  func Chain(handler http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
      for i := len(middleware) - 1; i >= 0; i-- {
          handler = middleware[i](handler)
      }
      return handler
  }

  // WithLogging adds request logging to a handler
  func WithLogging(next http.Handler) http.Handler {
      return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          logger := zerolog.Ctx(r.Context())
          logger.Debug().
              Str("method", r.Method).
              Str("path", r.URL.Path).
              Str("remote_addr", r.RemoteAddr).
              Msg("Handling request")
          next.ServeHTTP(w, r)
      })
  }
  ```
- [x] Save the file

### Task 8: Test Compilation and Fix Issues
- [x] Run `go build ./...` from project root
- [x] Fix any import issues:
  - [x] Ensure all types are properly imported
  - [x] Fix any circular dependencies
  - [x] Add missing package references
- [x] If `config.GithubAppConfig` is not accessible, adjust the function signatures
- [x] Run `go fmt ./server/middleware/...` to format code

### Task 9: Create Basic Unit Test File
- [x] Create `/Users/dannytrevino/development/policy-bot/server/middleware/header_check_test.go`:
  ```go
  package middleware

  import (
      "net/http"
      "net/http/httptest"
      "testing"
  )

  func TestDetectSource(t *testing.T) {
      tests := []struct {
          name     string
          headers  map[string]string
          query    string
          expected string
      }{
          {
              name:     "enterprise_header",
              headers:  map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
              expected: "enterprise",
          },
          {
              name:     "dcp_header",
              headers:  map[string]string{HeaderDCPDestinationHost: "github.com"},
              expected: "cloud",
          },
          {
              name:     "no_headers_default",
              headers:  map[string]string{},
              expected: "cloud",
          },
          {
              name:     "query_param_enterprise",
              query:    "?source=enterprise",
              expected: "enterprise",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              req := httptest.NewRequest("GET", "/test"+tt.query, nil)
              for k, v := range tt.headers {
                  req.Header.Set(k, v)
              }

              result := DetectSource(req)
              if result != tt.expected {
                  t.Errorf("DetectSource() = %v, want %v", result, tt.expected)
              }
          })
      }
  }
  ```
- [x] Run `go test ./server/middleware/...` to verify tests pass
- [x] Save the file

## Acceptance Criteria
- [x] `SelectWebhookDispatcher` correctly routes based on headers
- [x] `SelectIndexHandler` correctly routes index page requests
- [x] `SelectAPIHandler` correctly routes API requests
- [x] Helper functions (`DetectSource`, `IsEnterpriseRequest`) work correctly
- [x] Logging includes all relevant request details
- [x] Metrics are recorded for routing decisions
- [x] Default routing goes to cloud dispatcher when no headers present
- [x] Code compiles without errors
- [x] Basic unit tests pass
- [x] No circular dependencies introduced
- [x] Middleware follows Go best practices

## Testing Checklist
- [x] Test webhook routing with `X-GitHub-Enterprise-Host` header
- [x] Test webhook routing with `x-dcp-destination-host` header
- [x] Test webhook routing with no headers (should default to cloud)
- [x] Test API routing with query parameter `source=enterprise`
- [x] Test API routing with query parameter `source=cloud`
- [x] Verify logging output includes expected fields
- [x] Check metrics are incremented correctly

## Notes for Next Phase
- Phase 4 will integrate these middleware functions into the routing
- The stub references in server.go will be updated to use real middleware
- Additional testing will be added in Phase 6
- Performance optimization may be needed based on metrics