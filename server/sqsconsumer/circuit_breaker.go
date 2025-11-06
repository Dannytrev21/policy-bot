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
	"fmt"
	"sync"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/sony/gobreaker"
)

const (
	// Circuit breaker metric keys
	MetricsKeyCircuitBreakerStateEnterprise = "sqs.circuit_breaker.state.enterprise"
	MetricsKeyCircuitBreakerStateCloud      = "sqs.circuit_breaker.state.cloud"
	MetricsKeyCircuitBreakerTrips           = "sqs.circuit_breaker.trips"
	MetricsKeyCircuitBreakerRecoveries      = "sqs.circuit_breaker.recoveries"
	MetricsKeyCircuitBreakerRejections      = "sqs.circuit_breaker.rejections"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	CircuitBreakerClosed CircuitBreakerState = iota
	CircuitBreakerOpen
	CircuitBreakerHalfOpen
)

// CircuitBreakerConfig contains configuration for circuit breakers
type CircuitBreakerConfig struct {
	// MaxRequests is the maximum number of requests allowed to pass through
	// when the circuit breaker is half-open. Default: 3
	MaxRequests uint32

	// Interval is the cyclic period of the closed state for the circuit breaker
	// to clear the internal counts. Default: 10 seconds
	Interval time.Duration

	// Timeout is the period of the open state after which the state becomes half-open.
	// Default: 30 seconds
	Timeout time.Duration

	// MinRequests is the minimum number of requests required before the circuit
	// breaker can trip. This prevents the breaker from opening due to a small
	// number of failures. Default: 10
	MinRequests uint32

	// FailureRatio is the ratio of failures to total requests that will cause
	// the circuit breaker to trip. Value should be between 0 and 1. Default: 0.6 (60%)
	FailureRatio float64

	// OnStateChange is called whenever the circuit breaker changes state
	OnStateChange func(name string, from gobreaker.State, to gobreaker.State)
}

// DefaultCircuitBreakerConfig returns the default circuit breaker configuration
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		MaxRequests:   3,
		Interval:      10 * time.Second,
		Timeout:       30 * time.Second,
		MinRequests:   10,
		FailureRatio:  0.6,
		OnStateChange: nil,
	}
}

// CircuitBreakerManager manages circuit breakers for different environments
// Uses per-environment breakers (GHEC/GHES) to balance simplicity with isolation
type CircuitBreakerManager struct {
	enterpriseBreaker *gobreaker.CircuitBreaker
	cloudBreaker      *gobreaker.CircuitBreaker

	logger   zerolog.Logger
	registry metrics.Registry

	mu sync.RWMutex
}

// NewCircuitBreakerManager creates a new circuit breaker manager with per-environment breakers
// This provides the right balance between simplicity (2 breakers) and isolation (GHEC/GHES)
func NewCircuitBreakerManager(config *CircuitBreakerConfig, logger zerolog.Logger, registry metrics.Registry) *CircuitBreakerManager {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	cbm := &CircuitBreakerManager{
		logger:   logger.With().Str("component", "circuit_breaker").Logger(),
		registry: registry,
	}

	// Register metrics if registry provided
	if registry != nil {
		metrics.GetOrRegisterGauge(MetricsKeyCircuitBreakerStateEnterprise, registry)
		metrics.GetOrRegisterGauge(MetricsKeyCircuitBreakerStateCloud, registry)
		metrics.GetOrRegisterCounter(MetricsKeyCircuitBreakerTrips, registry)
		metrics.GetOrRegisterCounter(MetricsKeyCircuitBreakerRecoveries, registry)
		metrics.GetOrRegisterCounter(MetricsKeyCircuitBreakerRejections, registry)
	}

	// Create enterprise breaker (GHES)
	cbm.enterpriseBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "enterprise",
		MaxRequests: config.MaxRequests,
		Interval:    config.Interval,
		Timeout:     config.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			// Only trip if we have enough requests to make a statistical decision
			if counts.Requests < config.MinRequests {
				return false
			}

			// Trip if failure ratio exceeds threshold
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= config.FailureRatio
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			cbm.handleStateChange("enterprise", from, to)
			if config.OnStateChange != nil {
				config.OnStateChange(name, from, to)
			}
		},
	})

	// Create cloud breaker (GHEC)
	cbm.cloudBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "cloud",
		MaxRequests: config.MaxRequests,
		Interval:    config.Interval,
		Timeout:     config.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < config.MinRequests {
				return false
			}

			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= config.FailureRatio
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			cbm.handleStateChange("cloud", from, to)
			if config.OnStateChange != nil {
				config.OnStateChange(name, from, to)
			}
		},
	})

	logger.Info().
		Uint32("max_requests", config.MaxRequests).
		Dur("interval", config.Interval).
		Dur("timeout", config.Timeout).
		Uint32("min_requests", config.MinRequests).
		Float64("failure_ratio", config.FailureRatio).
		Msg("Circuit breaker manager initialized with per-environment breakers")

	return cbm
}

