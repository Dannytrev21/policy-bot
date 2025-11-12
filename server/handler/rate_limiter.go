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
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

const (
	// GitHub Enterprise Cloud allows 15,000 requests per hour per installation
	// = 4.16 requests/second
	// We use conservative limit: 3 req/sec with burst capacity of 10
	DefaultInstallationRateLimit = 3.0 // requests per second
	DefaultInstallationBurst     = 10  // burst capacity

	// Global safety limit to prevent overwhelming GitHub API
	// Even with many installations, total rate should be reasonable
	DefaultGlobalRateLimit = 100.0 // requests per second
	DefaultGlobalBurst     = 50    // burst capacity

	// Adaptive rate limiting defaults (Phase 2)
	DefaultAdaptiveSafetyFactor  = 0.8              // Use 80% of calculated safe rate
	DefaultAdaptiveMinRate       = 1.0              // Never go below 1 req/sec
	DefaultAdaptiveMaxRate       = 4.0              // Never exceed 4 req/sec
	DefaultAdaptiveSmoothingFactor = 0.3            // Balanced EMA smoothing
	DefaultAdaptiveUpdateInterval  = 10 * time.Second // Adjust every 10s

	// Metrics keys for rate limiting
	MetricsKeyRateLimitWaitTime         = "handler.rate_limit.wait_time"
	MetricsKeyRateLimitThrottled        = "handler.rate_limit.throttled"
	MetricsKeyRateLimitQuotaUsed        = "handler.rate_limit.quota_used"
	MetricsKeyRateLimitInstallations    = "handler.rate_limit.installations"
	MetricsKeyRateLimitAdaptiveAdjustments = "handler.rate_limit.adaptive.adjustments"
	MetricsKeyRateLimitGitHubRemaining  = "handler.rate_limit.github_remaining"
	MetricsKeyRateLimitAdaptiveRate     = "handler.rate_limit.adaptive.current_rate"
)

// RateLimitConfig configures rate limiting behavior
type RateLimitConfig struct {
	// InstallationRate is the rate limit per installation (requests/second)
	// Default: 3.0 req/sec (conservative for 15k/hour GitHub limit)
	InstallationRate float64

	// InstallationBurst is the burst capacity per installation
	// Default: 10
	InstallationBurst int

	// GlobalRate is the global rate limit across all installations (requests/second)
	// Default: 100.0 req/sec
	GlobalRate float64

	// GlobalBurst is the global burst capacity
	// Default: 50
	GlobalBurst int

	// Enabled controls whether rate limiting is active
	// Default: true
	Enabled bool

	// Adaptive controls adaptive rate limiting based on GitHub headers (Phase 2)
	Adaptive AdaptiveRateLimitConfig
}

// AdaptiveRateLimitConfig configures adaptive rate limiting
type AdaptiveRateLimitConfig struct {
	// Enabled controls whether adaptive rate limiting is active
	// Default: false (feature flag for Phase 2 rollout)
	Enabled bool

	// SafetyFactor for rate calculation (0.0-1.0)
	// Default: 0.8
	SafetyFactor float64

	// MinRate is the minimum allowed rate (requests/second)
	// Default: 1.0
	MinRate float64

	// MaxRate is the maximum allowed rate (requests/second)
	// Default: 4.0
	MaxRate float64

	// SmoothingFactor for exponential moving average (0.0-1.0)
	// Default: 0.3
	SmoothingFactor float64

	// UpdateInterval for rate adjustments
	// Default: 10s
	UpdateInterval time.Duration
}

// adaptiveRateState tracks adaptive rate limiting state per installation
type adaptiveRateState struct {
	mu sync.RWMutex

	// Current adaptive rate (EMA smoothed)
	currentRate float64

	// Last observed GitHub rate limit data
	lastRemaining int
	lastLimit     int
	lastReset     time.Time

	// Last update time for periodic adjustments
	lastUpdate time.Time
}

