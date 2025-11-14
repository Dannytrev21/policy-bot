# Rate Limit Error Analysis: "global rate limit wait failed: rate: Wait(n-1) exceeds limiter's burst 0"

## Executive Summary

The error "global rate limit wait failed: rate: Wait(n-1) exceeds limiter's burst 0" indicates that the rate limiter's burst capacity is being set to 0, which prevents any requests from being processed. This is a configuration issue where the burst value is either not being set properly or is explicitly being set to zero.

## Error Breakdown

### What the Error Means

The error message comes from the golang.org/x/time/rate package when:
1. A rate limiter is created with burst capacity of 0
2. Any attempt to consume a token (Wait(n=1)) fails because it exceeds the burst capacity
3. This creates an impossible situation where no requests can ever be processed

### Root Cause Analysis

After analyzing the codebase, there are several potential causes:

## 1. Configuration Issue (Most Likely)

The rate limiter configuration might be incorrectly set with zero burst values. Check your configuration files:

### Current Default Configuration
```go
// From server/handler/rate_limiter.go
DefaultGlobalBurst = 50        // Default global burst
DefaultInstallationBurst = 10  // Default installation burst
```

### Configuration Loading Chain
```
server/config.go → SetDefaults() → handler/rate_limiter.go → NewRateLimitedClientCreator()
```

The configuration defaults are properly set in `server/config.go`:
```go
func (c *RateLimitConfig) SetDefaults() {
    if c.GlobalBurst == 0 {
        c.GlobalBurst = 50  // Proper default
    }
    if c.InstallationBurst == 0 {
        c.InstallationBurst = 10  // Proper default
    }
}
```

## 2. Configuration Override Issue

The problem likely occurs when:
1. **Explicit Zero in Config File**: Your YAML/JSON configuration explicitly sets burst to 0
2. **Missing Config Initialization**: The RateLimitConfig is not being properly initialized
3. **Incorrect Config Passing**: The config is being lost or corrupted between initialization and use

### IDENTIFIED BUG IN SERVER INITIALIZATION

Found in `server/server.go:209-214`:
```go
// BUGGY CODE - doesn't apply defaults or validation!
rateLimitConfig := &handler.RateLimitConfig{
    InstallationRate:  c.RateLimit.InstallationRate,  // Could be 0
    InstallationBurst: c.RateLimit.InstallationBurst, // Could be 0 ❌
    GlobalRate:        c.RateLimit.GlobalRate,        // Could be 0
    GlobalBurst:       c.RateLimit.GlobalBurst,       // Could be 0 ❌
    Enabled:           c.RateLimit.Enabled,
}
```

The server is directly copying values without checking if they're zero. When these zero values reach the rate limiter, it fails with "exceeds limiter's burst 0".

## 3. Identified Bug Pattern

The issue appears when:
```go
// If config is nil or improperly initialized
globalLimiter := rate.NewLimiter(
    rate.Limit(config.GlobalRate),  // Might be 0
    config.GlobalBurst,              // Becomes 0 if not set
)
```

## The Specific Bug

The bug is in `server/server.go` lines 209-214. When the server creates a `handler.RateLimitConfig`, it directly copies values from the server config without validation:

```go
// Current BUGGY implementation in server/server.go
rateLimitConfig := &handler.RateLimitConfig{
    InstallationRate:  c.RateLimit.InstallationRate,  // No validation!
    InstallationBurst: c.RateLimit.InstallationBurst, // Could be 0!
    GlobalRate:        c.RateLimit.GlobalRate,
    GlobalBurst:       c.RateLimit.GlobalBurst,       // Could be 0!
    Enabled:           c.RateLimit.Enabled,
}
```

## Immediate Fix Options

### Option 1: Fix Server Initialization (RECOMMENDED - Root Cause Fix)

Add validation in `NewRateLimitedClientCreator` to ensure burst is never 0:

```go
func NewRateLimitedClientCreator(
    base githubapp.ClientCreator,
    config *RateLimitConfig,
    logger zerolog.Logger,
    registry metrics.Registry,
) *RateLimitedClientCreator {
    if config == nil {
        config = DefaultRateLimitConfig()
    }

    // DEFENSIVE CHECK: Ensure burst is never 0
    if config.GlobalBurst <= 0 {
        logger.Warn().
            Int("provided_burst", config.GlobalBurst).
            Msg("Invalid GlobalBurst configuration, using default")
        config.GlobalBurst = DefaultGlobalBurst
    }

    if config.InstallationBurst <= 0 {
        logger.Warn().
            Int("provided_burst", config.InstallationBurst).
            Msg("Invalid InstallationBurst configuration, using default")
        config.InstallationBurst = DefaultInstallationBurst
    }

    // Continue with initialization...
}
```

### Option 3: Configuration Validation

