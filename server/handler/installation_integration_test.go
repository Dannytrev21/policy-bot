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
	"sync"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestPhase8Step6_EndToEndIntegration validates the complete installation system flow
// This test covers the integration of all components working together
func TestPhase8Step6_EndToEndIntegration(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := metrics.NewRegistry()

	// Set up complete stack
	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(
		registry,
		zerolog.New(nil),
		nil, // no API client needed for this test
		circuitBreaker,
	)

	repoCache := NewMappingCache(1*time.Hour, 5*time.Minute)
	orgCache := NewMappingCache(1*time.Hour, 5*time.Minute)

	filterConfig := &FilterConfig{
		WebhookFilteringEnabled: true,
		SQSFilteringEnabled:     true,
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filter := NewInstallationFilterHandler(
		mockHandler,
		registry,
		nil, // no installation service
		metricsRegistry,
		repoCache,
		orgCache,
		locator,
		filterConfig,
	)

	t.Run("webhook event with known installation", func(t *testing.T) {
		// Mark installation as existing
		installationID := int64(12345)
		registry.MarkInstalled(installationID)

		// Create event payload
		payload := createTestPayload(installationID)

		// Process event
		err := filter.Handle(ctx, "pull_request", "webhook-delivery-1", payload)

		assert.NoError(t, err)

		// Verify metrics
		filtered, passed := filter.GetMetrics()
		assert.Equal(t, int64(0), filtered, "Event should not be filtered")
		assert.Equal(t, int64(1), passed, "Event should be passed")
	})

	t.Run("webhook event with unknown installation", func(t *testing.T) {
		// Installation not in cache
		unknownID := int64(99999)

		payload := createTestPayload(unknownID)

		// Process event - should pass through (webhook filtering enabled but unknown passes)
		err := filter.Handle(ctx, "pull_request", "webhook-delivery-2", payload)

		assert.NoError(t, err)
	})

	t.Run("SQS event with compound key lookup", func(t *testing.T) {
		// Set up SQS context
		sqsCtx := context.WithValue(ctx, SQSEventSourceKey, "sqs")

		// Pre-populate repo cache
		installationID := int64(54321)
		repoCache.Set("test-owner/test-repo", installationID)
		registry.MarkInstalled(installationID)

		// Create event with repository info
		payload := createTestPayloadWithRepository(installationID, "test-owner", "test-repo")

		// Process event
		err := filter.Handle(sqsCtx, "pull_request", "sqs-delivery-1", payload)

		assert.NoError(t, err)
	})

	t.Run("concurrent event processing", func(t *testing.T) {
		// Test concurrent access to shared components
		installationID := int64(11111)
		registry.MarkInstalled(installationID)

		payload := createTestPayload(installationID)

		var wg sync.WaitGroup
		errors := make(chan error, 10)

		// Process 10 concurrent events
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				err := filter.Handle(ctx, "pull_request", "concurrent-delivery", payload)
				if err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			t.Errorf("Concurrent processing error: %v", err)
		}
	})
}

// TestPhase8Step6_ComponentInteraction validates interaction between components
func TestPhase8Step6_ComponentInteraction(t *testing.T) {
	t.Run("registry and filter coordination", func(t *testing.T) {
		registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
		metricsRegistry := metrics.NewRegistry()

		mockHandler := &MockEventHandler{eventTypes: []string{"pull_request"}}
		filterConfig := &FilterConfig{
			WebhookFilteringEnabled: true,
			SQSFilteringEnabled:     true,
		}

		filter := NewInstallationFilterHandler(
			mockHandler,
			registry,
			nil,
			metricsRegistry,
			nil,
			nil,
			nil,
			filterConfig,
		)

		// Test 1: Unknown installation gets passed
		id1 := int64(100)
		payload1 := createTestPayload(id1)
		ctx := context.Background()

		err := filter.Handle(ctx, "pull_request", "test-1", payload1)
		assert.NoError(t, err)

		// Test 2: Mark as not installed and verify filtering
		registry.MarkNotInstalled(id1)

		err = filter.Handle(ctx, "pull_request", "test-2", payload1)
		assert.NoError(t, err)

		// Verify it was filtered
		filtered, _ := filter.GetMetrics()
		assert.Equal(t, int64(1), filtered, "Event should be filtered after marking not installed")

		// Test 3: Mark as installed and verify pass-through
		registry.MarkInstalled(id1)

		err = filter.Handle(ctx, "pull_request", "test-3", payload1)
		assert.NoError(t, err)
	})

	t.Run("circuit breaker integration", func(t *testing.T) {
		circuitBreaker := NewCircuitBreaker()

		// Initially closed
		assert.Equal(t, CircuitBreakerClosed, circuitBreaker.GetState())
		assert.True(t, circuitBreaker.Allow())

		// Record successes
		for i := 0; i < 3; i++ {
			circuitBreaker.RecordSuccess()
		}
		assert.Equal(t, CircuitBreakerClosed, circuitBreaker.GetState())

		// Record enough failures to trip
		for i := 0; i < circuitBreakerThreshold; i++ {
			circuitBreaker.RecordFailure()
		}

		// Should be open now
		assert.Equal(t, CircuitBreakerOpen, circuitBreaker.GetState())
		assert.False(t, circuitBreaker.Allow())
	})
}

