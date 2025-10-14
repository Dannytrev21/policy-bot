# Phase 1: Foundation & Shared Router (Sprint 1)

**Test ID Prefix**: SR (Shared Router), AUTH (Authentication), PROC (Processor), SCHED (Scheduler), INDEP (Independence)

## Overview
**Duration**: Week 1 (5 days)
**Objective**: Establish solid unit test foundation with shared utilities
**Success Criteria**: 
- 35+ unit tests passing with >85% coverage
- Shared router implemented and tested
- All mocks and test helpers created
- Ready for integration testing

---

## Prerequisites Checklist

### Day 0: Environment Setup
- [ ] Go 1.21+ installed and configured
- [ ] Install testify: `go get github.com/stretchr/testify`
- [ ] Install mock: `go get github.com/stretchr/testify/mock`
- [ ] Install goleak: `go get go.uber.org/goleak`
- [ ] Verify existing tests pass: `go test ./...`
- [ ] Create test directories:
  ```bash
  mkdir -p server/internal/sourcerouter
  mkdir -p server/handler/testdata
  mkdir -p server/sqsconsumer/testdata
  ```

---

## Day 1: Shared Source Router Implementation

### CRITICAL: This is an architectural improvement from Codex plan
The shared router eliminates duplication between HTTP middleware and SQS processor, ensuring consistent routing logic.

### Task SR-01: Create Source Router Package
**Priority**: CRITICAL - Foundation for all other tests
**Time Estimate**: 2 hours

#### Implementation Steps

1. **Create router file**: `server/internal/sourcerouter/router.go`

```go
// Copyright 2018 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License")

package sourcerouter

import (
	"strings"
)

// DetectionMethod describes how the source was detected
type DetectionMethod string

const (
	DetectionEnterpriseHeader DetectionMethod = "enterprise_header"
	DetectionGHECHost         DetectionMethod = "ghec_host"
	DetectionHostHeader       DetectionMethod = "host_header"
	DetectionQueryParam       DetectionMethod = "query_param"
	DetectionLegacySource     DetectionMethod = "legacy_source"
	DetectionDefault          DetectionMethod = "default"
)

// Router provides unified source detection for both HTTP and SQS paths
type Router struct{}

// NewRouter creates a new source router instance
func NewRouter() *Router {
	return &Router{}
}

// DetectSource determines whether an event originated from cloud or enterprise GitHub
// Priority order:
// 1. X-GitHub-Enterprise-Host header (enterprise)
// 2. Host header containing "ghec" (cloud)  
// 3. Host header without "ghec" (enterprise)
// 4. Query parameter "source=enterprise" (enterprise)
// 5. Legacy source field (backward compatibility)
// 6. Default to cloud
func (r *Router) DetectSource(headers map[string]interface{}, queryParams map[string]string, legacySource string) (source string, method DetectionMethod) {
	// Priority 1: X-GitHub-Enterprise-Host header
	if enterpriseHost, ok := headers["X-GitHub-Enterprise-Host"].(string); ok && enterpriseHost != "" {
		return "enterprise", DetectionEnterpriseHeader
	}

	// Priority 2 & 3: Host header
	if host, ok := headers["Host"].(string); ok {
		// Check for GHEC marker (case-insensitive)
		if strings.Contains(strings.ToLower(host), "ghec") {
			return "cloud", DetectionGHECHost
		}
		// Any other host header indicates enterprise
		return "enterprise", DetectionHostHeader
	}

	// Priority 4: Query parameter (HTTP webhook fallback)
	if source, ok := queryParams["source"]; ok {
		if source == "enterprise" {
			return "enterprise", DetectionQueryParam
		}
	}

	// Priority 5: Legacy source field (SQS backward compatibility)
	if legacySource == "enterprise" {
		return "enterprise", DetectionLegacySource
	}

	// Priority 6: Default to cloud (consistent with HTTP routing)
	return "cloud", DetectionDefault
}
```

**Acceptance Criteria**:
- [ ] File compiles without errors
- [ ] All priority levels implemented correctly
- [ ] Case-insensitive "ghec" detection
- [ ] Returns both source and detection method
- [ ] No external dependencies (only stdlib)

