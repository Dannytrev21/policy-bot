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

// Package handler provides thread-safe client caching for GitHub API clients.
//
// ClientCache Features:
//   - TTL-based expiration (10 min positive, 2 min negative)
//   - Negative caching to avoid repeated API calls for non-existent installations
//   - Metrics integration with go-metrics for OTEL export to New Relic
//   - Background cleanup of expired entries (every 1 minute)
//   - Lock-free reads using sync.Map (optimized for read-heavy workloads)
//   - LRU eviction when cache reaches maximum size
//   - Thread-safe operations with atomic counters
//
// Enhancements (January 2025):
//   - Added negative caching with separate TTL
//   - Integrated metrics publishing to go-metrics registry
//   - Added hit rate calculation and tracking
//   - All new features maintain backward compatibility
//
package handler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

const (
	// Default cache configuration
	defaultClientCacheTTL         = 10 * time.Minute // Client tokens typically valid for 1 hour, refresh earlier
	defaultClientCacheNegativeTTL = 2 * time.Minute  // Shorter TTL for negative cache entries
	defaultClientCacheMaxSize     = 1000             // Maximum number of cached clients
	defaultCleanupInterval        = 1 * time.Minute  // How often to clean up expired entries
	metricsPublishInterval        = 10 * time.Second // How often to publish metrics to registry

	// Metric keys for client cache
	MetricsKeyClientCacheHits      = "installation.client_cache.hits"
	MetricsKeyClientCacheMisses    = "installation.client_cache.misses"
	MetricsKeyClientCacheEvictions = "installation.client_cache.evictions"
	MetricsKeyClientCacheSize      = "installation.client_cache.size"
	MetricsKeyClientCacheHitRate   = "installation.client_cache.hit_rate"
)

// CachedClients represents cached GitHub API clients with expiration metadata.
// Supports both positive caching (successful lookups) and negative caching (not found).
type CachedClients struct {
	Clients        *InstallationClients
	InstallationID int64     // The GitHub App installation ID for this owner
	ExpiresAt      time.Time
	CreatedAt      time.Time
	IsNegative     bool // True if this caches a "not found" result (no Clients object)
}

// IsExpired checks if the cached clients have expired
func (cc *CachedClients) IsExpired() bool {
	return time.Now().After(cc.ExpiresAt)
}

// ClientCache provides thread-safe caching of GitHub API clients with TTL-based expiration.
// It uses sync.Map for lock-free reads (optimized for read-heavy workloads) and implements
// LRU eviction when the cache exceeds maximum size.
//
// Supports both positive caching (successful lookups) and negative caching (not found results)
// with separate TTLs for each type.
//
// For GHEC: Keys are owner IDs (int64), since there's ONE installation per org.
// This eliminates the need for a separate OrgMappingCache - installation ID is stored alongside clients.
type ClientCache struct {
	cache       sync.Map // map[int64]*CachedClients (key = owner ID)
	ttl         time.Duration
	negativeTTL time.Duration
	maxSize     int

	// Metrics (atomic for lock-free updates)
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	size      atomic.Int64

	// Integration with go-metrics for OTEL export
	metricsRegistry gometrics.Registry

	// Cleanup coordination
	stopCleanup       chan struct{}
	cleanupDone       chan struct{}
	stopMetrics       chan struct{}
	metricsDone       chan struct{}
	mu                sync.Mutex // Protects cleanup operations
}

// NewClientCache creates a new client cache with the specified TTL and max size.
// It starts background goroutines for cleanup and metrics publishing.
// Maintains backward compatibility - registry can be nil to disable metrics publishing.
func NewClientCache(ttl time.Duration, maxSize int) *ClientCache {
	return NewClientCacheWithOptions(ttl, defaultClientCacheNegativeTTL, maxSize, nil)
}

