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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallationRegistry_NewRegistry(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	require.NotNil(t, registry)
	assert.NotNil(t, registry.installations)
	assert.Equal(t, 1*time.Hour, registry.positiveTTL)
	assert.Equal(t, 5*time.Minute, registry.negativeTTL)
	assert.Equal(t, 0, registry.GetCacheSize())
}

func TestInstallationRegistry_CheckUnknown(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	status, cacheHit := registry.Check(installationID)

	assert.Equal(t, InstallationUnknown, status)
	assert.False(t, cacheHit)

	// Verify metrics
	hits, misses, apiCalls := registry.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(1), misses)
	assert.Equal(t, int64(0), apiCalls)
}

func TestInstallationRegistry_MarkInstalled_ThenCheck(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as installed
	registry.MarkInstalled(installationID)
	assert.Equal(t, 1, registry.GetCacheSize())

	// Check should return cached result
	status, cacheHit := registry.Check(installationID)

	assert.Equal(t, InstallationExists, status)
	assert.True(t, cacheHit)

	// Verify metrics
	hits, misses, _ := registry.GetMetrics()
	assert.Equal(t, int64(1), hits)
	assert.Equal(t, int64(0), misses)
}

func TestInstallationRegistry_MarkNotInstalled_ThenCheck(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as not installed
	registry.MarkNotInstalled(installationID)
	assert.Equal(t, 1, registry.GetCacheSize())

	// Check should return cached result
	status, cacheHit := registry.Check(installationID)

	assert.Equal(t, InstallationNotFound, status)
	assert.True(t, cacheHit)

	// Verify metrics
	hits, misses, _ := registry.GetMetrics()
	assert.Equal(t, int64(1), hits)
	assert.Equal(t, int64(0), misses)
}

func TestInstallationRegistry_PositiveTTLExpiration(t *testing.T) {
	// Use very short TTL for testing
	registry := NewInstallationRegistry(50*time.Millisecond, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as installed
	registry.MarkInstalled(installationID)

	// Should be cached immediately
	status, cacheHit := registry.Check(installationID)
	assert.Equal(t, InstallationExists, status)
	assert.True(t, cacheHit)

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	status, cacheHit = registry.Check(installationID)
	assert.Equal(t, InstallationUnknown, status)
	assert.False(t, cacheHit)

	// Cache should be empty after expiration check
	assert.Equal(t, 0, registry.GetCacheSize())
}

func TestInstallationRegistry_NegativeTTLExpiration(t *testing.T) {
	// Use very short TTL for testing
	registry := NewInstallationRegistry(1*time.Hour, 50*time.Millisecond, nil)
	installationID := int64(12345)

	// Mark as not installed
	registry.MarkNotInstalled(installationID)

	// Should be cached immediately
	status, cacheHit := registry.Check(installationID)
	assert.Equal(t, InstallationNotFound, status)
	assert.True(t, cacheHit)

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	status, cacheHit = registry.Check(installationID)
	assert.Equal(t, InstallationUnknown, status)
	assert.False(t, cacheHit)
}

func TestInstallationRegistry_Remove(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as installed
	registry.MarkInstalled(installationID)
	assert.Equal(t, 1, registry.GetCacheSize())

	// Remove from cache
	registry.Remove(installationID)
	assert.Equal(t, 0, registry.GetCacheSize())

	// Check should return unknown
	status, cacheHit := registry.Check(installationID)
	assert.Equal(t, InstallationUnknown, status)
	assert.False(t, cacheHit)
}

func TestInstallationRegistry_RecordAPICall(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Record API calls
	registry.RecordAPICall()
	registry.RecordAPICall()
	registry.RecordAPICall()

	// Verify metrics
	_, _, apiCalls := registry.GetMetrics()
	assert.Equal(t, int64(3), apiCalls)
}

func TestInstallationRegistry_Clear(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Add multiple entries
	registry.MarkInstalled(1)
	registry.MarkInstalled(2)
	registry.MarkNotInstalled(3)
	assert.Equal(t, 3, registry.GetCacheSize())

	// Clear cache
	registry.Clear()
	assert.Equal(t, 0, registry.GetCacheSize())

	// All entries should be unknown
	status1, hit1 := registry.Check(1)
	status2, hit2 := registry.Check(2)
	status3, hit3 := registry.Check(3)

	assert.Equal(t, InstallationUnknown, status1)
	assert.Equal(t, InstallationUnknown, status2)
	assert.Equal(t, InstallationUnknown, status3)
	assert.False(t, hit1)
	assert.False(t, hit2)
	assert.False(t, hit3)
}

func TestInstallationRegistry_OverwriteEntry(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Mark as not installed
	registry.MarkNotInstalled(installationID)
	status, cacheHit := registry.Check(installationID)
	assert.Equal(t, InstallationNotFound, status)
	assert.True(t, cacheHit)

	// Mark as installed (overwrite)
	registry.MarkInstalled(installationID)
	status, cacheHit = registry.Check(installationID)
	assert.Equal(t, InstallationExists, status)
	assert.True(t, cacheHit)

	// Should still be only one entry
	assert.Equal(t, 1, registry.GetCacheSize())
}

func TestInstallationRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Pre-populate with some entries
	for i := int64(1); i <= 10; i++ {
		registry.MarkInstalled(i)
	}

	// Concurrent reads and writes
	done := make(chan bool)

	// Readers
	for i := 0; i < 10; i++ {
		go func(id int64) {
			for j := 0; j < 100; j++ {
				registry.Check(id)
			}
			done <- true
		}(int64(i % 10))
	}

	// Writers
	for i := 0; i < 5; i++ {
		go func(id int64) {
			for j := 0; j < 50; j++ {
				if j%2 == 0 {
					registry.MarkInstalled(id)
				} else {
					registry.MarkNotInstalled(id)
				}
			}
			done <- true
		}(int64(i))
	}

	// Wait for all goroutines
	for i := 0; i < 15; i++ {
		<-done
	}

	// Should not crash or deadlock
	assert.True(t, true)
}

