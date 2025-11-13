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
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPhase8Step3_CircuitBreakerShared verifies that Manager and Locator share the same circuit breaker instance
func TestPhase8Step3_CircuitBreakerShared(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	logger := zerolog.Nop()

	// Create shared circuit breaker
	circuitBreaker := NewCircuitBreaker()

	// Create manager with shared circuit breaker
	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry, circuitBreaker)

	// Create locator with same shared circuit breaker
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return github.NewClient(nil), nil
	}
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	// Verify both components use the same circuit breaker instance
	managerCB := manager.circuitBreaker
	locatorCB := locator.circuitBreaker

	assert.Same(t, circuitBreaker, managerCB, "Manager should use provided circuit breaker")
	assert.Same(t, circuitBreaker, locatorCB, "Locator should use provided circuit breaker")
	assert.Same(t, managerCB, locatorCB, "Manager and Locator should share the same circuit breaker instance")

	t.Log("✅ Phase 8 Step 3: Manager and Locator share the same circuit breaker instance")
}

// TestPhase8Step3_BaseInitializesSharedCircuitBreaker verifies that Base.Initialize() creates and shares circuit breaker
func TestPhase8Step3_BaseInitializesSharedCircuitBreaker(t *testing.T) {
	base := &Base{
		ClientCreator:   &MockClientCreator{appClient: github.NewClient(nil)},
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}

	// Initialize base
	base.Initialize()

	// Verify circuit breaker was created
	require.NotNil(t, base.CircuitBreaker, "Base should initialize circuit breaker")

	// Verify manager was created with circuit breaker
	require.NotNil(t, base.InstallationManager, "Base should initialize manager")
	assert.Same(t, base.CircuitBreaker, base.InstallationManager.circuitBreaker,
		"Manager should use Base's circuit breaker")

	// Verify locator was created with circuit breaker
	require.NotNil(t, base.InstallationLocator, "Base should initialize locator")
	assert.Same(t, base.CircuitBreaker, base.InstallationLocator.circuitBreaker,
		"Locator should use Base's circuit breaker")

	// Verify all three point to the same instance
	assert.Same(t, base.CircuitBreaker, base.InstallationManager.circuitBreaker,
		"Base and Manager should share circuit breaker")
	assert.Same(t, base.CircuitBreaker, base.InstallationLocator.circuitBreaker,
		"Base and Locator should share circuit breaker")

	t.Log("✅ Phase 8 Step 3: Base.Initialize() creates and shares circuit breaker correctly")
}

// TestPhase8Step3_ManagerFailureAffectsLocator verifies that circuit breaker failures in Manager affect Locator
func TestPhase8Step3_ManagerFailureAffectsLocator(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	logger := zerolog.Nop()

	// Create shared circuit breaker
	circuitBreaker := NewCircuitBreaker()

	// Create manager with failing client creator (use retryable error)
	mockCreator := &MockClientCreator{
		appClient:       github.NewClient(nil),
		appClientErr:    nil,
		installationErr: errors.New("500 Internal Server Error - simulated GitHub API failure"),
	}
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry, circuitBreaker)

	// Create locator with same circuit breaker
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return github.NewClient(nil), nil
	}
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	ctx := context.Background()
	installationID := int64(12345)

	// Mark installation as installed to pass verification
	registry.MarkInstalled(installationID)

	// Trigger failures in manager to open circuit breaker
	// Need to trigger circuitBreakerThreshold (5) consecutive failures
	for i := 0; i < circuitBreakerThreshold; i++ {
		_, err := manager.GetClients(ctx, installationID, "test/repo")
		assert.Error(t, err, "Manager should fail with simulated error")
	}

	// Verify circuit breaker is now open
	assert.Equal(t, CircuitBreakerOpen, circuitBreaker.GetState(),
		"Circuit breaker should be open after threshold failures")

	// Verify that locator is also affected (circuit breaker blocks requests)
	assert.False(t, locator.circuitBreaker.Allow(),
		"Locator should see circuit breaker is blocking requests in open state")

	t.Log("✅ Phase 8 Step 3: Manager failures affect Locator through shared circuit breaker")
}

// TestPhase8Step3_CircuitBreakerStateTransitions verifies state transitions work correctly when shared
func TestPhase8Step3_CircuitBreakerStateTransitions(t *testing.T) {
	// Create shared circuit breaker
	circuitBreaker := NewCircuitBreaker()

	// Initial state should be closed
	assert.Equal(t, CircuitBreakerClosed, circuitBreaker.GetState(), "Initial state should be closed")
	assert.True(t, circuitBreaker.Allow(), "Should allow requests in closed state")

	// Trigger failures to open circuit
	for i := 0; i < circuitBreakerThreshold; i++ {
		circuitBreaker.RecordFailure()
	}

	assert.Equal(t, CircuitBreakerOpen, circuitBreaker.GetState(), "Should transition to open after threshold failures")
	assert.False(t, circuitBreaker.Allow(), "Should block requests in open state")

	// Wait for timeout to transition to half-open
	time.Sleep(circuitBreakerTimeout + 10*time.Millisecond)

	// Allow() should transition to half-open
	assert.True(t, circuitBreaker.Allow(), "Should allow requests after timeout (half-open)")
	assert.Equal(t, CircuitBreakerHalfOpen, circuitBreaker.GetState(), "Should be in half-open state")

	// Record success to close circuit
	circuitBreaker.RecordSuccess()
	assert.Equal(t, CircuitBreakerClosed, circuitBreaker.GetState(), "Should transition to closed after success in half-open")
	assert.True(t, circuitBreaker.Allow(), "Should allow requests in closed state")

	t.Log("✅ Phase 8 Step 3: Circuit breaker state transitions work correctly")
}