// NewClientCacheWithOptions creates a new client cache with full configuration options.
// Supports negative caching with separate TTL and optional metrics registry integration.
func NewClientCacheWithOptions(ttl, negativeTTL time.Duration, maxSize int, registry gometrics.Registry) *ClientCache {
	if ttl <= 0 {
		ttl = defaultClientCacheTTL
	}
	if negativeTTL <= 0 {
		negativeTTL = defaultClientCacheNegativeTTL
	}
	if maxSize <= 0 {
		maxSize = defaultClientCacheMaxSize
	}

	cache := &ClientCache{
		ttl:             ttl,
		negativeTTL:     negativeTTL,
		maxSize:         maxSize,
		metricsRegistry: registry,
		stopCleanup:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
		stopMetrics:     make(chan struct{}),
		metricsDone:     make(chan struct{}),
	}

	// Start background cleanup goroutine
	go cache.cleanupLoop()

	// Start background metrics publishing goroutine (if registry provided)
	if registry != nil {
		go cache.metricsLoop()
	} else {
		// Close metricsDone immediately if no metrics publishing
		close(cache.metricsDone)
	}

	return cache
}

// Get retrieves clients from the cache if they exist and haven't expired.
// Returns nil if not found, expired, or if a negative cache entry exists.
// Thread-safe and optimized for read-heavy workloads.
//
// For GHEC: ownerID is the GitHub org/user ID (int64).
func (c *ClientCache) Get(ownerID int64) *InstallationClients {
	if ownerID == 0 {
		c.misses.Add(1)
		return nil
	}

	value, ok := c.cache.Load(ownerID)
	if !ok {
		c.misses.Add(1)
		return nil
	}

	cached := value.(*CachedClients)
	if cached.IsExpired() {
		// Expired entry - remove it and return nil
		c.cache.Delete(ownerID)
		c.size.Add(-1)
		c.misses.Add(1)
		return nil
	}

	// Check if this is a negative cache entry (cached "not found")
	if cached.IsNegative {
		c.hits.Add(1) // Still a cache hit (we know it doesn't exist)
		return nil
	}

	c.hits.Add(1)
	return cached.Clients
}

// GetWithInstallationID retrieves clients and installation ID from the cache.
// Returns (nil, 0) if not found, expired, or if a negative cache entry exists.
// Thread-safe and optimized for read-heavy workloads.
func (c *ClientCache) GetWithInstallationID(ownerID int64) (*InstallationClients, int64) {
	if ownerID == 0 {
		c.misses.Add(1)
		return nil, 0
	}

	value, ok := c.cache.Load(ownerID)
	if !ok {
		c.misses.Add(1)
		return nil, 0
	}

	cached := value.(*CachedClients)
	if cached.IsExpired() {
		// Expired entry - remove it and return nil
		c.cache.Delete(ownerID)
		c.size.Add(-1)
		c.misses.Add(1)
		return nil, 0
	}

	// Check if this is a negative cache entry (cached "not found")
	if cached.IsNegative {
		c.hits.Add(1) // Still a cache hit (we know it doesn't exist)
		return nil, 0
	}

	c.hits.Add(1)
	return cached.Clients, cached.InstallationID
}

// Put stores clients in the cache with TTL expiration.
// If the cache exceeds maxSize, it will evict expired entries first,
// then least recently created entries if needed. Thread-safe.
// For GHEC: ownerID is the GitHub org/user ID (int64).
func (c *ClientCache) Put(ownerID int64, clients *InstallationClients) {
	c.PutWithInstallationID(ownerID, clients, 0)
}

