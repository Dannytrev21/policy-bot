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

package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInstallationManager(t *testing.T) {
	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	assert.NotNil(t, manager, "Manager should be created")
	assert.Equal(t, mockCreator, manager.clientCreator)
	assert.Equal(t, registry, manager.installationRegistry)
	assert.Equal(t, metricsRegistry, manager.metricsRegistry)
}

func TestInstallationManager_GetClients_Success(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:          github.NewClient(nil),
		appClientErr:       nil,
		installationClient: github.NewClient(nil),
		installationErr:    nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed in registry
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert
	require.NoError(t, err, "Should not return error on success")
	require.NotNil(t, clients, "Clients should not be nil")
	assert.NotNil(t, clients.V3Client, "V3 client should be created")
	assert.NotNil(t, clients.V4Client, "V4 client should be created")

	// Verify metrics were recorded
	v3Success := metricsRegistry.Get(MetricsKeyInstallationClientSuccess)
	require.NotNil(t, v3Success, "V3 success metric should be recorded")
	if counter, ok := v3Success.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V3 success metric should be 1")
	}

	v4Success := metricsRegistry.Get(MetricsKeyInstallationV4ClientSuccess)
	require.NotNil(t, v4Success, "V4 success metric should be recorded")
	if counter, ok := v4Success.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V4 success metric should be 1")
	}
}

func TestInstallationManager_GetClients_InstallationNotFound(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(99999)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as NOT installed in registry
	registry.MarkNotInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert
	require.Error(t, err, "Should return error when installation not found")
	assert.Nil(t, clients, "Clients should be nil on error")
	assert.Contains(t, err.Error(), "not found or not accessible", "Error should indicate installation not found")
}

func TestInstallationManager_GetClients_V3ClientCreationFails(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:          github.NewClient(nil),
		appClientErr:       nil,
		installationClient: nil,
		installationErr:    assert.AnError,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed in registry
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert
	require.Error(t, err, "Should return error when v3 client creation fails")
	assert.Nil(t, clients, "Clients should be nil on error")
	assert.Contains(t, err.Error(), "failed to create installation client", "Error should mention client creation failure")

	// Verify failure metric was recorded
	v3Failure := metricsRegistry.Get(MetricsKeyInstallationClientFailure)
	require.NotNil(t, v3Failure, "V3 failure metric should be recorded")
	if counter, ok := v3Failure.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V3 failure metric should be 1")
	}
}

func TestInstallationManager_GetClients_V4ClientCreationFails(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreatorWithV4Error{
		MockClientCreator: MockClientCreator{
			appClient:          github.NewClient(nil),
			appClientErr:       nil,
			installationClient: github.NewClient(nil),
			installationErr:    nil,
		},
		v4Error: assert.AnError,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed in registry
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert
	require.Error(t, err, "Should return error when v4 client creation fails")
	assert.Nil(t, clients, "Clients should be nil on error")
	assert.Contains(t, err.Error(), "failed to create installation v4 client", "Error should mention v4 client creation failure")

	// Verify success metric for v3 and failure metric for v4
	v3Success := metricsRegistry.Get(MetricsKeyInstallationClientSuccess)
	require.NotNil(t, v3Success, "V3 success metric should be recorded")
	if counter, ok := v3Success.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V3 success metric should be 1")
	}

	v4Failure := metricsRegistry.Get(MetricsKeyInstallationV4ClientFailure)
	require.NotNil(t, v4Failure, "V4 failure metric should be recorded")
	if counter, ok := v4Failure.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V4 failure metric should be 1")
	}
}

func TestInstallationManager_GetClients_CacheMiss(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Don't populate the cache - this simulates a cache miss
	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert
	require.Error(t, err, "Should return error when cache miss occurs")
	assert.Nil(t, clients, "Clients should be nil on cache miss")
	assert.Contains(t, err.Error(), "not found or not accessible", "Error should indicate installation not found")
}

func TestInstallationManager_RecordMetric_NilRegistry(t *testing.T) {
	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Create manager with nil metrics registry
	manager := NewInstallationManager(mockCreator, registry, nil)

	// This should not panic
	assert.NotPanics(t, func() {
		manager.recordMetric(MetricsKeyInstallationClientSuccess)
	}, "Recording metric with nil registry should not panic")
}