Add validation at the configuration loading stage:

```go
func (c *RateLimitConfig) Validate() error {
    if c.GlobalBurst <= 0 {
        return fmt.Errorf("GlobalBurst must be positive, got %d", c.GlobalBurst)
    }
    if c.InstallationBurst <= 0 {
        return fmt.Errorf("InstallationBurst must be positive, got %d", c.InstallationBurst)
    }
    if c.GlobalRate <= 0 {
        return fmt.Errorf("GlobalRate must be positive, got %f", c.GlobalRate)
    }
    if c.InstallationRate <= 0 {
        return fmt.Errorf("InstallationRate must be positive, got %f", c.InstallationRate)
    }
    return nil
}
```

## Configuration Check

### Check Your Configuration File

Look for rate limiting configuration in your YAML/JSON:

```yaml
# BAD - This will cause the error
rate_limit:
  global_burst: 0        # ❌ WRONG
  installation_burst: 0  # ❌ WRONG

# GOOD - Proper configuration
rate_limit:
  global_burst: 50       # ✅ Correct
  installation_burst: 10 # ✅ Correct
  global_rate: 100.0
  installation_rate: 3.0
  enabled: true
```

### Environment Variable Override

Check if environment variables are overriding config:
```bash
# These could cause issues if set to 0
POLICY_BOT_RATE_LIMIT_GLOBAL_BURST=0
POLICY_BOT_RATE_LIMIT_INSTALLATION_BURST=0
```

## The Exact Fix Needed

### Code Change Required in `server/server.go`

Replace lines 209-215 with:

```go
if c.RateLimit.Enabled {
    // Ensure defaults are applied before creating handler config
    c.RateLimit.SetDefaults()  // This already exists and sets proper defaults!

    // Convert server.RateLimitConfig to handler.RateLimitConfig
    rateLimitConfig := &handler.RateLimitConfig{
        InstallationRate:  c.RateLimit.InstallationRate,
        InstallationBurst: c.RateLimit.InstallationBurst,
        GlobalRate:        c.RateLimit.GlobalRate,
        GlobalBurst:       c.RateLimit.GlobalBurst,
        Enabled:           c.RateLimit.Enabled,
    }

    // Continue with rate limiter creation...
}
```

### Alternative: Use DefaultRateLimitConfig as Base

```go
if c.RateLimit.Enabled {
    // Start with defaults
    rateLimitConfig := handler.DefaultRateLimitConfig()

    // Override only if values are provided
    if c.RateLimit.InstallationRate > 0 {
        rateLimitConfig.InstallationRate = c.RateLimit.InstallationRate
    }
    if c.RateLimit.InstallationBurst > 0 {
        rateLimitConfig.InstallationBurst = c.RateLimit.InstallationBurst
    }
    if c.RateLimit.GlobalRate > 0 {
        rateLimitConfig.GlobalRate = c.RateLimit.GlobalRate
    }
    if c.RateLimit.GlobalBurst > 0 {
        rateLimitConfig.GlobalBurst = c.RateLimit.GlobalBurst
    }

    // Continue with rate limiter creation...
}
```

## Recommended Solution

### Immediate Workaround (Until Code is Fixed)

1. **Disable Rate Limiting Temporarily**:
```yaml
rate_limit:
  enabled: false  # Bypass rate limiting until fixed
```

2. **Set Explicit Valid Values**:
```yaml
rate_limit:
  enabled: true
  global_rate: 100.0
  global_burst: 50        # Must be > 0
  installation_rate: 3.0
  installation_burst: 10  # Must be > 0
```

### Long-term Fix

Implement defensive checks in the code:

```go
// In server/handler/rate_limiter.go - NewRateLimitedClientCreator function

func NewRateLimitedClientCreator(...) *RateLimitedClientCreator {
    // ... existing code ...

    // Add validation before creating limiters
    if config.GlobalBurst <= 0 {
        config.GlobalBurst = DefaultGlobalBurst
        logger.Warn().Msg("GlobalBurst was 0 or negative, using default value")
    }

    if config.InstallationBurst <= 0 {
        config.InstallationBurst = DefaultInstallationBurst
        logger.Warn().Msg("InstallationBurst was 0 or negative, using default value")
    }

    // Ensure rates are positive too
    if config.GlobalRate <= 0 {
        config.GlobalRate = DefaultGlobalRateLimit
    }

    if config.InstallationRate <= 0 {
        config.InstallationRate = DefaultInstallationRateLimit
    }

    // Now safe to create limiter
    globalLimiter := rate.NewLimiter(
        rate.Limit(config.GlobalRate),
        config.GlobalBurst,
    )

    // ... rest of initialization ...
}
```

## Additional Defensive Measures

### 1. Add Minimum Burst Enforcement

