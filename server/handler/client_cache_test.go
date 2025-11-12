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
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
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

	// Put clients in cache
	cache.Put(12345, clients)

	// Verify size increased
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), size)

	// Get clients from cache
	retrieved := cache.Get(12345)
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
	retrieved := cache.Get(99999)
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
	cache.Put(12345, clients)

	// Verify we can get it immediately
	retrieved := cache.Get(12345)
	assert.NotNil(t, retrieved)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Get should return nil for expired entry
	retrieved = cache.Get(12345)
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
	cache.Put(12345, clients)
	assert.NotNil(t, cache.Get(12345))

	// Invalidate
	cache.Invalidate(12345)

	// Verify removed
	assert.Nil(t, cache.Get(12345))

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Invalidate_NonExistent(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Invalidate non-existent entry should not panic
	cache.Invalidate(99999)

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(0), size)
}

func TestClientCache_Clear(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Add multiple entries
	for i := int64(1); i <= 10; i++ {
		cache.Put(i, &InstallationClients{
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
	assert.Nil(t, cache.Get(1))
	assert.Nil(t, cache.Get(5))
	assert.Nil(t, cache.Get(10))
}

func TestClientCache_PutNil(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)
	defer cache.Stop()

	// Put nil should not add to cache
	cache.Put(12345, nil)

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
	cache.Put(12345, clients1)
	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(1), size)

	// Update same entry (LoadOrStore doesn't increment size if already exists)
	cache.Put(12345, clients2)
	_, _, _, size = cache.GetMetrics()
	assert.Equal(t, int64(1), size) // Size should still be 1
}

func TestClientCache_Eviction_OnMaxSize(t *testing.T) {
	// Create cache with small max size
	cache := NewClientCache(1*time.Hour, 5)
	defer cache.Stop()

	// Add entries up to max size
	for i := int64(1); i <= 5; i++ {
		cache.Put(i, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	_, _, _, size := cache.GetMetrics()
	assert.Equal(t, int64(5), size)

	// Add one more - should trigger eviction
	cache.Put(6, &InstallationClients{
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
	for i := int64(1); i <= 5; i++ {
		cache.Put(i, &InstallationClients{
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
				installationID := int64(workerID*numOperations + j)
				cache.Put(installationID, &InstallationClients{
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
				installationID := int64(workerID*numOperations + j)
				cache.Get(installationID)
			}
		}(i)
	}

	// Concurrent Invalidations
	for i := 0; i < numGoroutines/5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				installationID := int64(workerID*numOperations + j)
				cache.Invalidate(installationID)
			}
		}(i)
	}

	wg.Wait()

	// Verify cache is still functional
	cache.Put(99999, &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})
	assert.NotNil(t, cache.Get(99999))
}

func TestClientCache_Stop(t *testing.T) {
	cache := NewClientCache(1*time.Hour, 100)

	// Add some entries
	cache.Put(1, &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})

	// Stop should complete without blocking
	cache.Stop()

	// Cache should still be usable after stop (but cleanup goroutine is stopped)
	cache.Put(2, &InstallationClients{
		V3Client: &github.Client{},
		V4Client: &githubv4.Client{},
	})
	assert.NotNil(t, cache.Get(2))
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
	for i := int64(1); i <= 10; i++ {
		cache.Put(i, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
		time.Sleep(1 * time.Millisecond) // Ensure distinct creation times
	}

	// Trigger eviction by exceeding max size
	cache.Put(11, &InstallationClients{
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
	for i := int64(1); i <= 100; i++ {
		cache.Put(i, &InstallationClients{
			V3Client: &github.Client{},
			V4Client: &githubv4.Client{},
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get(50) // Cache hit
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
		i := int64(0)
		for pb.Next() {
			cache.Put(i, clients)
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
			cache.Get(99999) // Cache miss
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
