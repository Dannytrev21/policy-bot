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
	"sync/atomic"
	"time"

	gometrics "github.com/rcrowley/go-metrics"
)

// Metric keys for installation registry metrics
const (
	MetricsKeyRegistryCacheHits     = "installation.registry.cache_hits_total"
	MetricsKeyRegistryCacheMisses   = "installation.registry.cache_misses_total"
	MetricsKeyRegistryAPICalls      = "installation.registry.api_calls_total"
	MetricsKeyRegistryCacheSize     = "installation.registry.cache_size"
	MetricsKeyRegistryPositiveCache = "installation.registry.positive_entries"
	MetricsKeyRegistryNegativeCache = "installation.registry.negative_entries"
)

// InstallationStatus represents the cached status of an installation
type InstallationStatus int

const (
	// InstallationUnknown means we haven't checked this installation yet
	InstallationUnknown InstallationStatus = iota
	// InstallationExists means the app is installed
	InstallationExists
	// InstallationNotFound means the app is not installed (404)
	InstallationNotFound
)

// installationCacheEntry represents a cached installation status with expiration
// Deprecated: Use InstallationRecord for new code
type installationCacheEntry struct {
	status    InstallationStatus
	expiresAt time.Time
}

// InstallationRegistry manages a cache of installation verification results
// to reduce API calls to GitHub. It caches both positive (installed) and
// negative (not installed) results with different TTLs.
//
// Enhanced with compound key support for owner:repo lookups in addition to
// installation ID lookups.
//
// Thread Safety: Uses RWMutex for cache access and atomic operations for counters
// to minimize lock contention under high load (200 events/sec target).
type InstallationRegistry struct {
	mu sync.RWMutex

	// installations maps installation ID to enhanced installation records
	installations map[int64]*InstallationRecord

	// repoIndex maps "owner:repo" to installation ID for quick lookups
	repoIndex map[string]int64

	// cache maps installation ID to its cached status (legacy compatibility)
	cache map[int64]installationCacheEntry

	// TTL for positive results (app is installed)
	positiveTTL time.Duration

	// TTL for negative results (app is not installed)
	negativeTTL time.Duration

	// Metrics - using atomics to avoid lock contention on counter updates
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
	apiCalls    atomic.Int64

	// Metrics registry for OTEL export
	metricsRegistry gometrics.Registry
}

// NewInstallationRegistry creates a new installation registry with specified TTLs.
// The metricsRegistry parameter can be nil for testing, but should be provided in production
// for metrics export via OTEL.
func NewInstallationRegistry(positiveTTL, negativeTTL time.Duration, metricsRegistry gometrics.Registry) *InstallationRegistry {
	r := &InstallationRegistry{
		installations:   make(map[int64]*InstallationRecord),
		repoIndex:       make(map[string]int64),
		cache:           make(map[int64]installationCacheEntry),
		positiveTTL:     positiveTTL,
		negativeTTL:     negativeTTL,
		metricsRegistry: metricsRegistry,
	}

	// Register metrics if registry is provided
	if metricsRegistry != nil {
		gometrics.GetOrRegisterCounter(MetricsKeyRegistryCacheHits, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyRegistryCacheMisses, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyRegistryAPICalls, metricsRegistry)
		gometrics.GetOrRegisterGauge(MetricsKeyRegistryCacheSize, metricsRegistry)
		gometrics.GetOrRegisterGauge(MetricsKeyRegistryPositiveCache, metricsRegistry)
		gometrics.GetOrRegisterGauge(MetricsKeyRegistryNegativeCache, metricsRegistry)
	}

	return r
}