func TestInstallationRegistry_MetricsAccuracy(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Scenario: 2 misses, then 3 hits, then 1 API call
	installationID := int64(12345)

	// First check - miss
	registry.Check(installationID)
	hits, misses, apiCalls := registry.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(1), misses)
	assert.Equal(t, int64(0), apiCalls)

	// Second check - miss
	registry.Check(installationID)
	hits, misses, apiCalls = registry.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(2), misses)

	// Mark installed and record API call
	registry.MarkInstalled(installationID)
	registry.RecordAPICall()
	hits, misses, apiCalls = registry.GetMetrics()
	assert.Equal(t, int64(0), hits)
	assert.Equal(t, int64(2), misses)
	assert.Equal(t, int64(1), apiCalls)

	// Three checks - all hits
	registry.Check(installationID)
	registry.Check(installationID)
	registry.Check(installationID)
	hits, misses, apiCalls = registry.GetMetrics()
	assert.Equal(t, int64(3), hits)
	assert.Equal(t, int64(2), misses)
	assert.Equal(t, int64(1), apiCalls)
}

func TestInstallationRegistry_DifferentTTLs(t *testing.T) {
	// Test that positive and negative entries have different TTLs
	positiveTTL := 200 * time.Millisecond
	negativeTTL := 50 * time.Millisecond

	registry := NewInstallationRegistry(positiveTTL, negativeTTL, nil)

	positiveID := int64(1)
	negativeID := int64(2)

	// Mark one positive, one negative
	registry.MarkInstalled(positiveID)
	registry.MarkNotInstalled(negativeID)

	// Both should be cached
	status1, hit1 := registry.Check(positiveID)
	status2, hit2 := registry.Check(negativeID)
	assert.Equal(t, InstallationExists, status1)
	assert.Equal(t, InstallationNotFound, status2)
	assert.True(t, hit1)
	assert.True(t, hit2)

	// Wait for negative TTL to expire but positive still valid
	time.Sleep(100 * time.Millisecond)

	// Negative should be expired, positive still cached
	status1, hit1 = registry.Check(positiveID)
	status2, hit2 = registry.Check(negativeID)
	assert.Equal(t, InstallationExists, status1)
	assert.Equal(t, InstallationUnknown, status2)
	assert.True(t, hit1)
	assert.False(t, hit2)

	// Wait for positive TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Both should be expired now
	status1, hit1 = registry.Check(positiveID)
	assert.Equal(t, InstallationUnknown, status1)
	assert.False(t, hit1)
}

// Tests for go-metrics integration

