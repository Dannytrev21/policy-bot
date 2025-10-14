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
	"time"

	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

const (
	// Metrics keys for worker pool
	MetricsKeyActiveWorkers      = "sqs.worker_pool.active_workers"
	MetricsKeyPoolCapacity       = "sqs.worker_pool.capacity"
	MetricsKeyPoolUtilization    = "sqs.worker_pool.utilization"
	MetricsKeyPoolRejected       = "sqs.worker_pool.rejected_total"
	MetricsKeyPoolProcessingTime = "sqs.worker_pool.processing_time"
	MetricsKeyPoolPanics         = "sqs.worker_pool.panics_total"
)

// WorkerPool manages a pool of workers for processing events
type WorkerPool struct {
	eventType string
	capacity  int
	semaphore chan struct{} // Buffered channel used as semaphore
	handler   githubapp.EventHandler
	metrics   *WorkerPoolMetrics
	logger    zerolog.Logger

	// Track active workers for metrics
	activeWorkers int64
	mu            sync.RWMutex

	// Shutdown coordination
	closed bool
	wg     sync.WaitGroup
}

// WorkerPoolMetrics tracks metrics for a worker pool
type WorkerPoolMetrics struct {
	activeWorkers metrics.Gauge
	capacity      metrics.Gauge
	utilization   metrics.GaugeFloat64
	rejected      metrics.Counter
	processingTime metrics.Timer
	panics        metrics.Counter
}

// NewWorkerPool creates a new worker pool for an event type
func NewWorkerPool(eventType string, capacity int, handler githubapp.EventHandler, registry metrics.Registry, logger zerolog.Logger) *WorkerPool {
	if capacity <= 0 {
		capacity = DefaultWorkersPerQueue
	}

	pool := &WorkerPool{
		eventType: eventType,
		capacity:  capacity,
		semaphore: make(chan struct{}, capacity),
		handler:   handler,
		logger:    logger.With().Str("event_type", eventType).Str("component", "worker_pool").Logger(),
	}

	// Initialize metrics if registry provided
	if registry != nil {
		pool.metrics = &WorkerPoolMetrics{
			activeWorkers:  metrics.GetOrRegisterGauge(fmt.Sprintf("%s.%s", MetricsKeyActiveWorkers, eventType), registry),
			capacity:       metrics.GetOrRegisterGauge(fmt.Sprintf("%s.%s", MetricsKeyPoolCapacity, eventType), registry),
			utilization:    metrics.GetOrRegisterGaugeFloat64(fmt.Sprintf("%s.%s", MetricsKeyPoolUtilization, eventType), registry),
			rejected:       metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyPoolRejected, eventType), registry),
			processingTime: metrics.GetOrRegisterTimer(fmt.Sprintf("%s.%s", MetricsKeyPoolProcessingTime, eventType), registry),
			panics:         metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyPoolPanics, eventType), registry),
		}
		pool.metrics.capacity.Update(int64(capacity))
	}

	return pool
}

// Process processes an event using the worker pool
func (p *WorkerPool) Process(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	// Check if pool is closed
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return fmt.Errorf("worker pool for %s is closed", p.eventType)
	}
	p.mu.RUnlock()

	// Try to acquire a worker slot with timeout
	select {
	case p.semaphore <- struct{}{}:
		// Successfully acquired worker slot
		defer func() {
			<-p.semaphore
			p.decrementActiveWorkers()
		}()
	case <-time.After(5 * time.Second):
		// Timeout acquiring worker - queue is full
		if p.metrics != nil {
			p.metrics.rejected.Inc(1)
		}
		p.logger.Warn().
			Str("delivery_id", deliveryID).
			Msg("Worker pool timeout - all workers busy")
		return fmt.Errorf("worker pool timeout for %s: all workers busy", p.eventType)
	case <-ctx.Done():
		return ctx.Err()
	}

	// Track active worker
	p.incrementActiveWorkers()

	// Start timer for metrics
	var timer metrics.Timer
	if p.metrics != nil {
		timer = p.metrics.processingTime
	}
	start := time.Now()

	// Execute handler with panic recovery
	err := p.safeExecuteHandler(ctx, eventType, deliveryID, payload)

	// Record processing time
	if timer != nil {
		timer.UpdateSince(start)
	}

	return err
}

// safeExecuteHandler executes the handler with panic recovery
func (p *WorkerPool) safeExecuteHandler(ctx context.Context, eventType, deliveryID string, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panic: %v", r)
			if p.metrics != nil {
				p.metrics.panics.Inc(1)
			}
			p.logger.Error().
				Interface("panic", r).
				Str("delivery_id", deliveryID).
				Msg("Handler panicked during execution")
		}
	}()

	// Execute the handler directly
	return p.handler.Handle(ctx, eventType, deliveryID, payload)
}

