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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/rs/zerolog"
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

// InstallationRegistry manages a cache of installation verification results
// to reduce API calls to GitHub. It caches both positive (installed) and
// negative (not installed) results with different TTLs.
//
// Enhanced with compound key support for owner:repo lookups in addition to
// installation ID lookups.
//
// Thread Safety: Uses RWMutex for cache access and atomic operations for counters
// to minimize lock contention under high load (200 events/sec target).
//
// Phase 8: Migrated to use only InstallationRecord system, removing legacy cache
// redundancy that was causing 2x memory usage and 2x writes.
type InstallationRegistry struct {
	mu sync.RWMutex

	// installations maps installation ID to enhanced installation records
	// This is the single source of truth for installation data
	installations map[int64]*InstallationRecord

	// repoIndex maps "owner:repo" to installation ID for quick lookups
	repoIndex map[string]int64

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
//
// Phase 8: Migrated to read from InstallationRecord instead of legacy cache.
func (r *InstallationRegistry) Check(installationID int64) (InstallationStatus, bool) {
	r.mu.RLock()
	record, exists := r.installations[installationID]
	r.mu.RUnlock()

	if !exists {
		// Use atomic increment to avoid lock contention
		r.cacheMisses.Add(1)
		r.recordCacheMiss()
		return InstallationUnknown, false
	}

	// Check if entry has expired
	if record.IsExpired() {
		// Entry expired, remove it
		r.mu.Lock()
		delete(r.installations, installationID)
		// Also clean up repo index entries for this installation
		for repoKey, instID := range r.repoIndex {
			if instID == installationID {
				delete(r.repoIndex, repoKey)
			}
		}
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
	return record.Status, true
}

// VerifyInstallation checks if the GitHub App is installed for the given installation ID.
// This is the single source of truth for installation verification, consolidating logic
// that was previously duplicated in Base and InstallationManager.
//
// It first checks the cache, and if not found or expired, optionally makes an API call
// (if appClient is provided) to verify the installation and update the cache.
//
// Returns:
//   - bool: true if installation exists, false if not found or error occurred
//   - error: nil on success, error if API call failed (not including 404)
//
// Thread-safe: Uses RWMutex for cache access and atomic operations for metrics.
func (r *InstallationRegistry) VerifyInstallation(ctx context.Context, installationID int64, appClient *github.Client) (bool, error) {
	logger := zerolog.Ctx(ctx)

	// Check cache first
	status, cached := r.Check(installationID)
	if cached {
		switch status {
		case InstallationExists:
			logger.Debug().
				Int64("installation_id", installationID).
				Msg("Installation found in cache (positive)")
			return true, nil
		case InstallationNotFound:
			logger.Debug().
				Int64("installation_id", installationID).
				Msg("Installation found in cache (negative - not installed)")
			return false, nil
		}
	}

	// Cache miss - need to verify via API
	if appClient == nil {
		// No API client provided, can't verify
		logger.Debug().
			Int64("installation_id", installationID).
			Msg("Installation cache miss, no API client for verification")
		return false, nil
	}

	// Verify installation via GitHub API
	logger.Debug().
		Int64("installation_id", installationID).
		Msg("Installation cache miss - verifying via API")

	r.RecordAPICall()
	installation, resp, err := appClient.Apps.GetInstallation(ctx, installationID)

	if err != nil {
		// Check if it's a 404 (installation not found)
		if resp != nil && resp.StatusCode == 404 || strings.Contains(err.Error(), "404") {
			logger.Info().
				Int64("installation_id", installationID).
				Msg("Installation not found - app may not be installed")

			// Cache negative result to avoid repeated API calls
			r.MarkNotInstalled(installationID)
			return false, nil
		}

		// Other errors are unexpected
		logger.Warn().Err(err).
			Int64("installation_id", installationID).
			Msg("Failed to verify installation")
		return false, err
	}

	// Check response status code even when err is nil
	// The GitHub client may not return errors for all non-200 responses
	if resp != nil && resp.StatusCode >= 400 {
		if resp.StatusCode == 404 {
			logger.Info().
				Int64("installation_id", installationID).
				Msg("Installation not found (404 status code)")
			r.MarkNotInstalled(installationID)
			return false, nil
		}

		// Other 4xx/5xx errors
		err := fmt.Errorf("unexpected status code %d for installation %d", resp.StatusCode, installationID)
		logger.Warn().
			Int("status_code", resp.StatusCode).
			Int64("installation_id", installationID).
			Msg("Failed to verify installation - unexpected status code")
		return false, err
	}

	// Cache the valid installation (positive result)
	r.MarkInstalled(installationID)

	logger.Debug().
		Int64("installation_id", installationID).
		Int64("github_installation_id", installation.GetID()).
		Msg("Installation verified and cached")

	return true, nil
}

// MarkInstalled marks an installation as installed (positive cache)
//
// Phase 8: Migrated to write to InstallationRecord instead of legacy cache.
func (r *InstallationRegistry) MarkInstalled(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if record exists, create or update it
	record, exists := r.installations[installationID]
	if !exists {
		record = &InstallationRecord{
			InstallationID: installationID,
			Status:         InstallationExists,
			ExpiresAt:      time.Now().Add(r.positiveTTL),
			Repositories:   make(map[string]bool),
			LastUpdated:    time.Now(),
		}
		r.installations[installationID] = record
	} else {
		// Update existing record
		record.Status = InstallationExists
		record.ExpiresAt = time.Now().Add(r.positiveTTL)
		record.LastUpdated = time.Now()
	}

	r.updateCacheGauges()
}

// MarkNotInstalled marks an installation as not installed (negative cache)
//
// Phase 8: Migrated to write to InstallationRecord instead of legacy cache.
func (r *InstallationRegistry) MarkNotInstalled(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if record exists, create or update it
	record, exists := r.installations[installationID]
	if !exists {
		record = &InstallationRecord{
			InstallationID: installationID,
			Status:         InstallationNotFound,
			ExpiresAt:      time.Now().Add(r.negativeTTL),
			Repositories:   make(map[string]bool),
			LastUpdated:    time.Now(),
		}
		r.installations[installationID] = record
	} else {
		// Update existing record
		record.Status = InstallationNotFound
		record.ExpiresAt = time.Now().Add(r.negativeTTL)
		record.LastUpdated = time.Now()
	}

	r.updateCacheGauges()
}

// Remove removes an installation from the cache.
// This should be called when an installation is deleted or repositories are removed.
//
// Phase 8: Migrated to remove from InstallationRecord instead of legacy cache.
func (r *InstallationRegistry) Remove(installationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove installation record
	delete(r.installations, installationID)

	// Also clean up repo index entries for this installation
	for repoKey, instID := range r.repoIndex {
		if instID == installationID {
			delete(r.repoIndex, repoKey)
		}
	}

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
//
// Phase 8: Migrated to count InstallationRecord entries instead of legacy cache.
func (r *InstallationRegistry) GetCacheSize() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.installations)
}

// Clear removes all entries from the cache
//
// Phase 8: Migrated to clear only InstallationRecord, legacy cache removed.
func (r *InstallationRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.installations = make(map[int64]*InstallationRecord)
	r.repoIndex = make(map[string]int64)
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
//
// Phase 8: Migrated to count from InstallationRecord instead of legacy cache.
func (r *InstallationRegistry) updateCacheGauges() {
	if r.metricsRegistry == nil {
		return
	}

	// Count positive and negative entries from InstallationRecord
	var positiveCount, negativeCount int64
	for _, record := range r.installations {
		// Skip expired entries in count (they'll be cleaned up on next access)
		if record.IsExpired() {
			continue
		}

		switch record.Status {
		case InstallationExists:
			positiveCount++
		case InstallationNotFound:
			negativeCount++
		}
	}

	// Update cache size gauge (count of non-expired entries)
	if gauge := r.metricsRegistry.Get(MetricsKeyRegistryCacheSize); gauge != nil {
		if g, ok := gauge.(gometrics.Gauge); ok {
			g.Update(positiveCount + negativeCount)
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
//
// Phase 8: Removed legacy cache update - now only updates InstallationRecord.
func (r *InstallationRegistry) UpdateInstallation(record *InstallationRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Update the installations map (single source of truth)
	r.installations[record.InstallationID] = record

	// Rebuild repo index for this installation
	if record.Repositories != nil {
		for repoKey := range record.Repositories {
			r.repoIndex[repoKey] = record.InstallationID
		}
	}

	r.updateCacheGauges()
}

// AddRepositories adds repositories to an existing installation
//
// Phase 8: Removed legacy cache update - now only updates InstallationRecord.
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
