# Phase 6: Testing Strategy

## Phase Overview
**Priority**: HIGH - Ensure reliability
**Estimated Time**: 4-5 hours
**Purpose**: Implement comprehensive testing for middleware routing, including unit tests, integration tests, and end-to-end webhook testing

## Prerequisites
- [ ] Phases 1-5 completed successfully
- [ ] Middleware functions implemented
- [ ] Routing integrated in server
- [ ] Understanding of Go testing patterns
- [ ] Access to test fixtures and sample payloads
- [ ] GitHub webhook samples available

## Context
We need comprehensive test coverage for:
1. Middleware routing logic
2. Header detection mechanisms
3. Payload inspection
4. Configuration handling
5. Route integration
6. End-to-end webhook processing
7. Error scenarios and edge cases

## Tasks

### Task 1: Set Up Test Infrastructure
- [ ] Create test utilities file `/Users/dannytrevino/development/policy-bot/server/middleware/test_utils.go`:
  ```go
  package middleware

  import (
      "bytes"
      "io"
      "net/http"
      "net/http/httptest"
      "testing"

      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
  )

  // TestDispatcher is a mock dispatcher for testing
  type TestDispatcher struct {
      Name       string
      CallCount  int
      LastRequest *http.Request
  }

  func (d *TestDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
      d.CallCount++
      d.LastRequest = r
      w.WriteHeader(http.StatusOK)
      w.Write([]byte(d.Name + " dispatcher called"))
  }

  // TestHandler is a mock handler for testing
  type TestHandler struct {
      Name      string
      CallCount int
  }

  func (h *TestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
      h.CallCount++
      w.WriteHeader(http.StatusOK)
      w.Write([]byte(h.Name + " handler called"))
  }

  // CreateTestRequest creates a test HTTP request with optional body
  func CreateTestRequest(method, path string, headers map[string]string, body string) *http.Request {
      var bodyReader io.Reader
      if body != "" {
          bodyReader = bytes.NewBufferString(body)
      }

      req := httptest.NewRequest(method, path, bodyReader)
      for key, value := range headers {
          req.Header.Set(key, value)
      }

      return req
  }

  // AssertDispatcherCalled verifies a dispatcher was called
  func AssertDispatcherCalled(t *testing.T, dispatcher *TestDispatcher, expectedCalls int) {
      assert.Equal(t, expectedCalls, dispatcher.CallCount,
          "%s dispatcher should be called %d times", dispatcher.Name, expectedCalls)
  }
  ```
- [ ] Save the file

