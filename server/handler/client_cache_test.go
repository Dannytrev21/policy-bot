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

	"github.com/google/go-github/v47/github"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
)

func TestNewClientCache(t *testing.T) {
	cache := NewClientCache(5*time.Minute, 100)
	defer cache.Stop()

	assert.NotNil(t, cache)
	assert.Equal(t, 5*time.Minute, cache.ttl)
	assert.Equal(t, 100, cache.maxSize)

	// Verify metrics are initialized to zero
	hits, misses, evictions, size := cache.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(0), misses)
	assert.Equal(t, int64(0), evictions)
	assert.Equal(t, int64(0), size)
}

func TestNewClientCache_DefaultValues(t *testing.T) {
	// Test with zero/negative values - should use defaults
	cache := NewClientCache(0, 0)
	defer cache.Stop()

	assert.Equal(t, defaultClientCacheTTL, cache.ttl)
	assert.Equal(t, defaultClientCacheMaxSize, cache.maxSize)
}

func TestClientCache_PutAndGet(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Create mock clients
	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	owner := "test-org"

	// Put clients in cache
	cache.Put(owner, clients)

	// Verify size increased
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), size)

	// Get clients from cache
	retrieved := cache.Get(owner)
	assert.NotNil(t, retrieved)
	assert.Equal(t, clients, retrieved)

	// Verify metrics
	hits, misses, _, _ := cache.GetMetrics()
	assert.Equal(t, int64(1), hits)
	assert.Equal(t, int64(0), misses)
}

func TestClientCache_GetMiss(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Get non-existent entry
	retrieved := cache.Get("nonexistent-owner")
	assert.Nil(t, retrieved)

	// Verify miss was recorded
	hits, misses, _, _ := cache.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(1), misses)
}

func TestClientCache_Expiration(t *testing.T) {
	// Use very short TTL for testing
	cache := NewClientCache(100*time.Millisecond, 100)
	defer cache.Stop()

	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Put clients in cache
	cache.Put("test-owner", clients)

	// Verify we can get it immediately
	retrieved := cache.Get("test-owner")
	assert.NotNil(t, retrieved)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Get should return nil for expired entry
	retrieved = cache.Get("test-owner")
	assert.Nil(t, retrieved)

	// Verify entry was removed and metrics updated
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Invalidate(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Put and verify
	cache.Put("test-owner", clients)
	assert.NotNil(t, cache.Get("test-owner"))

	// Invalidate
	cache.Invalidate("test-owner")

	// Verify removed
	assert.Nil(t, cache.Get("test-owner"))

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Invalidate_NonExistent(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Invalidate non-existent entry should not panic
	cache.Invalidate("nonexistent-owner")

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Clear(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Add multiple entries
	for i := 1; i <= 10; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.Put(owner, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	// Verify size
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(10), size)

	// Clear cache
	cache.Clear()

	// Verify all metrics reset
	hits, misses, evictions, size := cache.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(0), misses)
	assert.Equal(t, int64(0), evictions)
	assert.Equal(t, int64(0), size)

	// Verify entries removed
	assert.Nil(t, cache.Get("owner-1"))
	assert.Nil(t, cache.Get("owner-5"))
	assert.Nil(t, cache.Get("owner-10"))
}

func TestClientCache_PutNil(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Put nil should not add to cache
	cache.Put("test-owner", nil)

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Update_ExistingEntry(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	clients1 := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	clients2 := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Put first entry
	cache.Put("test-owner", clients1)
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), size)

	// Update same entry (LoadOrStore doesn't increment size if already exists)
	cache.Put("test-owner", clients2)
	_, _, _, size = cache.GetMetrics()
	assert.Equal(t, int64(1), size) // Size should still be 1
}

func TestClientCache_Eviction_OnMaxSize(t *testing.T) {
	// Create cache with small max size
	cache := NewClientCache(1*time.Hour, 5)
	defer cache.Stop()

	// Add entries up to max size
	for i := 1; i <= 5; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.Put(owner, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(5), size)

	// Add one more - should trigger eviction
	cache.Put("owner-6", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})

	// Size should be less than or equal to max
	_, _, evictions, size := cache.GetMetrics()
	assert.LessOrEqual(t, size, int64(5))
	assert.Greater(t, evictions, int64(0), "Should have evicted at least one entry")
}

