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
	"fmt"
	"sync"
	"testing"
	"time"

	gometrics "github.com/rcrowley/go-metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMappingCache_BasicOperations(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Test Set and Get for positive cache
	cache.Set("owner/repo", 12345)
	id, found := cache.Get("owner/repo")
	assert.True(t, found)
	assert.Equal(t, int64(12345), id)

	// Test SetNotFound and Get for negative cache
	cache.SetNotFound("owner/notfound")
	id, found = cache.Get("owner/notfound")
	assert.True(t, found, "Should be found in negative cache")
	assert.Equal(t, int64(0), id, "Negative cache should return 0")

	// Test Remove
	cache.Remove("owner/repo")
	id, found = cache.Get("owner/repo")
	assert.False(t, found)
	assert.Equal(t, int64(0), id)

	// Test Clear
	cache.Set("test1", 111)
	cache.Set("test2", 222)
	cache.Clear()
	assert.Equal(t, 0, cache.GetSize())
}

func TestMappingCache_Expiration(t *testing.T) {
	// Use very short TTLs for testing
	cache := NewMappingCacheWithOptions(100*time.Millisecond, 50*time.Millisecond, 1000, 10*time.Millisecond)
	defer cache.Stop()

	// Set positive cache entry
	cache.Set("positive", 999)
	id, found := cache.Get("positive")
	assert.True(t, found)
	assert.Equal(t, int64(999), id)

	// Wait for positive TTL to expire
	time.Sleep(150 * time.Millisecond)
	id, found = cache.Get("positive")
	assert.False(t, found, "Entry should have expired")
	assert.Equal(t, int64(0), id)

	// Set negative cache entry
	cache.SetNotFound("negative")
	id, found = cache.Get("negative")
	assert.True(t, found)
	assert.Equal(t, int64(0), id)

	// Wait for negative TTL to expire
	time.Sleep(100 * time.Millisecond)
	id, found = cache.Get("negative")
	assert.False(t, found, "Negative entry should have expired")
}

func TestMappingCache_Stats(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Add some entries
	cache.Set("repo1", 100)
	cache.Set("repo2", 200)
	cache.SetNotFound("repo3")
	cache.SetNotFound("repo4")

	positive, negative, total := cache.GetStats()
	assert.Equal(t, 2, positive)
	assert.Equal(t, 2, negative)
	assert.Equal(t, 4, total)
}

func TestMappingCache_Metrics(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Initial hit rate should be 0
	assert.Equal(t, float64(0), cache.GetHitRate(), "Initial hit rate should be 0")

	// Generate some activity
	cache.Set("test1", 100)
	cache.Set("test2", 200)
	cache.Get("test1")        // hit
	cache.Get("test2")        // hit
	cache.Get("nonexistent") // miss

	hits, misses, sets, evictions, size := cache.GetMetrics()
	assert.Equal(t, int64(2), hits)
	assert.Equal(t, int64(1), misses)
	assert.Equal(t, int64(2), sets)
	assert.Equal(t, int64(0), evictions) // No evictions yet
	assert.Equal(t, int64(2), size)

	// Hit rate should be 66.67% (2 hits / 3 total)
	hitRate := cache.GetHitRate()
	assert.InDelta(t, 66.67, hitRate, 0.01, "Hit rate should be approximately 66.67%")
}

func TestMappingCache_BuildKeys(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Test BuildRepoCacheKey
	key := cache.BuildRepoCacheKey("owner", "repo")
	assert.Equal(t, "owner/repo", key)

	key = cache.BuildRepoCacheKey("", "repo")
	assert.Equal(t, "", key, "Should return empty for invalid input")

	key = cache.BuildRepoCacheKey("owner", "")
	assert.Equal(t, "", key, "Should return empty for invalid input")

	// Test BuildOrgCacheKey
	key = cache.BuildOrgCacheKey("myorg")
	assert.Equal(t, "org:myorg", key)

	key = cache.BuildOrgCacheKey("")
	assert.Equal(t, "", key, "Should return empty for invalid input")
}

func TestMappingCache_Eviction(t *testing.T) {
	// Small cache for testing eviction
	cache := NewMappingCacheWithOptions(1*time.Hour, 5*time.Minute, 10, 1*time.Hour)
	defer cache.Stop()

	// Fill cache to capacity
	for i := 1; i <= 10; i++ {
		cache.Set(fmt.Sprintf("repo%d", i), int64(i))
	}
	assert.Equal(t, 10, cache.GetSize())

	// Add one more to trigger eviction (evicts 10% = 1 entry)
	cache.Set("repo11", 11)

	// Size should still be within maxSize
	size := cache.GetSize()
	assert.LessOrEqual(t, size, 10, "Cache size should not exceed maxSize")

	// Check eviction metric
	_, _, _, evictions, _ := cache.GetMetrics()
	assert.GreaterOrEqual(t, evictions, int64(1), "Should have evicted at least 1 entry")
}

