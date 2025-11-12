// Copyright 2025 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockEventHandler is a mock implementation of githubapp.EventHandler for testing
type MockEventHandler struct {
	handlesCalled int
	handlerFunc   func(ctx context.Context, eventType, deliveryID string, payload []byte) error
	eventTypes    []string
}

func (m *MockEventHandler) Handles() []string {
	m.handlesCalled++
	return m.eventTypes
}

func (m *MockEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	if m.handlerFunc != nil {
		return m.handlerFunc(ctx, eventType, deliveryID, payload)
	}
	return nil
}

func createTestPayload(installationID int64) []byte {
	payload := map[string]interface{}{
		"installation": map[string]interface{}{
			"id": installationID,
		},
		"action": "opened",
	}
	data, _ := json.Marshal(payload)
	return data
}

func createTestPayloadNoInstallation() []byte {
	payload := map[string]interface{}{
		"action": "opened",
	}
	data, _ := json.Marshal(payload)
	return data
}

func TestNewInstallationFilterHandler(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	mockHandler := &MockEventHandler{eventTypes: []string{"pull_request"}}
	metricsRegistry := gometrics.NewRegistry()

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	require.NotNil(t, filter)
	assert.Equal(t, mockHandler, filter.wrapped)
	assert.Equal(t, registry, filter.registry)
	assert.NotNil(t, filter.metrics)
	assert.NotNil(t, filter.metricsRegistry)

	// Verify metrics are registered
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyFilterEventsFiltered))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyFilterEventsPassed))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyFilterCacheHitsPositive))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyFilterCacheHitsNegative))
}

func TestInstallationFilterHandler_Handles(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	mockHandler := &MockEventHandler{eventTypes: []string{"pull_request", "issue_comment"}}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	handles := filter.Handles()

	assert.Equal(t, []string{"pull_request", "issue_comment"}, handles)
	assert.Equal(t, 1, mockHandler.handlesCalled)
}

func TestInstallationFilterHandler_PassThroughOnPositiveCacheHit(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as installed (positive cache)
	registry.MarkInstalled(installationID)

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-1", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called for positive cache hit")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_FilterOnNegativeCacheHit(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(99999)

	// Mark as NOT installed (negative cache)
	registry.MarkNotInstalled(installationID)

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-2", payload)

	// Should return nil (successfully filtered)
	assert.NoError(t, err)
	assert.False(t, handlerCalled, "Handler should NOT be called for negative cache hit")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(1), filtered)
	assert.Equal(t, int64(0), passed)
}

func TestInstallationFilterHandler_PassThroughOnCacheMiss(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(54321)

	// Don't add to cache - it's a cache miss

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-3", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called for cache miss (unknown status)")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_PassThroughWhenNoInstallationInPayload(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"ping"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayloadNoInstallation()
	err := filter.Handle(ctx, "ping", "test-delivery-4", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called when installation ID can't be extracted")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_PropagatesHandlerErrors(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	registry.MarkInstalled(installationID)

	expectedErr := errors.New("handler error")
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return expectedErr
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-5", payload)

	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_MultipleEvents(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Setup: ID 1 is installed, ID 2 is not installed, ID 3 is unknown
	registry.MarkInstalled(1)
	registry.MarkNotInstalled(2)
	// ID 3 is not in cache

	callLog := []int64{}
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			id, _ := extractInstallationID(payload)
			callLog = append(callLog, id)
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	// Process events
	filter.Handle(ctx, "pull_request", "d1", createTestPayload(1)) // Should pass (installed)
	filter.Handle(ctx, "pull_request", "d2", createTestPayload(2)) // Should filter (not installed)
	filter.Handle(ctx, "pull_request", "d3", createTestPayload(3)) // Should pass (unknown)
	filter.Handle(ctx, "pull_request", "d4", createTestPayload(1)) // Should pass (installed)
	filter.Handle(ctx, "pull_request", "d5", createTestPayload(2)) // Should filter (not installed)

	// Verify only IDs 1 and 3 reached the handler
	assert.Equal(t, []int64{1, 3, 1}, callLog)

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(2), filtered, "Should filter 2 events (ID 2)")
	assert.Equal(t, int64(3), passed, "Should pass 3 events (ID 1 twice, ID 3 once)")
}

func TestExtractInstallationID_ValidPayload(t *testing.T) {
	payload := createTestPayload(12345)
	id, err := extractInstallationID(payload)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), id)
}

func TestExtractInstallationID_NoInstallation(t *testing.T) {
	payload := createTestPayloadNoInstallation()
	_, err := extractInstallationID(payload)

	assert.Error(t, err)
}

func TestExtractInstallationID_InvalidJSON(t *testing.T) {
	payload := []byte("{invalid json")
	_, err := extractInstallationID(payload)

	assert.Error(t, err)
}