// Check returns the cached status of an installation.
// Returns (status, cacheHit) where cacheHit is true if result came from cache.
//
// Performance: Uses atomic operations for counter updates to avoid lock contention.
// Only takes write lock when modifying cache entries (expired cleanup).
func (r *InstallationRegistry) Check(installationID int64) (InstallationStatus, bool) {
	r.mu.RLock()
	entry, exists := r.cache[installationID]
	r.mu.RUnlock()

	if !exists {
		// Use atomic increment to avoid lock contention
		r.cacheMisses.Add(1)
		r.recordCacheMiss()
		return InstallationUnknown, false
	}

	// Check if entry has expired
	if time.Now().After(entry.expiresAt) {
		// Entry expired, remove it
		r.mu.Lock()
		delete(r.cache, installationID)
		r.updateCacheGauges()
		r.mu.Unlock()

		// Record miss with atomic operation (no lock needed)
		r.cacheMisses.Add(1)
		r.recordCacheMiss()
		return InstallationUnknown, false
	}

	// Valid cache hit - use atomic increment
	r.cacheHits.Add(1)
	r.recordCacheHit()
	return entry.status, true
}

// MarkInstalled marks an installation as installed (positive cache)
func (r *InstallationRegistry) MarkInstalled(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[installationID] = installationCacheEntry{
		status:    InstallationExists,
		expiresAt: time.Now().Add(r.positiveTTL),
	}

	r.updateCacheGauges()
}

// MarkNotInstalled marks an installation as not installed (negative cache)
func (r *InstallationRegistry) MarkNotInstalled(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[installationID] = installationCacheEntry{
		status:    InstallationNotFound,
		expiresAt: time.Now().Add(r.negativeTTL),
	}

	r.updateCacheGauges()
}

// Remove removes an installation from the cache.
// This should be called when an installation is deleted or repositories are removed.
func (r *InstallationRegistry) Remove(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.cache, installationID)
	r.updateCacheGauges()
}