// TestPhase8Step6_CacheEviction tests cache eviction under load
func TestPhase8Step6_CacheEviction(t *testing.T) {
	t.Run("client cache eviction", func(t *testing.T) {
		// Create small cache for testing eviction
		cache := NewClientCache(1*time.Minute, 5)
		defer cache.Stop()

		// Add more items than maxSize
		for i := int64(1); i <= 10; i++ {
			clients := &InstallationClients{}
			cache.Put(i, clients)
		}

		// Verify cache size is at or below max
		_, _, _, size := cache.GetMetrics()
		assert.LessOrEqual(t, size, int64(5), "Cache should not exceed max size")
	})

	t.Run("mapping cache operations", func(t *testing.T) {
		cache := NewMappingCache(1*time.Hour, 5*time.Minute)

		// Add entries
		cache.Set("owner/repo1", 100)
		cache.Set("owner/repo2", 200)
		cache.SetNotFound("owner/repo3")

		// Verify lookups - Get returns (installationID, found)
		id, found := cache.Get("owner/repo1")
		assert.True(t, found, "Should find owner/repo1 in cache")
		assert.Equal(t, int64(100), id)

		id, found = cache.Get("owner/repo2")
		assert.True(t, found, "Should find owner/repo2 in cache")
		assert.Equal(t, int64(200), id)

		// Negative cache entry - returns (0, true) to indicate found but not installed
		id, found = cache.Get("owner/repo3")
		assert.True(t, found, "Should find owner/repo3 in cache (negative entry)")
		assert.Equal(t, int64(0), id, "Negative cache entry should return 0")

		// Remove and verify - should return (0, false)
		cache.Remove("owner/repo1")
		id, found = cache.Get("owner/repo1")
		assert.False(t, found, "Should not find removed entry")
		assert.Equal(t, int64(0), id)
	})
}

// TestPhase8Step6_MetricsAccuracy validates metrics are being recorded correctly
func TestPhase8Step6_MetricsAccuracy(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metrics.NewRegistry())

	// Perform operations
	registry.MarkInstalled(1)
	registry.MarkInstalled(2)
	registry.MarkNotInstalled(3)

	// Check multiple times to record hits
	// Note: Check() returns (status, cacheHit) where cacheHit=true if in cache
	status1, hit1 := registry.Check(1)
	assert.Equal(t, InstallationExists, status1)
	assert.True(t, hit1, "Should be cache hit")

	status2, hit2 := registry.Check(1)
	assert.Equal(t, InstallationExists, status2)
	assert.True(t, hit2, "Should be cache hit")

	registry.Check(2)
	registry.Check(3)
	registry.Check(3)

	statusMiss, hitMiss := registry.Check(999) // miss
	assert.Equal(t, InstallationUnknown, statusMiss)
	assert.False(t, hitMiss, "Should be cache miss")

	// Verify cache size
	cacheSize := registry.GetCacheSize()
	assert.True(t, cacheSize >= 3, "At least 3 installations in cache")

	// Verify metrics are being tracked (actual values depend on internal implementation)
	cacheHits, cacheMisses, _ := registry.GetMetrics()
	assert.True(t, cacheHits > 0, "Should have cache hits")
	assert.True(t, cacheMisses > 0, "Should have cache misses")
}

// TestPhase8Step6_ErrorHandling validates error handling across components
func TestPhase8Step6_ErrorHandling(t *testing.T) {
	t.Run("invalid payload handling", func(t *testing.T) {
		ctx := context.Background()
		registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

		mockHandler := &MockEventHandler{eventTypes: []string{"pull_request"}}
		filter := NewInstallationFilterHandler(
			mockHandler,
			registry,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
		)

		// Invalid JSON
		invalidPayload := []byte(`{"invalid": json}`)

		// Should handle gracefully (pass through on parse error)
		err := filter.Handle(ctx, "pull_request", "invalid-payload", invalidPayload)
		assert.NoError(t, err, "Should handle invalid payload gracefully")
	})

	t.Run("handler error propagation", func(t *testing.T) {
		ctx := context.Background()
		registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

		expectedErr := assert.AnError
		mockHandler := &MockEventHandler{
			eventTypes: []string{"pull_request"},
			handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
				return expectedErr
			},
		}

		filter := NewInstallationFilterHandler(
			mockHandler,
			registry,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
		)

		// Mark as installed so event passes through
		registry.MarkInstalled(12345)
		payload := createTestPayload(12345)

		err := filter.Handle(ctx, "pull_request", "error-test", payload)
		assert.Equal(t, expectedErr, err, "Error should propagate from handler")
	})
}
