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
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
)

const (
	// Retry configuration for GitHub API client creation
	maxRetryAttempts = 3               // Maximum number of retry attempts (including initial)
	baseRetryDelay   = 1 * time.Second // Initial backoff delay
	maxRetryDelay    = 8 * time.Second // Maximum backoff delay
	retryDelayJitter = 0.2             // Jitter factor (20%)
)

const (
	// Circuit breaker configuration
	circuitBreakerThreshold   = 5                // Consecutive failures before opening circuit
	circuitBreakerTimeout     = 60 * time.Second // Time to wait before attempting half-open
	circuitBreakerHalfOpenMax = 1                // Max requests in half-open state before closing
)

const (
	// Metric keys for retry operations
	MetricsKeyInstallationClientRetrySuccess     = "installation.client.retry_success"
	MetricsKeyInstallationClientRetryExhausted   = "installation.client.retry_exhausted"
	MetricsKeyInstallationV4ClientRetrySuccess   = "installation.v4client.retry_success"
	MetricsKeyInstallationV4ClientRetryExhausted = "installation.v4client.retry_exhausted"
	// Circuit breaker metrics
	MetricsKeyCircuitBreakerOpened = "installation.circuit_breaker.opened_total"
	MetricsKeyCircuitBreakerClosed = "installation.circuit_breaker.closed_total"
	MetricsKeyCircuitBreakerState  = "installation.circuit_breaker.state" // 0=closed, 1=open, 2=half-open
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	CircuitBreakerClosed   CircuitBreakerState = iota // Normal operation
	CircuitBreakerOpen                                // Blocking requests
	CircuitBreakerHalfOpen                            // Testing recovery
)

// String returns a string representation of the circuit breaker state
func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker implements a simple circuit breaker pattern to prevent cascading failures.
// It tracks consecutive failures and opens the circuit after a threshold is reached,
// preventing further requests until a timeout period has elapsed.
type CircuitBreaker struct {
	mu                  sync.RWMutex
	state               CircuitBreakerState
	consecutiveFailures int
	lastFailureTime     time.Time
	halfOpenSuccesses   int
}

// NewCircuitBreaker creates a new circuit breaker in closed state
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		state: CircuitBreakerClosed,
	}
}

// Allow checks if a request should be allowed through the circuit breaker
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return true

	case CircuitBreakerOpen:
		// Check if timeout has elapsed, transition to half-open
		if time.Since(cb.lastFailureTime) > circuitBreakerTimeout {
			cb.state = CircuitBreakerHalfOpen
			cb.halfOpenSuccesses = 0
			return true
		}
		return false

	case CircuitBreakerHalfOpen:
		// Allow limited requests in half-open state
		return true

	default:
		return false
	}
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() CircuitBreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousState := cb.state

	switch cb.state {
	case CircuitBreakerClosed:
		// Reset failure counter on success
		cb.consecutiveFailures = 0

	case CircuitBreakerHalfOpen:
		// Increment success counter
		cb.halfOpenSuccesses++
		if cb.halfOpenSuccesses >= circuitBreakerHalfOpenMax {
			// Enough successful requests, close circuit
			cb.state = CircuitBreakerClosed
			cb.consecutiveFailures = 0
			cb.halfOpenSuccesses = 0
		}
	}

	return previousState
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() CircuitBreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousState := cb.state
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitBreakerClosed:
		cb.consecutiveFailures++
		if cb.consecutiveFailures >= circuitBreakerThreshold {
			// Open circuit
			cb.state = CircuitBreakerOpen
		}

	case CircuitBreakerHalfOpen:
		// Failure in half-open state, reopen circuit
		cb.state = CircuitBreakerOpen
		cb.consecutiveFailures = circuitBreakerThreshold
		cb.halfOpenSuccesses = 0
	}

	return previousState
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// InstallationManager is a centralized component for managing GitHub installation clients.
// It encapsulates client creation, verification, metrics recording, error handling, retry logic,
// and circuit breaker pattern to prevent cascading failures.
type InstallationManager struct {
	clientCreator        githubapp.ClientCreator
	installationRegistry *InstallationRegistry
	metricsRegistry      gometrics.Registry
	circuitBreaker       *CircuitBreaker
}

