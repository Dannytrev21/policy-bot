# Phase 5: Enhanced Detection and Fallback Logic

## Phase Overview
**Priority**: MEDIUM - Improve reliability
**Estimated Time**: 2-3 hours
**Purpose**: Enhance header detection with fallback mechanisms, payload inspection, and configurable routing behavior

## Prerequisites
- [ ] Phases 1-4 completed successfully
- [ ] Basic middleware routing working
- [ ] Understanding of GitHub webhook payload structure
- [ ] Access to `/Users/dannytrevino/development/policy-bot/server/middleware/` directory
- [ ] Sample webhook payloads for testing

## Context
Current detection relies only on headers. We need to add:
1. Payload inspection for cases where headers are missing
2. Configurable default routing behavior
3. Request body preservation for downstream handlers
4. Better error handling and recovery
5. Cache for routing decisions to improve performance

## Tasks

### Task 1: Create Detection Module
- [ ] Create new file `/Users/dannytrevino/development/policy-bot/server/middleware/detection.go`
- [ ] Add package declaration and imports:
  ```go
  package middleware

  import (
      "bytes"
      "encoding/json"
      "io"
      "net/http"
      "net/url"
      "strings"
      "sync"
      "time"

      "github.com/rs/zerolog"
      "github.com/pkg/errors"
  )
  ```
- [ ] Save the file

### Task 2: Implement Request Body Reading with Preservation
- [ ] In `detection.go`, add body reading utility:
  ```go
  // readBodyWithPreservation reads the request body and restores it for downstream use
  func readBodyWithPreservation(r *http.Request) ([]byte, error) {
      if r.Body == nil {
          return nil, nil
      }

      // Read the body
      body, err := io.ReadAll(r.Body)
      if err != nil {
          return nil, errors.Wrap(err, "failed to read request body")
      }

      // Restore the body for downstream handlers
      r.Body = io.NopCloser(bytes.NewBuffer(body))

      return body, nil
  }

  // restoreBody creates a new body reader from bytes
  func restoreBody(r *http.Request, body []byte) {
      r.Body = io.NopCloser(bytes.NewBuffer(body))
  }
  ```
- [ ] Save the file

### Task 3: Implement Payload Inspection
- [ ] Add payload inspection functions:
  ```go
  // GitHubPayload represents common fields in GitHub webhook payloads
  type GitHubPayload struct {
      Repository struct {
          HTMLURL  string `json:"html_url"`
          CloneURL string `json:"clone_url"`
          GitURL   string `json:"git_url"`
          Owner    struct {
              Login string `json:"login"`
              Type  string `json:"type"`
          } `json:"owner"`
      } `json:"repository"`
      Installation struct {
          ID int64 `json:"id"`
      } `json:"installation"`
      Enterprise struct {
          Slug string `json:"slug"`
      } `json:"enterprise"`
  }

  // detectSourceFromPayload inspects the webhook payload to determine source
  func detectSourceFromPayload(body []byte) (string, string, error) {
      if len(body) == 0 {
          return "", "empty_payload", nil
      }

      var payload GitHubPayload
      if err := json.Unmarshal(body, &payload); err != nil {
          // Not a JSON payload or malformed
          return "", "invalid_json", nil
      }

      // Check for enterprise field (GHES includes this)
      if payload.Enterprise.Slug != "" {
          return "enterprise", "enterprise_field", nil
      }

      // Check repository URLs
      repoURL := payload.Repository.HTMLURL
      if repoURL != "" {
          parsedURL, err := url.Parse(repoURL)
          if err == nil {
              if strings.Contains(parsedURL.Host, "github.com") {
                  return "cloud", "github_com_url", nil
              }
              // Non-github.com host indicates enterprise
              return "enterprise", "custom_host_url", nil
          }
      }

      // Check clone URL as fallback
      cloneURL := payload.Repository.CloneURL
      if cloneURL != "" {
          if strings.Contains(cloneURL, "github.com") {
              return "cloud", "clone_url", nil
          }
          return "enterprise", "clone_url", nil
      }

      return "", "no_detection", nil
  }
  ```
- [ ] Save the file