func TestInstallationManager_MultipleClientCreations(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:          github.NewClient(nil),
		appClientErr:       nil,
		installationClient: github.NewClient(nil),
		installationErr:    nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed in registry
	registry.MarkInstalled(installationID)

	// Create clients multiple times - with caching, only first call creates new clients
	for i := 0; i < 3; i++ {
		clients, err := manager.GetClients(ctx, installationID, repoFullName)
		require.NoError(t, err, "Should not return error on iteration %d", i)
		require.NotNil(t, clients, "Clients should not be nil on iteration %d", i)
	}

	// Verify metrics: only 1 creation (first call), subsequent calls use cache
	v3Success := metricsRegistry.Get(MetricsKeyInstallationClientSuccess)
	require.NotNil(t, v3Success, "V3 success metric should be recorded")
	if counter, ok := v3Success.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V3 success metric should be 1 (only first call creates)")
	}

	v4Success := metricsRegistry.Get(MetricsKeyInstallationV4ClientSuccess)
	require.NotNil(t, v4Success, "V4 success metric should be recorded")
	if counter, ok := v4Success.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V4 success metric should be 1 (only first call creates)")
	}

	// Verify cache hits were recorded
	cacheHits := metricsRegistry.Get(MetricsKeyClientCacheHits)
	require.NotNil(t, cacheHits, "Cache hits metric should be recorded")
	if counter, ok := cacheHits.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(2), counter.Count(), "Should have 2 cache hits (calls 2 and 3)")
	}
}

func TestInstallationManager_ConcurrentClientCreations(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	mockCreator := &MockClientCreator{
		appClient:          github.NewClient(nil),
		appClientErr:       nil,
		installationClient: github.NewClient(nil),
		installationErr:    nil,
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()

	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed in registry
	registry.MarkInstalled(installationID)

	// Create clients concurrently - with caching, only first goroutine creates clients
	// All others get cached clients. Race condition means we might have 1-2 creations
	// if multiple goroutines check cache before first one finishes
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			clients, err := manager.GetClients(ctx, installationID, repoFullName)
			assert.NoError(t, err)
			assert.NotNil(t, clients)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify metrics: With caching, should have 1-2 creations (race condition)
	// and rest should be cache hits
	v3Success := metricsRegistry.Get(MetricsKeyInstallationClientSuccess)
	require.NotNil(t, v3Success, "V3 success metric should be recorded")
	if counter, ok := v3Success.(interface{ Count() int64 }); ok {
		creationCount := counter.Count()
		assert.GreaterOrEqual(t, creationCount, int64(1), "Should have at least 1 creation")
		assert.LessOrEqual(t, creationCount, int64(2), "Should have at most 2 creations (race condition)")
	}

	// Total of creations + cache hits should equal 10
	cacheHits := metricsRegistry.Get(MetricsKeyClientCacheHits)
	if cacheHits != nil {
		if counter, ok := cacheHits.(interface{ Count() int64 }); ok {
			v3Counter, _ := v3Success.(interface{ Count() int64 })
			total := v3Counter.Count() + counter.Count()
			assert.GreaterOrEqual(t, total, int64(10), "Total of creations + cache hits should be at least 10")
		}
	}
}

// MockRetryableClientCreator simulates transient failures that can be retried
type MockRetryableClientCreator struct {
	MockClientCreator
	v3CallCount int
	v3FailUntil int // Fail until this call count
	v3Error     error
	v4CallCount int
	v4FailUntil int
	v4Error     error
}

func (m *MockRetryableClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	m.v3CallCount++
	if m.v3CallCount <= m.v3FailUntil {
		return nil, m.v3Error
	}
	return github.NewClient(nil), nil
}

func (m *MockRetryableClientCreator) NewInstallationV4Client(installationID int64) (*githubv4.Client, error) {
	m.v4CallCount++
	if m.v4CallCount <= m.v4FailUntil {
		return nil, m.v4Error
	}
	return githubv4.NewClient(nil), nil
}

func TestInstallationManager_RetryLogic_V3ClientTransientError(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that fails twice then succeeds (simulating transient error)
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 2,                                       // Fail first 2 attempts, succeed on 3rd
		v3Error:     errors.New("500 Internal Server Error"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert success after retries
	require.NoError(t, err, "Should succeed after retries")
	require.NotNil(t, clients, "Clients should be created")
	assert.NotNil(t, clients.V3Client, "V3 client should be created")
	assert.Equal(t, 3, mockCreator.v3CallCount, "Should have made 3 attempts")

	// Verify retry success metric was recorded
	retrySuccess := metricsRegistry.Get(MetricsKeyInstallationClientRetrySuccess)
	require.NotNil(t, retrySuccess, "Retry success metric should be recorded")
	if counter, ok := retrySuccess.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "Retry success metric should be 1")
	}
}