```go
const MinimumBurst = 1  // Never allow burst less than 1

func (r *RateLimitedClientCreator) getOrCreateInstallationLimiter(installationID int64) *rate.Limiter {
    burst := r.config.InstallationBurst
    if burst < MinimumBurst {
        burst = MinimumBurst
    }

    newLimiter := rate.NewLimiter(
        rate.Limit(r.config.InstallationRate),
        burst,
    )
    // ... rest of function
}
```

### 2. Add Startup Validation

```go
func (s *Server) validateRateLimitConfig() error {
    config := s.Config.RateLimit
    if config.Enabled {
        if config.GlobalBurst <= 0 {
            return fmt.Errorf("rate limiting enabled but GlobalBurst=%d (must be > 0)",
                config.GlobalBurst)
        }
        if config.InstallationBurst <= 0 {
            return fmt.Errorf("rate limiting enabled but InstallationBurst=%d (must be > 0)",
                config.InstallationBurst)
        }
    }
    return nil
}
```

### 3. Add Observability

Add logging to track when this happens:

```go
func NewRateLimitedClientCreator(...) *RateLimitedClientCreator {
    logger.Info().
        Float64("global_rate", config.GlobalRate).
        Int("global_burst", config.GlobalBurst).
        Float64("installation_rate", config.InstallationRate).
        Int("installation_burst", config.InstallationBurst).
        Bool("enabled", config.Enabled).
        Msg("Initializing rate limiter with configuration")

    if config.GlobalBurst <= 0 || config.InstallationBurst <= 0 {
        logger.Error().
            Int("global_burst", config.GlobalBurst).
            Int("installation_burst", config.InstallationBurst).
            Msg("CRITICAL: Rate limiter burst configuration is invalid!")
    }
    // ...
}
```

## Testing Recommendations

### Unit Test for Zero Burst

Add this test to verify the fix:

```go
func TestRateLimiter_ZeroBurstHandling(t *testing.T) {
    // Test that zero burst is handled gracefully
    config := &RateLimitConfig{
        GlobalRate:        10.0,
        GlobalBurst:       0,  // Intentionally invalid
        InstallationRate:  3.0,
        InstallationBurst: 0,  // Intentionally invalid
        Enabled:           true,
    }

    logger := zerolog.New(nil)
    registry := metrics.NewRegistry()
    base := &MockRateLimitClientCreator{}

    // Should not panic and should use defaults
    rlcc := NewRateLimitedClientCreator(base, config, logger, registry)

    // Should be able to create client without error
    _, err := rlcc.NewInstallationClient(123)
    assert.NoError(t, err, "Should handle zero burst gracefully")
}
```

## Monitoring and Alerts

Set up monitoring for:
1. Rate limiter configuration values at startup
2. Occurrences of the "exceeds limiter's burst" error
3. Metric: `handler.rate_limit.configuration_errors`

## Summary

The error "global rate limit wait failed: rate: Wait(n-1) exceeds limiter's burst 0" is caused by a **bug in `server/server.go`** where the rate limiter configuration is created without applying defaults or validation.

### Root Cause
The server creates `handler.RateLimitConfig` by directly copying values from the server config without checking if they're zero (lines 209-214 in `server/server.go`).

### Quick Fix (Choose One)

1. **Add explicit configuration values** in your YAML config:
   ```yaml
   rate_limit:
     enabled: true
     global_burst: 50        # Must be > 0
     installation_burst: 10  # Must be > 0
     global_rate: 100.0
     installation_rate: 3.0
   ```

2. **Temporarily disable rate limiting**:
   ```yaml
   rate_limit:
     enabled: false
   ```

### Permanent Fix (Code Change)
Apply the fix in `server/server.go` to ensure defaults are applied before creating the rate limiter. This prevents the error from ever occurring, even with missing or zero configuration values.

### Verification
After applying the fix, check the logs for:
```
"Rate-limited client creator initialized"
  global_burst=50       # Should NOT be 0
  installation_burst=10 # Should NOT be 0
```

**Impact**: This bug affects all SQS-based event processing when rate limiting is enabled with missing or zero burst configuration.

## Related Code Locations

- Configuration: `server/config.go:295-302`
- Rate Limiter Creation: `server/handler/rate_limiter.go:193-196`
- Default Values: `server/handler/rate_limiter.go:38-43`
- Configuration Loading: `server/server.go` (where RateLimitConfig is initialized)
- Server Initialization: `server/server.go:346-347` (where rate limiter is created)

## References

- [golang.org/x/time/rate documentation](https://pkg.go.dev/golang.org/x/time/rate)
- [Token Bucket Algorithm](https://en.wikipedia.org/wiki/Token_bucket)
- GitHub API Rate Limiting: 15,000 requests/hour per installation (4.16 req/sec)