### Task 4: Implement Routing Decision Cache
- [ ] Add caching mechanism for routing decisions:
  ```go
  // RoutingCache caches routing decisions for performance
  type RoutingCache struct {
      mu      sync.RWMutex
      entries map[string]*RoutingCacheEntry
      ttl     time.Duration
  }

  // RoutingCacheEntry represents a cached routing decision
  type RoutingCacheEntry struct {
      Source         string
      DetectionMethod string
      Timestamp      time.Time
  }

  // NewRoutingCache creates a new routing cache
  func NewRoutingCache(ttl time.Duration) *RoutingCache {
      cache := &RoutingCache{
          entries: make(map[string]*RoutingCacheEntry),
          ttl:     ttl,
      }

      // Start cleanup goroutine
      go cache.cleanup()

      return cache
  }

  // Get retrieves a routing decision from cache
  func (c *RoutingCache) Get(key string) (string, string, bool) {
      c.mu.RLock()
      defer c.mu.RUnlock()

      entry, exists := c.entries[key]
      if !exists {
          return "", "", false
      }

      // Check if entry is expired
      if time.Since(entry.Timestamp) > c.ttl {
          return "", "", false
      }

      return entry.Source, entry.DetectionMethod, true
  }

  // Set stores a routing decision in cache
  func (c *RoutingCache) Set(key, source, method string) {
      c.mu.Lock()
      defer c.mu.Unlock()

      c.entries[key] = &RoutingCacheEntry{
          Source:          source,
          DetectionMethod: method,
          Timestamp:       time.Now(),
      }
  }

  // cleanup removes expired entries periodically
  func (c *RoutingCache) cleanup() {
      ticker := time.NewTicker(c.ttl * 2)
      defer ticker.Stop()

      for range ticker.C {
          c.mu.Lock()
          now := time.Now()
          for key, entry := range c.entries {
              if now.Sub(entry.Timestamp) > c.ttl {
                  delete(c.entries, key)
              }
          }
          c.mu.Unlock()
      }
  }

  // Global routing cache instance
  var routingCache = NewRoutingCache(5 * time.Minute)
  ```
- [ ] Save the file

### Task 5: Implement Enhanced Detection Logic
- [ ] Add comprehensive detection function:
  ```go
  // DetectSourceEnhanced performs multi-level source detection with caching
  func DetectSourceEnhanced(r *http.Request, defaultSource string) (string, string, error) {
      logger := zerolog.Ctx(r.Context())

      // Generate cache key from request characteristics
      cacheKey := generateCacheKey(r)

      // Check cache first
      if source, method, found := routingCache.Get(cacheKey); found {
          logger.Debug().
              Str("source", source).
              Str("method", method).
              Msg("Using cached routing decision")
          return source, method, nil
      }

      // Priority 1: Explicit headers
      if enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost); enterpriseHost != "" {
          logger.Debug().
              Str("enterprise_host", enterpriseHost).
              Msg("Detected enterprise via header")
          routingCache.Set(cacheKey, "enterprise", "enterprise_header")
          return "enterprise", "enterprise_header", nil
      }

      if dcpHost := r.Header.Get(HeaderDCPDestinationHost); dcpHost != "" {
          logger.Debug().
              Str("dcp_host", dcpHost).
              Msg("Detected cloud via DCP header")
          routingCache.Set(cacheKey, "cloud", "dcp_header")
          return "cloud", "dcp_header", nil
      }

      // Priority 2: Query parameters
      if source := r.URL.Query().Get("source"); source != "" {
          normalized := normalizeSource(source)
          if normalized != "" {
              logger.Debug().
                  Str("source_param", source).
                  Str("normalized", normalized).
                  Msg("Detected source via query parameter")
              routingCache.Set(cacheKey, normalized, "query_param")
              return normalized, "query_param", nil
          }
      }

      // Priority 3: Host header analysis
      if host := r.Host; host != "" {
          if strings.Contains(host, "github.com") {
              logger.Debug().
                  Str("host", host).
                  Msg("Detected cloud via host header")
              routingCache.Set(cacheKey, "cloud", "host_header")
              return "cloud", "host_header", nil
          }
      }

      // Priority 4: Payload inspection (POST requests only)
      if r.Method == http.MethodPost {
          body, err := readBodyWithPreservation(r)
          if err != nil {
              logger.Warn().
                  Err(err).
                  Msg("Failed to read body for detection")
          } else if len(body) > 0 {
              source, method, err := detectSourceFromPayload(body)
              if err != nil {
                  logger.Warn().
                      Err(err).
                      Msg("Failed to parse payload for detection")
              } else if source != "" {
                  logger.Debug().
                      Str("source", source).
                      Str("method", method).
                      Msg("Detected source via payload inspection")
                  routingCache.Set(cacheKey, source, method)
                  return source, method, nil
              }
          }
      }

      // Priority 5: User-Agent analysis
      if ua := r.Header.Get("User-Agent"); ua != "" {
          if strings.Contains(strings.ToLower(ua), "github-hookshot") {
              // GitHub's webhook User-Agent
              if strings.Contains(ua, "ghes/") {
                  logger.Debug().
                      Str("user_agent", ua).
                      Msg("Detected enterprise via User-Agent")
                  routingCache.Set(cacheKey, "enterprise", "user_agent")
                  return "enterprise", "user_agent", nil
              }
          }
      }

      // Default fallback
      logger.Debug().
          Str("default", defaultSource).
          Msg("No detection criteria matched, using default")
      routingCache.Set(cacheKey, defaultSource, "default")
      return defaultSource, "default", nil
  }

  // normalizeSource converts various source inputs to standard values
  func normalizeSource(source string) string {
      lower := strings.ToLower(strings.TrimSpace(source))
      switch lower {
      case "enterprise", "ghes", "ghe":
          return "enterprise"
      case "cloud", "ghec", "github":
          return "cloud"
      default:
          return ""
      }
  }

  // generateCacheKey creates a cache key from request characteristics
  func generateCacheKey(r *http.Request) string {
      var parts []string

      // Include relevant headers
      parts = append(parts, r.Header.Get(HeaderGitHubEnterpriseHost))
      parts = append(parts, r.Header.Get(HeaderDCPDestinationHost))
      parts = append(parts, r.Header.Get(HeaderGitHubDelivery))
      parts = append(parts, r.Header.Get(HeaderGitHubEvent))

      // Include query params
      parts = append(parts, r.URL.Query().Get("source"))

      // Include host
      parts = append(parts, r.Host)

      return strings.Join(parts, "|")
  }
  ```
