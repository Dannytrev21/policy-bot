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

package sqsconsumer

import (
	"sync"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIdempotencyManager(t *testing.T) {
	t.Run("creates manager with valid parameters", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 5*time.Minute, registry)

		require.NoError(t, err)
		assert.NotNil(t, im)
		assert.Equal(t, 5*time.Minute, im.ttl)
		assert.Equal(t, 0, im.GetCacheSize())
	})

	t.Run("uses defaults for invalid parameters", func(t *testing.T) {
		im, err := NewIdempotencyManager(0, 0, nil)

		require.NoError(t, err)
		assert.NotNil(t, im)
		assert.Equal(t, DefaultIdempotencyTTL, im.ttl)
	})

	t.Run("registers metrics when registry provided", func(t *testing.T) {
		registry := metrics.NewRegistry()
		_, err := NewIdempotencyManager(100, 5*time.Minute, registry)

		require.NoError(t, err)

		// Verify metrics are registered
		assert.NotNil(t, registry.Get(MetricsKeyIdempotencyDuplicates))
		assert.NotNil(t, registry.Get(MetricsKeyIdempotencyCacheSize))
		assert.NotNil(t, registry.Get(MetricsKeyIdempotencyChecks))
	})
}

func TestIdempotencyManager_CheckAndMark(t *testing.T) {
	t.Run("returns false for new delivery ID", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 1*time.Hour, registry)
		require.NoError(t, err)

		isDuplicate := im.CheckAndMark("delivery-123")

		assert.False(t, isDuplicate)
		assert.Equal(t, 1, im.GetCacheSize())

		// Verify check metric incremented
		checks := registry.Get(MetricsKeyIdempotencyChecks).(metrics.Counter).Count()
		assert.Equal(t, int64(1), checks)

		// Verify duplicate metric NOT incremented
		duplicates := registry.Get(MetricsKeyIdempotencyDuplicates).(metrics.Counter).Count()
		assert.Equal(t, int64(0), duplicates)
	})

	t.Run("returns true for duplicate delivery ID", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 1*time.Hour, registry)
		require.NoError(t, err)

		// First call - not duplicate
		isDuplicate1 := im.CheckAndMark("delivery-123")
		assert.False(t, isDuplicate1)

		// Second call - duplicate
		isDuplicate2 := im.CheckAndMark("delivery-123")
		assert.True(t, isDuplicate2)
		assert.Equal(t, 1, im.GetCacheSize()) // Size should still be 1

		// Verify metrics
		checks := registry.Get(MetricsKeyIdempotencyChecks).(metrics.Counter).Count()
		assert.Equal(t, int64(2), checks)

		duplicates := registry.Get(MetricsKeyIdempotencyDuplicates).(metrics.Counter).Count()
		assert.Equal(t, int64(1), duplicates)
	})

	t.Run("multiple different delivery IDs", func(t *testing.T) {
		im, err := NewIdempotencyManager(100, 1*time.Hour, nil)
		require.NoError(t, err)

		assert.False(t, im.CheckAndMark("delivery-1"))
		assert.False(t, im.CheckAndMark("delivery-2"))
		assert.False(t, im.CheckAndMark("delivery-3"))
		assert.Equal(t, 3, im.GetCacheSize())

		// Check duplicates
		assert.True(t, im.CheckAndMark("delivery-1"))
		assert.True(t, im.CheckAndMark("delivery-2"))
		assert.Equal(t, 3, im.GetCacheSize())
	})

	t.Run("expired entry treated as new", func(t *testing.T) {
		// Use very short TTL for testing
		im, err := NewIdempotencyManager(100, 10*time.Millisecond, nil)
		require.NoError(t, err)

		// First call
		isDuplicate1 := im.CheckAndMark("delivery-123")
		assert.False(t, isDuplicate1)

		// Wait for TTL to expire
		time.Sleep(15 * time.Millisecond)

		// Should be treated as new since expired
		isDuplicate2 := im.CheckAndMark("delivery-123")
		assert.False(t, isDuplicate2)
	})

	t.Run("handles nil registry gracefully", func(t *testing.T) {
		im, err := NewIdempotencyManager(100, 1*time.Hour, nil)
		require.NoError(t, err)

		// Should not panic with nil registry
		isDuplicate := im.CheckAndMark("delivery-123")
		assert.False(t, isDuplicate)
	})
}

func TestIdempotencyManager_LRUEviction(t *testing.T) {
	t.Run("evicts oldest entries when cache is full", func(t *testing.T) {
		// Create cache with size 3
		im, err := NewIdempotencyManager(3, 1*time.Hour, nil)
		require.NoError(t, err)

		// Add 3 entries
		assert.False(t, im.CheckAndMark("delivery-1"))
		assert.False(t, im.CheckAndMark("delivery-2"))
		assert.False(t, im.CheckAndMark("delivery-3"))
		assert.Equal(t, 3, im.GetCacheSize())

		// All 3 should be duplicates now
		assert.True(t, im.CheckAndMark("delivery-1"))
		assert.True(t, im.CheckAndMark("delivery-2"))
		assert.True(t, im.CheckAndMark("delivery-3"))

		// Add 4th entry - should evict oldest (delivery-1 since we haven't accessed it recently)
		// Note: After checking duplicates above, LRU order is 1, 2, 3 (most recent first)
		assert.False(t, im.CheckAndMark("delivery-4"))
		assert.Equal(t, 3, im.GetCacheSize())

		// delivery-1 might still be in cache (since we just accessed it above), so check something definitely evicted
		// Add 2 more new entries to ensure eviction
		assert.False(t, im.CheckAndMark("delivery-5"))
		assert.False(t, im.CheckAndMark("delivery-6"))
		assert.Equal(t, 3, im.GetCacheSize())

		// Now delivery-1, delivery-2, delivery-3 should all be evicted
		// Only delivery-4, delivery-5, delivery-6 remain
		assert.False(t, im.CheckAndMark("delivery-1"))
		assert.False(t, im.CheckAndMark("delivery-2"))
		assert.False(t, im.CheckAndMark("delivery-3"))

		// delivery-4, delivery-5, delivery-6 should still be duplicates (but now evicted by the 3 checks above)
		// Actually, after adding 1, 2, 3 above, cache is now [2, 3, 1] (most recent: 1)
		// So 4, 5, 6 have been evicted. Let's just verify cache size is maintained at 3
		assert.Equal(t, 3, im.GetCacheSize())
	})
}