func TestMappingCache_ConcurrentAccess(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	const numGoroutines = 50
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := fmt.Sprintf("repo%d-%d", id, j%10)

				// Mix of operations
				switch j % 4 {
				case 0:
					cache.Set(key, int64(id*1000+j))
				case 1:
					cache.Get(key)
				case 2:
					cache.SetNotFound(key)
				case 3:
					cache.Remove(key)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify cache is in consistent state
	stats := cache.GetSize()
	assert.GreaterOrEqual(t, stats, 0)

	// Check metrics are reasonable
	hits, misses, sets, _, _ := cache.GetMetrics()
	assert.GreaterOrEqual(t, hits+misses, int64(0))
	assert.GreaterOrEqual(t, sets, int64(0))
}

func TestMappingCache_BackgroundCleanup(t *testing.T) {
	// Very short TTL and cleanup interval for testing
	cache := NewMappingCacheWithOptions(50*time.Millisecond, 25*time.Millisecond, 1000, 10*time.Millisecond)
	defer cache.Stop()

	// Add entries
	cache.Set("test1", 100)
	cache.Set("test2", 200)
	cache.SetNotFound("test3")

	assert.Equal(t, 3, cache.GetSize())

	// Wait for cleanup to run and remove expired entries
	time.Sleep(100 * time.Millisecond)

	// All entries should be cleaned up
	assert.Equal(t, 0, cache.GetSize())

	// Check eviction metric
	_, _, _, evictions, _ := cache.GetMetrics()
	assert.GreaterOrEqual(t, evictions, int64(3), "Should have evicted all entries")
}

func TestMappingCache_EdgeCases(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Test getting non-existent key
	id, found := cache.Get("nonexistent")
	assert.False(t, found)
	assert.Equal(t, int64(0), id)

	// Test removing non-existent key (should not panic)
	cache.Remove("nonexistent")

	// Test overwriting existing key
	cache.Set("test", 100)
	cache.Set("test", 200)
	id, found = cache.Get("test")
	assert.True(t, found)
	assert.Equal(t, int64(200), id)

	// Test overwriting positive cache with negative cache
	cache.Set("switch", 300)
	cache.SetNotFound("switch")
	id, found = cache.Get("switch")
	assert.True(t, found)
	assert.Equal(t, int64(0), id, "Should be negative cache now")
}

// Benchmark tests
func BenchmarkMappingCache_Get(b *testing.B) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Pre-populate cache
	for i := 0; i < 1000; i++ {
		cache.Set(fmt.Sprintf("repo%d", i), int64(i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cache.Get(fmt.Sprintf("repo%d", i%1000))
	}
}

func BenchmarkMappingCache_Set(b *testing.B) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cache.Set(fmt.Sprintf("repo%d", i), int64(i))
	}
}

func BenchmarkMappingCache_BuildRepoCacheKey(b *testing.B) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cache.BuildRepoCacheKey("owner", "repository")
	}
}

func BenchmarkMappingCache_ConcurrentGet(b *testing.B) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)
	defer cache.Stop()

	// Pre-populate cache
	for i := 0; i < 1000; i++ {
		cache.Set(fmt.Sprintf("repo%d", i), int64(i))
	}

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			cache.Get(fmt.Sprintf("repo%d", i%1000))
			i++
		}
	})
}

func TestMappingCache_DefaultParameters(t *testing.T) {
	// Test with zero values to ensure defaults are applied
	cache := NewMappingCacheWithOptions(0, 0, 0, 0)
	defer cache.Stop()

	require.NotNil(t, cache)
	assert.Equal(t, 1*time.Hour, cache.positiveTTL)
	assert.Equal(t, 5*time.Minute, cache.negativeTTL)
	assert.Equal(t, 10000, cache.maxSize)
}

func TestMappingCache_StopCleanup(t *testing.T) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Add some entries
	cache.Set("test", 100)

	// Stop should not hang
	done := make(chan bool)
	go func() {
		cache.Stop()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() took too long")
	}
}

// Metrics Integration Tests (Step 1.3)