func TestInstallationFilterHandler_ConcurrentAccess(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Mark some installations
	registry.MarkInstalled(1)
	registry.MarkNotInstalled(2)

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	// Process events concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int64) {
			for j := 0; j < 10; j++ {
				filter.Handle(ctx, "pull_request", "test", createTestPayload(id))
			}
			done <- true
		}(int64(i % 3))
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify metrics are consistent (should not crash or deadlock)
	filtered, passed := filter.GetMetrics()
	assert.True(t, filtered+passed == 100, "Total events should be 100")
}

// Test go-metrics integration
func TestInstallationFilterHandler_GoMetrics_FilteredEvents(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	installationID := int64(99999)

	// Mark as NOT installed (negative cache)
	registry.MarkNotInstalled(installationID)

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-1", payload)

	assert.NoError(t, err)

	// Verify go-metrics counters were incremented
	filteredCounter := metricsRegistry.Get(MetricsKeyFilterEventsFiltered).(gometrics.Counter)
	assert.Equal(t, int64(1), filteredCounter.Count(), "Should record 1 filtered event")

	passedCounter := metricsRegistry.Get(MetricsKeyFilterEventsPassed).(gometrics.Counter)
	assert.Equal(t, int64(0), passedCounter.Count(), "Should record 0 passed events")

	// Verify cache hit metric
	cacheHitCounter := metricsRegistry.Get(MetricsKeyFilterCacheHitsNegative).(gometrics.Counter)
	assert.Equal(t, int64(1), cacheHitCounter.Count(), "Should record 1 cache hit for not_installed")
}

func TestInstallationFilterHandler_GoMetrics_PassedEvents(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	installationID := int64(12345)

	// Mark as installed (positive cache)
	registry.MarkInstalled(installationID)

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery-2", payload)

	assert.NoError(t, err)

	// Verify go-metrics counters
	filteredCounter := metricsRegistry.Get(MetricsKeyFilterEventsFiltered).(gometrics.Counter)
	assert.Equal(t, int64(0), filteredCounter.Count(), "Should record 0 filtered events")

	passedCounter := metricsRegistry.Get(MetricsKeyFilterEventsPassed).(gometrics.Counter)
	assert.Equal(t, int64(1), passedCounter.Count(), "Should record 1 passed event")

	// Verify cache hit metric for positive status
	cacheHitCounter := metricsRegistry.Get(MetricsKeyFilterCacheHitsPositive).(gometrics.Counter)
	assert.Equal(t, int64(1), cacheHitCounter.Count(), "Should record 1 cache hit for installed")
}

func TestInstallationFilterHandler_GoMetrics_WithNilRegistry(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	registry.MarkInstalled(installationID)

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	// Test with nil metrics registry - should not crash
	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)

	assert.NoError(t, err)

	// Atomic metrics should still work
	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

// Test extractInstallationID with zero ID (Phase 1 enhancement)
func TestExtractInstallationID_ZeroID(t *testing.T) {
	// Create payload with installation ID = 0
	payload := createTestPayload(0)
	id, err := extractInstallationID(payload)

	assert.Error(t, err, "Should return error for installation ID = 0")
	assert.Equal(t, int64(0), id)
	assert.Contains(t, err.Error(), "no installation", "Error should indicate no installation")
	assert.Contains(t, err.Error(), "zero", "Error should mention zero ID")
}