func TestInstallationRegistry_GoMetrics_Registration(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)

	// Verify all metrics are registered
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryCacheHits))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryCacheMisses))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryAPICalls))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryCacheSize))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryPositiveCache))
	assert.NotNil(t, metricsRegistry.Get(MetricsKeyRegistryNegativeCache))

	// Verify initial values
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryCacheHits).(gometrics.Counter).Count())
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryCacheMisses).(gometrics.Counter).Count())
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryAPICalls).(gometrics.Counter).Count())
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryCacheSize).(gometrics.Gauge).Value())
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryPositiveCache).(gometrics.Gauge).Value())
	assert.Equal(t, int64(0), metricsRegistry.Get(MetricsKeyRegistryNegativeCache).(gometrics.Gauge).Value())

	// Should not panic with nil registry
	_ = registry
}

func TestInstallationRegistry_GoMetrics_CacheHits(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)
	installationID := int64(12345)

	// Mark as installed
	registry.MarkInstalled(installationID)

	// Check should record a cache hit
	_, _ = registry.Check(installationID)

	// Verify metric was incremented
	cacheHitCounter := metricsRegistry.Get(MetricsKeyRegistryCacheHits).(gometrics.Counter)
	assert.Equal(t, int64(1), cacheHitCounter.Count())

	// Multiple checks should increment counter
	registry.Check(installationID)
	registry.Check(installationID)
	assert.Equal(t, int64(3), cacheHitCounter.Count())
}

func TestInstallationRegistry_GoMetrics_CacheMisses(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)
	installationID := int64(99999)

	// Check for non-existent entry should record a cache miss
	_, _ = registry.Check(installationID)

	// Verify metric was incremented
	cacheMissCounter := metricsRegistry.Get(MetricsKeyRegistryCacheMisses).(gometrics.Counter)
	assert.Equal(t, int64(1), cacheMissCounter.Count())

	// Multiple misses should increment counter
	registry.Check(installationID + 1)
	registry.Check(installationID + 2)
	assert.Equal(t, int64(3), cacheMissCounter.Count())
}

func TestInstallationRegistry_GoMetrics_APICalls(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)

	// Record API calls
	registry.RecordAPICall()
	registry.RecordAPICall()
	registry.RecordAPICall()

	// Verify metric was incremented
	apiCallCounter := metricsRegistry.Get(MetricsKeyRegistryAPICalls).(gometrics.Counter)
	assert.Equal(t, int64(3), apiCallCounter.Count())
}

func TestInstallationRegistry_GoMetrics_CacheSize(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)

	// Initially empty
	cacheSizeGauge := metricsRegistry.Get(MetricsKeyRegistryCacheSize).(gometrics.Gauge)
	assert.Equal(t, int64(0), cacheSizeGauge.Value())

	// Add entries
	registry.MarkInstalled(1)
	assert.Equal(t, int64(1), cacheSizeGauge.Value())

	registry.MarkNotInstalled(2)
	assert.Equal(t, int64(2), cacheSizeGauge.Value())

	registry.MarkInstalled(3)
	assert.Equal(t, int64(3), cacheSizeGauge.Value())

	// Remove entry
	registry.Remove(2)
	assert.Equal(t, int64(2), cacheSizeGauge.Value())

	// Clear all
	registry.Clear()
	assert.Equal(t, int64(0), cacheSizeGauge.Value())
}

func TestInstallationRegistry_GoMetrics_PositiveAndNegativeEntries(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)

	positiveGauge := metricsRegistry.Get(MetricsKeyRegistryPositiveCache).(gometrics.Gauge)
	negativeGauge := metricsRegistry.Get(MetricsKeyRegistryNegativeCache).(gometrics.Gauge)

	// Initially empty
	assert.Equal(t, int64(0), positiveGauge.Value())
	assert.Equal(t, int64(0), negativeGauge.Value())

	// Add positive entries
	registry.MarkInstalled(1)
	registry.MarkInstalled(2)
	assert.Equal(t, int64(2), positiveGauge.Value())
	assert.Equal(t, int64(0), negativeGauge.Value())

	// Add negative entries
	registry.MarkNotInstalled(3)
	registry.MarkNotInstalled(4)
	assert.Equal(t, int64(2), positiveGauge.Value())
	assert.Equal(t, int64(2), negativeGauge.Value())

	// Overwrite positive with negative
	registry.MarkNotInstalled(1)
	assert.Equal(t, int64(1), positiveGauge.Value())
	assert.Equal(t, int64(3), negativeGauge.Value())

	// Clear all
	registry.Clear()
	assert.Equal(t, int64(0), positiveGauge.Value())
	assert.Equal(t, int64(0), negativeGauge.Value())
}