### Task 2: Create Unit Tests for SelectWebhookDispatcher
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/webhook_dispatcher_test.go`:
  ```go
  package middleware

  import (
      "net/http/httptest"
      "testing"

      "github.com/stretchr/testify/assert"
  )

  func TestSelectWebhookDispatcher(t *testing.T) {
      tests := []struct {
          name               string
          headers           map[string]string
          expectedDispatcher string
      }{
          {
              name: "enterprise_header_routes_to_enterprise",
              headers: map[string]string{
                  HeaderGitHubEnterpriseHost: "ghes.example.com",
                  HeaderGitHubEvent:         "pull_request",
                  HeaderGitHubDelivery:      "12345",
              },
              expectedDispatcher: "enterprise",
          },
          {
              name: "dcp_header_routes_to_cloud",
              headers: map[string]string{
                  HeaderDCPDestinationHost: "github.com",
                  HeaderGitHubEvent:       "push",
                  HeaderGitHubDelivery:    "67890",
              },
              expectedDispatcher: "cloud",
          },
          {
              name: "no_headers_defaults_to_cloud",
              headers: map[string]string{
                  HeaderGitHubEvent:    "issue_comment",
                  HeaderGitHubDelivery: "11111",
              },
              expectedDispatcher: "cloud",
          },
          {
              name: "both_headers_enterprise_wins",
              headers: map[string]string{
                  HeaderGitHubEnterpriseHost: "ghes.example.com",
                  HeaderDCPDestinationHost:   "github.com",
              },
              expectedDispatcher: "enterprise",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              // Create test dispatchers
              enterpriseDispatcher := &TestDispatcher{Name: "enterprise"}
              cloudDispatcher := &TestDispatcher{Name: "cloud"}

              // Create middleware
              middleware := SelectWebhookDispatcher(
                  enterpriseDispatcher,
                  cloudDispatcher,
              )

              // Create test request
              req := CreateTestRequest("POST", "/api/github/hook", tt.headers, "")
              rec := httptest.NewRecorder()

              // Execute middleware
              middleware.ServeHTTP(rec, req)

              // Verify correct dispatcher was called
              if tt.expectedDispatcher == "enterprise" {
                  AssertDispatcherCalled(t, enterpriseDispatcher, 1)
                  AssertDispatcherCalled(t, cloudDispatcher, 0)
              } else {
                  AssertDispatcherCalled(t, enterpriseDispatcher, 0)
                  AssertDispatcherCalled(t, cloudDispatcher, 1)
              }

              assert.Equal(t, 200, rec.Code)
          })
      }
  }
  ```
- [ ] Run tests: `go test ./server/middleware/... -run TestSelectWebhookDispatcher`
- [ ] Save the file

### Task 3: Create Integration Tests for Enhanced Detection
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/enhanced_detection_test.go`:
  ```go
  package middleware

  import (
      "net/http/httptest"
      "testing"
      "time"

      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
  )

  func TestEnhancedDetectionWithPayload(t *testing.T) {
      tests := []struct {
          name               string
          headers           map[string]string
          payload           string
          query             string
          expectedSource    string
          expectedMethod    string
      }{
          {
              name: "payload_with_github_com_url",
              payload: `{
                  "repository": {
                      "html_url": "https://github.com/owner/repo",
                      "clone_url": "https://github.com/owner/repo.git"
                  },
                  "sender": {
                      "login": "user"
                  }
              }`,
              expectedSource: "cloud",
              expectedMethod: "github_com_url",
          },
          {
              name: "payload_with_enterprise_field",
              payload: `{
                  "enterprise": {
                      "slug": "my-company",
                      "id": 42
                  },
                  "repository": {
                      "html_url": "https://ghes.company.com/owner/repo"
                  }
              }`,
              expectedSource: "enterprise",
              expectedMethod: "enterprise_field",
          },
          {
              name: "header_overrides_payload",
              headers: map[string]string{
                  HeaderGitHubEnterpriseHost: "ghes.example.com",
              },
              payload: `{
                  "repository": {
                      "html_url": "https://github.com/owner/repo"
                  }
              }`,
              expectedSource: "enterprise",
              expectedMethod: "enterprise_header",
          },
          {
              name: "query_param_overrides_payload",
              query: "?source=enterprise",
              payload: `{
                  "repository": {
                      "html_url": "https://github.com/owner/repo"
                  }
              }`,
              expectedSource: "enterprise",
              expectedMethod: "query_param",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              req := CreateTestRequest("POST", "/webhook"+tt.query, tt.headers, tt.payload)

              source, method, err := DetectSourceEnhanced(req, "cloud")
              require.NoError(t, err)

              assert.Equal(t, tt.expectedSource, source)
              assert.Equal(t, tt.expectedMethod, method)

              // Verify body is still readable
              body, err := readBodyWithPreservation(req)
              require.NoError(t, err)
              assert.Equal(t, tt.payload, string(body))
          })
      }
  }

  func TestRoutingCache(t *testing.T) {
      cache := NewRoutingCache(100 * time.Millisecond)

      // Test set and get
      cache.Set("key1", "enterprise", "header")
      source, method, found := cache.Get("key1")
      assert.True(t, found)
      assert.Equal(t, "enterprise", source)
      assert.Equal(t, "header", method)

      // Test expiration
      time.Sleep(150 * time.Millisecond)
      _, _, found = cache.Get("key1")
      assert.False(t, found)

      // Test multiple entries
      cache.Set("key2", "cloud", "payload")
      cache.Set("key3", "enterprise", "query")

      source2, _, _ := cache.Get("key2")
      source3, _, _ := cache.Get("key3")
      assert.Equal(t, "cloud", source2)
      assert.Equal(t, "enterprise", source3)
  }
  ```
- [ ] Run tests: `go test ./server/middleware/... -run TestEnhancedDetection`
- [ ] Save the file