// TestPhase8Step3_NoCircuitBreakerFieldsInStructs verifies there are no duplicate circuit breaker fields
func TestPhase8Step3_NoCircuitBreakerFieldsInStructs(t *testing.T) {
	base := &Base{
		ClientCreator:   &MockClientCreator{appClient: github.NewClient(nil)},
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Count how many circuit breaker instances exist
	// Should only be 1: in Base (shared by Manager and Locator)
	baseType := reflect.TypeOf(base).Elem()
	cbFieldCount := 0

	for i := 0; i < baseType.NumField(); i++ {
		field := baseType.Field(i)
		if field.Type.String() == "*handler.CircuitBreaker" {
			cbFieldCount++
		}
	}

	assert.Equal(t, 1, cbFieldCount, "Base should have exactly 1 CircuitBreaker field")

	// Verify Manager and Locator reference the same instance (already tested above, but confirming)
	assert.Same(t, base.CircuitBreaker, base.InstallationManager.circuitBreaker)
	assert.Same(t, base.CircuitBreaker, base.InstallationLocator.circuitBreaker)

	t.Log("✅ Phase 8 Step 3: No duplicate circuit breaker fields")
}

// TestPhase8Step3_ConsistentFailureTracking verifies failures are tracked consistently across components
func TestPhase8Step3_ConsistentFailureTracking(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	metricsRegistry := gometrics.NewRegistry()
	logger := zerolog.Nop()

	// Create shared circuit breaker
	circuitBreaker := NewCircuitBreaker()

	// Create manager with retryable error
	mockCreator := &MockClientCreator{
		appClient:       github.NewClient(nil),
		appClientErr:    nil,
		installationErr: errors.New("503 Service Unavailable - API failure"),
	}
	manager := NewInstallationManager(mockCreator, registry, metricsRegistry, circuitBreaker)

	// Create locator
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, errors.New("API failure")
	}
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	ctx := context.Background()
	installationID := int64(12345)
	registry.MarkInstalled(installationID)

	// Record 3 failures from manager
	for i := 0; i < 3; i++ {
		manager.GetClients(ctx, installationID, "test/repo")
	}

	// State should still be closed (need 5 failures)
	assert.Equal(t, CircuitBreakerClosed, circuitBreaker.GetState())

	// Record 2 more failures from manager to reach threshold
	for i := 0; i < 2; i++ {
		manager.GetClients(ctx, installationID, "test/repo")
	}

	// Now circuit should be open
	assert.Equal(t, CircuitBreakerOpen, circuitBreaker.GetState(),
		"Circuit breaker should open after 5 total failures")

	// Locator should see the open circuit
	assert.False(t, locator.circuitBreaker.Allow(),
		"Locator should see circuit breaker is open")

	t.Log("✅ Phase 8 Step 3: Failures tracked consistently across Manager and Locator")
}

// TestPhase8Step3_BackwardCompatibility verifies existing behavior is preserved
func TestPhase8Step3_BackwardCompatibility(t *testing.T) {
	t.Run("Manager still creates clients correctly", func(t *testing.T) {
		ctx := zerolog.New(nil).WithContext(context.Background())
		installationID := int64(12345)

		mockCreator := &MockClientCreator{
			appClient:          github.NewClient(nil),
			installationClient: github.NewClient(nil),
		}

		registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
		registry.MarkInstalled(installationID)

		circuitBreaker := NewCircuitBreaker()
		manager := NewInstallationManager(mockCreator, registry, nil, circuitBreaker)

		clients, err := manager.GetClients(ctx, installationID, "test/repo")
		require.NoError(t, err)
		assert.NotNil(t, clients)
		assert.NotNil(t, clients.V3Client)
		assert.NotNil(t, clients.V4Client)
	})

	t.Run("Locator still performs lookups correctly", func(t *testing.T) {
		registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
		logger := zerolog.Nop()

		clientFactory := func(ctx context.Context) (*github.Client, error) {
			return github.NewClient(nil), nil
		}

		circuitBreaker := NewCircuitBreaker()
		locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

		installationID := int64(12345)
		registry.MarkInstalled(installationID)

		req := LookupRequest{
			InstallationID: installationID,
			EventSource:    EventSourceWebhook,
			EventType:      "test",
		}

		result := locator.Lookup(context.Background(), req)
		assert.True(t, result.Exists)
		assert.Equal(t, installationID, result.InstallationID)
	})

	t.Log("✅ Phase 8 Step 3: Backward compatibility maintained")
}