// InstallationClients contains both v3 (REST) and v4 (GraphQL) GitHub API clients
// for a specific installation.
type InstallationClients struct {
	V3Client *github.Client
	V4Client *githubv4.Client
}

// NewInstallationManager creates a new InstallationManager with the provided dependencies.
func NewInstallationManager(
	clientCreator githubapp.ClientCreator,
	installationRegistry *InstallationRegistry,
	metricsRegistry gometrics.Registry,
) *InstallationManager {
	return &InstallationManager{
		clientCreator:        clientCreator,
		installationRegistry: installationRegistry,
		metricsRegistry:      metricsRegistry,
		circuitBreaker:       NewCircuitBreaker(),
	}
}

// GetClients creates and returns both v3 and v4 GitHub API clients for the specified installation.
// It performs the following steps:
// 1. Checks circuit breaker - fails fast if circuit is open
// 2. Verifies the installation exists and is accessible
// 3. Creates the v3 REST API client with error handling, retry logic, and metrics
// 4. Creates the v4 GraphQL API client with error handling, retry logic, and metrics
// 5. Records success/failure with circuit breaker
// 6. Returns both clients or an error if any step fails
//
// The circuit breaker prevents cascading failures by blocking requests when GitHub API
// is consistently unavailable, implementing a fail-fast strategy.
func (m *InstallationManager) GetClients(ctx context.Context, installationID int64, repoFullName string) (*InstallationClients, error) {
	logger := zerolog.Ctx(ctx)

	// Step 0: Check circuit breaker - fail fast if circuit is open
	if !m.circuitBreaker.Allow() {
		cbState := m.circuitBreaker.GetState()
		logger.Warn().
			Int64("installation_id", installationID).
			Str("repository", repoFullName).
			Str("circuit_breaker_state", cbState.String()).
			Msg("Circuit breaker is open, rejecting request")
		return nil, fmt.Errorf("circuit breaker is open (state: %s), GitHub API may be unavailable", cbState.String())
	}

	// Step 1: Verify installation exists before attempting to create clients
	if !m.verifyInstallation(ctx, installationID, repoFullName) {
		return nil, fmt.Errorf("installation %d not found or not accessible - app may not be installed on repository %s", installationID, repoFullName)
	}

	// Step 2: Create v3 REST API client
	v3Client, err := m.createV3Client(ctx, installationID, repoFullName)
	if err != nil {
		logger.Error().
			Err(err).
			Int64("installation_id", installationID).
			Str("repository", repoFullName).
			Str("error_type", "installation_client_creation").
			Msg("Failed to create GitHub installation client (REST API v3)")

		m.recordMetric(MetricsKeyInstallationClientFailure)

		// Record failure with circuit breaker (only for retryable errors that might indicate service issues)
		if isRetryableError(err) {
			previousState := m.circuitBreaker.RecordFailure()
			if previousState == CircuitBreakerClosed && m.circuitBreaker.GetState() == CircuitBreakerOpen {
				// Circuit just opened
				logger.Error().
					Int64("installation_id", installationID).
					Str("repository", repoFullName).
					Int("threshold", circuitBreakerThreshold).
					Msg("Circuit breaker opened due to consecutive failures")
				m.recordMetric(MetricsKeyCircuitBreakerOpened)
				m.recordCircuitBreakerState()
			}
		}

		return nil, errors.Wrapf(err, "failed to create installation client for %s (installation %d)", repoFullName, installationID)
	}

	m.recordMetric(MetricsKeyInstallationClientSuccess)

	// Step 3: Create v4 GraphQL API client
	v4Client, err := m.createV4Client(ctx, installationID, repoFullName)
	if err != nil {
		logger.Error().
			Err(err).
			Int64("installation_id", installationID).
			Str("repository", repoFullName).
			Str("error_type", "installation_v4client_creation").
			Msg("Failed to create GitHub installation client (GraphQL API v4)")

		m.recordMetric(MetricsKeyInstallationV4ClientFailure)

		// Record failure with circuit breaker (only for retryable errors that might indicate service issues)
		if isRetryableError(err) {
			previousState := m.circuitBreaker.RecordFailure()
			if previousState == CircuitBreakerClosed && m.circuitBreaker.GetState() == CircuitBreakerOpen {
				// Circuit just opened
				logger.Error().
					Int64("installation_id", installationID).
					Str("repository", repoFullName).
					Int("threshold", circuitBreakerThreshold).
					Msg("Circuit breaker opened due to consecutive failures")
				m.recordMetric(MetricsKeyCircuitBreakerOpened)
				m.recordCircuitBreakerState()
			}
		}

		return nil, errors.Wrapf(err, "failed to create installation v4 client for %s (installation %d)", repoFullName, installationID)
	}

	m.recordMetric(MetricsKeyInstallationV4ClientSuccess)

	// Step 4: Record success with circuit breaker
	previousState := m.circuitBreaker.RecordSuccess()
	if previousState == CircuitBreakerHalfOpen && m.circuitBreaker.GetState() == CircuitBreakerClosed {
		// Circuit closed after being half-open
		logger.Info().
			Int64("installation_id", installationID).
			Str("repository", repoFullName).
			Msg("Circuit breaker closed after successful recovery")
		m.recordMetric(MetricsKeyCircuitBreakerClosed)
		m.recordCircuitBreakerState()
	}

	return &InstallationClients{
		V3Client: v3Client,
		V4Client: v4Client,
	}, nil
}