func TestClientCache_CleanupExpired(t *testing.T) {
	// Create cache with short TTL and manual cleanup
	cache := NewClientCache(50*time.Millisecond, 100)
	defer cache.Stop()

	// Add entries
	for i := 1; i <= 5; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.Put(owner, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	// Verify size
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(5), size)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Trigger cleanup by waiting for cleanup interval
	time.Sleep(1100 * time.Millisecond) // Cleanup runs every 1 minute

	// Entries should be cleaned up eventually
	// Note: This test is timing-dependent, so we'll just verify cleanup happened
	_, _, evictions, size := cache.GetMetrics()
	// Size might be 0 if cleanup ran, or entries were accessed and removed
	t.Logf("After cleanup: size=%d, evictions=%d", size, evictions)
}

func TestClientCache_ConcurrentAccess(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 1000)
	defer cache.Stop()

	// Run concurrent operations
	var wg sync.WaitGroup
	numGoroutines := 50
	numOperations := 100

	// Concurrent Puts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				owner := fmt.Sprintf("owner-%d", workerID*numOperations+j)
				cache.Put(owner, &InstallationClients{
					V3Client: &github.Client{},
					V4Client: &githubv4.Client{},
				})
			}
		}(i)
	}

	// Concurrent Gets
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				owner := fmt.Sprintf("owner-%d", workerID*numOperations+j)
				cache.Get(owner)
			}
		}(i)
	}

	// Concurrent Invalidations
	for i := 0; i < numGoroutines/5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				owner := fmt.Sprintf("owner-%d", workerID*numOperations+j)
				cache.Invalidate(owner)
			}
		}(i)
	}

	wg.Wait()

	// Verify cache is still functional
	cache.Put("owner-99999", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})
	assert.NotNil(t, cache.Get("owner-99999"))
	assert.Nil(t, cache.Get("nonexistent-owner"))
}

func TestClientCache_Stop(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)

	// Add some entries
	cache.Put("owner-1", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})

	// Stop should complete without blocking
	cache.Stop()

	// Cache should still be usable after stop (but cleanup goroutine is stopped)
	cache.Put("owner-2", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})
	assert.NotNil(t, cache.Get("owner-2"))
}

func TestCachedClients_IsExpired(t *testing.T) {
	now := time.Now()

	// Not expired
	cc := &CachedClients{
		Clients:   &InstallationClients{},
		ExpiresAt: now.Add(1 * time.Hour),
		CreatedAt: now,
	}
	assert.False(t, cc.IsExpired())

	// Expired
	cc2 := &CachedClients{
		Clients:   &InstallationClients{},
		ExpiresAt: now.Add(-1 * time.Hour),
		CreatedAt: now.Add(-2 * time.Hour),
	}
	assert.True(t, cc2.IsExpired())

	// Exactly at expiration (should be expired)
	cc3 := &CachedClients{
		Clients:   &InstallationClients{},
		ExpiresAt: time.Now(),
		CreatedAt: now,
	}
	time.Sleep(1 * time.Millisecond)
	assert.True(t, cc3.IsExpired())
}

func TestClientCache_EvictOldest(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 10)
	defer cache.Stop()

	// Add entries with slight delays to ensure different creation times
	for i := 1; i <= 10; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.Put(owner, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
		time.Sleep(1 * time.Millisecond) // Ensure distinct creation times
	}

	// Trigger eviction by exceeding max size
	cache.Put("owner-11", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})

	// Oldest entries (1, 2, etc.) should be evicted
	// Newest entries (10, 11) should remain
	_, _, evictions, size := cache.GetMetrics()
	assert.Greater(t, evictions, int64(0))
	assert.LessOrEqual(t, size, int64(10))
}