func TestInstallationManager_RetryLogic_V3ClientNonRetryableError(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that returns 404 (non-retryable)
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 10,                          // Would fail many times
		v3Error:     errors.New("404 Not Found"), // Non-retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert immediate failure without retries
	require.Error(t, err, "Should fail immediately for non-retryable error")
	assert.Nil(t, clients, "Clients should be nil")
	assert.Equal(t, 1, mockCreator.v3CallCount, "Should have made only 1 attempt (no retries)")
	assert.Contains(t, err.Error(), "404", "Error should contain 404")

	// Verify NO retry metrics were recorded
	retrySuccess := metricsRegistry.Get(MetricsKeyInstallationClientRetrySuccess)
	if retrySuccess != nil {
		if counter, ok := retrySuccess.(interface{ Count() int64 }); ok {
			assert.Equal(t, int64(0), counter.Count(), "No retry success metric should be recorded")
		}
	}
}

func TestInstallationManager_RetryLogic_V3ClientRetryExhausted(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that always fails with retryable error
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 10,                                    // Fail more times than max retries
		v3Error:     errors.New("503 Service Unavailable"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert failure after all retries exhausted
	require.Error(t, err, "Should fail after all retries exhausted")
	assert.Nil(t, clients, "Clients should be nil")
	assert.Equal(t, maxRetryAttempts, mockCreator.v3CallCount, "Should have made max attempts")
	assert.Contains(t, err.Error(), "after 3 attempts", "Error should mention exhausted retries")

	// Verify retry exhausted metric was recorded
	retryExhausted := metricsRegistry.Get(MetricsKeyInstallationClientRetryExhausted)
	require.NotNil(t, retryExhausted, "Retry exhausted metric should be recorded")
	if counter, ok := retryExhausted.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "Retry exhausted metric should be 1")
	}
}

func TestInstallationManager_RetryLogic_V4ClientTransientError(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that fails v4 client twice then succeeds
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:          github.NewClient(nil),
			appClientErr:       nil,
			installationClient: github.NewClient(nil),
			installationErr:    nil,
		},
		v4FailUntil: 2,                             // Fail first 2 attempts, succeed on 3rd
		v4Error:     errors.New("502 Bad Gateway"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert success after retries
	require.NoError(t, err, "Should succeed after retries")
	require.NotNil(t, clients, "Clients should be created")
	assert.NotNil(t, clients.V4Client, "V4 client should be created")
	assert.Equal(t, 3, mockCreator.v4CallCount, "Should have made 3 attempts for v4")

	// Verify v4 retry success metric was recorded
	retrySuccess := metricsRegistry.Get(MetricsKeyInstallationV4ClientRetrySuccess)
	require.NotNil(t, retrySuccess, "V4 retry success metric should be recorded")
	if counter, ok := retrySuccess.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "V4 retry success metric should be 1")
	}
}

func TestInstallationManager_RetryLogic_ContextCancellation(t *testing.T) {
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that always fails with retryable error
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 10,                    // Fail many times
		v3Error:     errors.New("timeout"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = zerolog.New(nil).WithContext(ctx)

	// Execute
	clients, err := manager.GetClients(ctx, installationID, repoFullName)

	// Assert context cancellation error
	require.Error(t, err, "Should fail due to context cancellation")
	assert.Nil(t, clients, "Clients should be nil")
	// Should have attempted at least once, but not necessarily all retries
	assert.GreaterOrEqual(t, mockCreator.v3CallCount, 1, "Should have made at least 1 attempt")
	assert.LessOrEqual(t, mockCreator.v3CallCount, maxRetryAttempts, "Should not exceed max attempts")
}

// TestIsRetryableError is now in errors_test.go since the function was moved to errors.go

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		attempt     int
		minExpected time.Duration
		maxExpected time.Duration
	}{
		{0, 800 * time.Millisecond, 1200 * time.Millisecond},               // 1s ± 20%
		{1, 1600 * time.Millisecond, 2400 * time.Millisecond},              // 2s ± 20%
		{2, 3200 * time.Millisecond, 4800 * time.Millisecond},              // 4s ± 20%
		{3, maxRetryDelay - 2*time.Second, maxRetryDelay + 2*time.Second},  // Capped at 8s ± jitter
		{10, maxRetryDelay - 2*time.Second, maxRetryDelay + 2*time.Second}, // Still capped at 8s
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.attempt)), func(t *testing.T) {
			backoff := calculateBackoff(tt.attempt)
			assert.GreaterOrEqual(t, backoff, tt.minExpected, "Backoff should be >= min expected")
			assert.LessOrEqual(t, backoff, tt.maxExpected, "Backoff should be <= max expected")
		})
	}
}

// Circuit Breaker Tests

func TestNewCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker()
	assert.NotNil(t, cb, "Circuit breaker should be created")
	assert.Equal(t, CircuitBreakerClosed, cb.GetState(), "Circuit breaker should start in closed state")
}

func TestCircuitBreaker_AllowWhenClosed(t *testing.T) {
	cb := NewCircuitBreaker()
	assert.True(t, cb.Allow(), "Circuit breaker should allow requests when closed")
}

func TestCircuitBreaker_OpenAfterConsecutiveFailures(t *testing.T) {
	cb := NewCircuitBreaker()

	// Record failures up to threshold
	for i := 0; i < circuitBreakerThreshold; i++ {
		previousState := cb.RecordFailure()
		if i < circuitBreakerThreshold-1 {
			assert.Equal(t, CircuitBreakerClosed, previousState, "Should stay closed until threshold")
			assert.Equal(t, CircuitBreakerClosed, cb.GetState(), "Should stay closed until threshold")
		} else {
			// Last failure should open circuit
			assert.Equal(t, CircuitBreakerClosed, previousState, "Previous state should be closed")
			assert.Equal(t, CircuitBreakerOpen, cb.GetState(), "Circuit should be open after threshold failures")
		}
	}
}

func TestCircuitBreaker_BlockRequestsWhenOpen(t *testing.T) {
	cb := NewCircuitBreaker()

	// Open circuit by recording failures
	for i := 0; i < circuitBreakerThreshold; i++ {
		cb.RecordFailure()
	}

	assert.Equal(t, CircuitBreakerOpen, cb.GetState(), "Circuit should be open")
	assert.False(t, cb.Allow(), "Circuit breaker should block requests when open")
}

func TestCircuitBreaker_TransitionToHalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker()

	// Open circuit
	for i := 0; i < circuitBreakerThreshold; i++ {
		cb.RecordFailure()
	}
	assert.Equal(t, CircuitBreakerOpen, cb.GetState(), "Circuit should be open")

	// Fast forward time by modifying lastFailureTime (simulate timeout)
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-circuitBreakerTimeout - 1*time.Second)
	cb.mu.Unlock()

	// Next Allow() call should transition to half-open
	assert.True(t, cb.Allow(), "Should allow request after timeout")
	assert.Equal(t, CircuitBreakerHalfOpen, cb.GetState(), "Circuit should be half-open after timeout")
}

func TestCircuitBreaker_CloseAfterSuccessInHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker()

	// Open circuit
	for i := 0; i < circuitBreakerThreshold; i++ {
		cb.RecordFailure()
	}

	// Transition to half-open
	cb.mu.Lock()
	cb.state = CircuitBreakerHalfOpen
	cb.halfOpenSuccesses = 0
	cb.mu.Unlock()

	// Record success should close circuit (with circuitBreakerHalfOpenMax = 1)
	previousState := cb.RecordSuccess()
	assert.Equal(t, CircuitBreakerHalfOpen, previousState, "Previous state should be half-open")
	assert.Equal(t, CircuitBreakerClosed, cb.GetState(), "Circuit should be closed after success in half-open")
}

func TestCircuitBreaker_ReopenOnFailureInHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker()

	// Set to half-open state directly
	cb.mu.Lock()
	cb.state = CircuitBreakerHalfOpen
	cb.halfOpenSuccesses = 0
	cb.mu.Unlock()

	// Record failure should reopen circuit
	previousState := cb.RecordFailure()
	assert.Equal(t, CircuitBreakerHalfOpen, previousState, "Previous state should be half-open")
	assert.Equal(t, CircuitBreakerOpen, cb.GetState(), "Circuit should reopen on failure in half-open")
}

func TestCircuitBreaker_ResetFailureCountOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker()

	// Record some failures (but not enough to open)
	for i := 0; i < circuitBreakerThreshold-1; i++ {
		cb.RecordFailure()
	}
	assert.Equal(t, CircuitBreakerClosed, cb.GetState(), "Circuit should still be closed")

	// Record success should reset failure counter
	cb.RecordSuccess()

	// Now record more failures - should take threshold attempts to open again
	for i := 0; i < circuitBreakerThreshold-1; i++ {
		cb.RecordFailure()
		assert.Equal(t, CircuitBreakerClosed, cb.GetState(), "Circuit should still be closed after reset")
	}

	// One more failure should open
	cb.RecordFailure()
	assert.Equal(t, CircuitBreakerOpen, cb.GetState(), "Circuit should open after threshold failures")
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker()

	// Concurrently record successes and failures
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				if id%2 == 0 {
					cb.RecordSuccess()
				} else {
					cb.RecordFailure()
				}
				cb.Allow()
				cb.GetState()
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not crash or deadlock
	state := cb.GetState()
	assert.Contains(t, []CircuitBreakerState{CircuitBreakerClosed, CircuitBreakerOpen, CircuitBreakerHalfOpen}, state, "State should be valid")
}