// PutWithInstallationID stores clients and their installation ID in the cache.
// If the cache exceeds maxSize, it will evict expired entries first,
// then least recently created entries if needed. Thread-safe.
// For GHEC: ownerID is the GitHub org/user ID (int64).
func (c *ClientCache) PutWithInstallationID(ownerID int64, clients *InstallationClients, installationID int64) {
	if ownerID == 0 || clients == nil {
		return
	}

	now := time.Now()
	cached := &CachedClients{
		Clients:        clients,
		InstallationID: installationID,
		ExpiresAt:      now.Add(c.ttl),
		CreatedAt:      now,
		IsNegative:     false,
	}

	// Check if we need to evict before adding
	currentSize := int(c.size.Load())
	if currentSize >= c.maxSize {
		c.evictOldest()
	}

	// Store the new entry (use Store to override any existing entry including negative cache)
	existing, loaded := c.cache.Swap(ownerID, cached)
	if !loaded {
		// New entry added
		c.size.Add(1)
	} else {
		// Entry was replaced - check if we're replacing a negative cache
		if existingCached, ok := existing.(*CachedClients); ok && existingCached.IsNegative {
			// We're replacing a negative cache with a positive one
			// This is good - no size change needed
		}
	}
}

// PutNegative stores a negative cache entry (caches "not found" result).
// Uses shorter TTL since these entries should expire faster.
// Useful to avoid repeated API calls for non-existent installations.
// Thread-safe.
func (c *ClientCache) PutNegative(ownerID int64) {
	if ownerID == 0 {
		return
	}

	now := time.Now()
	cached := &CachedClients{
		Clients:        nil, // No clients for negative cache
		InstallationID: 0,
		ExpiresAt:      now.Add(c.negativeTTL),
		CreatedAt:      now,
		IsNegative:     true,
	}

	// Check if we need to evict before adding
	currentSize := int(c.size.Load())
	if currentSize >= c.maxSize {
		c.evictOldest()
	}

	// Store the negative cache entry (use Swap to override any existing entry)
	_, loaded := c.cache.Swap(ownerID, cached)
	if !loaded {
		// New entry added
		c.size.Add(1)
	}
}

// IsNegativelyCached returns true if the owner has a negative cache entry
// (i.e., we know this owner doesn't have an installation).
// Returns false if not cached or if it's a positive cache entry.
func (c *ClientCache) IsNegativelyCached(ownerID int64) bool {
	if ownerID == 0 {
		return false
	}

	value, ok := c.cache.Load(ownerID)
	if !ok {
		return false
	}

	cached := value.(*CachedClients)
	if cached.IsExpired() {
		// Expired entry doesn't count
		return false
	}

	return cached.IsNegative
}

// Invalidate removes a specific owner's clients from the cache.
// Useful when installation is deleted or credentials are revoked.
func (c *ClientCache) Invalidate(ownerID int64) {
	if ownerID == 0 {
		return
	}
	_, ok := c.cache.LoadAndDelete(ownerID)
	if ok {
		c.size.Add(-1)
	}
}

// Clear removes all entries from the cache.
// Primarily used for testing or administrative operations.
func (c *ClientCache) Clear() {
	c.cache.Range(func(key, value interface{}) bool {
		c.cache.Delete(key)
		return true
	})
	c.size.Store(0)
	c.hits.Store(0)
	c.misses.Store(0)
	c.evictions.Store(0)
}

// GetMetrics returns current cache metrics.
// Returns: hits, misses, evictions, size
func (c *ClientCache) GetMetrics() (int64, int64, int64, int64) {
	return c.hits.Load(), c.misses.Load(), c.evictions.Load(), c.size.Load()
}

// Stop gracefully shuts down the cache's background goroutines.
// Should be called when the cache is no longer needed.
func (c *ClientCache) Stop() {
	close(c.stopCleanup)
	<-c.cleanupDone

	// Stop metrics publishing if it was started
	if c.metricsRegistry != nil {
		close(c.stopMetrics)
		<-c.metricsDone
	}
}

// cleanupLoop runs periodically to remove expired entries from the cache.
// Runs in a background goroutine until Stop() is called.
func (c *ClientCache) cleanupLoop() {
	ticker := time.NewTicker(defaultCleanupInterval)
	defer ticker.Stop()
	defer close(c.cleanupDone)

	for {
		select {
		case <-c.stopCleanup:
			return
		case <-ticker.C:
			c.cleanupExpired()
		}
	}
}

