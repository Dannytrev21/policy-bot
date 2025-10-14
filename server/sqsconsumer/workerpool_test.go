// Copyright 2018 Palantir Technologies, Inc.
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

package sqsconsumer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// mockHandler implements githubapp.EventHandler for testing
type mockHandler struct {
	handleCount  int64
	handleDelay  time.Duration
	handleError  error
	shouldPanic  bool
	mu           sync.Mutex
	handledEvents []string
}

func (m *mockHandler) Handles() []string {
	return []string{"test_event"}
}

func (m *mockHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	atomic.AddInt64(&m.handleCount, 1)

	m.mu.Lock()
	m.handledEvents = append(m.handledEvents, deliveryID)
	m.mu.Unlock()

	if m.handleDelay > 0 {
		time.Sleep(m.handleDelay)
	}

	if m.shouldPanic {
		panic("intentional panic for testing")
	}

	return m.handleError
}

func (m *mockHandler) GetHandleCount() int64 {
	return atomic.LoadInt64(&m.handleCount)
}

func (m *mockHandler) GetHandledEvents() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.handledEvents))
	copy(result, m.handledEvents)
	return result
}

func TestWorkerPool_BasicProcessing(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 5, handler, registry, logger)

	ctx := context.Background()
	err := pool.Process(ctx, "test_event", "delivery-1", []byte(`{"test": "data"}`))

	assert.NoError(t, err)
	assert.Equal(t, int64(1), handler.GetHandleCount())
}

func TestWorkerPool_ConcurrencyLimit(t *testing.T) {
	// Create handler with delay to hold workers
	handler := &mockHandler{
		handleDelay: 200 * time.Millisecond,
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	capacity := 2
	pool := NewWorkerPool("test_event", capacity, handler, registry, logger)

	ctx := context.Background()
	var wg sync.WaitGroup
	startTime := time.Now()

	// Launch more concurrent requests than the pool capacity
	numRequests := 5
	results := make([]error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = pool.Process(ctx, "test_event", fmt.Sprintf("delivery-%d", idx), []byte(`{}`))
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	// All should succeed
	for i, err := range results {
		assert.NoError(t, err, "Request %d should succeed", i)
	}

	// Total time should be at least (numRequests / capacity) * handleDelay
	// because only `capacity` workers can run concurrently
	minExpectedDuration := time.Duration(numRequests/capacity) * handler.handleDelay
	assert.GreaterOrEqual(t, duration, minExpectedDuration, "Should respect concurrency limit")

	assert.Equal(t, int64(numRequests), handler.GetHandleCount())
}

func TestWorkerPool_PanicRecovery(t *testing.T) {
	handler := &mockHandler{
		shouldPanic: true,
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 5, handler, registry, logger)

	ctx := context.Background()
	err := pool.Process(ctx, "test_event", "delivery-1", []byte(`{}`))

	// Should return error instead of panicking
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler panic")

	// Metrics should track panic
	panicMetric := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.test_event", MetricsKeyPoolPanics), registry)
	assert.Equal(t, int64(1), panicMetric.Count())
}

func TestWorkerPool_ErrorHandling(t *testing.T) {
	expectedError := fmt.Errorf("handler error")
	handler := &mockHandler{
		handleError: expectedError,
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 5, handler, registry, logger)

	ctx := context.Background()
	err := pool.Process(ctx, "test_event", "delivery-1", []byte(`{}`))

	assert.Equal(t, expectedError, err)
	assert.Equal(t, int64(1), handler.GetHandleCount())
}

func TestWorkerPool_ContextCancellation(t *testing.T) {
	handler := &mockHandler{
		handleDelay: 1 * time.Second, // Long delay
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 1, handler, registry, logger)

	// Fill the worker pool
	ctx := context.Background()
	go pool.Process(ctx, "test_event", "delivery-1", []byte(`{}`))
	time.Sleep(50 * time.Millisecond) // Ensure first request is processing

	// Try to process with cancelled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := pool.Process(cancelledCtx, "test_event", "delivery-2", []byte(`{}`))

	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestWorkerPool_Timeout(t *testing.T) {
	handler := &mockHandler{
		handleDelay: 10 * time.Second, // Very long delay
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	// Pool with capacity 1
	pool := NewWorkerPool("test_event", 1, handler, registry, logger)

	ctx := context.Background()

	// Start first request to fill the pool
	go pool.Process(ctx, "test_event", "delivery-1", []byte(`{}`))
	time.Sleep(50 * time.Millisecond) // Ensure first request is processing

	// Second request should timeout waiting for a worker
	start := time.Now()
	err := pool.Process(ctx, "test_event", "delivery-2", []byte(`{}`))
	duration := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "worker pool timeout")

	// Should timeout around 5 seconds (the timeout in workerpool.go)
	assert.GreaterOrEqual(t, duration, 5*time.Second)
	assert.LessOrEqual(t, duration, 6*time.Second)

	// Metrics should track rejection
	rejectedMetric := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.test_event", MetricsKeyPoolRejected), registry)
	assert.Equal(t, int64(1), rejectedMetric.Count())
}

func TestWorkerPool_Metrics(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 5, handler, registry, logger)

	// Check initial metrics
	capacityMetric := metrics.GetOrRegisterGauge(fmt.Sprintf("%s.test_event", MetricsKeyPoolCapacity), registry)
	assert.Equal(t, int64(5), capacityMetric.Value())

	activeWorkersMetric := metrics.GetOrRegisterGauge(fmt.Sprintf("%s.test_event", MetricsKeyActiveWorkers), registry)
	assert.Equal(t, int64(0), activeWorkersMetric.Value())

	ctx := context.Background()
	err := pool.Process(ctx, "test_event", "delivery-1", []byte(`{}`))

	assert.NoError(t, err)

	// Check processing time metric was recorded
	processingTimeMetric := metrics.GetOrRegisterTimer(fmt.Sprintf("%s.test_event", MetricsKeyPoolProcessingTime), registry)
	assert.Equal(t, int64(1), processingTimeMetric.Count())
}

func TestWorkerPool_Shutdown(t *testing.T) {
	handler := &mockHandler{
		handleDelay: 100 * time.Millisecond,
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 2, handler, registry, logger)

	ctx := context.Background()

	// Start some work
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool.Process(ctx, "test_event", fmt.Sprintf("delivery-%d", idx), []byte(`{}`))
		}(i)
	}

	// Shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		wg.Wait() // Wait for all processing to complete
	}()

	err := pool.Shutdown(shutdownCtx)
	assert.NoError(t, err)

	// Try to process after shutdown
	err = pool.Process(ctx, "test_event", "delivery-after-shutdown", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

func TestWorkerPoolManager_GetOrCreatePool(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	manager := NewWorkerPoolManager(logger, registry)

	// Create first pool
	pool1 := manager.GetOrCreatePool("event1", 5, handler)
	assert.NotNil(t, pool1)
	assert.Equal(t, 5, pool1.GetCapacity())

	// Getting same pool should return existing pool
	pool2 := manager.GetOrCreatePool("event1", 10, handler)
	assert.Equal(t, pool1, pool2, "Should return existing pool")
	assert.Equal(t, 5, pool2.GetCapacity(), "Capacity should not change")

	// Different event type should create new pool
	pool3 := manager.GetOrCreatePool("event2", 7, handler)
	assert.NotEqual(t, pool1, pool3)
	assert.Equal(t, 7, pool3.GetCapacity())
}

func TestWorkerPoolManager_GetStats(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	manager := NewWorkerPoolManager(logger, registry)

	manager.GetOrCreatePool("event1", 5, handler)
	manager.GetOrCreatePool("event2", 10, handler)

	stats := manager.GetStats()

	assert.Len(t, stats, 2)
	assert.Equal(t, 5, stats["event1"].Capacity)
	assert.Equal(t, 10, stats["event2"].Capacity)
	assert.Equal(t, "event1", stats["event1"].EventType)
	assert.Equal(t, "event2", stats["event2"].EventType)
}

func TestWorkerPoolManager_Shutdown(t *testing.T) {
	handler := &mockHandler{
		handleDelay: 100 * time.Millisecond,
	}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	manager := NewWorkerPoolManager(logger, registry)

	pool1 := manager.GetOrCreatePool("event1", 2, handler)
	pool2 := manager.GetOrCreatePool("event2", 2, handler)

	ctx := context.Background()

	// Start some work on both pools
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool1.Process(ctx, "event1", fmt.Sprintf("delivery-%d", idx), []byte(`{}`))
		}(i)

		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool2.Process(ctx, "event2", fmt.Sprintf("delivery-%d", idx), []byte(`{}`))
		}(i)
	}

	// Shutdown manager
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		wg.Wait()
	}()

	err := manager.Shutdown(shutdownCtx)
	assert.NoError(t, err)
}