// DefaultRateLimitConfig returns the default rate limiting configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		InstallationRate:  DefaultInstallationRateLimit,
		InstallationBurst: DefaultInstallationBurst,
		GlobalRate:        DefaultGlobalRateLimit,
		GlobalBurst:       DefaultGlobalBurst,
		Enabled:           true,
		Adaptive: AdaptiveRateLimitConfig{
			Enabled:         false, // Disabled by default (feature flag)
			SafetyFactor:    DefaultAdaptiveSafetyFactor,
			MinRate:         DefaultAdaptiveMinRate,
			MaxRate:         DefaultAdaptiveMaxRate,
			SmoothingFactor: DefaultAdaptiveSmoothingFactor,
			UpdateInterval:  DefaultAdaptiveUpdateInterval,
		},
	}
}

// RateLimitedClientCreator wraps a githubapp.ClientCreator with rate limiting
// to prevent exceeding GitHub API rate limits proactively.
//
// It maintains per-installation rate limiters and a global rate limiter for safety.
// This prevents 429 (Too Many Requests) errors before they occur.
//
// Phase 2 adds adaptive rate limiting based on GitHub API response headers.
type RateLimitedClientCreator struct {
	base githubapp.ClientCreator

	config *RateLimitConfig

	// Per-installation rate limiters
	// Key: installation ID
	installationLimiters sync.Map // map[int64]*rate.Limiter

	// Adaptive rate states (Phase 2)
	// Key: installation ID
	adaptiveStates sync.Map // map[int64]*adaptiveRateState

	// Global rate limiter for safety
	globalLimiter *rate.Limiter

	logger   zerolog.Logger
	registry metrics.Registry

	mu sync.RWMutex

	// Context for background goroutine management
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRateLimitedClientCreator creates a new rate-limited client creator
func NewRateLimitedClientCreator(
	base githubapp.ClientCreator,
	config *RateLimitConfig,
	logger zerolog.Logger,
	registry metrics.Registry,
) *RateLimitedClientCreator {
	if config == nil {
		config = DefaultRateLimitConfig()
	}

	// Create context for background goroutine management
	ctx, cancel := context.WithCancel(context.Background())

	// Create global rate limiter
	globalLimiter := rate.NewLimiter(
		rate.Limit(config.GlobalRate),
		config.GlobalBurst,
	)

	// Register metrics
	if registry != nil {
		metrics.GetOrRegisterTimer(MetricsKeyRateLimitWaitTime, registry)
		metrics.GetOrRegisterCounter(MetricsKeyRateLimitThrottled, registry)
		metrics.GetOrRegisterGauge(MetricsKeyRateLimitQuotaUsed, registry)
		metrics.GetOrRegisterGauge(MetricsKeyRateLimitInstallations, registry)

		// Adaptive metrics (Phase 2)
		if config.Adaptive.Enabled {
			metrics.GetOrRegisterCounter(MetricsKeyRateLimitAdaptiveAdjustments, registry)
			metrics.GetOrRegisterGauge(MetricsKeyRateLimitGitHubRemaining, registry)
			metrics.GetOrRegisterGauge(MetricsKeyRateLimitAdaptiveRate, registry)
		}
	}

	rlcc := &RateLimitedClientCreator{
		base:          base,
		config:        config,
		globalLimiter: globalLimiter,
		logger:        logger.With().Str("component", "rate_limiter").Logger(),
		registry:      registry,
		ctx:           ctx,
		cancel:        cancel,
	}

	logger.Info().
		Float64("installation_rate", config.InstallationRate).
		Int("installation_burst", config.InstallationBurst).
		Float64("global_rate", config.GlobalRate).
		Int("global_burst", config.GlobalBurst).
		Bool("enabled", config.Enabled).
		Bool("adaptive_enabled", config.Adaptive.Enabled).
		Msg("Rate-limited client creator initialized")

	// Start adaptive rate adjustment goroutine if enabled (Phase 2)
	if config.Adaptive.Enabled {
		go rlcc.adaptiveRateAdjustmentLoop()
		logger.Info().Msg("Adaptive rate limiting enabled")
	}

	return rlcc
}

// Close cleans up resources (Phase 2)
func (r *RateLimitedClientCreator) Close() {
	if r.cancel != nil {
		r.cancel()
	}
}

// NewInstallationClient creates a new installation client with rate limiting
func (r *RateLimitedClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	ctx := context.Background()

	// If rate limiting is disabled, bypass and use base creator directly
	if !r.config.Enabled {
		return r.base.NewInstallationClient(installationID)
	}

	// Wait for rate limit tokens
	startWait := time.Now()
	if err := r.waitForRateLimit(ctx, installationID); err != nil {
		return nil, err
	}
	waitDuration := time.Since(startWait)

	// Record wait time metric
	if r.registry != nil {
		if timer := r.registry.Get(MetricsKeyRateLimitWaitTime); timer != nil {
			if t, ok := timer.(metrics.Timer); ok {
				t.Update(waitDuration)
			}
		}

		// If we had to wait, record throttling event
		if waitDuration > time.Millisecond {
			if counter := r.registry.Get(MetricsKeyRateLimitThrottled); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}
	}

	// Log significant waits
	if waitDuration > 100*time.Millisecond {
		r.logger.Debug().
			Int64("installation_id", installationID).
			Dur("wait_duration", waitDuration).
			Msg("Rate limit caused significant wait")
	}

	// Create the actual client
	return r.base.NewInstallationClient(installationID)
}

// NewAppClient creates a new app-level client
// App-level clients are not rate limited as they're used for metadata operations
func (r *RateLimitedClientCreator) NewAppClient() (*github.Client, error) {
	return r.base.NewAppClient()
}

// NewAppV4Client creates a new app-level v4 client
// App-level clients are not rate limited as they're used for metadata operations
func (r *RateLimitedClientCreator) NewAppV4Client() (*githubv4.Client, error) {
	return r.base.NewAppV4Client()
}

// NewInstallationV4Client creates a new installation v4 client with rate limiting
func (r *RateLimitedClientCreator) NewInstallationV4Client(installationID int64) (*githubv4.Client, error) {
	ctx := context.Background()

	// If rate limiting is disabled, bypass and use base creator directly
	if !r.config.Enabled {
		return r.base.NewInstallationV4Client(installationID)
	}

	// Wait for rate limit tokens
	startWait := time.Now()
	if err := r.waitForRateLimit(ctx, installationID); err != nil {
		return nil, err
	}
	waitDuration := time.Since(startWait)

	// Record wait time metric
	if r.registry != nil {
		if timer := r.registry.Get(MetricsKeyRateLimitWaitTime); timer != nil {
			if t, ok := timer.(metrics.Timer); ok {
				t.Update(waitDuration)
			}
		}

		// If we had to wait, record throttling event
		if waitDuration > time.Millisecond {
			if counter := r.registry.Get(MetricsKeyRateLimitThrottled); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}
	}

	// Log significant waits
	if waitDuration > 100*time.Millisecond {
		r.logger.Debug().
			Int64("installation_id", installationID).
			Dur("wait_duration", waitDuration).
			Msg("Rate limit caused significant wait")
	}

	// Create the actual client
	return r.base.NewInstallationV4Client(installationID)
}

// waitForRateLimit waits for both global and per-installation rate limit tokens
func (r *RateLimitedClientCreator) waitForRateLimit(ctx context.Context, installationID int64) error {
	// Wait for global rate limit first (prevents overwhelming GitHub API)
	if err := r.globalLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("global rate limit wait failed: %w", err)
	}

	// Then wait for per-installation rate limit
	limiter := r.getOrCreateInstallationLimiter(installationID)
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("installation rate limit wait failed: %w", err)
	}

	return nil
}