func TestInstallationRegistry_GoMetrics_NilRegistry(t *testing.T) {
	// Should not panic with nil metrics registry
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// All operations should work without panics
	assert.NotPanics(t, func() {
		registry.MarkInstalled(1)
		registry.MarkNotInstalled(2)
		registry.Check(1)
		registry.Check(2)
		registry.Check(3)
		registry.RecordAPICall()
		registry.Remove(1)
		registry.Clear()
	})
}

func TestInstallationRegistry_GoMetrics_ExpiredEntriesUpdateGauges(t *testing.T) {
	metricsRegistry := gometrics.NewRegistry()
	// Use short TTL for testing
	registry := NewInstallationRegistry(50*time.Millisecond, 50*time.Millisecond, metricsRegistry)

	cacheSizeGauge := metricsRegistry.Get(MetricsKeyRegistryCacheSize).(gometrics.Gauge)
	positiveGauge := metricsRegistry.Get(MetricsKeyRegistryPositiveCache).(gometrics.Gauge)

	// Add entry
	registry.MarkInstalled(1)
	assert.Equal(t, int64(1), cacheSizeGauge.Value())
	assert.Equal(t, int64(1), positiveGauge.Value())

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Check expired entry (should update gauges)
	registry.Check(1)

	// Gauges should reflect the removal
	assert.Equal(t, int64(0), cacheSizeGauge.Value())
	assert.Equal(t, int64(0), positiveGauge.Value())
}

func TestInstallationRegistry_UpdateInstallation(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	t.Run("update existing installation with new repos", func(t *testing.T) {
		// Create initial installation with repos
		registry.MarkInstalled(12345)
		registry.AddRepositories(12345, []struct{ Owner, Repo string }{
			{Owner: "owner1", Repo: "repo1"},
			{Owner: "owner1", Repo: "repo2"},
		})

		// Update with new information
		record := &InstallationRecord{
			InstallationID: 12345,
			Status:         InstallationExists,
			ExpiresAt:      time.Now().Add(1 * time.Hour),
			LastUpdated:    time.Now(),
			Repositories: map[string]bool{
				"owner1:repo3": true,
				"owner1:repo4": true,
			},
		}

		registry.UpdateInstallation(record)

		// Check that installation exists
		status, hit := registry.Check(12345)
		assert.True(t, hit)
		assert.Equal(t, InstallationExists, status)

		// Check new repos are added
		id, status, hit := registry.CheckByRepo("owner1", "repo3")
		assert.True(t, hit)
		assert.Equal(t, InstallationExists, status)
		assert.Equal(t, int64(12345), id)
	})

	t.Run("update non-existent installation", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 99999,
			Status:         InstallationExists,
			ExpiresAt:      time.Now().Add(1 * time.Hour),
			LastUpdated:    time.Now(),
			Repositories: map[string]bool{
				"newowner:newrepo": true,
			},
		}

		registry.UpdateInstallation(record)

		// Should be created
		status, hit := registry.Check(99999)
		assert.True(t, hit)
		assert.Equal(t, InstallationExists, status)

		// Check repo is associated
		id, status, hit := registry.CheckByRepo("newowner", "newrepo")
		assert.True(t, hit)
		assert.Equal(t, int64(99999), id)
	})
}

func TestInstallationRegistry_RemoveRepositories(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	t.Run("remove repos from installation", func(t *testing.T) {
		// Setup installation with multiple repos
		registry.MarkInstalled(12345)
		registry.AddRepositories(12345, []struct{ Owner, Repo string }{
			{Owner: "owner1", Repo: "repo1"},
			{Owner: "owner1", Repo: "repo2"},
			{Owner: "owner1", Repo: "repo3"},
		})

		// Remove some repos
		registry.RemoveRepositories(12345, []struct{ Owner, Repo string }{
			{Owner: "owner1", Repo: "repo2"},
			{Owner: "owner1", Repo: "repo3"},
		})

		// repo1 should still be associated
		id, status, hit := registry.CheckByRepo("owner1", "repo1")
		assert.True(t, hit)
		assert.Equal(t, int64(12345), id)
		assert.Equal(t, InstallationExists, status)

		// repo2 and repo3 should not be associated
		_, _, hit = registry.CheckByRepo("owner1", "repo2")
		assert.False(t, hit)

		_, _, hit = registry.CheckByRepo("owner1", "repo3")
		assert.False(t, hit)

		// Installation itself should still exist
		status, hit = registry.Check(12345)
		assert.True(t, hit)
		assert.Equal(t, InstallationExists, status)
	})

	t.Run("remove repos from non-existent installation", func(t *testing.T) {
		// Should not panic
		registry.RemoveRepositories(99999, []struct{ Owner, Repo string }{
			{Owner: "owner", Repo: "repo"},
		})

		// Should not create the installation
		status, hit := registry.Check(99999)
		assert.False(t, hit)
		assert.Equal(t, InstallationUnknown, status)
	})
}

