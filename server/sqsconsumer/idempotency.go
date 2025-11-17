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
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/rcrowley/go-metrics"
)

const (
	// Default cache size - can handle 10,000 unique messages
	DefaultIdempotencyCacheSize = 10000

	// Default TTL for cache entries - 1 hour (longer than typical message retention)
	DefaultIdempotencyTTL = 1 * time.Hour

	// Metrics keys for idempotency
	MetricsKeyIdempotencyDuplicates = "sqs.idempotency.duplicates"
	MetricsKeyIdempotencyCacheSize  = "sqs.idempotency.cache_size"
	MetricsKeyIdempotencyChecks     = "sqs.idempotency.checks_total"
)

// IdempotencyManager manages duplicate detection for SQS messages
// using an LRU cache with TTL-based expiration.
//
// Thread Safety: All methods are thread-safe using mutex protection.
// The LRU cache evicts oldest entries when capacity is reached.
type IdempotencyManager struct {
	cache    *lru.Cache[string, time.Time]
	ttl      time.Duration
	mu       sync.RWMutex
	registry metrics.Registry
}

// NewIdempotencyManager creates a new idempotency manager with specified cache size and TTL.
// The registry parameter can be nil for testing, but should be provided in production for metrics.
func NewIdempotencyManager(size int, ttl time.Duration, registry metrics.Registry) (*IdempotencyManager, error) {
	if size <= 0 {
		size = DefaultIdempotencyCacheSize
	}

	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}

	cache, err := lru.New[string, time.Time](size)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	im := &IdempotencyManager{
		cache:    cache,
		ttl:      ttl,
		registry: registry,
	}

	// Register metrics if registry is provided
	if registry != nil {
		metrics.GetOrRegisterCounter(MetricsKeyIdempotencyDuplicates, registry)
		metrics.GetOrRegisterGauge(MetricsKeyIdempotencyCacheSize, registry)
		metrics.GetOrRegisterCounter(MetricsKeyIdempotencyChecks, registry)
	}

	return im, nil
}

// IsProcessed checks if a delivery ID has been successfully processed.
// Returns true if the message was already processed (duplicate), false otherwise.
// This method does NOT mark the message as processed - use MarkProcessed for that.
//
// Performance: O(1) due to LRU cache.
// Uses RWMutex for minimal lock contention on reads (cache hits).
func (im *IdempotencyManager) IsProcessed(deliveryID string) bool {
	now := time.Now()

	// Record check metric
	im.recordCheck()

	// Use read lock for cache check (optimized for cache hits)
	im.mu.RLock()
	processedAt, exists := im.cache.Get(deliveryID)
	im.mu.RUnlock()

	if exists {
		// Check if entry has expired
		if now.Sub(processedAt) < im.ttl {
			// Valid cache hit - already processed
			im.recordDuplicate()
			return true
		}
		// Entry expired, treat as not processed
	}

	return false
}

// MarkProcessed marks a delivery ID as successfully processed.
// This should be called AFTER the message has been successfully processed
// or after a non-retryable error (to prevent future retries).
//
// Performance: O(1) due to LRU cache.
func (im *IdempotencyManager) MarkProcessed(deliveryID string) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.cache.Add(deliveryID, time.Now())
	im.updateCacheSizeMetric()
}

// CheckAndMark checks if a delivery ID has been processed and marks it as processed.
// Returns true if the message was already processed (duplicate), false otherwise.
//
// DEPRECATED: Use IsProcessed() followed by MarkProcessed() instead.
// This method marks messages as processed BEFORE they are actually processed,
// which can cause issues with retries. The new pattern is:
//   1. Check with IsProcessed() before processing
//   2. Process the message
//   3. Mark with MarkProcessed() after success or non-retryable error
//
// Performance: O(1) for both check and insert due to LRU cache.
// Uses RWMutex for minimal lock contention on reads (cache hits).
func (im *IdempotencyManager) CheckAndMark(deliveryID string) bool {
	now := time.Now()

	// Record check metric
	im.recordCheck()

	// First try read lock for cache check (optimized for cache hits)
	im.mu.RLock()
	processedAt, exists := im.cache.Get(deliveryID)
	im.mu.RUnlock()

	if exists {
		// Check if entry has expired
		if now.Sub(processedAt) < im.ttl {
			// Valid cache hit - already processed
			im.recordDuplicate()
			return true
		}
		// Entry expired, need to remove and re-add (treat as new)
	}

	// Take write lock to mark as processing
	im.mu.Lock()
	defer im.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine might have added it)
	processedAt, exists = im.cache.Peek(deliveryID) // Peek doesn't update LRU order
	if exists && now.Sub(processedAt) < im.ttl {
		im.recordDuplicate()
		return true
	}

	// Mark as processing now
	im.cache.Add(deliveryID, now)

	// Update cache size metric
	im.updateCacheSizeMetric()

	return false
}

// Remove removes a delivery ID from the cache (useful for testing or manual cleanup)
func (im *IdempotencyManager) Remove(deliveryID string) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.cache.Remove(deliveryID)
	im.updateCacheSizeMetric()
}

// Clear removes all entries from the cache (useful for testing or reset)
func (im *IdempotencyManager) Clear() {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.cache.Purge()
	im.updateCacheSizeMetric()
}

// GetCacheSize returns the current number of entries in the cache
func (im *IdempotencyManager) GetCacheSize() int {
	im.mu.RLock()
	defer im.mu.RUnlock()

	return im.cache.Len()
}

// recordDuplicate increments the duplicate counter metric
func (im *IdempotencyManager) recordDuplicate() {
	if im.registry != nil {
		if counter := im.registry.Get(MetricsKeyIdempotencyDuplicates); counter != nil {
			if c, ok := counter.(metrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// recordCheck increments the check counter metric
func (im *IdempotencyManager) recordCheck() {
	if im.registry != nil {
		if counter := im.registry.Get(MetricsKeyIdempotencyChecks); counter != nil {
			if c, ok := counter.(metrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// updateCacheSizeMetric updates the cache size gauge metric
// NOTE: This method assumes the mutex is already held by the caller
func (im *IdempotencyManager) updateCacheSizeMetric() {
	if im.registry != nil {
		if gauge := im.registry.Get(MetricsKeyIdempotencyCacheSize); gauge != nil {
			if g, ok := gauge.(metrics.Gauge); ok {
				g.Update(int64(im.cache.Len()))
			}
		}
	}
}