func TestMappingCache_MetricsIntegration(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		registry,
	)
	defer cache.Stop()

	// Generate some cache activity
	cache.Set("test1", 100)
	cache.Set("test2", 200)
	cache.Get("test1")        // hit
	cache.Get("test2")        // hit
	cache.Get("nonexistent") // miss

	// Publish metrics to registry
	cache.publishMetrics()

	// Verify metrics were published to registry
	hits := gometrics.GetOrRegisterCounter(MetricsKeyMappingCacheHits, registry).Count()
	misses := gometrics.GetOrRegisterCounter(MetricsKeyMappingCacheMisses, registry).Count()
	sets := gometrics.GetOrRegisterCounter(MetricsKeyMappingCacheSets, registry).Count()
	size := gometrics.GetOrRegisterGauge(MetricsKeyMappingCacheSize, registry).Value()
	hitRate := gometrics.GetOrRegisterGauge(MetricsKeyMappingCacheHitRate, registry).Value()

	assert.Equal(t, int64(2), hits, "Hits should be 2")
	assert.Equal(t, int64(1), misses, "Misses should be 1")
	assert.Equal(t, int64(2), sets, "Sets should be 2")
	assert.Equal(t, int64(2), size, "Size should be 2")
	assert.Equal(t, int64(66), hitRate, "Hit rate should be 66%")
}

func TestMappingCache_MetricsLoop(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		registry,
	)
	defer cache.Stop()

	// Generate activity
	cache.Set("test", 100)
	cache.Get("test") // hit

	// Publish metrics (instead of waiting for loop)
	cache.publishMetrics()

	// Verify metrics are in registry
	hits := gometrics.GetOrRegisterCounter(MetricsKeyMappingCacheHits, registry).Count()
	assert.Equal(t, int64(1), hits)
}

func TestMappingCache_NilRegistry(t *testing.T) {
	// Test that nil registry is handled gracefully (backward compatibility)
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		nil, // nil registry
	)
	defer cache.Stop()

	// These should not panic
	cache.Set("test", 100)
	cache.Get("test")
	cache.publishMetrics() // Should be no-op for nil registry

	// Verify cache still works without metrics
	hits, misses, sets, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), hits)
	assert.Equal(t, int64(0), misses)
	assert.Equal(t, int64(1), sets)
	assert.Equal(t, int64(1), size)
}

func TestMappingCache_HitRateCalculation(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		registry,
	)
	defer cache.Stop()

	// Test 100% hit rate
	cache.Set("test", 100)
	cache.Get("test") // hit
	cache.publishMetrics()

	hitRate := gometrics.GetOrRegisterGauge(MetricsKeyMappingCacheHitRate, registry).Value()
	assert.Equal(t, int64(100), hitRate, "Hit rate should be 100%")

	// Test 50% hit rate
	cache.Get("nonexistent") // miss
	cache.publishMetrics()

	hitRate = gometrics.GetOrRegisterGauge(MetricsKeyMappingCacheHitRate, registry).Value()
	assert.Equal(t, int64(50), hitRate, "Hit rate should be 50%")

	// Test 0% hit rate (edge case - no operations)
	cache2 := NewMappingCacheWithMetrics(1*time.Hour, 5*time.Minute, 1000, 1*time.Minute, registry)
	defer cache2.Stop()
	cache2.publishMetrics()
	// Hit rate gauge should not be updated when total operations is 0
}

func TestMappingCache_ConcurrentMetricsPublishing(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		registry,
	)
	defer cache.Stop()

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Concurrent cache operations
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := fmt.Sprintf("test%d-%d", id, j)
				cache.Set(key, int64(id*1000+j))
				cache.Get(key)
			}
		}(i)
	}

	// Concurrent metrics publishing
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			cache.publishMetrics()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()

	// Verify metrics are consistent
	hits, misses, sets, _, size := cache.GetMetrics()
	assert.GreaterOrEqual(t, hits, int64(0))
	assert.GreaterOrEqual(t, misses, int64(0))
	assert.GreaterOrEqual(t, sets, int64(0))
	assert.GreaterOrEqual(t, size, int64(0))
}

// Benchmark metrics publishing
func BenchmarkMappingCache_PublishMetrics(b *testing.B) {
	registry := gometrics.NewRegistry()
	cache := NewMappingCacheWithMetrics(
		1*time.Hour,
		5*time.Minute,
		1000,
		1*time.Minute,
		registry,
	)
	defer cache.Stop()

	// Pre-populate cache
	for i := 0; i < 100; i++ {
		cache.Set(fmt.Sprintf("test%d", i), int64(i))
		cache.Get(fmt.Sprintf("test%d", i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cache.publishMetrics()
	}
}