### Task 4: Create End-to-End Webhook Tests
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/e2e_test.go`:
  ```go
  package middleware_test

  import (
      "bytes"
      "crypto/hmac"
      "crypto/sha256"
      "encoding/hex"
      "encoding/json"
      "net/http"
      "net/http/httptest"
      "testing"

      "github.com/palantir/policy-bot/server/middleware"
      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
  )

  // generateWebhookSignature creates a GitHub webhook signature
  func generateWebhookSignature(secret string, body []byte) string {
      mac := hmac.New(sha256.New, []byte(secret))
      mac.Write(body)
      return "sha256=" + hex.EncodeToString(mac.Sum(nil))
  }

  func TestEndToEndWebhookRouting(t *testing.T) {
      // Sample webhook payloads
      enterprisePayload := map[string]interface{}{
          "action": "opened",
          "pull_request": map[string]interface{}{
              "number": 123,
              "title":  "Test PR",
          },
          "repository": map[string]interface{}{
              "name":     "test-repo",
              "html_url": "https://ghes.company.com/org/test-repo",
          },
          "enterprise": map[string]interface{}{
              "slug": "company",
          },
      }

      cloudPayload := map[string]interface{}{
          "action": "synchronize",
          "pull_request": map[string]interface{}{
              "number": 456,
              "title":  "Cloud PR",
          },
          "repository": map[string]interface{}{
              "name":     "cloud-repo",
              "html_url": "https://github.com/org/cloud-repo",
          },
      }

      tests := []struct {
          name               string
          payload           map[string]interface{}
          headers           map[string]string
          expectedRoute     string
      }{
          {
              name:    "enterprise_webhook_with_header",
              payload: enterprisePayload,
              headers: map[string]string{
                  "X-GitHub-Enterprise-Host": "ghes.company.com",
                  "X-GitHub-Event":          "pull_request",
                  "X-GitHub-Delivery":       "ent-123",
              },
              expectedRoute: "enterprise",
          },
          {
              name:    "cloud_webhook_with_dcp",
              payload: cloudPayload,
              headers: map[string]string{
                  "x-dcp-destination-host": "github.com",
                  "X-GitHub-Event":        "pull_request",
                  "X-GitHub-Delivery":     "cloud-456",
              },
              expectedRoute: "cloud",
          },
          {
              name:    "enterprise_detected_from_payload",
              payload: enterprisePayload,
              headers: map[string]string{
                  "X-GitHub-Event":    "pull_request",
                  "X-GitHub-Delivery": "auto-789",
              },
              expectedRoute: "enterprise",
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              // Marshal payload
              body, err := json.Marshal(tt.payload)
              require.NoError(t, err)

              // Create test server with middleware
              enterpriseHit := false
              cloudHit := false

              enterpriseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                  enterpriseHit = true
                  w.WriteHeader(http.StatusOK)
              })

              cloudHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                  cloudHit = true
                  w.WriteHeader(http.StatusOK)
              })

              // Create middleware with config
              config := middleware.MiddlewareConfig{
                  DefaultSource:     "cloud",
                  EnableCaching:     true,
                  CacheTTL:          time.Minute,
                  PayloadInspection: true,
              }

              handler := middleware.SelectWebhookDispatcherWithConfig(
                  enterpriseHandler,
                  cloudHandler,
                  config,
              )

              // Create test server
              server := httptest.NewServer(handler)
              defer server.Close()

              // Create request
              req, err := http.NewRequest("POST", server.URL, bytes.NewBuffer(body))
              require.NoError(t, err)

              // Add headers
              for key, value := range tt.headers {
                  req.Header.Set(key, value)
              }
              req.Header.Set("Content-Type", "application/json")

              // Send request
              client := &http.Client{}
              resp, err := client.Do(req)
              require.NoError(t, err)
              defer resp.Body.Close()

              // Verify routing
              if tt.expectedRoute == "enterprise" {
                  assert.True(t, enterpriseHit, "Enterprise handler should be called")
                  assert.False(t, cloudHit, "Cloud handler should not be called")
              } else {
                  assert.False(t, enterpriseHit, "Enterprise handler should not be called")
                  assert.True(t, cloudHit, "Cloud handler should be called")
              }

              assert.Equal(t, http.StatusOK, resp.StatusCode)
          })
      }
  }
  ```
- [ ] Run tests: `go test ./server/middleware/... -run TestEndToEnd`
- [ ] Save the file

### Task 5: Create Configuration Tests
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/config_test.go`:
  ```go
  package server

  import (
      "testing"
      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"
      "gopkg.in/yaml.v2"
  )

  func TestConfigMigration(t *testing.T) {
      tests := []struct {
          name           string
          configYAML     string
          expectEnterprise bool
          expectCloud     bool
          expectWarning   bool
      }{
          {
              name: "legacy_config_migrated",
              configYAML: `
  github:
    app:
      id: 12345
      webhook_secret: "legacy-secret"
  `,
              expectEnterprise: true,
              expectCloud:     true,
              expectWarning:   true,
          },
          {
              name: "new_config_separate",
              configYAML: `
  github_enterprise:
    app:
      id: 11111
      webhook_secret: "enterprise-secret"
  github_cloud:
    app:
      id: 22222
      webhook_secret: "cloud-secret"
  `,
              expectEnterprise: true,
              expectCloud:     true,
              expectWarning:   false,
          },
          {
              name: "enterprise_only",
              configYAML: `
  github_enterprise:
    app:
      id: 33333
      webhook_secret: "ent-only"
  `,
              expectEnterprise: true,
              expectCloud:     false,
              expectWarning:   false,
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              var config Config
              err := yaml.Unmarshal([]byte(tt.configYAML), &config)
              require.NoError(t, err)

              config.SetDefaults()

              if tt.expectEnterprise {
                  assert.NotZero(t, config.GithubEnterprise.App.ID)
              }
              if tt.expectCloud {
                  assert.NotZero(t, config.GithubCloud.App.ID)
              }

              // Validate config
              err = config.ValidateConfig()
              if tt.expectEnterprise || tt.expectCloud {
                  assert.NoError(t, err)
              }
          })
      }
  }

  func TestMiddlewareConfig(t *testing.T) {
      configYAML := `
  middleware:
    default_route: enterprise
    enable_caching: true
    cache_ttl: 10m
    payload_inspection: true
  `

      var config Config
      err := yaml.Unmarshal([]byte(configYAML), &config)
      require.NoError(t, err)

      assert.Equal(t, "enterprise", config.Middleware.DefaultRoute)
      assert.True(t, config.Middleware.EnableCaching)
      assert.Equal(t, 10*time.Minute, config.Middleware.CacheTTL)
      assert.True(t, config.Middleware.PayloadInspection)
  }
  ```