func TestInstallationRegistry_GetInstallation(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	t.Run("get existing installation with repos", func(t *testing.T) {
		// Setup installation
		registry.MarkInstalled(12345)
		registry.AddRepositories(12345, []struct{ Owner, Repo string }{
			{Owner: "owner1", Repo: "repo1"},
			{Owner: "owner1", Repo: "repo2"},
		})

		// Get the installation
		record, exists := registry.GetInstallation(12345)
		assert.True(t, exists)
		assert.NotNil(t, record)
		assert.Equal(t, int64(12345), record.InstallationID)
		assert.Equal(t, InstallationExists, record.Status)
		assert.True(t, record.HasRepository("owner1", "repo1"))
		assert.True(t, record.HasRepository("owner1", "repo2"))
		assert.Equal(t, 2, record.GetRepositoryCount())
	})

	t.Run("get non-existent installation", func(t *testing.T) {
		record, exists := registry.GetInstallation(99999)
		assert.False(t, exists)
		assert.Nil(t, record)
	})

	t.Run("get installation marked as not found", func(t *testing.T) {
		// Phase 8: MarkNotInstalled now creates an InstallationRecord
		registry.MarkNotInstalled(54321)

		// GetInstallation should now find it
		record, exists := registry.GetInstallation(54321)
		assert.True(t, exists, "MarkNotInstalled now creates InstallationRecord")
		assert.NotNil(t, record)
		assert.Equal(t, InstallationNotFound, record.Status)

		// Check method should also find it
		status, hit := registry.Check(54321)
		assert.True(t, hit)
		assert.Equal(t, InstallationNotFound, status)
	})
}

func TestInstallationRegistry_CompoundKeyConcurrency(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Concurrent additions and lookups
	var wg sync.WaitGroup
	iterations := 100

	// Add repos concurrently
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			installID := int64(12345 + (idx % 10))
			registry.MarkInstalled(installID)
			registry.AddRepositories(installID, []struct{ Owner, Repo string }{
				{Owner: "owner", Repo: fmt.Sprintf("repo%d", idx)},
			})
		}(i)
	}

	// Lookup concurrently
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, _ = registry.CheckByRepo("owner", fmt.Sprintf("repo%d", idx))
		}(i)
	}

	wg.Wait()

	// Verify some entries exist
	id, status, hit := registry.CheckByRepo("owner", "repo0")
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)
	assert.GreaterOrEqual(t, id, int64(12345))
}

func TestInstallationRegistry_CheckByRepoEdgeCases(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	t.Run("lookup with empty owner", func(t *testing.T) {
		_, _, hit := registry.CheckByRepo("", "repo")
		assert.False(t, hit)
	})

	t.Run("lookup with empty repo", func(t *testing.T) {
		_, _, hit := registry.CheckByRepo("owner", "")
		assert.False(t, hit)
	})

	t.Run("lookup with both empty", func(t *testing.T) {
		_, _, hit := registry.CheckByRepo("", "")
		assert.False(t, hit)
	})

	t.Run("lookup after installation deleted but repo mapping remains", func(t *testing.T) {
		registry.MarkInstalled(12345)
		registry.AddRepositories(12345, []struct{ Owner, Repo string }{
			{Owner: "owner", Repo: "repo"},
		})

		// Delete the installation
		registry.Remove(12345)

		// Compound key should return not found
		_, status, hit := registry.CheckByRepo("owner", "repo")
		// The mapping might still exist but installation is gone
		if hit {
			// If mapping exists, status should indicate installation doesn't exist
			assert.NotEqual(t, InstallationExists, status)
		}
	})
}
// Tests for consolidated VerifyInstallation method