- [ ] Save the file

### Task 6: Update Middleware to Use Enhanced Detection
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/middleware/header_check.go`
- [ ] Update `SelectWebhookDispatcher` to use enhanced detection:
  ```go
  // Add configuration option for default source
  type MiddlewareConfig struct {
      DefaultSource string // "enterprise" or "cloud"
      EnableCaching bool
      CacheTTL      time.Duration
  }

  // SelectWebhookDispatcherWithConfig creates a webhook dispatcher with configuration
  func SelectWebhookDispatcherWithConfig(enterprise, cloud http.Handler, config MiddlewareConfig) http.Handler {
      if config.DefaultSource == "" {
          config.DefaultSource = "cloud"
      }

      return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          ctx := r.Context()
          logger := zerolog.Ctx(ctx)

          // Use enhanced detection
          source, method, err := DetectSourceEnhanced(r, config.DefaultSource)
          if err != nil {
              logger.Error().
                  Err(err).
                  Msg("Error during source detection, using default")
              source = config.DefaultSource
              method = "error_fallback"
          }

          // Extract GitHub headers for logging
          deliveryID := r.Header.Get(HeaderGitHubDelivery)
          eventType := r.Header.Get(HeaderGitHubEvent)

          // Route to appropriate dispatcher
          var selectedDispatcher http.Handler
          if source == "enterprise" {
              selectedDispatcher = enterprise
          } else {
              selectedDispatcher = cloud
          }

          // Log routing decision
          logger.Info().
              Str("delivery_id", deliveryID).
              Str("event_type", eventType).
              Str("route", source).
              Str("detection_method", method).
              Msg("Routing webhook via enhanced detection")

          // Update metrics
          routingDecisions.WithLabelValues(source, method, "webhook").Inc()

          selectedDispatcher.ServeHTTP(w, r)
      })
  }
  ```
- [ ] Keep the original `SelectWebhookDispatcher` for backward compatibility
- [ ] Save the file

### Task 7: Add Fallback Configuration
- [ ] Update `/Users/dannytrevino/development/policy-bot/server/config.go`
- [ ] Add middleware configuration:
  ```go
  // In the Config struct, add:
  Middleware MiddlewareConfig `yaml:"middleware" json:"middleware"`

  // Define the configuration structure
  type MiddlewareConfig struct {
      DefaultRoute    string        `yaml:"default_route" json:"default_route"`       // "enterprise" or "cloud"
      EnableCaching   bool          `yaml:"enable_caching" json:"enable_caching"`
      CacheTTL        time.Duration `yaml:"cache_ttl" json:"cache_ttl"`
      PayloadInspection bool        `yaml:"payload_inspection" json:"payload_inspection"`
  }
  ```
- [ ] Add defaults in `SetDefaults`:
  ```go
  // Middleware defaults
  if c.Middleware.DefaultRoute == "" {
      c.Middleware.DefaultRoute = "cloud"
  }
  if c.Middleware.CacheTTL == 0 {
      c.Middleware.CacheTTL = 5 * time.Minute
  }
  if !c.Middleware.EnableCaching {
      c.Middleware.EnableCaching = true // Default to enabled
  }
  ```
- [ ] Save the file

### Task 8: Create Detection Tests
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/detection_test.go`
- [ ] Add comprehensive tests:
  ```go
  package middleware

  import (
      "bytes"
      "net/http"
      "net/http/httptest"
      "testing"
  )

  func TestDetectSourceFromPayload(t *testing.T) {
      tests := []struct {
          name           string
          payload        string
          expectedSource string
          expectedMethod string
      }{
          {
              name: "github_com_url",
              payload: `{
                  "repository": {
                      "html_url": "https://github.com/owner/repo"
                  }
              }`,
              expectedSource: "cloud",
              expectedMethod: "github_com_url",
          },
          {
              name: "enterprise_url",
              payload: `{
                  "repository": {
                      "html_url": "https://github.enterprise.com/owner/repo"
                  }
              }`,
              expectedSource: "enterprise",
              expectedMethod: "custom_host_url",
          },
          {
              name: "enterprise_field",
              payload: `{
                  "enterprise": {
                      "slug": "my-enterprise"
                  },
                  "repository": {
                      "html_url": "https://ghes.example.com/owner/repo"
                  }
              }`,
              expectedSource: "enterprise",
              expectedMethod: "enterprise_field",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              source, method, err := detectSourceFromPayload([]byte(tt.payload))
              if err != nil {
                  t.Fatalf("unexpected error: %v", err)
              }
              if source != tt.expectedSource {
                  t.Errorf("source = %v, want %v", source, tt.expectedSource)
              }
              if method != tt.expectedMethod {
                  t.Errorf("method = %v, want %v", method, tt.expectedMethod)
              }
          })
      }
  }

  func TestDetectSourceEnhanced(t *testing.T) {
      tests := []struct {
          name           string
          headers        map[string]string
          payload        string
          query          string
          host           string
          expectedSource string
      }{
          {
              name:           "header_priority",
              headers:        map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
              payload:        `{"repository": {"html_url": "https://github.com/owner/repo"}}`,
              expectedSource: "enterprise",
          },
          {
              name:           "query_param_priority",
              query:          "?source=ghes",
              payload:        `{"repository": {"html_url": "https://github.com/owner/repo"}}`,
              expectedSource: "enterprise",
          },
          {
              name:           "payload_fallback",
              payload:        `{"enterprise": {"slug": "test"}}`,
              expectedSource: "enterprise",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              req := httptest.NewRequest("POST", "/webhook"+tt.query, bytes.NewBufferString(tt.payload))
              for k, v := range tt.headers {
                  req.Header.Set(k, v)
              }
              if tt.host != "" {
                  req.Host = tt.host
              }

              source, _, err := DetectSourceEnhanced(req, "cloud")
              if err != nil {
                  t.Fatalf("unexpected error: %v", err)
              }
              if source != tt.expectedSource {
                  t.Errorf("source = %v, want %v", source, tt.expectedSource)
              }
          })
      }
  }
  ```
