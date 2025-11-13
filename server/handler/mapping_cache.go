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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MappingCacheMetrics tracks mapping cache statistics
type MappingCacheMetrics struct {
	hits      atomic.Int64 // Cache hits
	misses    atomic.Int64 // Cache misses
	sets      atomic.Int64 // Successful sets
	evictions atomic.Int64 // Expired entries cleaned up
	size      atomic.Int64 // Current cache size
}

// mappingEntry represents a cached mapping with expiration
type mappingEntry struct {
	installationID int64     // 0 means "not found"
	isNotFound     bool      // Explicitly tracks negative cache
	expiresAt      time.Time
}

// IsExpired checks if the entry has expired
func (e *mappingEntry) IsExpired() bool {
	return time.Now().After(e.expiresAt)
}

// MappingCache handles caching of repository/organization to installation ID mappings.
// It supports both positive caching (successful lookups) and negative caching (not found).
// Thread-safe for concurrent access with background cleanup of expired entries.
//
// Design decisions:
// - Uses sync.RWMutex for read-heavy workloads (most operations are cache reads)
// - Atomic counters for metrics to avoid lock contention
// - Background cleanup goroutine to remove expired entries
// - sync.Pool for string building to reduce allocations
type MappingCache struct {
	mu          sync.RWMutex
	entries     map[string]mappingEntry
	positiveTTL time.Duration // TTL for successful lookups (1 hour default)
	negativeTTL time.Duration // TTL for failed lookups (5 minutes default)
	maxSize     int           // Maximum cache size (10000 default)

	// Metrics
	metrics *MappingCacheMetrics

	// Cleanup coordination
	stopCleanup chan struct{}
	cleanupDone chan struct{}

	// String builder pool to reduce allocations
	builderPool *sync.Pool
}

// NewMappingCache creates a new mapping cache with specified TTLs
func NewMappingCache(positiveTTL, negativeTTL time.Duration) *MappingCache {
	return NewMappingCacheWithOptions(positiveTTL, negativeTTL, 10000, 1*time.Minute)
}

// NewMappingCacheWithOptions creates a new mapping cache with full configuration
func NewMappingCacheWithOptions(positiveTTL, negativeTTL time.Duration, maxSize int, cleanupInterval time.Duration) *MappingCache {
	if positiveTTL <= 0 {
		positiveTTL = 1 * time.Hour
	}
	if negativeTTL <= 0 {
		negativeTTL = 5 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 10000
	}
	if cleanupInterval <= 0 {
		cleanupInterval = 1 * time.Minute
	}

	cache := &MappingCache{
		entries:     make(map[string]mappingEntry, 100), // Pre-allocate with initial capacity
		positiveTTL: positiveTTL,
		negativeTTL: negativeTTL,
		maxSize:     maxSize,
		metrics:     &MappingCacheMetrics{},
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
		builderPool: &sync.Pool{
			New: func() interface{} {
				return new(strings.Builder)
			},
		},
	}

	// Start background cleanup goroutine
	go cache.cleanupLoop(cleanupInterval)

	return cache
}

// Get returns installation ID and whether it was found in cache.
// Returns (0, false) if not in cache or expired.
// Returns (0, true) if negative cache entry (installation doesn't exist).
// Returns (installationID, true) if positive cache entry.
func (c *MappingCache) Get(key string) (installationID int64, found bool) {
	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()

	if !exists {
		c.metrics.misses.Add(1)
		return 0, false
	}

	if entry.IsExpired() {
		// Remove expired entry
		c.mu.Lock()
		delete(c.entries, key)
		c.metrics.size.Store(int64(len(c.entries)))
		c.mu.Unlock()

		c.metrics.misses.Add(1)
		c.metrics.evictions.Add(1)
		return 0, false
	}

	c.metrics.hits.Add(1)

	// Check if this is a negative cache entry
	if entry.isNotFound {
		return 0, true // Found in cache, but installation doesn't exist
	}

	return entry.installationID, true
}

// Set caches a successful lookup (positive cache)
func (c *MappingCache) Set(key string, installationID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check size limit and evict if necessary
	if len(c.entries) >= c.maxSize {
		c.evictOldest(c.maxSize / 10) // Evict 10% of entries
	}

	c.entries[key] = mappingEntry{
		installationID: installationID,
		isNotFound:     false,
		expiresAt:      time.Now().Add(c.positiveTTL),
	}

	c.metrics.sets.Add(1)
	c.metrics.size.Store(int64(len(c.entries)))
}