// verifyInstallation checks if the GitHub App is installed for the given installation ID.
// It delegates to the InstallationRegistry which uses TTL-based caching to minimize API calls.
func (m *InstallationManager) verifyInstallation(ctx context.Context, installationID int64, repoFullName string) bool {
	logger := zerolog.Ctx(ctx)

	// Delegate to registry for verification (uses cache)
	status, cacheHit := m.installationRegistry.Check(installationID)
	if cacheHit {
		switch status {
		case InstallationExists:
			logger.Debug().
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Msg("Installation verified via cache (positive)")
			return true
		case InstallationNotFound:
			logger.Debug().
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Msg("Installation not found via cache (negative)")
			return false
		}
	}

	// Cache miss - this shouldn't happen if Base.VerifyInstallation was called first,
	// but we handle it gracefully
	logger.Warn().
		Int64("installation_id", installationID).
		Str("repository", repoFullName).
		Msg("Installation verification cache miss in manager - this should have been verified earlier")
	return false
}

// createV3Client creates a GitHub REST API v3 client for the specified installation.
// It implements retry logic with exponential backoff for transient failures.
func (m *InstallationManager) createV3Client(ctx context.Context, installationID int64, repoFullName string) (*github.Client, error) {
	logger := zerolog.Ctx(ctx)
	var lastErr error

	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		// Attempt to create client
		client, err := m.clientCreator.NewInstallationClient(installationID)
		if err == nil {
			// Success
			if attempt > 0 {
				// Record successful retry
				logger.Info().
					Int64("installation_id", installationID).
					Str("repository", repoFullName).
					Int("attempt", attempt+1).
					Msg("Successfully created v3 client after retry")
				m.recordMetric(MetricsKeyInstallationClientRetrySuccess)
			}
			return client, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Non-retryable error (e.g., 404, 401, 403)
			logger.Debug().
				Err(err).
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Msg("Non-retryable error creating v3 client, not retrying")
			return nil, err
		}

		// Calculate backoff for next attempt
		if attempt < maxRetryAttempts-1 {
			backoff := calculateBackoff(attempt)
			logger.Warn().
				Err(err).
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Int("attempt", attempt+1).
				Int("max_attempts", maxRetryAttempts).
				Dur("backoff", backoff).
				Msg("Retryable error creating v3 client, will retry after backoff")

			// Sleep with backoff
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}
	}

	// All retries exhausted
	logger.Error().
		Err(lastErr).
		Int64("installation_id", installationID).
		Str("repository", repoFullName).
		Int("max_attempts", maxRetryAttempts).
		Msg("Failed to create v3 client after all retry attempts")
	m.recordMetric(MetricsKeyInstallationClientRetryExhausted)

	return nil, errors.Wrapf(lastErr, "failed to create v3 client after %d attempts", maxRetryAttempts)
}

