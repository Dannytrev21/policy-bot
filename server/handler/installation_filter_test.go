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
	"testing"
	"time"

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

	filter := NewInstallationFilterHandler(mockHandler, registry, metricsRegistry)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, metricsRegistry)

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

	filter := NewInstallationFilterHandler(mockHandler, registry, metricsRegistry)

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
	filter := NewInstallationFilterHandler(mockHandler, registry, nil)

	payload := createTestPayload(installationID)
	err := filter.Handle(ctx, "pull_request", "test-delivery", payload)

	assert.NoError(t, err)

	// Atomic metrics should still work
	filtered, passed := filter.GetMetrics()
	assert.Equal(t, int64(0), filtered)
	assert.Equal(t, int64(1), passed)
}