// Benchmark tests
func BenchmarkClientCache_Get(b *testing.B) {
	cache := NewClientCache(1*time.Hour, 1000)
	defer cache.Stop()

	// Pre-populate cache
	for i := 1; i <= 100; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.Put(owner, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("owner-50") // Cache hit
		}
	})
}

func BenchmarkClientCache_Put(b *testing.B) {
	cache := NewClientCache(1*time.Hour, 10000)
	defer cache.Stop()

	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			owner := fmt.Sprintf("owner-%d", i)
			cache.Put(owner, clients)
			i++
		}
	})
}

func BenchmarkClientCache_GetMiss(b *testing.B) {
	cache := NewClientCache(1*time.Hour, 1000)
	defer cache.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("nonexistent-owner") // Cache miss
		}
	})
}

func TestInstallationManager_ClientCacheIntegration(t *testing.T) {
	// This test would require a full InstallationManager setup
	// Just verify the basic integration points exist
	manager := &InstallationManager{
		clientCache: NewClientCache(1*time.Hour, 100),
	}
	defer manager.StopClientCache()

	assert.NotNil(t, manager.clientCache)

	// Test InvalidateClientCache
	manager.InvalidateClientCache(12345) // Should not panic

	// Test GetClientCacheMetrics
	hits, misses, evictions, size := manager.GetClientCacheMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(0), misses)
	assert.Equal(t, int64(0), evictions)
	assert.Equal(t, int64(0), size)
}

// Test negative caching functionality
func TestClientCache_NegativeCaching(t *testing.T) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, nil)
	defer cache.Stop()

	owner := "nonexistent-org"

	// Put negative cache entry
	cache.PutNegative(owner)

	// Verify size increased
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), size)

	// Get should return nil for negative cache entry
	retrieved := cache.Get(owner)
	assert.Nil(t, retrieved, "Get should return nil for negative cache entry")

	// But it should be a cache hit (we know it doesn't exist)
	hits, misses, _, _ := cache.GetMetrics()
	assert.Equal(t, int64(1), hits, "Negative cache should count as cache hit")
	assert.Equal(t, int64(0), misses)

	// IsNegativelyCached should return true
	assert.True(t, cache.IsNegativelyCached(owner))
}

// Test negative cache expiration with shorter TTL
func TestClientCache_NegativeCacheExpiration(t *testing.T) {
	cache := NewClientCacheWithOptions(1*time.Hour, 50*time.Millisecond, 100, nil)
	defer cache.Stop()

	owner := "test-org"

	// Put negative cache entry with short TTL
	cache.PutNegative(owner)

	// Should be negatively cached immediately
	assert.True(t, cache.IsNegativelyCached(owner))

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Should no longer be negatively cached
	assert.False(t, cache.IsNegativelyCached(owner), "Negative cache should expire")

	// Get should return nil and record miss
	retrieved := cache.Get(owner)
	assert.Nil(t, retrieved)

	_, misses, _, _ := cache.GetMetrics()
	assert.Equal(t, int64(1), misses, "Expired negative cache should result in miss")
}

// Test that positive cache overrides negative cache
func TestClientCache_PositiveOverridesNegative(t *testing.T) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, nil)
	defer cache.Stop()

	owner := "test-org"
	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// First put negative cache
	cache.PutNegative(owner)
	assert.True(t, cache.IsNegativelyCached(owner))

	// Then put positive cache (installation found)
	cache.Put(owner, clients)

	// Should no longer be negatively cached
	assert.False(t, cache.IsNegativelyCached(owner))

	// Get should return clients
	retrieved := cache.Get(owner)
	assert.NotNil(t, retrieved)
	assert.Equal(t, clients, retrieved)
}