- [ ] Run tests: `go test ./server/... -run TestConfig`
- [ ] Save the file

### Task 6: Create Performance/Load Tests
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/performance_test.go`:
  ```go
  package middleware

  import (
      "fmt"
      "net/http"
      "net/http/httptest"
      "sync"
      "testing"
      "time"

      "github.com/stretchr/testify/assert"
  )

  func BenchmarkWebhookDispatcher(b *testing.B) {
      enterprise := &TestDispatcher{Name: "enterprise"}
      cloud := &TestDispatcher{Name: "cloud"}

      middleware := SelectWebhookDispatcher(enterprise, cloud)

      requests := []*http.Request{
          CreateTestRequest("POST", "/webhook", map[string]string{
              HeaderGitHubEnterpriseHost: "ghes.example.com",
          }, "{}"),
          CreateTestRequest("POST", "/webhook", map[string]string{
              HeaderDCPDestinationHost: "github.com",
          }, "{}"),
          CreateTestRequest("POST", "/webhook", nil, "{}"),
      }

      b.ResetTimer()
      for i := 0; i < b.N; i++ {
          req := requests[i%len(requests)]
          rec := httptest.NewRecorder()
          middleware.ServeHTTP(rec, req)
      }
  }

  func TestConcurrentWebhookRouting(t *testing.T) {
      enterprise := &TestDispatcher{Name: "enterprise"}
      cloud := &TestDispatcher{Name: "cloud"}

      middleware := SelectWebhookDispatcher(enterprise, cloud)
      server := httptest.NewServer(middleware)
      defer server.Close()

      numRequests := 100
      var wg sync.WaitGroup
      wg.Add(numRequests)

      errors := make(chan error, numRequests)
      start := time.Now()

      for i := 0; i < numRequests; i++ {
          go func(index int) {
              defer wg.Done()

              headers := make(map[string]string)
              if index%2 == 0 {
                  headers[HeaderGitHubEnterpriseHost] = "ghes.example.com"
              } else {
                  headers[HeaderDCPDestinationHost] = "github.com"
              }

              req := CreateTestRequest("POST", server.URL, headers, "{}")
              client := &http.Client{Timeout: 5 * time.Second}

              resp, err := client.Do(req)
              if err != nil {
                  errors <- err
                  return
              }
              defer resp.Body.Close()

              if resp.StatusCode != http.StatusOK {
                  errors <- fmt.Errorf("unexpected status: %d", resp.StatusCode)
              }
          }(i)
      }

      wg.Wait()
      close(errors)

      duration := time.Since(start)

      // Check for errors
      var errorCount int
      for err := range errors {
          t.Errorf("Request error: %v", err)
          errorCount++
      }

      assert.Zero(t, errorCount, "Should have no errors")
      assert.Less(t, duration.Seconds(), 5.0, "Should complete within 5 seconds")

      // Verify distribution
      enterpriseCount := enterprise.CallCount
      cloudCount := cloud.CallCount
      assert.Equal(t, numRequests, enterpriseCount+cloudCount)

      // Should be roughly 50/50 distribution
      assert.InDelta(t, numRequests/2, enterpriseCount, float64(numRequests)*0.1)
      assert.InDelta(t, numRequests/2, cloudCount, float64(numRequests)*0.1)
  }

  func TestCachePerformance(t *testing.T) {
      cache := NewRoutingCache(1 * time.Minute)

      // Populate cache
      for i := 0; i < 1000; i++ {
          key := fmt.Sprintf("key_%d", i)
          cache.Set(key, "cloud", "test")
      }

      // Measure read performance
      start := time.Now()
      for i := 0; i < 10000; i++ {
          key := fmt.Sprintf("key_%d", i%1000)
          cache.Get(key)
      }
      duration := time.Since(start)

      assert.Less(t, duration.Milliseconds(), int64(100),
          "10000 cache reads should complete within 100ms")
  }
  ```
- [ ] Run benchmarks: `go test -bench=. ./server/middleware/...`
- [ ] Save the file

### Task 7: Create Error Scenario Tests
- [ ] Create `/Users/dannytrevino/development/policy-bot/server/middleware/error_test.go`:
  ```go
  package middleware

  import (
      "errors"
      "net/http"
      "net/http/httptest"
      "testing"

      "github.com/stretchr/testify/assert"
  )

  // ErrorDispatcher simulates errors
  type ErrorDispatcher struct {
      ShouldError bool
      ErrorCode   int
  }

  func (d *ErrorDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
      if d.ShouldError {
          w.WriteHeader(d.ErrorCode)
          w.Write([]byte("Error occurred"))
      } else {
          w.WriteHeader(http.StatusOK)
      }
  }

  func TestErrorHandling(t *testing.T) {
      tests := []struct {
          name          string
          setupError    bool
          errorCode     int
          expectedCode  int
      }{
          {
              name:         "successful_routing",
              setupError:   false,
              expectedCode: http.StatusOK,
          },
          {
              name:         "dispatcher_returns_500",
              setupError:   true,
              errorCode:    http.StatusInternalServerError,
              expectedCode: http.StatusInternalServerError,
          },
          {
              name:         "dispatcher_returns_403",
              setupError:   true,
              errorCode:    http.StatusForbidden,
              expectedCode: http.StatusForbidden,
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              enterprise := &ErrorDispatcher{
                  ShouldError: tt.setupError,
                  ErrorCode:   tt.errorCode,
              }
              cloud := &ErrorDispatcher{
                  ShouldError: tt.setupError,
                  ErrorCode:   tt.errorCode,
              }

              middleware := SelectWebhookDispatcher(enterprise, cloud)

              req := CreateTestRequest("POST", "/webhook", nil, "{}")
              rec := httptest.NewRecorder()

              middleware.ServeHTTP(rec, req)

              assert.Equal(t, tt.expectedCode, rec.Code)
          })
      }
  }

  func TestMalformedPayload(t *testing.T) {
      tests := []struct {
          name    string
          payload string
      }{
          {
              name:    "invalid_json",
              payload: "{invalid json}",
          },
          {
              name:    "empty_payload",
              payload: "",
          },
          {
              name:    "null_payload",
              payload: "null",
          },
          {
              name:    "truncated_payload",
              payload: `{"repository": {"html_url": "https://github.co`,
          },
      }

      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              req := CreateTestRequest("POST", "/webhook", nil, tt.payload)

              // Should not panic, should fall back to default
              source, method, err := DetectSourceEnhanced(req, "cloud")

              assert.NoError(t, err, "Should handle malformed payload gracefully")
              assert.NotEmpty(t, source, "Should return a source")
              assert.NotEmpty(t, method, "Should return a method")
          })
      }
  }

  func TestNilHandlers(t *testing.T) {
      defer func() {
          if r := recover(); r == nil {
              t.Errorf("Expected panic with nil handler")
          }
      }()

      // This should panic
      _ = SelectWebhookDispatcher(nil, nil)
  }
  ```
- [ ] Run tests: `go test ./server/middleware/... -run TestError`
- [ ] Save the file

### Task 8: Create Test Coverage Report
- [ ] Run coverage analysis:
  ```bash
  go test -coverprofile=coverage.out ./server/middleware/...
  go tool cover -html=coverage.out -o coverage.html
  ```
- [ ] Review coverage report
- [ ] Identify uncovered code paths
- [ ] Add tests for uncovered scenarios:
  - [ ] Timeout scenarios
  - [ ] Large payload handling
  - [ ] Header injection attempts
  - [ ] Concurrent cache access

### Task 9: Create Integration Test Suite
- [ ] Create `/Users/dannytrevino/development/policy-bot/test/integration/middleware_test.go`:
  ```go
  // +build integration

  package integration

  import (
      "testing"
      "github.com/palantir/policy-bot/server"
      "github.com/stretchr/testify/suite"
  )

  type MiddlewareIntegrationSuite struct {
      suite.Suite
      server *server.Server
  }

  func (suite *MiddlewareIntegrationSuite) SetupSuite() {
      // Start test server with real configuration
      config := loadTestConfig()
      suite.server = server.New(config)
      go suite.server.Start()

      // Wait for server to be ready
      waitForServer(suite.server.URL)
  }

  func (suite *MiddlewareIntegrationSuite) TearDownSuite() {
      suite.server.Stop()
  }

  func (suite *MiddlewareIntegrationSuite) TestRealWebhookDelivery() {
      // Test with actual GitHub webhook payloads
      // Load from fixtures
  }

  func TestMiddlewareIntegration(t *testing.T) {
      suite.Run(t, new(MiddlewareIntegrationSuite))
  }
  ```
- [ ] Create test fixtures directory
- [ ] Add sample webhook payloads
- [ ] Run integration tests with build tag

## Acceptance Criteria
- [ ] Unit tests cover all middleware functions
- [ ] Integration tests verify routing behavior
- [ ] End-to-end tests confirm webhook processing
- [ ] Performance tests validate no degradation
- [ ] Error scenarios are properly handled
- [ ] Test coverage exceeds 80%
- [ ] All tests pass consistently
- [ ] Tests are properly documented
- [ ] Benchmarks establish performance baselines
- [ ] Concurrent access is tested

## Testing Checklist
- [ ] Unit tests for each middleware function
- [ ] Integration tests for routing logic
- [ ] End-to-end webhook delivery tests
- [ ] Configuration migration tests
- [ ] Performance/load tests
- [ ] Error handling tests
- [ ] Edge case tests (malformed data, nil values)
- [ ] Security tests (header injection, large payloads)
- [ ] Concurrent access tests
- [ ] Cache behavior tests

## CI/CD Integration
- [ ] Add test stage to CI pipeline
- [ ] Configure coverage reporting
- [ ] Set coverage threshold (80%)
- [ ] Add performance regression checks
- [ ] Configure parallel test execution
- [ ] Add integration test stage

## Notes for Next Phase
- Phase 7 will focus on migration and rollout
- Collect baseline metrics from tests
- Use test results to identify optimization opportunities
- Consider adding fuzz testing for payload detection