func TestCircuitBreakerState_String(t *testing.T) {
	tests := []struct {
		state    CircuitBreakerState
		expected string
	}{
		{CircuitBreakerClosed, "closed"},
		{CircuitBreakerOpen, "open"},
		{CircuitBreakerHalfOpen, "half-open"},
		{CircuitBreakerState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.state.String())
		})
	}
}

func TestInstallationManager_CircuitBreakerIntegration_OpensOnConsecutiveFailures(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that always fails with retryable error
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 100,                                   // Always fail
		v3Error:     errors.New("503 Service Unavailable"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Make requests until circuit opens
	for i := 0; i < circuitBreakerThreshold; i++ {
		_, err := manager.GetClients(ctx, installationID, repoFullName)
		assert.Error(t, err, "Should fail on attempt %d", i+1)
	}

	// Circuit should be open now
	assert.Equal(t, CircuitBreakerOpen, manager.circuitBreaker.GetState(), "Circuit should be open after threshold failures")

	// Next request should be rejected immediately by circuit breaker
	_, err := manager.GetClients(ctx, installationID, repoFullName)
	assert.Error(t, err, "Should be rejected by circuit breaker")
	assert.Contains(t, err.Error(), "circuit breaker is open", "Error should mention circuit breaker")

	// Verify circuit breaker opened metric was recorded
	openedMetric := metricsRegistry.Get(MetricsKeyCircuitBreakerOpened)
	require.NotNil(t, openedMetric, "Circuit breaker opened metric should be recorded")
	if counter, ok := openedMetric.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "Circuit breaker should have opened once")
	}
}

func TestInstallationManager_CircuitBreakerIntegration_RecoveryFlow(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that fails for many attempts (accounting for retries)
	// Each GetClients call makes up to 3 retry attempts, so we need enough failures
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 100,                                   // Fail for a long time
		v3Error:     errors.New("503 Service Unavailable"), // Retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Open circuit by making enough failed requests
	for i := 0; i < circuitBreakerThreshold; i++ {
		manager.GetClients(ctx, installationID, repoFullName)
	}
	assert.Equal(t, CircuitBreakerOpen, manager.circuitBreaker.GetState(), "Circuit should be open")

	// Simulate timeout - force transition to half-open
	manager.circuitBreaker.mu.Lock()
	manager.circuitBreaker.lastFailureTime = time.Now().Add(-circuitBreakerTimeout - 1*time.Second)
	manager.circuitBreaker.mu.Unlock()

	// Make request succeed (set fail until to 0)
	mockCreator.v3FailUntil = 0

	// Next request should succeed and close circuit
	clients, err := manager.GetClients(ctx, installationID, repoFullName)
	assert.NoError(t, err, "Should succeed after recovery")
	assert.NotNil(t, clients, "Clients should be created")
	assert.Equal(t, CircuitBreakerClosed, manager.circuitBreaker.GetState(), "Circuit should be closed after successful recovery")

	// Verify circuit breaker closed metric was recorded
	closedMetric := metricsRegistry.Get(MetricsKeyCircuitBreakerClosed)
	require.NotNil(t, closedMetric, "Circuit breaker closed metric should be recorded")
	if counter, ok := closedMetric.(interface{ Count() int64 }); ok {
		assert.Equal(t, int64(1), counter.Count(), "Circuit breaker should have closed once")
	}
}

func TestInstallationManager_CircuitBreakerIntegration_NonRetryableErrorsDoNotTrigger(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)
	repoFullName := "test-owner/test-repo"

	// Mock creator that always fails with non-retryable error (404)
	mockCreator := &MockRetryableClientCreator{
		MockClientCreator: MockClientCreator{
			appClient:    github.NewClient(nil),
			appClientErr: nil,
		},
		v3FailUntil: 100,                         // Always fail
		v3Error:     errors.New("404 Not Found"), // Non-retryable error
	}

	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry)

	// Mark installation as installed
	registry.MarkInstalled(installationID)

	// Make many requests with 404 errors
	for i := 0; i < circuitBreakerThreshold*2; i++ {
		_, err := manager.GetClients(ctx, installationID, repoFullName)
		assert.Error(t, err, "Should fail on attempt %d", i+1)
	}

	// Circuit should NOT open for non-retryable errors
	assert.Equal(t, CircuitBreakerClosed, manager.circuitBreaker.GetState(), "Circuit should stay closed for non-retryable errors")
}