---

### Task SR-02: Create Router Unit Tests
**Priority**: CRITICAL
**Time Estimate**: 3 hours

#### Create test file: `server/internal/sourcerouter/router_test.go`

```go
package sourcerouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRouter_DetectSource_AllScenarios(t *testing.T) {
	router := NewRouter()

	tests := []struct {
		testID         string
		name           string
		headers        map[string]interface{}
		queryParams    map[string]string
		legacySource   string
		expectedSource string
		expectedMethod DetectionMethod
	}{
		// Test SR-T01: Enterprise header detection (highest priority)
		{
			testID: "SR-T01",
			name:   "enterprise_header_present",
			headers: map[string]interface{}{
				"X-GitHub-Enterprise-Host": "github.company.com",
				"Host":                     "ghec.github.com", // Should be ignored
			},
			expectedSource: "enterprise",
			expectedMethod: DetectionEnterpriseHeader,
		},

		// Test SR-T02: GHEC host detection
		{
			testID: "SR-T02",
			name:   "ghec_in_host_header",
			headers: map[string]interface{}{
				"Host": "api.ghec.github.com",
			},
			expectedSource: "cloud",
			expectedMethod: DetectionGHECHost,
		},

		// Test SR-T03: GHEC detection case-insensitive
		{
			testID: "SR-T03",
			name:   "ghec_uppercase_in_host",
			headers: map[string]interface{}{
				"Host": "GITHUB.GHEC.COMPANY.COM",
			},
			expectedSource: "cloud",
			expectedMethod: DetectionGHECHost,
		},

		// Test SR-T04: Enterprise host detection (no ghec)
		{
			testID: "SR-T04",
			name:   "enterprise_host_no_ghec",
			headers: map[string]interface{}{
				"Host": "github.enterprise.internal",
			},
			expectedSource: "enterprise",
			expectedMethod: DetectionHostHeader,
		},

		// Test SR-T05: Query parameter fallback
		{
			testID: "SR-T05",
			name:   "query_param_enterprise",
			queryParams: map[string]string{
				"source": "enterprise",
			},
			expectedSource: "enterprise",
			expectedMethod: DetectionQueryParam,
		},

		// Test SR-T06: Legacy source field
		{
			testID:         "SR-T06",
			name:           "legacy_source_field",
			legacySource:   "enterprise",
			expectedSource: "enterprise",
			expectedMethod: DetectionLegacySource,
		},

		// Test SR-T07: Default to cloud (no headers)
		{
			testID:         "SR-T07",
			name:           "default_to_cloud_no_headers",
			expectedSource: "cloud",
			expectedMethod: DetectionDefault,
		},

		// Test SR-T08: Empty headers map
		{
			testID:         "SR-T08",
			name:           "empty_headers_map",
			headers:        map[string]interface{}{},
			expectedSource: "cloud",
			expectedMethod: DetectionDefault,
		},

		// Test SR-T09: Priority order - enterprise header beats host
		{
			testID: "SR-T09",
			name:   "enterprise_header_overrides_ghec_host",
			headers: map[string]interface{}{
				"X-GitHub-Enterprise-Host": "ghes.company.com",
				"Host":                     "api.ghec.github.com",
			},
			queryParams: map[string]string{
				"source": "cloud",
			},
			legacySource:   "cloud",
			expectedSource: "enterprise",
			expectedMethod: DetectionEnterpriseHeader,
		},

		// Test SR-T10: Priority order - host beats query param
		{
			testID: "SR-T10",
			name:   "host_overrides_query_param",
			headers: map[string]interface{}{
				"Host": "api.ghec.github.com",
			},
			queryParams: map[string]string{
				"source": "enterprise",
			},
			expectedSource: "cloud",
			expectedMethod: DetectionGHECHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, method := router.DetectSource(tt.headers, tt.queryParams, tt.legacySource)

			assert.Equal(t, tt.expectedSource, source,
				"Test %s: Expected source %s, got %s", tt.testID, tt.expectedSource, source)
			assert.Equal(t, tt.expectedMethod, method,
				"Test %s: Expected method %s, got %s", tt.testID, tt.expectedMethod, method)
		})
	}
}

func TestRouter_DetectSource_EdgeCases(t *testing.T) {
	router := NewRouter()

	tests := []struct {
		testID         string
		name           string
		headers        map[string]interface{}
		expectedSource string
	}{
		{
			testID: "SR-T11",
			name:   "nil_headers_map",
			headers: nil,
			expectedSource: "cloud",
		},
		{
			testID: "SR-T12",
			name:   "host_header_wrong_type",
			headers: map[string]interface{}{
				"Host": 12345, // Not a string
			},
			expectedSource: "cloud",
		},
		{
			testID: "SR-T13",
			name:   "empty_host_string",
			headers: map[string]interface{}{
				"Host": "",
			},
			expectedSource: "cloud",
		},
		{
			testID: "SR-T14",
			name:   "ghec_partial_match",
			headers: map[string]interface{}{
				"Host": "myghec-like.com", // Contains "ghec" substring
			},
			expectedSource: "cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, _ := router.DetectSource(tt.headers, nil, "")
			assert.Equal(t, tt.expectedSource, source,
				"Test %s: Unexpected source", tt.testID)
		})
	}
}

// Benchmark router performance
func BenchmarkRouter_DetectSource(b *testing.B) {
	router := NewRouter()
	headers := map[string]interface{}{
		"Host": "api.ghec.github.com",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router.DetectSource(headers, nil, "")
	}
}
```