// RecordAPICall increments the API call counter
// Performance: Uses atomic operation - no lock needed
func (r *InstallationRegistry) RecordAPICall() {
	// Use atomic increment - no lock contention
	r.apiCalls.Add(1)

	// Record in go-metrics registry if available
	if r.metricsRegistry != nil {
		if counter := r.metricsRegistry.Get(MetricsKeyRegistryAPICalls); counter != nil {
			if c, ok := counter.(gometrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// GetMetrics returns current cache metrics
// Performance: Uses atomic loads - no lock needed
func (r *InstallationRegistry) GetMetrics() (cacheHits, cacheMisses, apiCalls int64) {
	// Use atomic loads - no lock needed
	return r.cacheHits.Load(), r.cacheMisses.Load(), r.apiCalls.Load()
}

// GetCacheSize returns the current number of entries in the cache
func (r *InstallationRegistry) GetCacheSize() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.cache)
}

// Clear removes all entries from the cache
func (r *InstallationRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.installations = make(map[int64]*InstallationRecord)
	r.repoIndex = make(map[string]int64)
	r.cache = make(map[int64]installationCacheEntry)
	r.updateCacheGauges()
}

// recordCacheHit increments the cache hit counter in the metrics registry
// Thread-safe: Can be called without holding the mutex (uses go-metrics internal thread safety)
func (r *InstallationRegistry) recordCacheHit() {
	if r.metricsRegistry != nil {
		if counter := r.metricsRegistry.Get(MetricsKeyRegistryCacheHits); counter != nil {
			if c, ok := counter.(gometrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// recordCacheMiss increments the cache miss counter in the metrics registry
// Thread-safe: Can be called without holding the mutex (uses go-metrics internal thread safety)
func (r *InstallationRegistry) recordCacheMiss() {
	if r.metricsRegistry != nil {
		if counter := r.metricsRegistry.Get(MetricsKeyRegistryCacheMisses); counter != nil {
			if c, ok := counter.(gometrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// updateCacheGauges updates the gauge metrics for cache size and composition
// NOTE: This method assumes the mutex is already held by the caller
func (r *InstallationRegistry) updateCacheGauges() {
	if r.metricsRegistry == nil {
		return
	}

	// Count positive and negative entries
	var positiveCount, negativeCount int64
	for _, entry := range r.cache {
		switch entry.status {
		case InstallationExists:
			positiveCount++
		case InstallationNotFound:
			negativeCount++
		}
	}

	// Update cache size gauge
	if gauge := r.metricsRegistry.Get(MetricsKeyRegistryCacheSize); gauge != nil {
		if g, ok := gauge.(gometrics.Gauge); ok {
			g.Update(int64(len(r.cache)))
		}
	}

	// Update positive entries gauge
	if gauge := r.metricsRegistry.Get(MetricsKeyRegistryPositiveCache); gauge != nil {
		if g, ok := gauge.(gometrics.Gauge); ok {
			g.Update(positiveCount)
		}
	}

	// Update negative entries gauge
	if gauge := r.metricsRegistry.Get(MetricsKeyRegistryNegativeCache); gauge != nil {
		if g, ok := gauge.(gometrics.Gauge); ok {
			g.Update(negativeCount)
		}
	}
}

// === Enhanced Methods for Compound Key Support ===

// CheckByRepo looks up an installation by owner:repo compound key
// Returns (installationID, status, cacheHit)
func (r *InstallationRegistry) CheckByRepo(owner, repo string) (int64, InstallationStatus, bool) {
	key := fmt.Sprintf("%s:%s", owner, repo)

	r.mu.RLock()
	installationID, exists := r.repoIndex[key]
	r.mu.RUnlock()

	if !exists {
		r.cacheMisses.Add(1)
		r.recordCacheMiss()
		return 0, InstallationUnknown, false
	}

	// Now check the installation itself
	status, hit := r.Check(installationID)
	if !hit {
		// Installation expired or not found, clean up repo index
		r.mu.Lock()
		delete(r.repoIndex, key)
		r.mu.Unlock()
		return 0, InstallationUnknown, false
	}

	return installationID, status, true
}

// UpdateInstallation updates or creates an enhanced installation record
func (r *InstallationRegistry) UpdateInstallation(record *InstallationRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Update the installations map
	r.installations[record.InstallationID] = record

	// Update the legacy cache for compatibility
	r.cache[record.InstallationID] = installationCacheEntry{
		status:    record.Status,
		expiresAt: record.ExpiresAt,
	}

	// Rebuild repo index for this installation
	if record.Repositories != nil {
		for repoKey := range record.Repositories {
			r.repoIndex[repoKey] = record.InstallationID
		}
	}

	r.updateCacheGauges()
}

// AddRepositories adds repositories to an existing installation
func (r *InstallationRegistry) AddRepositories(installationID int64, repos []struct{ Owner, Repo string }) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, exists := r.installations[installationID]
	if !exists {
		// Create a new record if it doesn't exist
		record = &InstallationRecord{
			InstallationID: installationID,
			Status:         InstallationExists,
			ExpiresAt:      time.Now().Add(r.positiveTTL),
			Repositories:   make(map[string]bool),
			LastUpdated:    time.Now(),
		}
		r.installations[installationID] = record
	}

	// Add repositories to the record and update index
	for _, repo := range repos {
		key := fmt.Sprintf("%s:%s", repo.Owner, repo.Repo)
		record.Repositories[key] = true
		r.repoIndex[key] = installationID
	}

	record.LastUpdated = time.Now()

	// Update legacy cache
	r.cache[installationID] = installationCacheEntry{
		status:    InstallationExists,
		expiresAt: record.ExpiresAt,
	}
}

// RemoveRepositories removes repositories from an installation
func (r *InstallationRegistry) RemoveRepositories(installationID int64, repos []struct{ Owner, Repo string }) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, exists := r.installations[installationID]
	if !exists {
		return
	}

	// Remove repositories from the record and index
	for _, repo := range repos {
		key := fmt.Sprintf("%s:%s", repo.Owner, repo.Repo)
		delete(record.Repositories, key)
		delete(r.repoIndex, key)
	}

	record.LastUpdated = time.Now()
}

// GetInstallation returns the full installation record if it exists and is not expired
func (r *InstallationRegistry) GetInstallation(installationID int64) (*InstallationRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, exists := r.installations[installationID]
	if !exists {
		return nil, false
	}

	if record.IsExpired() {
		return nil, false
	}

	return record, true
}