// incrementActiveWorkers safely increments the active worker count
func (p *WorkerPool) incrementActiveWorkers() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeWorkers++
	p.updateMetrics()
}

// decrementActiveWorkers safely decrements the active worker count
func (p *WorkerPool) decrementActiveWorkers() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeWorkers--
	p.updateMetrics()
}

// updateMetrics updates gauge metrics (must be called with lock held)
func (p *WorkerPool) updateMetrics() {
	if p.metrics != nil {
		p.metrics.activeWorkers.Update(p.activeWorkers)
		if p.capacity > 0 {
			utilization := float64(p.activeWorkers) / float64(p.capacity)
			p.metrics.utilization.Update(utilization)
		}
	}
}

// Shutdown gracefully shuts down the worker pool
func (p *WorkerPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	p.logger.Info().Msg("Shutting down worker pool")

	// Wait for all in-flight work to complete
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Info().Msg("Worker pool shut down gracefully")
		return nil
	case <-ctx.Done():
		p.logger.Warn().Msg("Worker pool shutdown context cancelled")
		return ctx.Err()
	}
}

// GetActiveWorkers returns the current number of active workers
func (p *WorkerPool) GetActiveWorkers() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.activeWorkers
}

// GetCapacity returns the worker pool capacity
func (p *WorkerPool) GetCapacity() int {
	return p.capacity
}

// WorkerPoolManager manages multiple worker pools
type WorkerPoolManager struct {
	pools    map[string]*WorkerPool
	mu       sync.RWMutex
	logger   zerolog.Logger
	registry metrics.Registry
}

// NewWorkerPoolManager creates a new worker pool manager
func NewWorkerPoolManager(logger zerolog.Logger, registry metrics.Registry) *WorkerPoolManager {
	return &WorkerPoolManager{
		pools:    make(map[string]*WorkerPool),
		logger:   logger.With().Str("component", "worker_pool_manager").Logger(),
		registry: registry,
	}
}

// GetOrCreatePool gets an existing pool or creates a new one
func (m *WorkerPoolManager) GetOrCreatePool(eventType string, capacity int, handler githubapp.EventHandler) *WorkerPool {
	m.mu.RLock()
	pool, exists := m.pools[eventType]
	m.mu.RUnlock()

	if exists {
		return pool
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if pool, exists := m.pools[eventType]; exists {
		return pool
	}

	// Create new pool
	pool = NewWorkerPool(eventType, capacity, handler, m.registry, m.logger)
	m.pools[eventType] = pool

	m.logger.Info().
		Str("event_type", eventType).
		Int("capacity", capacity).
		Msg("Created new worker pool")

	return pool
}

// GetPool retrieves an existing pool
func (m *WorkerPoolManager) GetPool(eventType string) *WorkerPool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pools[eventType]
}

// Shutdown shuts down all worker pools
func (m *WorkerPoolManager) Shutdown(ctx context.Context) error {
	m.mu.RLock()
	pools := make([]*WorkerPool, 0, len(m.pools))
	for _, pool := range m.pools {
		pools = append(pools, pool)
	}
	m.mu.RUnlock()

	m.logger.Info().
		Int("pool_count", len(pools)).
		Msg("Shutting down all worker pools")

	// Shutdown all pools concurrently
	errChan := make(chan error, len(pools))
	for _, pool := range pools {
		go func(p *WorkerPool) {
			errChan <- p.Shutdown(ctx)
		}(pool)
	}

	// Collect errors
	var errors []error
	for range pools {
		if err := <-errChan; err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("worker pool shutdown errors: %v", errors)
	}

	m.logger.Info().Msg("All worker pools shut down successfully")
	return nil
}

// GetStats returns statistics for all worker pools
func (m *WorkerPoolManager) GetStats() map[string]PoolStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]PoolStats)
	for eventType, pool := range m.pools {
		stats[eventType] = PoolStats{
			EventType:     eventType,
			Capacity:      pool.GetCapacity(),
			ActiveWorkers: pool.GetActiveWorkers(),
			Utilization:   float64(pool.GetActiveWorkers()) / float64(pool.GetCapacity()),
		}
	}
	return stats
}

// PoolStats represents statistics for a worker pool
type PoolStats struct {
	EventType     string  `json:"event_type"`
	Capacity      int     `json:"capacity"`
	ActiveWorkers int64   `json:"active_workers"`
	Utilization   float64 `json:"utilization"`
}
