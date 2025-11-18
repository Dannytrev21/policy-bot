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

package test

import (
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server/handler"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// BenchmarkRateLimiter_StaticMode benchmarks static rate limiting overhead
func BenchmarkRateLimiter_StaticMode(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     3.0,
		OrgBurst:    10,
		GlobalRate:  100.0,
		GlobalBurst: 50,
		Enabled:     true,
		Adaptive: handler.AdaptiveRateLimitConfig{
			Enabled: false, // Static mode
		},
	}

	rateLimiter := setupRateLimiter(b, config)
	installationID := int64(12345)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := rateLimiter.NewInstallationClient(installationID)
			if err != nil {
				b.Errorf("Failed to create client: %v", err)
			}
		}
	})

	b.StopTimer()

	// Report custom metrics
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/sec")
}

// BenchmarkRateLimiter_AdaptiveMode benchmarks adaptive rate limiting overhead
func BenchmarkRateLimiter_AdaptiveMode(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     3.0,
		OrgBurst:    10,
		GlobalRate:  100.0,
		GlobalBurst:       50,
		Enabled:           true,
		Adaptive: handler.AdaptiveRateLimitConfig{
			Enabled:         true, // Adaptive mode
			SafetyFactor:    0.8,
			MinRate:         1.0,
			MaxRate:         4.0,
			SmoothingFactor: 0.3,
			UpdateInterval:  10 * time.Second,
		},
	}

	rateLimiter := setupRateLimiter(b, config)
	installationID := int64(12345)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := rateLimiter.NewInstallationClient(installationID)
			if err != nil {
				b.Errorf("Failed to create client: %v", err)
			}
		}
	})

	b.StopTimer()

	// Report custom metrics
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/sec")
}

// BenchmarkRateLimiter_HighConcurrency tests performance under high concurrency
func BenchmarkRateLimiter_HighConcurrency(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     3.0,
		OrgBurst:    10,
		GlobalRate:  100.0,
		GlobalBurst:       50,
		Enabled:           true,
	}

	rateLimiter := setupRateLimiter(b, config)

	// Test with multiple installations
	installationIDs := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	b.ResetTimer()
	b.ReportAllocs()

	b.SetParallelism(100) // High concurrency

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			installationID := installationIDs[i%len(installationIDs)]
			_, err := rateLimiter.NewInstallationClient(installationID)
			if err != nil {
				b.Errorf("Failed to create client: %v", err)
			}
			i++
		}
	})

	b.StopTimer()

	// Report custom metrics
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/sec")
}

// BenchmarkRateLimiter_InstallationIsolation tests per-installation isolation overhead
func BenchmarkRateLimiter_InstallationIsolation(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     3.0,
		OrgBurst:    10,
		GlobalRate:  100.0,
		GlobalBurst:       50,
		Enabled:           true,
	}

	rateLimiter := setupRateLimiter(b, config)

	// Test with many unique installations to measure isolation overhead
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		installationID := int64(i) // Unique installation per iteration
		_, err := rateLimiter.NewInstallationClient(installationID)
		if err != nil {
			b.Errorf("Failed to create client: %v", err)
		}
	}

	b.StopTimer()
}

// BenchmarkRateLimiter_NoRateLimiting benchmarks baseline without rate limiting
func BenchmarkRateLimiter_NoRateLimiting(b *testing.B) {
	config := handler.RateLimitConfig{
		Enabled: false, // Disabled
	}

	rateLimiter := setupRateLimiter(b, config)
	installationID := int64(12345)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := rateLimiter.NewInstallationClient(installationID)
			if err != nil {
				b.Errorf("Failed to create client: %v", err)
			}
		}
	})

	b.StopTimer()

	// Report custom metrics
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/sec")
}

// BenchmarkRateLimiter_GlobalLimitContention tests global limit contention
func BenchmarkRateLimiter_GlobalLimitContention(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     1000.0, // Very high per-org (won't be hit)
		OrgBurst:    1000,
		GlobalRate:  10.0, // Low global limit (will be hit)
		GlobalBurst: 10,
		Enabled:     true,
	}

	rateLimiter := setupRateLimiter(b, config)

	// Many installations hitting global limit
	b.ResetTimer()
	b.ReportAllocs()

	b.SetParallelism(50)

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			installationID := int64(i)
			_, err := rateLimiter.NewInstallationClient(installationID)
			if err != nil {
				b.Errorf("Failed to create client: %v", err)
			}
			i++
		}
	})

	b.StopTimer()
}

// BenchmarkRateLimiter_AdaptiveAdjustment benchmarks adaptive rate adjustment overhead
func BenchmarkRateLimiter_AdaptiveAdjustment(b *testing.B) {
	config := handler.RateLimitConfig{
		OrgRate:     3.0,
		OrgBurst:    10,
		GlobalRate:  100.0,
		GlobalBurst:       50,
		Enabled:           true,
		Adaptive: handler.AdaptiveRateLimitConfig{
			Enabled:         true,
			SafetyFactor:    0.8,
			MinRate:         1.0,
			MaxRate:         4.0,
			SmoothingFactor: 0.3,
			UpdateInterval:  10 * time.Second,
		},
	}

	rateLimiter := setupRateLimiter(b, config)
	installationID := int64(12345)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Benchmark adaptive rate adjustment path
		_, _ = rateLimiter.NewInstallationClient(installationID)
	}

	b.StopTimer()
}

// setupRateLimiter creates a rate limiter for benchmarking
func setupRateLimiter(b *testing.B, config handler.RateLimitConfig) *handler.RateLimitedClientCreator {
	b.Helper()

	// Create mock base client creator
	baseCreator := &mockClientCreator{}

	// Create metrics registry
	registry := metrics.NewRegistry()

	// Create logger (discarding output for benchmarks)
	logger := zerolog.Nop()

	// Create rate limiter
	rateLimiter := handler.NewRateLimitedClientCreator(baseCreator, &config, logger, registry)

	return rateLimiter
}

// mockClientCreator is a mock for benchmarking
type mockClientCreator struct{}

func (m *mockClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	// Return a mock client (no actual GitHub API calls)
	return github.NewClient(nil), nil
}

func (m *mockClientCreator) NewAppClient() (*github.Client, error) {
	return github.NewClient(nil), nil
}

func (m *mockClientCreator) NewTokenClient(token string) (*github.Client, error) {
	return github.NewClient(nil), nil
}

func (m *mockClientCreator) NewAppV4Client() (*githubv4.Client, error) {
	return githubv4.NewClient(nil), nil
}

func (m *mockClientCreator) NewInstallationV4Client(installationID int64) (*githubv4.Client, error) {
	return githubv4.NewClient(nil), nil
}

func (m *mockClientCreator) NewTokenV4Client(token string) (*githubv4.Client, error) {
	return githubv4.NewClient(nil), nil
}

func (m *mockClientCreator) NewTokenSourceClient(source oauth2.TokenSource) (*github.Client, error) {
	return github.NewClient(nil), nil
}

func (m *mockClientCreator) NewTokenSourceV4Client(source oauth2.TokenSource) (*githubv4.Client, error) {
	return githubv4.NewClient(nil), nil
}

// Compile-time check that mockClientCreator implements githubapp.ClientCreator
var _ githubapp.ClientCreator = (*mockClientCreator)(nil)