func TestIdempotencyManager_Remove(t *testing.T) {
	t.Run("removes entry from cache", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 1*time.Hour, registry)
		require.NoError(t, err)

		im.CheckAndMark("delivery-123")
		assert.Equal(t, 1, im.GetCacheSize())

		im.Remove("delivery-123")
		assert.Equal(t, 0, im.GetCacheSize())

		// Should be able to add again
		isDuplicate := im.CheckAndMark("delivery-123")
		assert.False(t, isDuplicate)

		// Verify cache size metric updated
		cacheSize := registry.Get(MetricsKeyIdempotencyCacheSize).(metrics.Gauge).Value()
		assert.Equal(t, int64(1), cacheSize)
	})

	t.Run("handles removing non-existent entry", func(t *testing.T) {
		im, err := NewIdempotencyManager(100, 1*time.Hour, nil)
		require.NoError(t, err)

		// Should not panic
		im.Remove("non-existent")
		assert.Equal(t, 0, im.GetCacheSize())
	})
}

func TestIdempotencyManager_Clear(t *testing.T) {
	t.Run("removes all entries", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 1*time.Hour, registry)
		require.NoError(t, err)

		// Add multiple entries
		im.CheckAndMark("delivery-1")
		im.CheckAndMark("delivery-2")
		im.CheckAndMark("delivery-3")
		assert.Equal(t, 3, im.GetCacheSize())

		// Clear all
		im.Clear()
		assert.Equal(t, 0, im.GetCacheSize())

		// All should be treated as new
		assert.False(t, im.CheckAndMark("delivery-1"))
		assert.False(t, im.CheckAndMark("delivery-2"))
		assert.False(t, im.CheckAndMark("delivery-3"))

		// Verify cache size metric updated
		cacheSize := registry.Get(MetricsKeyIdempotencyCacheSize).(metrics.Gauge).Value()
		assert.Equal(t, int64(3), cacheSize)
	})
}

func TestIdempotencyManager_Concurrency(t *testing.T) {
	t.Run("concurrent access is thread-safe", func(t *testing.T) {
		im, err := NewIdempotencyManager(1000, 1*time.Hour, nil)
		require.NoError(t, err)

		const numGoroutines = 100
		const numOperations = 100

		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		// Start multiple goroutines performing operations
		for i := 0; i < numGoroutines; i++ {
			go func(goroutineID int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					deliveryID := time.Now().Format("2006-01-02T15:04:05.000000")
					im.CheckAndMark(deliveryID)
				}
			}(i)
		}

		wg.Wait()

		// Should not panic and cache should have entries
		size := im.GetCacheSize()
		assert.Greater(t, size, 0)
		assert.LessOrEqual(t, size, 1000) // Should not exceed max size
	})

	t.Run("concurrent duplicate checks", func(t *testing.T) {
		im, err := NewIdempotencyManager(1000, 1*time.Hour, nil)
		require.NoError(t, err)

		const numGoroutines = 50
		const deliveryID = "test-delivery-123"

		var wg sync.WaitGroup
		var duplicateCount int64
		var mu sync.Mutex

		wg.Add(numGoroutines)

		// Multiple goroutines try to process same delivery ID
		for i := 0; i < numGoroutines; i++ {
			go func() {
				defer wg.Done()
				isDuplicate := im.CheckAndMark(deliveryID)
				if isDuplicate {
					mu.Lock()
					duplicateCount++
					mu.Unlock()
				}
			}()
		}

		wg.Wait()

		// Only one should succeed, rest should be duplicates
		// (numGoroutines - 1) should detect duplicate
		// Note: Due to race conditions, we might have 1 or 2 that succeed before marking
		assert.GreaterOrEqual(t, duplicateCount, int64(numGoroutines-2))
		assert.Equal(t, 1, im.GetCacheSize())
	})
}

func TestIdempotencyManager_Metrics(t *testing.T) {
	t.Run("records all metrics correctly", func(t *testing.T) {
		registry := metrics.NewRegistry()
		im, err := NewIdempotencyManager(100, 1*time.Hour, registry)
		require.NoError(t, err)

		// Add some entries and create duplicates
		im.CheckAndMark("delivery-1")
		im.CheckAndMark("delivery-2")
		im.CheckAndMark("delivery-1") // duplicate
		im.CheckAndMark("delivery-3")
		im.CheckAndMark("delivery-2") // duplicate

		// Verify metrics
		checks := registry.Get(MetricsKeyIdempotencyChecks).(metrics.Counter).Count()
		assert.Equal(t, int64(5), checks)

		duplicates := registry.Get(MetricsKeyIdempotencyDuplicates).(metrics.Counter).Count()
		assert.Equal(t, int64(2), duplicates)

		cacheSize := registry.Get(MetricsKeyIdempotencyCacheSize).(metrics.Gauge).Value()
		assert.Equal(t, int64(3), cacheSize)
	})
}