func TestInstallationRegistry_VerifyInstallation_CacheHit_Positive(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Pre-populate cache with positive entry
	registry.MarkInstalled(installationID)

	// Verify should return true without API call (appClient can be nil)
	exists, err := registry.VerifyInstallation(ctx, installationID, nil)

	assert.NoError(t, err)
	assert.True(t, exists)
}

func TestInstallationRegistry_VerifyInstallation_CacheHit_Negative(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(99999)

	// Pre-populate cache with negative entry
	registry.MarkNotInstalled(installationID)

	// Verify should return false without API call
	exists, err := registry.VerifyInstallation(ctx, installationID, nil)

	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestInstallationRegistry_VerifyInstallation_CacheMiss_NoClient(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(54321)

	// Cache miss with no API client should return false
	exists, err := registry.VerifyInstallation(ctx, installationID, nil)

	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestInstallationRegistry_VerifyInstallation_APISuccess(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Create mock server that returns successful installation
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/app/installations/12345", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": 12345,
			"account": map[string]interface{}{
				"login": "test-org",
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// Create GitHub client pointing to mock server
	client, err := github.NewEnterpriseClient(server.URL, server.URL, nil)
	require.NoError(t, err)

	// Verify installation via API
	exists, err := registry.VerifyInstallation(ctx, installationID, client)

	assert.NoError(t, err)
	assert.True(t, exists)

	// Check that it was cached
	status, cached := registry.Check(installationID)
	assert.True(t, cached)
	assert.Equal(t, InstallationExists, status)
}

func TestInstallationRegistry_VerifyInstallation_API404(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(99999)

	// Create mock server that returns 404
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/app/installations/99999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Not Found",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// Create GitHub client pointing to mock server
	client, err := github.NewEnterpriseClient(server.URL, server.URL, nil)
	require.NoError(t, err)

	// Verify installation via API - should return false, no error (404 is expected)
	exists, err := registry.VerifyInstallation(ctx, installationID, client)

	assert.NoError(t, err)
	assert.False(t, exists)

	// Check that negative result was cached
	status, cached := registry.Check(installationID)
	assert.True(t, cached)
	assert.Equal(t, InstallationNotFound, status)
}

func TestInstallationRegistry_VerifyInstallation_APIError(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Create mock server that returns 500 error
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/app/installations/12345", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Internal Server Error",
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// Create GitHub client pointing to mock server
	client, err := github.NewEnterpriseClient(server.URL, server.URL, nil)
	require.NoError(t, err)

	// Verify installation via API - should return error
	exists, err := registry.VerifyInstallation(ctx, installationID, client)

	assert.Error(t, err)
	assert.False(t, exists)

	// Check that error result was NOT cached
	status, cached := registry.Check(installationID)
	assert.False(t, cached)
	assert.Equal(t, InstallationUnknown, status)
}

func TestInstallationRegistry_VerifyInstallation_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	// Pre-populate cache
	registry.MarkInstalled(installationID)

	// Concurrent verification calls
	var wg sync.WaitGroup
	errors := make(chan error, 100)
	results := make(chan bool, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exists, err := registry.VerifyInstallation(ctx, installationID, nil)
			if err != nil {
				errors <- err
			}
			results <- exists
		}()
	}

	wg.Wait()
	close(errors)
	close(results)

	// Check no errors occurred
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}

	// Check all results are consistent
	for exists := range results {
		assert.True(t, exists, "All concurrent calls should return true")
	}
}

func TestInstallationRegistry_VerifyInstallation_MetricsRecorded(t *testing.T) {
	ctx := context.Background()
	metricsRegistry := gometrics.NewRegistry()
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metricsRegistry)
	installationID := int64(12345)

	// Create mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/12345", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": 12345})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := github.NewEnterpriseClient(server.URL, server.URL, nil)
	require.NoError(t, err)

	// First call - cache miss, API call
	_, _ = registry.VerifyInstallation(ctx, installationID, client)

	// Verify API call metric was incremented
	apiCallCounter := metricsRegistry.Get(MetricsKeyRegistryAPICalls).(gometrics.Counter)
	assert.Equal(t, int64(1), apiCallCounter.Count())

	// Second call - cache hit
	_, _ = registry.VerifyInstallation(ctx, installationID, nil)

	// Verify cache hit metric was incremented
	cacheHitCounter := metricsRegistry.Get(MetricsKeyRegistryCacheHits).(gometrics.Counter)
	assert.Equal(t, int64(1), cacheHitCounter.Count())
}