**Acceptance Criteria**:
- [ ] 14+ test cases covering all scenarios
- [ ] 100% code coverage on router.go
- [ ] All priority levels tested
- [ ] Edge cases handled
- [ ] Benchmark included
- [ ] Tests run in <1 second

**Run Tests**:
```bash
go test -v ./server/internal/sourcerouter/...
go test -cover ./server/internal/sourcerouter/...
go test -bench=. ./server/internal/sourcerouter/...
```

---

## Day 2: Authentication & Handler Tests

### Task AUTH-01: Create Authentication Test Mocks
**Priority**: HIGH
**Time Estimate**: 2 hours

#### Create mock file: `server/handler/mocks_test.go`

```go
package handler

import (
	"context"

	"github.com/google/go-github/v74/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/stretchr/testify/mock"
)

// MockClientCreator mocks the githubapp.ClientCreator interface
type MockClientCreator struct {
	mock.Mock
}

func (m *MockClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	args := m.Called(installationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*github.Client), args.Error(1)
}

func (m *MockClientCreator) NewAppClient() (*github.Client, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*github.Client), args.Error(1)
}

func (m *MockClientCreator) NewInstallationV4Client(installationID int64) (*github.Client, error) {
	args := m.Called(installationID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*github.Client), args.Error(1)
}

// MockInstallationsService mocks the installations service
type MockInstallationsService struct {
	mock.Mock
}

func (m *MockInstallationsService) Get(ctx context.Context, installationID int64) (githubapp.Installation, error) {
	args := m.Called(ctx, installationID)
	return args.Get(0).(githubapp.Installation), args.Error(1)
}
```

---

### Task AUTH-02: Test Base Authentication Flow
**Priority**: HIGH - Answers Q1 (Authentication)
**Time Estimate**: 3 hours

#### Create test file: `server/handler/base_auth_test.go`