func TestWorkerPoolManager_ConcurrentAccess(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	manager := NewWorkerPoolManager(logger, registry)

	// Concurrently create and access pools
	var wg sync.WaitGroup
	numGoroutines := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			eventType := fmt.Sprintf("event%d", idx%5) // Create 5 different event types
			pool := manager.GetOrCreatePool(eventType, 5, handler)
			assert.NotNil(t, pool)
		}(i)
	}

	wg.Wait()

	// Should have exactly 5 pools
	stats := manager.GetStats()
	assert.Len(t, stats, 5)
}

func TestWorkerPoolManager_GetPool(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	manager := NewWorkerPoolManager(logger, registry)

	// Get non-existent pool should return nil
	pool := manager.GetPool("non_existent")
	assert.Nil(t, pool)

	// Create a pool
	createdPool := manager.GetOrCreatePool("event1", 5, handler)
	assert.NotNil(t, createdPool)

	// Get existing pool should return it
	pool = manager.GetPool("event1")
	assert.NotNil(t, pool)
	assert.Equal(t, createdPool, pool)
}

func TestWorkerPool_ClosedPoolRejectsWork(t *testing.T) {
	handler := &mockHandler{}
	registry := metrics.NewRegistry()
	logger := zerolog.Nop()

	pool := NewWorkerPool("test_event", 2, handler, registry, logger)

	ctx := context.Background()

	// Shutdown the pool
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := pool.Shutdown(shutdownCtx)
	assert.NoError(t, err)

	// Try to process after shutdown should fail
	err = pool.Process(ctx, "test_event", "delivery-after-shutdown", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}