// Test filter handler with installation ID = 0
func TestInstallationFilterHandler_ZeroInstallationID(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	// Create payload with installation ID = 0 (should be treated as no installation)
	payload := createTestPayload(0)
	err := filter.Handle(ctx, "pull_request", "test-delivery-zero", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called for zero ID (treated as no installation)")

	// Should be counted as passed (not filtered) since we pass through when we can't extract valid ID
	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

// ========== Phase 2 Tests: Repository-Based Fallback ==========

// MockInstallationsService is a mock implementation for testing repository-based lookup
type MockInstallationsService struct {
	installations  map[string]int64 // key is "owner/repo", value is installation ID
	getByRepoFunc  func(ctx context.Context, owner, repo string) (githubapp.Installation, error)
	getByOwnerFunc func(ctx context.Context, owner string) (githubapp.Installation, error)
}

func (m *MockInstallationsService) ListAll(ctx context.Context) ([]githubapp.Installation, error) {
	return nil, errors.New("not implemented in mock")
}

func (m *MockInstallationsService) GetByOwner(ctx context.Context, owner string) (githubapp.Installation, error) {
	if m.getByOwnerFunc != nil {
		return m.getByOwnerFunc(ctx, owner)
	}
	return githubapp.Installation{}, errors.New("not implemented in mock")
}

func (m *MockInstallationsService) GetByRepository(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
	if m.getByRepoFunc != nil {
		return m.getByRepoFunc(ctx, owner, repo)
	}

	key := owner + "/" + repo
	if installationID, exists := m.installations[key]; exists {
		return githubapp.Installation{ID: installationID}, nil
	}

	return githubapp.Installation{}, errors.New("404 Not Found: No installation found for repository")
}

// Helper to create payload with repository information
func createTestPayloadWithRepository(installationID int64, owner, repo string) []byte {
	payload := map[string]interface{}{
		"installation": map[string]interface{}{
			"id": installationID,
		},
		"repository": map[string]interface{}{
			"name": repo,
			"owner": map[string]interface{}{
				"login": owner,
			},
		},
		"action": "opened",
	}
	data, _ := json.Marshal(payload)
	return data
}

// Helper to create payload with only repository (no installation)
func createTestPayloadRepositoryOnly(owner, repo string) []byte {
	payload := map[string]interface{}{
		"repository": map[string]interface{}{
			"name": repo,
			"owner": map[string]interface{}{
				"login": owner,
			},
		},
		"action": "opened",
	}
	data, _ := json.Marshal(payload)
	return data
}

// Test extractRepository() function
func TestExtractRepository_ValidRepository(t *testing.T) {
	payload := createTestPayloadWithRepository(12345, "octocat", "Hello-World")
	owner, repo, err := extractRepository(payload)

	assert.NoError(t, err)
	assert.Equal(t, "octocat", owner)
	assert.Equal(t, "Hello-World", repo)
}

func TestExtractRepository_NoRepository(t *testing.T) {
	payload := createTestPayload(12345)
	owner, repo, err := extractRepository(payload)

	assert.Error(t, err)
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.Contains(t, err.Error(), "no repository")
}

func TestExtractRepository_NoOwner(t *testing.T) {
	payload := []byte(`{"repository": {"name": "test-repo"}, "action": "opened"}`)
	owner, repo, err := extractRepository(payload)

	assert.Error(t, err)
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.Contains(t, err.Error(), "owner")
}

func TestExtractRepository_NoName(t *testing.T) {
	payload := []byte(`{"repository": {"owner": {"login": "octocat"}}, "action": "opened"}`)
	owner, repo, err := extractRepository(payload)

	assert.Error(t, err)
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.Contains(t, err.Error(), "name")
}

func TestExtractRepository_InvalidJSON(t *testing.T) {
	payload := []byte(`{invalid json}`)
	owner, repo, err := extractRepository(payload)

	assert.Error(t, err)
	assert.Equal(t, "", owner)
	assert.Equal(t, "", repo)
	assert.Contains(t, err.Error(), "unmarshal")
}

// Test repository-based fallback integration
func TestInstallationFilterHandler_RepositoryFallback_Success(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Mock InstallationsService that returns installation for "octocat/Hello-World"
	mockService := &MockInstallationsService{
		installations: map[string]int64{
			"octocat/Hello-World": 99999,
		},
	}

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Create payload with NO installation ID, but with repository
	payload := createTestPayloadRepositoryOnly("octocat", "Hello-World")
	err := filter.Handle(ctx, "pull_request", "test-fallback-1", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called after fallback lookup succeeds")

	// Verify installation was cached
	status, cacheHit := registry.Check(99999)
	assert.True(t, cacheHit, "Installation should be cached after fallback lookup")
	assert.Equal(t, InstallationExists, status)

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_RepositoryFallback_NotFound(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Mock InstallationsService that returns 404 for unknown repos
	mockService := &MockInstallationsService{
		getByRepoFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			return githubapp.Installation{}, errors.New("404 Not Found: No installation found for repository")
		},
	}

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Create payload with NO installation ID, but with repository
	payload := createTestPayloadRepositoryOnly("unknown", "unknown-repo")
	err := filter.Handle(ctx, "pull_request", "test-fallback-2", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called when fallback lookup fails (pass through)")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_RepositoryFallback_NilService(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	// No InstallationsService provided
	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	// Create payload with NO installation ID, but with repository
	payload := createTestPayloadRepositoryOnly("octocat", "Hello-World")
	err := filter.Handle(ctx, "pull_request", "test-fallback-3", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called when service is nil (pass through)")

	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}

func TestInstallationFilterHandler_Layer1_ThenLayer2(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Mock InstallationsService
	mockService := &MockInstallationsService{
		installations: map[string]int64{
			"octocat/Hello-World": 88888,
		},
	}

	handlerCalled := 0
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled++
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Event 1: Has installation ID (Layer 1 - direct extraction)
	// The filter extracts the ID but doesn't cache it (handler is responsible for caching)
	payload1 := createTestPayloadWithRepository(77777, "octocat", "Hello-World")
	err := filter.Handle(ctx, "pull_request", "test-layer1", payload1)
	assert.NoError(t, err)

	// Event 2: No installation ID, use repository (Layer 2 - fallback)
	// This DOES cache the installation ID because repository lookup caches the result
	payload2 := createTestPayloadRepositoryOnly("octocat", "Hello-World")
	err = filter.Handle(ctx, "pull_request", "test-layer2", payload2)
	assert.NoError(t, err)

	assert.Equal(t, 2, handlerCalled, "Both events should reach handler")

	// Verify Layer 2 installation was cached
	status2, hit2 := registry.Check(88888)
	assert.True(t, hit2, "Layer 2 (repository lookup) should cache the installation")
	assert.Equal(t, InstallationExists, status2)

	// Layer 1 doesn't cache (handler is responsible for that), so check will be a cache miss
	_, hit1 := registry.Check(77777)
	assert.False(t, hit1, "Layer 1 (direct extraction) does not cache - handler is responsible")
}

// ============================================================================
// Tests for extractIdentifiers() - Phase 1 Enhanced Field Extraction
// ============================================================================

func TestExtractIdentifiers_AllFields(t *testing.T) {
	// Complete payload with all possible fields
	payload := []byte(`{
		"installation": {
			"id": 12345,
			"account": {
				"login": "octocat",
				"id": 1
			}
		},
		"repository": {
			"id": 54321,
			"name": "Hello-World",
			"owner": {
				"login": "octocat",
				"id": 1
			}
		},
		"organization": {
			"login": "github",
			"id": 2
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(12345), ids.InstallationID, "installation.id should be extracted")
	assert.Equal(t, "octocat", ids.AccountLogin, "installation.account.login should be extracted")
	assert.Equal(t, int64(1), ids.AccountID, "installation.account.id should be extracted")
	assert.Equal(t, int64(54321), ids.RepoID, "repository.id should be extracted")
	assert.Equal(t, "Hello-World", ids.RepoName, "repository.name should be extracted")
	assert.Equal(t, "octocat", ids.OwnerLogin, "repository.owner.login should be preferred over organization.login")
	assert.Equal(t, int64(1), ids.OwnerID, "repository.owner.id should be preferred over organization.id")
}

func TestExtractIdentifiers_RepositoryEvent(t *testing.T) {
	// Typical pull request or issue event with repository but no installation
	payload := []byte(`{
		"action": "opened",
		"repository": {
			"id": 98765,
			"name": "policy-test",
			"owner": {
				"login": "cof-sandbox",
				"id": 100
			}
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(0), ids.InstallationID, "installation.id should be zero (missing)")
	assert.Equal(t, "", ids.AccountLogin, "account.login should be empty (missing)")
	assert.Equal(t, int64(98765), ids.RepoID, "repository.id should be extracted")
	assert.Equal(t, "policy-test", ids.RepoName, "repository.name should be extracted")
	assert.Equal(t, "cof-sandbox", ids.OwnerLogin, "repository.owner.login should be extracted")
	assert.Equal(t, int64(100), ids.OwnerID, "repository.owner.id should be extracted")
}

func TestExtractIdentifiers_OrganizationEvent(t *testing.T) {
	// Org-level event without repository (e.g., organization webhook)
	payload := []byte(`{
		"action": "member_added",
		"organization": {
			"login": "cof-primary",
			"id": 200
		},
		"installation": {
			"id": 0
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(0), ids.InstallationID, "installation.id should be zero (invalid)")
	assert.Equal(t, "cof-primary", ids.OwnerLogin, "organization.login should be extracted when no repository")
	assert.Equal(t, int64(200), ids.OwnerID, "organization.id should be extracted when no repository")
	assert.Equal(t, "", ids.RepoName, "repository.name should be empty (missing)")
}

func TestExtractIdentifiers_InstallationEvent(t *testing.T) {
	// Installation created/deleted event
	payload := []byte(`{
		"action": "created",
		"installation": {
			"id": 99999,
			"account": {
				"login": "github-enterprise",
				"id": 300
			}
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(99999), ids.InstallationID, "installation.id should be extracted")
	assert.Equal(t, "github-enterprise", ids.AccountLogin, "installation.account.login should be extracted")
	assert.Equal(t, int64(300), ids.AccountID, "installation.account.id should be extracted")
}

func TestExtractIdentifiers_MinimalPayload(t *testing.T) {
	// Minimal event with only installation ID
	payload := []byte(`{
		"installation": {
			"id": 55555
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(55555), ids.InstallationID, "installation.id should be extracted")
	assert.Equal(t, "", ids.AccountLogin, "account.login should be empty")
	assert.Equal(t, int64(0), ids.AccountID, "account.id should be zero")
	assert.Equal(t, "", ids.OwnerLogin, "owner.login should be empty")
	assert.Equal(t, "", ids.RepoName, "repository.name should be empty")
}

func TestExtractIdentifiers_EmptyPayload(t *testing.T) {
	// Empty JSON object
	payload := []byte(`{}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(0), ids.InstallationID, "all fields should be zero/empty")
	assert.Equal(t, "", ids.OwnerLogin, "all fields should be zero/empty")
	assert.Equal(t, "", ids.RepoName, "all fields should be zero/empty")
}

func TestExtractIdentifiers_InvalidJSON(t *testing.T) {
	// Invalid JSON should not cause a panic, but return empty identifiers
	// The function is lenient and uses json.Unmarshal with error checking
	payload := []byte(`{"invalid": json}`)

	ids, err := extractIdentifiers(payload)

	// The function doesn't return error on invalid JSON, it just returns empty identifiers
	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(0), ids.InstallationID)
}

func TestExtractIdentifiers_RepositoryTakesPrecedenceOverOrganization(t *testing.T) {
	// Both repository.owner and organization present - repository should win
	payload := []byte(`{
		"repository": {
			"name": "test-repo",
			"owner": {
				"login": "repo-owner",
				"id": 111
			}
		},
		"organization": {
			"login": "org-owner",
			"id": 222
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, "repo-owner", ids.OwnerLogin, "repository.owner.login should take precedence")
	assert.Equal(t, int64(111), ids.OwnerID, "repository.owner.id should take precedence")
}

func TestExtractIdentifiers_MissingInstallationID(t *testing.T) {
	// Installation object present but ID is zero (invalid)
	payload := []byte(`{
		"installation": {
			"id": 0,
			"account": {
				"login": "test-account",
				"id": 777
			}
		},
		"repository": {
			"name": "test",
			"owner": {
				"login": "test-org",
				"id": 888
			}
		}
	}`)

	ids, err := extractIdentifiers(payload)

	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Equal(t, int64(0), ids.InstallationID, "installation.id should be zero (invalid)")
	assert.Equal(t, "test-account", ids.AccountLogin, "account.login should still be extracted")
	assert.Equal(t, "test-org", ids.OwnerLogin, "repository.owner.login should be extracted")
	assert.Equal(t, "test", ids.RepoName, "repository.name should be extracted")
}

// ============================================================================
// Tests for MappingCache - Phase 2 Enhanced Caching
// ============================================================================

func TestMappingCache_SetAndGet(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Set a positive entry
	cache.Set("octocat/Hello-World", 12345)

	// Get should return the value
	id, found := cache.Get("octocat/Hello-World")
	assert.True(t, found, "Should find cached entry")
	assert.Equal(t, int64(12345), id, "Should return correct installation ID")
}

func TestMappingCache_SetNotFound(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Set a negative entry
	cache.SetNotFound("octocat/NonExistent")

	// Get should return found=true but id=0 (negative cache)
	id, found := cache.Get("octocat/NonExistent")
	assert.True(t, found, "Should find negative cache entry")
	assert.Equal(t, int64(0), id, "Should return 0 for negative cache")
}

func TestMappingCache_Miss(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Get non-existent key
	id, found := cache.Get("nonexistent/key")
	assert.False(t, found, "Should not find non-existent key")
	assert.Equal(t, int64(0), id, "Should return 0 for cache miss")
}

func TestMappingCache_PositiveTTLExpiration(t *testing.T) {
	cache := NewMappingCache(100*time.Millisecond, 50*time.Millisecond)

	// Set a positive entry
	cache.Set("octocat/Hello-World", 12345)

	// Should be found immediately
	id, found := cache.Get("octocat/Hello-World")
	assert.True(t, found)
	assert.Equal(t, int64(12345), id)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not be found after expiration
	_, found = cache.Get("octocat/Hello-World")
	assert.False(t, found, "Should not find expired entry")
}

func TestMappingCache_NegativeTTLExpiration(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 100*time.Millisecond)

	// Set a negative entry
	cache.SetNotFound("octocat/NonExistent")

	// Should be found immediately
	id, found := cache.Get("octocat/NonExistent")
	assert.True(t, found)
	assert.Equal(t, int64(0), id)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should not be found after expiration
	_, found = cache.Get("octocat/NonExistent")
	assert.False(t, found, "Should not find expired negative entry")
}

func TestMappingCache_Remove(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	cache.Set("octocat/Hello-World", 12345)

	// Should be found
	_, found := cache.Get("octocat/Hello-World")
	assert.True(t, found)

	// Remove
	cache.Remove("octocat/Hello-World")

	// Should not be found after removal
	_, found = cache.Get("octocat/Hello-World")
	assert.False(t, found)
}

func TestMappingCache_Clear(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	cache.Set("key1", 111)
	cache.Set("key2", 222)
	cache.SetNotFound("key3")

	assert.Equal(t, 3, cache.GetSize())

	// Clear all
	cache.Clear()

	assert.Equal(t, 0, cache.GetSize())
	_, found := cache.Get("key1")
	assert.False(t, found)
}

func TestMappingCache_GetStats(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	cache.Set("positive1", 111)
	cache.Set("positive2", 222)
	cache.SetNotFound("negative1")
	cache.SetNotFound("negative2")
	cache.SetNotFound("negative3")

	positive, negative, total := cache.GetStats()
	assert.Equal(t, 2, positive, "Should have 2 positive entries")
	assert.Equal(t, 3, negative, "Should have 3 negative entries")
	assert.Equal(t, 5, total, "Should have 5 total entries")
}

func TestMappingCache_ThreadSafety(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Run concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func(idx int) {
			defer wg.Done()
			cache.Set(fmt.Sprintf("key-%d", idx), int64(idx))
		}(i)
		go func(idx int) {
			defer wg.Done()
			cache.Get(fmt.Sprintf("key-%d", idx))
		}(i)
		go func(idx int) {
			defer wg.Done()
			cache.SetNotFound(fmt.Sprintf("negative-%d", idx))
		}(i)
	}

	wg.Wait()

	// Should have entries without panicking
	assert.True(t, cache.GetSize() > 0)
}

// ============================================================================
// Tests for Organization Lookup - Phase 2
// ============================================================================

func TestInstallationFilterHandler_OrganizationLookup_Success(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockService := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			if owner == "cof-primary" {
				return githubapp.Installation{ID: 99999}, nil
			}
			return githubapp.Installation{}, errors.New("404 Not Found: No installation found")
		},
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"organization"},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Test organization lookup
	installationID, err := filter.lookupInstallationByOrganization(ctx, "cof-primary")

	assert.NoError(t, err)
	assert.Equal(t, int64(99999), installationID)

	// Verify it was cached in registry
	status, hit := registry.Check(99999)
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)
}

func TestInstallationFilterHandler_OrganizationLookup_NotFound(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockService := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			return githubapp.Installation{}, errors.New("404 Not Found: No installation found")
		},
	}

	mockHandler := &MockEventHandler{eventTypes: []string{"organization"}}
	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	installationID, err := filter.lookupInstallationByOrganization(ctx, "nonexistent-org")

	assert.Error(t, err)
	assert.Equal(t, int64(0), installationID)
	// Phase 4: Now returns original 404 error instead of ErrNoInstallation for smart lookup compatibility
	assert.True(t, IsInstallationNotFoundError(err), "Error should be a not-found error")
}

// ============================================================================
// Tests for SQS Event Detection and Smart Lookup - Phase 2
// ============================================================================

func TestInstallationFilterHandler_SQSEvent_SmartLookup_RepositoryCache(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	// Add SQS event source to context
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockService := &MockInstallationsService{
		installations: map[string]int64{
			"cof-sandbox/my-repo": 88888,
		},
	}

	handlerCalled := false
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			handlerCalled = true
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Event with missing installation ID but has repository
	payload := []byte(`{
		"installation": {"id": 0},
		"repository": {
			"owner": {"login": "cof-sandbox"},
			"name": "my-repo"
		}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-sqs-1", payload)

	assert.NoError(t, err)
	assert.True(t, handlerCalled, "Handler should be called after successful lookup")

	// Verify installation was cached
	status, hit := registry.Check(88888)
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)

	// Verify repository mapping was cached
	cachedID, found := filter.repoCache.Get("cof-sandbox/my-repo")
	assert.True(t, found, "Repository mapping should be cached")
	assert.Equal(t, int64(88888), cachedID)

	// Second event should use cached mapping (no API call)
	handlerCalled = false
	err = filter.Handle(ctx, "pull_request", "test-sqs-2", payload)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)
}

func TestInstallationFilterHandler_SQSEvent_SmartLookup_OrganizationCache(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	// Add SQS event source to context
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockService := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			if owner == "cof-primary" {
				return githubapp.Installation{ID: 77777}, nil
			}
			return githubapp.Installation{}, errors.New("404 Not Found")
		},
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"organization"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Org-level event without repository
	payload := []byte(`{
		"action": "member_added",
		"organization": {"login": "cof-primary"}
	}`)

	err := filter.Handle(ctx, "organization", "test-org-1", payload)
	assert.NoError(t, err)

	// Verify organization mapping was cached
	cachedID, found := filter.orgCache.Get("org:cof-primary")
	assert.True(t, found, "Organization mapping should be cached")
	assert.Equal(t, int64(77777), cachedID)
}

func TestInstallationFilterHandler_WebhookEvent_NoSmartLookup(t *testing.T) {
	// Regular context without SQS marker
	ctx := zerolog.New(nil).WithContext(context.Background())

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockService := &MockInstallationsService{
		installations: map[string]int64{
			"octocat/Hello-World": 55555,
		},
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Webhook event (no SQS context) with missing installation ID
	payload := []byte(`{
		"installation": {"id": 0},
		"repository": {
			"owner": {"login": "octocat"},
			"name": "Hello-World"
		}
	}`)

	err := filter.Handle(ctx, "pull_request", "webhook-1", payload)
	assert.NoError(t, err)

	// For webhook, only basic repository lookup should be used
	// It should NOT cache in repoCache or orgCache (those are SQS-only)
	_, found := filter.repoCache.Get("octocat/Hello-World")
	assert.False(t, found, "Webhook events should not use mapping cache")
}

func TestInstallationFilterHandler_SQSEvent_MultiMethodLookup(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Track which API methods are called for SQS events
	repoLookupCalled := false
	orgLookupCalled := false

	mockService := &MockInstallationsService{
		getByRepoFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			repoLookupCalled = true
			// First method fails, should try organization lookup
			return githubapp.Installation{}, errors.New("404 Not Found: No installation found")
		},
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			orgLookupCalled = true
			// Second method also fails
			return githubapp.Installation{}, errors.New("404 Not Found: No installation found")
		},
	}

	mockHandler := &MockEventHandler{eventTypes: []string{"pull_request"}}
	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, nil, nil, nil, nil)

	// Event from non-installed org
	payload := []byte(`{
		"repository": {
			"owner": {"login": "non-installed-org"},
			"name": "test-repo"
		}
	}`)

	// SQS event should try multiple lookup methods
	err := filter.Handle(ctx, "pull_request", "test-sqs-multi", payload)
	assert.NoError(t, err)

	// Verify that SQS events try both repository and organization lookups
	assert.True(t, repoLookupCalled, "SQS events should try repository lookup")
	assert.True(t, orgLookupCalled, "SQS events should try organization lookup as fallback")

	// Verify the mapping caches were created (Phase 2 functionality)
	assert.NotNil(t, filter.repoCache, "Repository mapping cache should be initialized")
	assert.NotNil(t, filter.orgCache, "Organization mapping cache should be initialized")
}

// ============================================================================
// Metrics and Observability Tests (Phase 4)
// ============================================================================

// TestLookupMethodMetrics_DirectID tests that direct ID lookup is recorded
func TestLookupMethodMetrics_DirectID(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	// Mark installation as existing
	registry.MarkInstalled(12345)

	// Payload with direct installation ID
	payload := []byte(`{
		"installation": {"id": 12345},
		"repository": {"owner": {"login": "test-org"}, "name": "test-repo"}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupMethodDirect).(gometrics.Counter)
	assert.Equal(t, int64(1), counter.Count(), "Direct lookup metric should be recorded")
}

// TestLookupMethodMetrics_RepoCacheHit tests that repo cache hits are recorded
func TestLookupMethodMetrics_RepoCacheHit(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	repoCache := NewMappingCache(1*time.Hour, 5*time.Minute)
	orgCache := NewMappingCache(1*time.Hour, 5*time.Minute)

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, repoCache, orgCache, nil)

	// Pre-populate repo cache
	repoCache.Set("test-org/test-repo", 67890)

	// Payload with NO installation ID (will use cache)
	payload := []byte(`{
		"installation": {"id": 0},
		"repository": {"owner": {"login": "test-org"}, "name": "test-repo"}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupMethodRepoCache).(gometrics.Counter)
	assert.Equal(t, int64(1), counter.Count(), "Repo cache hit metric should be recorded")
}

// TestLookupMethodMetrics_OrgCacheHit tests that org cache hits are recorded
func TestLookupMethodMetrics_OrgCacheHit(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"organization"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	repoCache := NewMappingCache(1*time.Hour, 5*time.Minute)
	orgCache := NewMappingCache(1*time.Hour, 5*time.Minute)

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, repoCache, orgCache, nil)

	// Pre-populate org cache
	orgCache.Set("org:test-org", 99999)

	// Payload with organization but no repo (org-level event)
	payload := []byte(`{
		"action": "member_added",
		"organization": {"login": "test-org"}
	}`)

	err := filter.Handle(ctx, "organization", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupMethodOrgCache).(gometrics.Counter)
	assert.Equal(t, int64(1), counter.Count(), "Org cache hit metric should be recorded")
}

// TestLookupMethodMetrics_RepoAPILookup tests that repo API lookups are recorded
func TestLookupMethodMetrics_RepoAPILookup(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	mockService := &MockInstallationsService{
		getByRepoFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			return githubapp.Installation{ID: 55555}, nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, metricsRegistry, nil, nil, nil)

	// Payload with NO installation ID (will trigger API lookup)
	payload := []byte(`{
		"installation": {"id": 0},
		"repository": {"owner": {"login": "api-org"}, "name": "api-repo"}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupMethodRepoAPI).(gometrics.Counter)
	assert.Equal(t, int64(1), counter.Count(), "Repo API lookup metric should be recorded")
}

// TestLookupMethodMetrics_OrgAPILookup tests that org API lookups are recorded
func TestLookupMethodMetrics_OrgAPILookup(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"organization"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	mockService := &MockInstallationsService{
		getByRepoFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			// Repo lookup fails
			return githubapp.Installation{}, errors.New("404 Not Found")
		},
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			// Org lookup succeeds
			return githubapp.Installation{ID: 77777}, nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, metricsRegistry, nil, nil, nil)

	// Org-level event (no repo)
	payload := []byte(`{
		"action": "member_added",
		"organization": {"login": "api-org"}
	}`)

	err := filter.Handle(ctx, "organization", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupMethodOrgAPI).(gometrics.Counter)
	assert.Equal(t, int64(1), counter.Count(), "Org API lookup metric should be recorded")
}

// TestLookupMethodMetrics_AllFailed tests that complete failures are recorded
func TestLookupMethodMetrics_AllFailed(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	apiCallCount := 0
	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			t.Log("MockHandler called - event was passed through, not filtered")
			return nil
		},
	}

	mockService := &MockInstallationsService{
		getByRepoFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			apiCallCount++
			t.Logf("getByRepoFunc called: owner=%s, repo=%s", owner, repo)
			return githubapp.Installation{}, errors.New("404 Not Found")
		},
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			apiCallCount++
			t.Logf("getByOwnerFunc called: owner=%s", owner)
			return githubapp.Installation{}, errors.New("404 Not Found")
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, mockService, metricsRegistry, nil, nil, nil)

	// Payload from non-installed org
	payload := []byte(`{
		"installation": {"id": 0},
		"repository": {"owner": {"login": "non-installed"}, "name": "test-repo"}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err) // Should not error, just filter

	t.Logf("API call count: %d", apiCallCount)

	// Verify metric was recorded
	counter := metricsRegistry.Get(MetricsKeyLookupAllFailed)
	if counter == nil {
		t.Fatal("MetricsKeyLookupAllFailed counter not found in registry")
	}
	assert.Equal(t, int64(1), counter.(gometrics.Counter).Count(), "All failed metric should be recorded")

	// Verify event was filtered
	filtered, passed := filter.GetMetrics()
	t.Logf("Filtered: %d, Passed: %d", filtered, passed)
	assert.Equal(t, int64(1), filtered, "Event should be filtered")
}

// TestLookupMethodMetrics_WebhookNotRecorded tests that webhook events don't record metrics
func TestLookupMethodMetrics_WebhookNotRecorded(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	// NO SQS event source key - this is a webhook

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	// Mark installation as existing
	registry.MarkInstalled(12345)

	// Payload with direct installation ID
	payload := []byte(`{
		"installation": {"id": 12345},
		"repository": {"owner": {"login": "test-org"}, "name": "test-repo"}
	}`)

	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err)

	// Verify metrics were NOT recorded for webhook events
	counter := metricsRegistry.Get(MetricsKeyLookupMethodDirect)
	if counter != nil {
		assert.Equal(t, int64(0), counter.(gometrics.Counter).Count(), "Webhook events should NOT record lookup metrics")
	}
}

// TestLookupMethodMetrics_MultipleEvents tests metric accumulation
func TestLookupMethodMetrics_MultipleEvents(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	repoCache := NewMappingCache(1*time.Hour, 5*time.Minute)
	orgCache := NewMappingCache(1*time.Hour, 5*time.Minute)

	filter := NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, repoCache, orgCache, nil)

	// Event 1: Direct ID
	registry.MarkInstalled(111)
	payload1 := []byte(`{"installation": {"id": 111}, "repository": {"owner": {"login": "org1"}, "name": "repo1"}}`)
	filter.Handle(ctx, "pull_request", "d1", payload1)

	// Event 2: Repo cache hit
	repoCache.Set("org2/repo2", 222)
	payload2 := []byte(`{"installation": {"id": 0}, "repository": {"owner": {"login": "org2"}, "name": "repo2"}}`)
	filter.Handle(ctx, "pull_request", "d2", payload2)

	// Event 3: Direct ID again
	registry.MarkInstalled(333)
	payload3 := []byte(`{"installation": {"id": 333}, "repository": {"owner": {"login": "org3"}, "name": "repo3"}}`)
	filter.Handle(ctx, "pull_request", "d3", payload3)

	// Verify metrics accumulated correctly
	directCounter := metricsRegistry.Get(MetricsKeyLookupMethodDirect).(gometrics.Counter)
	assert.Equal(t, int64(2), directCounter.Count(), "Should have 2 direct lookups")

	repoCacheCounter := metricsRegistry.Get(MetricsKeyLookupMethodRepoCache).(gometrics.Counter)
	assert.Equal(t, int64(1), repoCacheCounter.Count(), "Should have 1 repo cache hit")
}

// TestMetricsRegistration tests that all lookup method metrics are registered
func TestMetricsRegistration(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	mockHandler := &MockEventHandler{eventTypes: []string{"pull_request"}}

	_ = NewInstallationFilterHandler(mockHandler, registry, nil, metricsRegistry, nil, nil, nil)

	// Verify all lookup method metrics are registered
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupMethodDirect), "Direct lookup metric should be registered")
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupMethodRepoCache), "Repo cache metric should be registered")
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupMethodOrgCache), "Org cache metric should be registered")
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupMethodRepoAPI), "Repo API metric should be registered")
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupMethodOrgAPI), "Org API metric should be registered")
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyLookupAllFailed), "All failed metric should be registered")
}

// TestNilMetricsRegistry tests that nil registry doesn't crash
func TestNilMetricsRegistry(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs") // SQS event

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	// Nil metrics registry - should not crash
	filter := NewInstallationFilterHandler(mockHandler, registry, nil, nil, nil, nil, nil)

	registry.MarkInstalled(12345)
	payload := []byte(`{"installation": {"id": 12345}, "repository": {"owner": {"login": "org"}, "name": "repo"}}`)

	// Should not panic
	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)
	require.NoError(t, err)
}
