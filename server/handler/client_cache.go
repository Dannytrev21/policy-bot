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
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

const (
	// Default cache configuration
	defaultClientCacheTTL     = 10 * time.Minute // Client tokens typically valid for 1 hour, refresh earlier
	defaultClientCacheMaxSize = 1000             // Maximum number of cached clients
	defaultCleanupInterval    = 1 * time.Minute  // How often to clean up expired entries

	// Metric keys for client cache
	MetricsKeyClientCacheHits      = "installation.client_cache.hits"
	MetricsKeyClientCacheMisses    = "installation.client_cache.misses"
	MetricsKeyClientCacheEvictions = "installation.client_cache.evictions"
	MetricsKeyClientCacheSize      = "installation.client_cache.size"
)

// CachedClients represents cached GitHub API clients with expiration metadata
type CachedClients struct {
	Clients   *InstallationClients
	ExpiresAt time.Time
	CreatedAt time.Time
}

// IsExpired checks if the cached clients have expired
func (cc *CachedClients) IsExpired() bool {
	return time.Now().After(cc.ExpiresAt)
}

// ClientCache provides thread-safe caching of GitHub API clients with TTL-based expiration.
// It uses sync.Map for lock-free reads (optimized for read-heavy workloads) and implements
// LRU eviction when the cache exceeds maximum size.
type ClientCache struct {
	cache   sync.Map // map[int64]*CachedClients
	ttl     time.Duration
	maxSize int

	// Metrics (atomic for lock-free updates)
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
	size      atomic.Int64

	// Cleanup coordination
	stopCleanup chan struct{}
	cleanupDone chan struct{}
	mu          sync.Mutex // Protects cleanup operations
}

// NewClientCache creates a new client cache with the specified TTL and max size.
// It starts a background goroutine to periodically clean up expired entries.
func NewClientCache(ttl time.Duration, maxSize int) *ClientCache {
	if ttl <= 0 {
		ttl = defaultClientCacheTTL
	}
	if maxSize <= 0 {
		maxSize = defaultClientCacheMaxSize
	}

	cache := &ClientCache{
		ttl:         ttl,
		maxSize:     maxSize,
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
	}

	// Start background cleanup goroutine
	go cache.cleanupLoop()

	return cache
}

// Get retrieves clients from the cache if they exist and haven't expired.
// Returns nil if not found or expired. Thread-safe.
func (c *ClientCache) Get(installationID int64) *InstallationClients {
	value, ok := c.cache.Load(installationID)
	if !ok {
		c.misses.Add(1)
		return nil
	}

	cached := value.(*CachedClients)
	if cached.IsExpired() {
		// Expired entry - remove it and return nil
		c.cache.Delete(installationID)
		c.size.Add(-1)
		c.misses.Add(1)
		return nil
	}

	c.hits.Add(1)
	return cached.Clients
}

// Put stores clients in the cache with TTL expiration.
// If the cache exceeds maxSize, it will evict expired entries first,
// then least recently created entries if needed. Thread-safe.
func (c *ClientCache) Put(installationID int64, clients *InstallationClients) {
	if clients == nil {
		return
	}

	now := time.Now()
	cached := &CachedClients{
		Clients:   clients,
		ExpiresAt: now.Add(c.ttl),
		CreatedAt: now,
	}

	// Check if we need to evict before adding
	currentSize := int(c.size.Load())
	if currentSize >= c.maxSize {
		c.evictOldest()
	}

	// Store the new entry
	_, loaded := c.cache.LoadOrStore(installationID, cached)
	if !loaded {
		// New entry added
		c.size.Add(1)
	}
}

// Invalidate removes a specific installation's clients from the cache.
// Useful when installation is deleted or credentials are revoked.
func (c *ClientCache) Invalidate(installationID int64) {
	_, ok := c.cache.LoadAndDelete(installationID)
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

// Stop gracefully shuts down the cache's background cleanup goroutine.
// Should be called when the cache is no longer needed.
func (c *ClientCache) Stop() {
	close(c.stopCleanup)
	<-c.cleanupDone
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
		installationID int64
		createdAt      time.Time
	}

	var entries []entry
	c.cache.Range(func(key, value interface{}) bool {
		cached := value.(*CachedClients)
		entries = append(entries, entry{
			installationID: key.(int64),
			createdAt:      cached.CreatedAt,
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
		c.cache.Delete(entries[i].installationID)
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