// cleanupExpired removes expired entries from the cache.
// Called periodically by the cleanup goroutine.
func (c *ClientCache) cleanupExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiredKeys []int64

	// Find expired entries
	c.cache.Range(func(key, value interface{}) bool {
		cached := value.(*CachedClients)
		if cached.IsExpired() {
			expiredKeys = append(expiredKeys, key.(int64))
		}
		return true
	})

	// Remove expired entries
	for _, key := range expiredKeys {
		c.cache.Delete(key)
		c.size.Add(-1)
		c.evictions.Add(1)
	}

	if len(expiredKeys) > 0 {
		// Log cleanup activity (using background context since this is async)
		logger := zerolog.Ctx(context.Background())
		logger.Debug().
			Int("expired_count", len(expiredKeys)).
			Int64("cache_size", c.size.Load()).
			Msg("Cleaned up expired client cache entries")
	}
}

// evictOldest removes the oldest entries from the cache to make room for new ones.
// Uses creation time to determine which entries are oldest (simple LRU).
// Should be called with appropriate locking.
func (c *ClientCache) evictOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Collect all entries with their creation times
	type entry struct {
		ownerID   int64
		createdAt time.Time
	}

	var entries []entry
	c.cache.Range(func(key, value interface{}) bool {
		cached := value.(*CachedClients)
		entries = append(entries, entry{
			ownerID:   key.(int64),
			createdAt: cached.CreatedAt,
		})
		return true
	})

	if len(entries) == 0 {
		return
	}

	// Sort by creation time (oldest first)
	// Simple bubble sort for small datasets, good enough for cache eviction
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].createdAt.After(entries[j].createdAt) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Evict oldest 10% of entries
	evictCount := len(entries) / 10
	if evictCount < 1 {
		evictCount = 1
	}

	for i := 0; i < evictCount && i < len(entries); i++ {
		c.cache.Delete(entries[i].ownerID)
		c.size.Add(-1)
		c.evictions.Add(1)
	}

	logger := zerolog.Ctx(context.Background())
	logger.Debug().
		Int("evicted_count", evictCount).
		Int64("cache_size", c.size.Load()).
		Int("max_size", c.maxSize).
		Msg("Evicted oldest client cache entries to make room")
}

// metricsLoop runs periodically to publish cache metrics to the go-metrics registry.
// Runs in a background goroutine until Stop() is called.
// Only runs if a metrics registry was provided during construction.
func (c *ClientCache) metricsLoop() {
	ticker := time.NewTicker(metricsPublishInterval)
	defer ticker.Stop()
	defer close(c.metricsDone)

	for {
		select {
		case <-c.stopMetrics:
			return
		case <-ticker.C:
			c.publishMetrics()
		}
	}
}

// publishMetrics publishes current cache metrics to the go-metrics registry.
// This allows metrics to be exported via OTEL to New Relic.
func (c *ClientCache) publishMetrics() {
	if c.metricsRegistry == nil {
		return
	}

	// Update counters
	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheHits, c.metricsRegistry).Clear()
	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheHits, c.metricsRegistry).Inc(c.hits.Load())

	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheMisses, c.metricsRegistry).Clear()
	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheMisses, c.metricsRegistry).Inc(c.misses.Load())

	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheEvictions, c.metricsRegistry).Clear()
	gometrics.GetOrRegisterCounter(MetricsKeyClientCacheEvictions, c.metricsRegistry).Inc(c.evictions.Load())

	// Update gauges
	gometrics.GetOrRegisterGauge(MetricsKeyClientCacheSize, c.metricsRegistry).Update(c.size.Load())

	// Calculate and update hit rate
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	if total > 0 {
		hitRate := int64(float64(hits) / float64(total) * 100)
		gometrics.GetOrRegisterGauge(MetricsKeyClientCacheHitRate, c.metricsRegistry).Update(hitRate)
	}
}