// getOrCreateInstallationLimiter gets or creates a rate limiter for an installation
func (r *RateLimitedClientCreator) getOrCreateInstallationLimiter(installationID int64) *rate.Limiter {
	// Try to load existing limiter
	if limiter, ok := r.installationLimiters.Load(installationID); ok {
		return limiter.(*rate.Limiter)
	}

	// Create new limiter for this installation
	newLimiter := rate.NewLimiter(
		rate.Limit(r.config.InstallationRate),
		r.config.InstallationBurst,
	)

	// Try to store it (LoadOrStore handles race conditions)
	actual, loaded := r.installationLimiters.LoadOrStore(installationID, newLimiter)

	// Update metrics if this is a new installation
	if !loaded && r.registry != nil {
		r.updateInstallationCountMetric()
	}

	return actual.(*rate.Limiter)
}

// updateInstallationCountMetric updates the count of tracked installations
func (r *RateLimitedClientCreator) updateInstallationCountMetric() {
	count := 0
	r.installationLimiters.Range(func(key, value interface{}) bool {
		count++
		return true
	})

	if r.registry != nil {
		if gauge := r.registry.Get(MetricsKeyRateLimitInstallations); gauge != nil {
			if g, ok := gauge.(metrics.Gauge); ok {
				g.Update(int64(count))
			}
		}
	}
}