// SetNotFound caches a failed lookup (negative cache)
func (c *MappingCache) SetNotFound(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check size limit and evict if necessary
	if len(c.entries) >= c.maxSize {
		c.evictOldest(c.maxSize / 10) // Evict 10% of entries
	}

	c.entries[key] = mappingEntry{
		installationID: 0,
		isNotFound:     true,
		expiresAt:      time.Now().Add(c.negativeTTL),
	}

	c.metrics.sets.Add(1)
	c.metrics.size.Store(int64(len(c.entries)))
}

// Remove invalidates a cache entry
func (c *MappingCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
	c.metrics.size.Store(int64(len(c.entries)))
}

// Clear removes all entries
func (c *MappingCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]mappingEntry, 100)
	c.metrics.size.Store(0)
}

// GetSize returns the current number of cached entries
func (c *MappingCache) GetSize() int {
	return int(c.metrics.size.Load())
}

// GetStats returns cache statistics (positive, negative, total)
func (c *MappingCache) GetStats() (positive, negative, total int) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total = len(c.entries)
	for _, entry := range c.entries {
		if entry.isNotFound {
			negative++
		} else {
			positive++
		}
	}
	return
}

// GetMetrics returns cache metrics
func (c *MappingCache) GetMetrics() (hits, misses, sets, evictions, size int64) {
	return c.metrics.hits.Load(),
		c.metrics.misses.Load(),
		c.metrics.sets.Load(),
		c.metrics.evictions.Load(),
		c.metrics.size.Load()
}

// Stop gracefully shuts down the cache and its cleanup goroutine
func (c *MappingCache) Stop() {
	close(c.stopCleanup)
	<-c.cleanupDone
}

// BuildRepoCacheKey builds a cache key for repository mapping lookups.
// Format: "owner/repo"
// This helper reduces allocations by reusing string builders from pool.
func (c *MappingCache) BuildRepoCacheKey(owner, repo string) string {
	if owner == "" || repo == "" {
		return ""
	}

	builder := c.builderPool.Get().(*strings.Builder)
	defer func() {
		builder.Reset()
		c.builderPool.Put(builder)
	}()

	// Pre-size to avoid reallocation
	builder.Grow(len(owner) + 1 + len(repo))
	builder.WriteString(owner)
	builder.WriteByte('/')
	builder.WriteString(repo)
	return builder.String()
}

// BuildOrgCacheKey builds a cache key for organization mapping lookups.
// Format: "org:orgname"
// This helper reduces allocations by reusing string builders from pool.
func (c *MappingCache) BuildOrgCacheKey(org string) string {
	if org == "" {
		return ""
	}

	builder := c.builderPool.Get().(*strings.Builder)
	defer func() {
		builder.Reset()
		c.builderPool.Put(builder)
	}()

	// Pre-size to avoid reallocation
	builder.Grow(4 + len(org))
	builder.WriteString("org:")
	builder.WriteString(org)
	return builder.String()
}

// cleanupLoop runs periodically to remove expired entries
func (c *MappingCache) cleanupLoop(interval time.Duration) {
	defer close(c.cleanupDone)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCleanup:
			return
		case <-ticker.C:
			c.cleanupExpired()
		}
	}
}

// cleanupExpired removes all expired entries
func (c *MappingCache) cleanupExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	var keysToDelete []string

	// Collect expired keys
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			keysToDelete = append(keysToDelete, key)
		}
	}

	// Delete expired entries
	for _, key := range keysToDelete {
		delete(c.entries, key)
		c.metrics.evictions.Add(1)
	}

	c.metrics.size.Store(int64(len(c.entries)))
}

// evictOldest removes the oldest entries (called with lock held)
func (c *MappingCache) evictOldest(count int) {
	if count <= 0 || len(c.entries) == 0 {
		return
	}

	// Find oldest entries
	type entryAge struct {
		key       string
		expiresAt time.Time
	}

	ages := make([]entryAge, 0, len(c.entries))
	for key, entry := range c.entries {
		ages = append(ages, entryAge{key: key, expiresAt: entry.expiresAt})
	}

	// Simple selection of oldest entries (no need to fully sort)
	toEvict := count
	if toEvict > len(ages) {
		toEvict = len(ages)
	}

	// Delete the entries that expire soonest
	for i := 0; i < toEvict && i < len(ages); i++ {
		var oldestIdx int
		oldestTime := ages[0].expiresAt

		for j := 1; j < len(ages)-i; j++ {
			if ages[j].expiresAt.Before(oldestTime) {
				oldestTime = ages[j].expiresAt
				oldestIdx = j
			}
		}

		delete(c.entries, ages[oldestIdx].key)
		c.metrics.evictions.Add(1)

		// Swap with last unprocessed element
		ages[oldestIdx] = ages[len(ages)-i-1]
	}
}