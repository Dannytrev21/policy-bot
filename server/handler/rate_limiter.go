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

	// Metrics keys for rate limiting
	MetricsKeyRateLimitWaitTime     = "handler.rate_limit.wait_time"
	MetricsKeyRateLimitThrottled    = "handler.rate_limit.throttled"
	MetricsKeyRateLimitQuotaUsed    = "handler.rate_limit.quota_used"
	MetricsKeyRateLimitInstallations = "handler.rate_limit.installations"
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
}

// DefaultRateLimitConfig returns the default rate limiting configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		InstallationRate:  DefaultInstallationRateLimit,
		InstallationBurst: DefaultInstallationBurst,
		GlobalRate:        DefaultGlobalRateLimit,
		GlobalBurst:       DefaultGlobalBurst,
		Enabled:           true,
	}
}

// RateLimitedClientCreator wraps a githubapp.ClientCreator with rate limiting
// to prevent exceeding GitHub API rate limits proactively.
//
// It maintains per-installation rate limiters and a global rate limiter for safety.
// This prevents 429 (Too Many Requests) errors before they occur.
type RateLimitedClientCreator struct {
	base githubapp.ClientCreator

	config *RateLimitConfig

	// Per-installation rate limiters
	// Key: installation ID
	installationLimiters sync.Map // map[int64]*rate.Limiter

	// Global rate limiter for safety
	globalLimiter *rate.Limiter

	logger   zerolog.Logger
	registry metrics.Registry

	mu sync.RWMutex
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
	}

	rlcc := &RateLimitedClientCreator{
		base:          base,
		config:        config,
		globalLimiter: globalLimiter,
		logger:        logger.With().Str("component", "rate_limiter").Logger(),
		registry:      registry,
	}

	logger.Info().
		Float64("installation_rate", config.InstallationRate).
		Int("installation_burst", config.InstallationBurst).
		Float64("global_rate", config.GlobalRate).
		Int("global_burst", config.GlobalBurst).
		Bool("enabled", config.Enabled).
		Msg("Rate-limited client creator initialized")

	return rlcc
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

	return r.base.NewInstallationClient(installationID)
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