// GetInstallationStats returns rate limit statistics for an installation
func (r *RateLimitedClientCreator) GetInstallationStats(installationID int64) *RateLimitStats {
	limiter, ok := r.installationLimiters.Load(installationID)
	if !ok {
		return nil
	}

	l := limiter.(*rate.Limiter)
	return &RateLimitStats{
		InstallationID: installationID,
		Limit:          l.Limit(),
		Burst:          l.Burst(),
		TokensAvailable: l.Tokens(),
	}
}

// GetGlobalStats returns global rate limit statistics
func (r *RateLimitedClientCreator) GetGlobalStats() *RateLimitStats {
	return &RateLimitStats{
		InstallationID:  0, // 0 indicates global
		Limit:           r.globalLimiter.Limit(),
		Burst:           r.globalLimiter.Burst(),
		TokensAvailable: r.globalLimiter.Tokens(),
	}
}

// RateLimitStats contains rate limit statistics
type RateLimitStats struct {
	InstallationID  int64
	Limit           rate.Limit
	Burst           int
	TokensAvailable float64
}

// NewTokenClient creates a new client from a token (no rate limiting)
func (r *RateLimitedClientCreator) NewTokenClient(token string) (*github.Client, error) {
	return r.base.NewTokenClient(token)
}

// NewTokenV4Client creates a new v4 client from a token (no rate limiting)
func (r *RateLimitedClientCreator) NewTokenV4Client(token string) (*githubv4.Client, error) {
	return r.base.NewTokenV4Client(token)
}

// NewTokenSourceClient creates a new client from a token source (no rate limiting)
func (r *RateLimitedClientCreator) NewTokenSourceClient(tokenSource oauth2.TokenSource) (*github.Client, error) {
	return r.base.NewTokenSourceClient(tokenSource)
}

// NewTokenSourceV4Client creates a new v4 client from a token source (no rate limiting)
func (r *RateLimitedClientCreator) NewTokenSourceV4Client(tokenSource oauth2.TokenSource) (*githubv4.Client, error) {
	return r.base.NewTokenSourceV4Client(tokenSource)
}

// NewInstallationClientWithContext creates a client respecting context cancellation
// This allows the caller to set timeouts on rate limit waits
func (r *RateLimitedClientCreator) NewInstallationClientWithContext(ctx context.Context, installationID int64) (*github.Client, error) {
	if !r.config.Enabled {
		return r.base.NewInstallationClient(installationID)
	}

	startWait := time.Now()
	if err := r.waitForRateLimit(ctx, installationID); err != nil {
		return nil, err
	}
	waitDuration := time.Since(startWait)

	// Record metrics
	if r.registry != nil {
		if timer := r.registry.Get(MetricsKeyRateLimitWaitTime); timer != nil {
			if t, ok := timer.(metrics.Timer); ok {
				t.Update(waitDuration)
			}
		}

		if waitDuration > time.Millisecond {
			if counter := r.registry.Get(MetricsKeyRateLimitThrottled); counter != nil {
				if c, ok := counter.(metrics.Counter); ok {
					c.Inc(1)
				}
			}
		}
	}

	// Create the client
	client, err := r.base.NewInstallationClient(installationID)
	if err != nil {
		return nil, err
	}

	// Wrap transport with adaptive rate limiting header inspector if enabled (Phase 2)
	if r.config.Adaptive.Enabled && client != nil && client.Client() != nil {
		client.Client().Transport = r.newAdaptiveTransport(client.Client().Transport, installationID)
	}

	return client, nil
}

// =============================================================================
// Phase 2: Adaptive Rate Limiting Implementation
// =============================================================================