```go
package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-github/v74/github"
	"github.com/palantir/policy-bot/pull"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Test AUTH-T01: Base can create installation clients
func TestBase_NewInstallationClient_Success(t *testing.T) {
	tests := []struct {
		testID         string
		name           string
		installationID int64
		isCloud        bool
		expectError    bool
	}{
		{
			testID:         "AUTH-T01a",
			name:           "cloud_valid_installation_id",
			installationID: 12345,
			isCloud:        true,
			expectError:    false,
		},
		{
			testID:         "AUTH-T01b",
			name:           "enterprise_valid_installation_id",
			installationID: 67890,
			isCloud:        false,
			expectError:    false,
		},
		{
			testID:         "AUTH-T01c",
			name:           "zero_installation_id_fails",
			installationID: 0,
			isCloud:        true,
			expectError:    true,
		},
		{
			testID:         "AUTH-T01d",
			name:           "negative_installation_id_fails",
			installationID: -1,
			isCloud:        true,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCreator := new(MockClientCreator)
			
			base := &Base{
				ClientCreator: mockCreator,
				GithubCloud:   tt.isCloud,
			}

			if !tt.expectError {
				// Mock successful client creation
				mockClient := new(github.Client)
				mockCreator.On("NewInstallationClient", tt.installationID).
					Return(mockClient, nil)
			} else {
				// Mock error on invalid ID
				mockCreator.On("NewInstallationClient", tt.installationID).
					Return((*github.Client)(nil), errors.New("invalid installation ID"))
			}

			client, err := base.NewInstallationClient(tt.installationID)

			if tt.expectError {
				assert.Error(t, err, "Test %s: Expected error for invalid installation ID", tt.testID)
				assert.Nil(t, client, "Test %s: Client should be nil on error", tt.testID)
			} else {
				assert.NoError(t, err, "Test %s: Should not error for valid installation ID", tt.testID)
				assert.NotNil(t, client, "Test %s: Client should not be nil on success", tt.testID)
			}

			mockCreator.AssertExpectations(t)
		})
	}
}

// Test AUTH-T02: Base creates both REST and GraphQL clients
func TestBase_NewEvalContext_CreatesBothClients(t *testing.T) {
	testID := "AUTH-T02"
	
	mockCreator := new(MockClientCreator)
	mockInstallations := new(MockInstallationsService)
	
	base := &Base{
		ClientCreator: mockCreator,
		Installations: mockInstallations,
		GlobalCache:   &mockGlobalCache{},
	}

	// Mock REST client creation
	mockRESTClient := new(github.Client)
	mockCreator.On("NewInstallationClient", int64(12345)).
		Return(mockRESTClient, nil)

	// Mock GraphQL client creation
	mockV4Client := new(github.Client)
	mockCreator.On("NewInstallationV4Client", int64(12345)).
		Return(mockV4Client, nil)

	locator := pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	}

	evalCtx, err := base.NewEvalContext(context.Background(), 12345, locator)

	require.NoError(t, err, "Test %s: Should create eval context successfully", testID)
	assert.NotNil(t, evalCtx.Client, "Test %s: REST client should be initialized", testID)
	assert.NotNil(t, evalCtx.V4Client, "Test %s: GraphQL client should be initialized", testID)
	assert.NotNil(t, evalCtx.PullContext, "Test %s: PullContext should be initialized", testID)

	mockCreator.AssertExpectations(t)
}

// Test AUTH-T03: Cross-environment credential isolation
func TestBase_CredentialIsolation(t *testing.T) {
	cloudCreator := new(MockClientCreator)
	enterpriseCreator := new(MockClientCreator)

	cloudBase := &Base{
		ClientCreator: cloudCreator,
		GithubCloud:   true,
	}

	enterpriseBase := &Base{
		ClientCreator: enterpriseCreator,
		GithubCloud:   false,
	}

	installationID := int64(12345)

	// Mock cloud client
	mockCloudClient := new(github.Client)
	cloudCreator.On("NewInstallationClient", installationID).
		Return(mockCloudClient, nil)

	// Mock enterprise client
	mockEnterpriseClient := new(github.Client)
	enterpriseCreator.On("NewInstallationClient", installationID).
		Return(mockEnterpriseClient, nil)

	// Create clients
	cloudClient, err := cloudBase.NewInstallationClient(installationID)
	require.NoError(t, err)

	enterpriseClient, err := enterpriseBase.NewInstallationClient(installationID)
	require.NoError(t, err)

	// Verify different creators were used
	assert.NotSame(t, cloudClient, enterpriseClient,
		"Test AUTH-T03: Cloud and enterprise should use different client creators")

	cloudCreator.AssertExpectations(t)
	enterpriseCreator.AssertExpectations(t)
}

// Mock global cache for testing
type mockGlobalCache struct{}

func (m *mockGlobalCache) GetPushedAt(ctx context.Context, owner, repo, ref string) (int64, bool) {
	return 0, false
}

func (m *mockGlobalCache) SetPushedAt(ctx context.Context, owner, repo, ref string, pushedAt int64) {
}
```