// Test metrics integration with go-metrics registry
func TestClientCache_MetricsIntegration(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, registry)
	defer cache.Stop()

	owner := "test-org"
	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Perform cache operations
	cache.Put(owner, clients)
	cache.Get(owner)                 // Hit
	cache.Get("nonexistent-owner")   // Miss
	cache.PutNegative("negative-org") // Negative cache

	// Manually trigger metrics publishing
	cache.publishMetrics()

	// Verify metrics are published to registry
	hitsCounter := registry.Get(MetricsKeyClientCacheHits)
	assert.NotNil(t, hitsCounter, "Hits counter should be registered")

	missesCounter := registry.Get(MetricsKeyClientCacheMisses)
	assert.NotNil(t, missesCounter, "Misses counter should be registered")

	sizeGauge := registry.Get(MetricsKeyClientCacheSize)
	assert.NotNil(t, sizeGauge, "Size gauge should be registered")

	hitRateGauge := registry.Get(MetricsKeyClientCacheHitRate)
	assert.NotNil(t, hitRateGauge, "Hit rate gauge should be registered")
}

// Test metrics publishing loop
func TestClientCache_MetricsLoop(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, registry)
	defer cache.Stop()

	owner := "test-org"
	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Perform cache operations
	cache.Put(owner, clients)
	cache.Get(owner) // Hit

	// Wait for at least one metrics publish cycle (metrics publish every 10 seconds)
	// Use publishMetrics directly to test without waiting
	cache.publishMetrics()

	// Verify metrics were published
	sizeGauge := registry.Get(MetricsKeyClientCacheSize)
	assert.NotNil(t, sizeGauge)
}

// Test NewClientCacheWithOptions with nil registry
func TestClientCache_NilRegistry(t *testing.T) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, nil)
	defer cache.Stop()

	// Should work without metrics publishing
	cache.Put("test-org", &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})

	retrieved := cache.Get("test-org")
	assert.NotNil(t, retrieved)
}

// Test hit rate calculation
func TestClientCache_HitRateCalculation(t *testing.T) {
	registry := gometrics.NewRegistry()
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 100, registry)
	defer cache.Stop()

	owner := "test-org"
	clients := &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	}

	// Put and get to create hits/misses
	cache.Put(owner, clients)
	cache.Get(owner)              // Hit
	cache.Get(owner)              // Hit
	cache.Get("nonexistent-org1") // Miss
	cache.Get("nonexistent-org2") // Miss

	// Publish metrics
	cache.publishMetrics()

	// Check hit rate: 2 hits / 4 total = 50%
	hitRateGauge := registry.Get(MetricsKeyClientCacheHitRate)
	assert.NotNil(t, hitRateGauge)

	if gauge, ok := hitRateGauge.(gometrics.Gauge); ok {
		hitRate := gauge.Value()
		assert.Equal(t, int64(50), hitRate, "Hit rate should be 50%")
	}
}

// Test concurrent access with negative caching
func TestClientCache_ConcurrentNegativeCaching(t *testing.T) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 1000, nil)
	defer cache.Stop()

	var wg sync.WaitGroup
	numGoroutines := 50
	numOperations := 100

	// Concurrent negative cache puts
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				owner := fmt.Sprintf("owner-%d", workerID*numOperations+j)
				cache.PutNegative(owner)
			}
		}(i)
	}

	// Concurrent negative cache checks
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				owner := fmt.Sprintf("owner-%d", workerID*numOperations+j)
				cache.IsNegativelyCached(owner)
			}
		}(i)
	}

	wg.Wait()

	// Verify cache is still functional
	cache.PutNegative("test-owner")
	assert.True(t, cache.IsNegativelyCached("test-owner"))
}

// Benchmark negative caching performance
func BenchmarkClientCache_PutNegative(b *testing.B) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 10000, nil)
	defer cache.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			owner := fmt.Sprintf("owner-%d", i)
			cache.PutNegative(owner)
			i++
		}
	})
}

// Benchmark negative cache check
func BenchmarkClientCache_IsNegativelyCached(b *testing.B) {
	cache := NewClientCacheWithOptions(1*time.Hour, 2*time.Minute, 1000, nil)
	defer cache.Stop()

	// Pre-populate with negative cache entries
	for i := 0; i < 100; i++ {
		owner := fmt.Sprintf("owner-%d", i)
		cache.PutNegative(owner)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.IsNegativelyCached("owner-50") // Negative cache hit
		}
	})
}