// parseGitHubRateLimitHeaders extracts rate limit information from GitHub API response headers
func parseGitHubRateLimitHeaders(resp *http.Response) (remaining, limit int, reset time.Time, ok bool) {
	if resp == nil {
		return 0, 0, time.Time{}, false
	}

	// Parse X-RateLimit-Remaining
	if remainingStr := resp.Header.Get("X-RateLimit-Remaining"); remainingStr != "" {
		if r, err := fmt.Sscanf(remainingStr, "%d", &remaining); err == nil && r == 1 {
			// Parse X-RateLimit-Limit
			if limitStr := resp.Header.Get("X-RateLimit-Limit"); limitStr != "" {
				if l, err := fmt.Sscanf(limitStr, "%d", &limit); err == nil && l == 1 {
					// Parse X-RateLimit-Reset (Unix timestamp)
					if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
						var resetUnix int64
						if r, err := fmt.Sscanf(resetStr, "%d", &resetUnix); err == nil && r == 1 {
							reset = time.Unix(resetUnix, 0)
							return remaining, limit, reset, true
						}
					}
				}
			}
		}
	}

	return 0, 0, time.Time{}, false
}

// getOrCreateAdaptiveState gets or creates an adaptive rate state for an installation
func (r *RateLimitedClientCreator) getOrCreateAdaptiveState(installationID int64) *adaptiveRateState {
	// Try to load existing state
	if state, ok := r.adaptiveStates.Load(installationID); ok {
		return state.(*adaptiveRateState)
	}

	// Create new state with initial rate
	state := &adaptiveRateState{
		currentRate: r.config.InstallationRate, // Start with configured rate
		lastUpdate:  time.Now(),
	}

	// Store and return (handle race condition)
	actual, loaded := r.adaptiveStates.LoadOrStore(installationID, state)
	if loaded {
		return actual.(*adaptiveRateState)
	}

	return state
}

// updateAdaptiveRate updates the adaptive rate state based on GitHub API headers
func (r *RateLimitedClientCreator) updateAdaptiveRate(installationID int64, remaining, limit int, reset time.Time) {
	if !r.config.Adaptive.Enabled {
		return
	}

	state := r.getOrCreateAdaptiveState(installationID)
	state.mu.Lock()
	defer state.mu.Unlock()

	// Update observed GitHub data
	state.lastRemaining = remaining
	state.lastLimit = limit
	state.lastReset = reset

	// Calculate safe rate based on GitHub headers
	newRate := r.calculateAdaptiveRate(remaining, reset)

	// Apply exponential moving average for smoothing
	// EMA formula: newValue = alpha * currentValue + (1 - alpha) * previousValue
	// where alpha = smoothingFactor
	alpha := r.config.Adaptive.SmoothingFactor
	state.currentRate = alpha*newRate + (1-alpha)*state.currentRate

	// Update last update time
	state.lastUpdate = time.Now()

	// Update limiter with new rate
	r.updateInstallationLimiter(installationID, state.currentRate)

	// Record metrics
	if r.registry != nil {
		if gauge := r.registry.Get(MetricsKeyRateLimitGitHubRemaining); gauge != nil {
			if g, ok := gauge.(metrics.Gauge); ok {
				g.Update(int64(remaining))
			}
		}
		if gauge := r.registry.Get(MetricsKeyRateLimitAdaptiveRate); gauge != nil {
			if g, ok := gauge.(metrics.Gauge); ok {
				g.Update(int64(state.currentRate * 1000)) // Store as millis for precision
			}
		}
		if counter := r.registry.Get(MetricsKeyRateLimitAdaptiveAdjustments); counter != nil {
			if c, ok := counter.(metrics.Counter); ok {
				c.Inc(1)
			}
		}
	}

	// Log rate adjustment
	r.logger.Debug().
		Int64("installation_id", installationID).
		Float64("old_rate", state.currentRate/alpha).
		Float64("new_rate", state.currentRate).
		Int("remaining", remaining).
		Int("limit", limit).
		Time("reset", reset).
		Msg("Adaptive rate adjusted")
}

// calculateAdaptiveRate calculates a safe rate based on GitHub remaining quota
func (r *RateLimitedClientCreator) calculateAdaptiveRate(remaining int, reset time.Time) float64 {
	// Calculate time until reset
	timeUntilReset := time.Until(reset)
	if timeUntilReset <= 0 {
		// Reset time has passed, use max rate
		return r.config.Adaptive.MaxRate
	}

	// Calculate safe rate: (remaining / seconds_until_reset) * safety_factor
	secondsUntilReset := timeUntilReset.Seconds()
	if secondsUntilReset <= 0 {
		return r.config.Adaptive.MaxRate
	}

	calculatedRate := (float64(remaining) / secondsUntilReset) * r.config.Adaptive.SafetyFactor

	// Apply min/max bounds
	if calculatedRate < r.config.Adaptive.MinRate {
		return r.config.Adaptive.MinRate
	}
	if calculatedRate > r.config.Adaptive.MaxRate {
		return r.config.Adaptive.MaxRate
	}

	return calculatedRate
}