**Acceptance Criteria**:
- [ ] Tests validate authentication flow
- [ ] Both cloud and enterprise paths tested
- [ ] Error cases handled
- [ ] Mocks properly configured
- [ ] >90% coverage of Base authentication methods

---

## Day 3: Processor Routing Tests

### Task PROC-01: Create Processor Test File
**Priority**: HIGH
**Time Estimate**: 4 hours

#### Create test file: `server/sqsconsumer/processor_routing_test.go`

```go
package sqsconsumer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Test PROC-T01: Detect source from headers (cloud)
func TestProcessor_DetectSource_Cloud(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	tests := []struct {
		testID         string
		name           string
		sqsMsg         SQSMessage
		expectedSource string
	}{
		{
			testID: "PROC-T01a",
			name:   "ghec_in_host_lowercase",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "api.ghec.github.com",
				},
			},
			expectedSource: "cloud",
		},
		{
			testID: "PROC-T01b",
			name:   "ghec_in_host_uppercase",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "API.GHEC.GITHUB.COM",
				},
			},
			expectedSource: "cloud",
		},
		{
			testID: "PROC-T01c",
			name:   "ghec_in_middle_of_hostname",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "github-ghec-prod.company.com",
				},
			},
			expectedSource: "cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := processor.detectSourceFromHeaders(tt.sqsMsg)
			assert.Equal(t, tt.expectedSource, source,
				"Test %s: Expected %s, got %s", tt.testID, tt.expectedSource, source)
		})
	}
}

// Test PROC-T02: Detect source from headers (enterprise)
func TestProcessor_DetectSource_Enterprise(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	tests := []struct {
		testID         string
		name           string
		sqsMsg         SQSMessage
		expectedSource string
	}{
		{
			testID: "PROC-T02a",
			name:   "enterprise_hostname",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "github.enterprise.company.com",
				},
			},
			expectedSource: "enterprise",
		},
		{
			testID: "PROC-T02b",
			name:   "ghes_hostname",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "ghes.internal.local",
				},
			},
			expectedSource: "enterprise",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := processor.detectSourceFromHeaders(tt.sqsMsg)
			assert.Equal(t, tt.expectedSource, source,
				"Test %s: Expected %s, got %s", tt.testID, tt.expectedSource, source)
		})
	}
}

// Test PROC-T03: Fallback and edge cases
func TestProcessor_DetectSource_Fallbacks(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	tests := []struct {
		testID         string
		name           string
		sqsMsg         SQSMessage
		expectedSource string
	}{
		{
			testID: "PROC-T03a",
			name:   "no_headers_default_cloud",
			sqsMsg: SQSMessage{},
			expectedSource: "cloud",
		},
		{
			testID: "PROC-T03b",
			name:   "empty_headers_default_cloud",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{},
			},
			expectedSource: "cloud",
		},
		{
			testID: "PROC-T03c",
			name:   "legacy_source_field_enterprise",
			sqsMsg: SQSMessage{
				Source: "enterprise",
			},
			expectedSource: "enterprise",
		},
		{
			testID: "PROC-T03d",
			name:   "headers_override_legacy_source",
			sqsMsg: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "api.ghec.github.com",
				},
				Source: "enterprise", // Should be overridden
			},
			expectedSource: "cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := processor.detectSourceFromHeaders(tt.sqsMsg)
			assert.Equal(t, tt.expectedSource, source,
				"Test %s: Expected %s, got %s", tt.testID, tt.expectedSource, source)
		})
	}
}

// Test PROC-T04: selectHandler chooses correct handler and scheduler
func TestProcessor_SelectHandler_CorrectRouting(t *testing.T) {
	// Create mock handlers
	cloudHandler := new(MockEventHandler)
	cloudHandler.On("Handles").Return([]string{"pull_request", "status"})

	enterpriseHandler := new(MockEventHandler)
	enterpriseHandler.On("Handles").Return([]string{"pull_request", "status"})

	// Create mock schedulers
	cloudScheduler := new(MockScheduler)
	enterpriseScheduler := new(MockScheduler)

	processor := NewProcessor(
		&ProcessorConfig{},
		nil,
		[]githubapp.EventHandler{enterpriseHandler},
		[]githubapp.EventHandler{cloudHandler},
		enterpriseScheduler,
		cloudScheduler,
		cloudScheduler,
		zerolog.New(nil),
		nil,
	)

	tests := []struct {
		testID            string
		name              string
		sqsMsg            SQSMessage
		expectHandler     bool
		expectScheduler   *MockScheduler
	}{
		{
			testID: "PROC-T04a",
			name:   "cloud_message_uses_cloud_handler_and_scheduler",
			sqsMsg: SQSMessage{
				EventType: "pull_request",
				Headers: map[string]interface{}{
					"Host": "api.ghec.github.com",
				},
			},
			expectHandler:   true,
			expectScheduler: cloudScheduler,
		},
		{
			testID: "PROC-T04b",
			name:   "enterprise_message_uses_enterprise_handler_and_scheduler",
			sqsMsg: SQSMessage{
				EventType: "pull_request",
				Headers: map[string]interface{}{
					"Host": "github.enterprise.com",
				},
			},
			expectHandler:   true,
			expectScheduler: enterpriseScheduler,
		},
		{
			testID: "PROC-T04c",
			name:   "unknown_event_type_returns_nil",
			sqsMsg: SQSMessage{
				EventType: "unknown_event",
				Headers: map[string]interface{}{
					"Host": "api.ghec.github.com",
				},
			},
			expectHandler:   false,
			expectScheduler: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, scheduler := processor.selectHandler(tt.sqsMsg)

			if tt.expectHandler {
				assert.NotNil(t, handler, "Test %s: Handler should not be nil", tt.testID)
				assert.NotNil(t, scheduler, "Test %s: Scheduler should not be nil", tt.testID)
				assert.Same(t, tt.expectScheduler, scheduler,
					"Test %s: Wrong scheduler selected", tt.testID)
			} else {
				assert.Nil(t, handler, "Test %s: Handler should be nil for unknown event", tt.testID)
				assert.Nil(t, scheduler, "Test %s: Scheduler should be nil for unknown event", tt.testID)
			}
		})
	}
}

// Mock implementations
type MockEventHandler struct {
	mock.Mock
}

func (m *MockEventHandler) Handles() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

func (m *MockEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	args := m.Called(ctx, eventType, deliveryID, payload)
	return args.Error(0)
}

type MockScheduler struct {
	mock.Mock
}

func (m *MockScheduler) Schedule(ctx context.Context, dispatch githubapp.Dispatch) error {
	args := m.Called(ctx, dispatch)
	return args.Error(0)
}
```

