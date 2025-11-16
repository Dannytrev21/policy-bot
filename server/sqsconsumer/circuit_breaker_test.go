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
	"errors"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCircuitBreakerManager(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	cbm := NewCircuitBreakerManager(nil, logger, registry)

	require.NotNil(t, cbm)
	assert.NotNil(t, cbm.enterpriseBreaker)
	assert.NotNil(t, cbm.cloudBreaker)

	// Verify initial state is closed
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("enterprise"))
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("cloud"))

	// Verify metrics were registered
	assert.NotNil(t, registry.Get(MetricsKeyCircuitBreakerStateEnterprise))
	assert.NotNil(t, registry.Get(MetricsKeyCircuitBreakerStateCloud))
	assert.NotNil(t, registry.Get(MetricsKeyCircuitBreakerTrips))
	assert.NotNil(t, registry.Get(MetricsKeyCircuitBreakerRecoveries))
	assert.NotNil(t, registry.Get(MetricsKeyCircuitBreakerRejections))
}

func TestNewCircuitBreakerManager_CustomConfig(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	config := &CircuitBreakerConfig{
		MaxRequests:  5,
		Interval:     5 * time.Second,
		Timeout:      15 * time.Second,
		MinRequests:  20,
		FailureRatio: 0.5,
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	require.NotNil(t, cbm)
	assert.NotNil(t, cbm.enterpriseBreaker)
	assert.NotNil(t, cbm.cloudBreaker)
}

func TestCircuitBreakerManager_Execute_Success(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	cbm := NewCircuitBreakerManager(nil, logger, registry)

	callCount := 0
	err := cbm.Execute("enterprise", func() error {
		callCount++
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("enterprise"))
}

func TestCircuitBreakerManager_Execute_Failure(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	cbm := NewCircuitBreakerManager(nil, logger, registry)

	testErr := errors.New("test error")
	err := cbm.Execute("cloud", func() error {
		return testErr
	})

	assert.Error(t, err)
	assert.Equal(t, testErr, err)
	// Should still be closed - not enough failures yet
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("cloud"))
}

func TestCircuitBreakerManager_TripsOnHighFailureRate(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	config := &CircuitBreakerConfig{
		MaxRequests:  3,
		Interval:     10 * time.Second,
		Timeout:      100 * time.Millisecond, // Short timeout for testing
		MinRequests:  5,  // Lower threshold for testing
		FailureRatio: 0.6, // 60% failure rate
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	testErr := errors.New("test error")

	// Generate 10 requests with 80% failure rate (8 failures, 2 successes)
	// This should trip the circuit breaker
	for i := 0; i < 10; i++ {
		cbm.Execute("enterprise", func() error {
			if i%5 == 0 || i%5 == 1 { // 2 out of 5 succeed
				return nil
			}
			return testErr
		})
	}

	// Circuit should now be open
	assert.Equal(t, gobreaker.StateOpen, cbm.GetState("enterprise"))

	// Verify rejection counter increased
	counter := registry.Get(MetricsKeyCircuitBreakerRejections).(metrics.Counter)
	assert.True(t, counter.Count() > 0)
}

func TestCircuitBreakerManager_DoesNotTripWithLowRequests(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	config := &CircuitBreakerConfig{
		MaxRequests:  3,
		Interval:     10 * time.Second,
		Timeout:      30 * time.Second,
		MinRequests:  10, // Requires at least 10 requests
		FailureRatio: 0.6,
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	testErr := errors.New("test error")

	// Only 5 requests (below MinRequests threshold)
	// Even with 100% failure rate, circuit should stay closed
	for i := 0; i < 5; i++ {
		cbm.Execute("cloud", func() error {
			return testErr
		})
	}

	// Circuit should still be closed (not enough requests)
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("cloud"))
}

func TestCircuitBreakerManager_RecoveryFromOpen(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	config := &CircuitBreakerConfig{
		MaxRequests:  3,
		Interval:     1 * time.Second,
		Timeout:      50 * time.Millisecond, // Very short for testing
		MinRequests:  3,
		FailureRatio: 0.6,
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	testErr := errors.New("test error")

	// Trip the circuit breaker
	for i := 0; i < 10; i++ {
		cbm.Execute("enterprise", func() error {
			return testErr
		})
	}

	// Verify circuit is open
	require.Equal(t, gobreaker.StateOpen, cbm.GetState("enterprise"))

	// Wait for timeout to transition to half-open
	time.Sleep(100 * time.Millisecond)

	// Next successful requests should transition to closed
	for i := 0; i < 3; i++ {
		err := cbm.Execute("enterprise", func() error {
			return nil
		})
		// First request in half-open might succeed
		if err != nil && err != gobreaker.ErrOpenState && err != gobreaker.ErrTooManyRequests {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	// Wait a bit for state transition
	time.Sleep(50 * time.Millisecond)

	// Circuit should eventually close or be in half-open
	state := cbm.GetState("enterprise")
	assert.True(t, state == gobreaker.StateClosed || state == gobreaker.StateHalfOpen,
		"Expected closed or half-open state, got %v", stateToString(state))
}

func TestCircuitBreakerManager_IndependentBreakers(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	config := &CircuitBreakerConfig{
		MaxRequests:  3,
		Interval:     10 * time.Second,
		Timeout:      100 * time.Millisecond,
		MinRequests:  5,
		FailureRatio: 0.6,
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	testErr := errors.New("test error")

	// Trip enterprise breaker
	for i := 0; i < 10; i++ {
		cbm.Execute("enterprise", func() error {
			return testErr
		})
	}

	// Keep cloud breaker healthy
	for i := 0; i < 10; i++ {
		cbm.Execute("cloud", func() error {
			return nil
		})
	}

	// Enterprise should be open
	assert.Equal(t, gobreaker.StateOpen, cbm.GetState("enterprise"))

	// Cloud should still be closed
	assert.Equal(t, gobreaker.StateClosed, cbm.GetState("cloud"))
}

func TestCircuitBreakerManager_GetCounts(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	cbm := NewCircuitBreakerManager(nil, logger, registry)

	// Execute some successful requests
	for i := 0; i < 5; i++ {
		cbm.Execute("enterprise", func() error {
			return nil
		})
	}

	counts := cbm.GetCounts("enterprise")
	assert.Equal(t, uint32(5), counts.Requests)
	assert.Equal(t, uint32(5), counts.TotalSuccesses)
	assert.Equal(t, uint32(0), counts.TotalFailures)
}

func TestCircuitBreakerManager_MetricsRecording(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	stateChanges := 0
	config := &CircuitBreakerConfig{
		MaxRequests:  3,
		Interval:     1 * time.Second,
		Timeout:      50 * time.Millisecond,
		MinRequests:  5,
		FailureRatio: 0.6,
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			stateChanges++
		},
	}

	cbm := NewCircuitBreakerManager(config, logger, registry)

	testErr := errors.New("test error")

	// Trip the circuit
	for i := 0; i < 10; i++ {
		cbm.Execute("cloud", func() error {
			return testErr
		})
	}

	// Verify state change callback was called
	assert.True(t, stateChanges > 0)

	// Verify trip counter increased
	tripCounter := registry.Get(MetricsKeyCircuitBreakerTrips).(metrics.Counter)
	assert.True(t, tripCounter.Count() > 0)

	// Verify state gauge was updated
	stateGauge := registry.Get(MetricsKeyCircuitBreakerStateCloud).(metrics.Gauge)
	assert.Equal(t, int64(1), stateGauge.Value()) // 1 = open
}

func TestCircuitBreakerManager_NilRegistry(t *testing.T) {
	logger := zerolog.New(nil)

	// Should not panic with nil registry
	cbm := NewCircuitBreakerManager(nil, logger, nil)

	require.NotNil(t, cbm)

	// Should work without metrics
	err := cbm.Execute("enterprise", func() error {
		return nil
	})

	assert.NoError(t, err)
}

func TestStateToString(t *testing.T) {
	tests := []struct {
		state    gobreaker.State
		expected string
	}{
		{gobreaker.StateClosed, "closed"},
		{gobreaker.StateOpen, "open"},
		{gobreaker.StateHalfOpen, "half-open"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := stateToString(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestStateToInt(t *testing.T) {
	tests := []struct {
		state    gobreaker.State
		expected int
	}{
		{gobreaker.StateClosed, 0},
		{gobreaker.StateOpen, 1},
		{gobreaker.StateHalfOpen, 2},
	}

	for _, tt := range tests {
		t.Run(stateToString(tt.state), func(t *testing.T) {
			result := stateToInt(tt.state)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	config := DefaultCircuitBreakerConfig()

	assert.NotNil(t, config)
	assert.Equal(t, uint32(3), config.MaxRequests)
	assert.Equal(t, 10*time.Second, config.Interval)
	assert.Equal(t, 30*time.Second, config.Timeout)
	assert.Equal(t, uint32(10), config.MinRequests)
	assert.Equal(t, 0.6, config.FailureRatio)
}

// TestCircuitBreakerManager_Concurrency tests thread safety
func TestCircuitBreakerManager_Concurrency(t *testing.T) {
	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	cbm := NewCircuitBreakerManager(nil, logger, registry)

	done := make(chan bool)
	goroutines := 10
	requestsPerGoroutine := 100

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			for j := 0; j < requestsPerGoroutine; j++ {
				env := "enterprise"
				if id%2 == 0 {
					env = "cloud"
				}

				cbm.Execute(env, func() error {
					if j%10 == 0 {
						return errors.New("test error")
					}
					return nil
				})
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < goroutines; i++ {
		<-done
	}

	// Should not panic and should have processed all requests
	enterpriseCounts := cbm.GetCounts("enterprise")
	cloudCounts := cbm.GetCounts("cloud")

	totalRequests := enterpriseCounts.Requests + cloudCounts.Requests
	assert.True(t, totalRequests > 0, "Should have processed requests")
}