// updateInstallationLimiter updates the rate limiter for an installation
func (r *RateLimitedClientCreator) updateInstallationLimiter(installationID int64, newRate float64) {
	limiter := r.getOrCreateInstallationLimiter(installationID)

	// Update the limiter's rate
	// Note: rate.Limiter.SetLimit is safe to call concurrently
	limiter.SetLimit(rate.Limit(newRate))

	r.logger.Debug().
		Int64("installation_id", installationID).
		Float64("new_rate", newRate).
		Msg("Installation rate limiter updated")
}

// adaptiveRateAdjustmentLoop periodically adjusts rates based on accumulated data
func (r *RateLimitedClientCreator) adaptiveRateAdjustmentLoop() {
	ticker := time.NewTicker(r.config.Adaptive.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			r.logger.Info().Msg("Adaptive rate adjustment loop stopped")
			return

		case <-ticker.C:
			// Iterate over all adaptive states and adjust stale rates
			r.adaptiveStates.Range(func(key, value interface{}) bool {
				installationID := key.(int64)
				state := value.(*adaptiveRateState)

				state.mu.RLock()
				lastUpdate := state.lastUpdate
				lastReset := state.lastReset
				lastRemaining := state.lastRemaining
				state.mu.RUnlock()

				// If we haven't seen an update in a while, check if we should reset to default
				timeSinceUpdate := time.Since(lastUpdate)
				if timeSinceUpdate > 2*r.config.Adaptive.UpdateInterval {
					// No recent activity, gradually decay back to default rate
					state.mu.Lock()
					targetRate := r.config.InstallationRate
					alpha := 0.1 // Slow decay
					state.currentRate = alpha*targetRate + (1-alpha)*state.currentRate
					state.mu.Unlock()

					r.updateInstallationLimiter(installationID, state.currentRate)

					r.logger.Debug().
						Int64("installation_id", installationID).
						Float64("decayed_rate", state.currentRate).
						Dur("time_since_update", timeSinceUpdate).
						Msg("Adaptive rate decayed to default")
				} else if !lastReset.IsZero() && time.Now().After(lastReset) {
					// Reset time has passed, recalculate with fresh quota
					newRate := r.calculateAdaptiveRate(lastRemaining, lastReset)
					state.mu.Lock()
					alpha := r.config.Adaptive.SmoothingFactor
					state.currentRate = alpha*newRate + (1-alpha)*state.currentRate
					state.mu.Unlock()

					r.updateInstallationLimiter(installationID, state.currentRate)
				}

				return true // Continue iteration
			})
		}
	}
}

// newAdaptiveTransport wraps an HTTP transport to inspect GitHub rate limit headers
func (r *RateLimitedClientCreator) newAdaptiveTransport(base http.RoundTripper, installationID int64) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	return &adaptiveTransport{
		base:           base,
		installationID: installationID,
		creator:        r,
	}
}

// adaptiveTransport is an HTTP RoundTripper that inspects GitHub rate limit headers
type adaptiveTransport struct {
	base           http.RoundTripper
	installationID int64
	creator        *RateLimitedClientCreator
}

func (t *adaptiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Execute request
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Parse GitHub rate limit headers
	if remaining, limit, reset, ok := parseGitHubRateLimitHeaders(resp); ok {
		// Update adaptive rate asynchronously (don't block the request)
		go t.creator.updateAdaptiveRate(t.installationID, remaining, limit, reset)
	}

	return resp, nil
}

// Ensure RateLimitedClientCreator implements githubapp.ClientCreator
var _ githubapp.ClientCreator = (*RateLimitedClientCreator)(nil)

// newClientMiddleware is a helper to create HTTP middleware for client customization
// This can be used to add additional headers, logging, etc.
func newClientMiddleware(next http.RoundTripper) http.RoundTripper {
	return &clientMiddleware{next: next}
}

type clientMiddleware struct {
	next http.RoundTripper
}

func (c *clientMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	// Can add request ID, tracing headers, etc. here
	return c.next.RoundTrip(req)
}