**Acceptance Criteria**:
- [ ] Source detection tested for all scenarios
- [ ] Handler selection validated
- [ ] Scheduler pairing confirmed
- [ ] Edge cases covered
- [ ] >95% coverage on processor routing functions

---

## Day 4: Consumer Thread Safety Tests

[Due to length, continuing in next file...]

**Run All Day 1-3 Tests**:
```bash
# Run all phase 1 tests so far
go test -v ./server/internal/sourcerouter/...
go test -v ./server/handler/ -run "TestBase_"
go test -v ./server/sqsconsumer/ -run "TestProcessor_"

# Check coverage
go test -cover ./server/internal/sourcerouter/...
go test -cover ./server/handler/ -run "TestBase_"
go test -cover ./server/sqsconsumer/ -run "TestProcessor_"

# Run with race detector
go test -race ./server/sqsconsumer/...
```

---

## Acceptance Criteria for Days 1-3

- [ ] Shared router implemented and tested (100% coverage)
- [ ] Authentication flow validated (>90% coverage)
- [ ] Processor routing tested (>95% coverage)
- [ ] 25+ unit tests passing
- [ ] No race conditions detected
- [ ] All mocks properly configured
- [ ] Test execution time <5 seconds

**Next**: Days 4-5 will complete consumer concurrency, scheduler, and independence tests.