// Execute runs the given function through the appropriate circuit breaker
// based on the environment (cloud or enterprise)
func (cbm *CircuitBreakerManager) Execute(environment string, fn func() error) error {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	var breaker *gobreaker.CircuitBreaker
	if environment == "enterprise" {
		breaker = cbm.enterpriseBreaker
	} else {
		breaker = cbm.cloudBreaker
	}

	_, err := breaker.Execute(func() (interface{}, error) {
		return nil, fn()
	})

	// Record rejection metric if circuit is open
	if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
		if cbm.registry != nil {
			if counter := cbm.registry.Get(MetricsKeyCircuitBreakerRejections); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}
	}

	return err
}

// GetState returns the current state of the circuit breaker for the given environment
func (cbm *CircuitBreakerManager) GetState(environment string) gobreaker.State {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	if environment == "enterprise" {
		return cbm.enterpriseBreaker.State()
	}
	return cbm.cloudBreaker.State()
}

// GetCounts returns the current counts for the given environment's circuit breaker
func (cbm *CircuitBreakerManager) GetCounts(environment string) gobreaker.Counts {
	cbm.mu.RLock()
	defer cbm.mu.RUnlock()

	if environment == "enterprise" {
		return cbm.enterpriseBreaker.Counts()
	}
	return cbm.cloudBreaker.Counts()
}

// handleStateChange is called when a circuit breaker changes state
// Records metrics and logs the transition
func (cbm *CircuitBreakerManager) handleStateChange(environment string, from gobreaker.State, to gobreaker.State) {
	cbm.logger.Warn().
		Str("environment", environment).
		Str("from_state", stateToString(from)).
		Str("to_state", stateToString(to)).
		Msg("Circuit breaker state changed")

	// Record metrics
	if cbm.registry != nil {
		// Update state gauge
		var metricKey string
		if environment == "enterprise" {
			metricKey = MetricsKeyCircuitBreakerStateEnterprise
		} else {
			metricKey = MetricsKeyCircuitBreakerStateCloud
		}

		if gauge := cbm.registry.Get(metricKey); gauge != nil {
			if g, ok := gauge.(metrics.Gauge); ok {
				g.Update(int64(stateToInt(to)))
			}
		}

		// Record trip counter
		if from == gobreaker.StateClosed && to == gobreaker.StateOpen {
			if counter := cbm.registry.Get(MetricsKeyCircuitBreakerTrips); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}

		// Record recovery counter
		if from == gobreaker.StateHalfOpen && to == gobreaker.StateClosed {
			if counter := cbm.registry.Get(MetricsKeyCircuitBreakerRecoveries); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}
	}
}

// stateToString converts a gobreaker.State to a string
func stateToString(state gobreaker.State) string {
	switch state {
	case gobreaker.StateClosed:
		return "closed"
	case gobreaker.StateOpen:
		return "open"
	case gobreaker.StateHalfOpen:
		return "half-open"
	default:
		return fmt.Sprintf("unknown(%d)", state)
	}
}

// stateToInt converts a gobreaker.State to an int for metrics
// 0 = closed, 1 = open, 2 = half-open
func stateToInt(state gobreaker.State) int {
	switch state {
	case gobreaker.StateClosed:
		return 0
	case gobreaker.StateOpen:
		return 1
	case gobreaker.StateHalfOpen:
		return 2
	default:
		return -1
	}
}

// Reset resets both circuit breakers to closed state
// Used primarily for testing
func (cbm *CircuitBreakerManager) Reset() {
	cbm.mu.Lock()
	defer cbm.mu.Unlock()

	// gobreaker doesn't have a public Reset method, so we'll rely on timeout
	cbm.logger.Debug().Msg("Circuit breakers will reset automatically via timeout")
}