- [ ] Run tests: `go test ./server/middleware/...`
- [ ] Save the file

### Task 9: Add Monitoring and Alerting
- [ ] Add detection failure metrics:
  ```go
  var (
      detectionFailures = promauto.NewCounterVec(
          prometheus.CounterOpts{
              Name: "policy_bot_detection_failures_total",
              Help: "Total number of source detection failures",
          },
          []string{"reason"},
      )

      detectionLatency = promauto.NewHistogramVec(
          prometheus.HistogramOpts{
              Name: "policy_bot_detection_latency_seconds",
              Help: "Latency of source detection",
              Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
          },
          []string{"method"},
      )

      cacheHitRate = promauto.NewCounterVec(
          prometheus.CounterOpts{
              Name: "policy_bot_routing_cache_hits_total",
              Help: "Total number of routing cache hits and misses",
          },
          []string{"result"}, // "hit" or "miss"
      )
  )
  ```
- [ ] Update detection functions to record metrics
- [ ] Save the file

## Acceptance Criteria
- [ ] Enhanced detection supports multiple fallback methods
- [ ] Payload inspection works without consuming request body
- [ ] Routing decisions are cached for performance
- [ ] Configuration allows customizing default route
- [ ] User-Agent detection identifies GHES webhooks
- [ ] All detection methods are logged appropriately
- [ ] Metrics track detection methods and failures
- [ ] Tests cover all detection scenarios
- [ ] No performance degradation with payload inspection
- [ ] Cache cleanup prevents memory leaks

## Testing Checklist
- [ ] Test header-based detection (highest priority)
- [ ] Test query parameter detection
- [ ] Test payload inspection with various formats
- [ ] Test User-Agent detection
- [ ] Test caching behavior
- [ ] Test with malformed payloads
- [ ] Test with missing bodies
- [ ] Test default fallback behavior
- [ ] Load test to verify performance

## Performance Considerations
- Cache routing decisions to avoid repeated detection
- Limit payload reading to reasonable size (e.g., 1MB)
- Use buffered reading for large payloads
- Consider async payload inspection for non-critical paths
- Monitor cache hit rates

## Notes for Next Phase
- Phase 6 will add comprehensive testing
- Monitor detection methods in production
- Collect data on which detection methods are most used
- Consider adding ML-based detection in future