// createV4Client creates a GitHub GraphQL API v4 client for the specified installation.
// It implements retry logic with exponential backoff for transient failures.
func (m *InstallationManager) createV4Client(ctx context.Context, installationID int64, repoFullName string) (*githubv4.Client, error) {
	logger := zerolog.Ctx(ctx)
	var lastErr error

	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		// Attempt to create client
		client, err := m.clientCreator.NewInstallationV4Client(installationID)
		if err == nil {
			// Success
			if attempt > 0 {
				// Record successful retry
				logger.Info().
					Int64("installation_id", installationID).
					Str("repository", repoFullName).
					Int("attempt", attempt+1).
					Msg("Successfully created v4 client after retry")
				m.recordMetric(MetricsKeyInstallationV4ClientRetrySuccess)
			}
			return client, nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			// Non-retryable error (e.g., 404, 401, 403)
			logger.Debug().
				Err(err).
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Msg("Non-retryable error creating v4 client, not retrying")
			return nil, err
		}

		// Calculate backoff for next attempt
		if attempt < maxRetryAttempts-1 {
			backoff := calculateBackoff(attempt)
			logger.Warn().
				Err(err).
				Int64("installation_id", installationID).
				Str("repository", repoFullName).
				Int("attempt", attempt+1).
				Int("max_attempts", maxRetryAttempts).
				Dur("backoff", backoff).
				Msg("Retryable error creating v4 client, will retry after backoff")

			// Sleep with backoff
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}
	}

	// All retries exhausted
	logger.Error().
		Err(lastErr).
		Int64("installation_id", installationID).
		Str("repository", repoFullName).
		Int("max_attempts", maxRetryAttempts).
		Msg("Failed to create v4 client after all retry attempts")
	m.recordMetric(MetricsKeyInstallationV4ClientRetryExhausted)

	return nil, errors.Wrapf(lastErr, "failed to create v4 client after %d attempts", maxRetryAttempts)
}

// recordMetric records a metric for installation client operations.
// This method safely handles cases where the metrics registry is nil.
func (m *InstallationManager) recordMetric(metricKey string) {
	if m.metricsRegistry == nil {
		return
	}

	counter := m.metricsRegistry.Get(metricKey)
	if counter != nil {
		if c, ok := counter.(interface{ Inc(int64) }); ok {
			c.Inc(1)
		}
	} else {
		// Register counter if it doesn't exist
		gometrics.GetOrRegisterCounter(metricKey, m.metricsRegistry).Inc(1)
	}
}

// recordCircuitBreakerState records the current circuit breaker state as a gauge metric.
// This allows monitoring of the circuit breaker state via OTEL/New Relic.
// State values: 0=closed, 1=open, 2=half-open
func (m *InstallationManager) recordCircuitBreakerState() {
	if m.metricsRegistry == nil {
		return
	}

	state := m.circuitBreaker.GetState()
	stateValue := int64(state)

	gauge := m.metricsRegistry.Get(MetricsKeyCircuitBreakerState)
	if gauge != nil {
		if g, ok := gauge.(interface{ Update(int64) }); ok {
			g.Update(stateValue)
		}
	} else {
		// Register gauge if it doesn't exist
		g := gometrics.GetOrRegisterGauge(MetricsKeyCircuitBreakerState, m.metricsRegistry)
		g.Update(stateValue)
	}
}

// isRetryableError is a local wrapper that calls the shared IsRetryableError function.
// This maintains backward compatibility and keeps the existing code working.
func isRetryableError(err error) bool {
	return IsRetryableError(err)
}

// calculateBackoff calculates the exponential backoff delay with jitter.
// It uses the formula: delay = baseDelay * 2^attempt * (1 ± jitter)
// The jitter helps prevent thundering herd when multiple requests retry simultaneously.
func calculateBackoff(attempt int) time.Duration {
	// Calculate exponential backoff
	delay := baseRetryDelay * time.Duration(1<<uint(attempt))

	// Cap at maximum delay
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	// Add jitter: random value between (1-jitter) and (1+jitter)
	jitter := 1.0 + (rand.Float64()*2-1)*retryDelayJitter
	delay = time.Duration(float64(delay) * jitter)

	return delay
}
