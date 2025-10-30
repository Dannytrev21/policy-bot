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
	"sync/atomic"

	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

// Metric keys for installation filter metrics
const (
	MetricsKeyFilterEventsFiltered    = "installation.filter.events_filtered_total"
	MetricsKeyFilterEventsPassed      = "installation.filter.events_passed_total"
	MetricsKeyFilterCacheHitsPositive = "installation.filter.cache_hits.positive"
	MetricsKeyFilterCacheHitsNegative = "installation.filter.cache_hits.negative"
)

// InstallationFilterHandler is a decorator that wraps an EventHandler to filter
// events from non-installed repositories before they are processed.
// This reduces queue pressure and processing overhead for events that would be
// rejected anyway.
type InstallationFilterHandler struct {
	wrapped              githubapp.EventHandler
	registry             *InstallationRegistry
	installationsService githubapp.InstallationsService
	metrics              *InstallationFilterMetrics
	metricsRegistry      gometrics.Registry
}

// InstallationFilterMetrics tracks filtering statistics
// Using atomic operations for thread safety
type InstallationFilterMetrics struct {
	Filtered int64 // Events filtered out (app not installed)
	Passed   int64 // Events passed through to handler
}

// NewInstallationFilterHandler creates a new filtering wrapper around an event handler
// The installationsService parameter can be nil for testing or when repository-based fallback is not needed.
// The metricsRegistry parameter can be nil for testing, but should be provided in production
// for metrics export via OTEL.
func NewInstallationFilterHandler(
	handler githubapp.EventHandler,
	registry *InstallationRegistry,
	installationsService githubapp.InstallationsService,
	metricsRegistry gometrics.Registry,
) *InstallationFilterHandler {
	h := &InstallationFilterHandler{
		wrapped:              handler,
		registry:             registry,
		installationsService: installationsService,
		metrics:              &InstallationFilterMetrics{},
		metricsRegistry:      metricsRegistry,
	}

	// Register metrics counters if registry is provided
	if metricsRegistry != nil {
		gometrics.GetOrRegisterCounter(MetricsKeyFilterEventsFiltered, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterEventsPassed, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterCacheHitsPositive, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterCacheHitsNegative, metricsRegistry)
	}

	return h
}

// Handles returns the event types that this handler can process
func (h *InstallationFilterHandler) Handles() []string {
	return h.wrapped.Handles()
}

// Handle processes an event, filtering it first based on installation status
func (h *InstallationFilterHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	logger := zerolog.Ctx(ctx)

	// Extract installation ID from payload with fallback strategies
	// Layer 1: Direct extraction from installation.id
	// Layer 2: Repository-based lookup via GitHub API
	// Layer 3: Pass through to handler (if both fail)
	installationID, err := h.extractInstallationIDWithFallback(ctx, payload)
	if err != nil {
		// If we can't extract installation ID via any method, pass through to handler
		// (it will handle the error appropriately)
		logger.Debug().
			Err(err).
			Str("event_type", eventType).
			Str("delivery_id", deliveryID).
			Msg("Could not extract installation ID (all layers failed), passing to handler")

		h.recordPassedEvent()
		return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
	}

	// Check installation registry
	status, cacheHit := h.registry.Check(installationID)

	// Record cache hit metrics
	if cacheHit {
		h.recordCacheHit(status)
	}

	// Only filter if we have a definitive negative cache hit
	// If unknown (cache miss), pass through to handler for verification
	if cacheHit && status == InstallationNotFound {
		logger.Info().
			Int64("installation_id", installationID).
			Str("event_type", eventType).
			Str("delivery_id", deliveryID).
			Msg("Event filtered - app not installed (negative cache hit)")

		h.recordFilteredEvent()
		// Return nil to indicate successful handling (event was appropriately filtered)
		return nil
	}

	// Pass through to wrapped handler for:
	// - Installed (positive cache hit)
	// - Unknown (cache miss - let handler verify)
	logger.Debug().
		Int64("installation_id", installationID).
		Str("event_type", eventType).
		Str("delivery_id", deliveryID).
		Bool("cache_hit", cacheHit).
		Msg("Event passed to handler")

	h.recordPassedEvent()
	return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
}

// recordFilteredEvent records metrics for a filtered event
func (h *InstallationFilterHandler) recordFilteredEvent() {
	atomic.AddInt64(&h.metrics.Filtered, 1)

	// Record in go-metrics registry if available
	if h.metricsRegistry != nil {
		if counter := h.metricsRegistry.Get(MetricsKeyFilterEventsFiltered); counter != nil {
			if c, ok := counter.(gometrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// recordPassedEvent records metrics for a passed event
func (h *InstallationFilterHandler) recordPassedEvent() {
	atomic.AddInt64(&h.metrics.Passed, 1)

	// Record in go-metrics registry if available
	if h.metricsRegistry != nil {
		if counter := h.metricsRegistry.Get(MetricsKeyFilterEventsPassed); counter != nil {
			if c, ok := counter.(gometrics.Counter); ok {
				c.Inc(1)
			}
		}
	}
}

// recordCacheHit records metrics for a cache hit
func (h *InstallationFilterHandler) recordCacheHit(status InstallationStatus) {
	if h.metricsRegistry == nil {
		return
	}

	var metricKey string
	switch status {
	case InstallationExists:
		metricKey = MetricsKeyFilterCacheHitsPositive
	case InstallationNotFound:
		metricKey = MetricsKeyFilterCacheHitsNegative
	default:
		return // Don't record for unknown status
	}

	if counter := h.metricsRegistry.Get(metricKey); counter != nil {
		if c, ok := counter.(gometrics.Counter); ok {
			c.Inc(1)
		}
	}
}

// GetMetrics returns the current filtering metrics
func (h *InstallationFilterHandler) GetMetrics() (filtered, passed int64) {
	return atomic.LoadInt64(&h.metrics.Filtered), atomic.LoadInt64(&h.metrics.Passed)
}

// extractInstallationID extracts the installation ID from a GitHub webhook payload.
// It validates that the installation ID is non-zero before returning it.
// Returns:
//   - (installationID, nil) if a valid installation ID (> 0) is found
//   - (0, ErrNoInstallation) if installation field is nil or ID is zero
//   - (0, error) if JSON parsing fails
func extractInstallationID(payload []byte) (int64, error) {
	// Parse payload to extract installation.id
	var event struct {
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return 0, errors.Wrap(err, "failed to unmarshal event payload")
	}

	if event.Installation == nil {
		return 0, ErrNoInstallation
	}

	// Validate installation ID is non-zero
	// A zero ID indicates the installation field exists but is invalid
	if event.Installation.ID == 0 {
		return 0, errors.Wrap(ErrNoInstallation, "installation ID is zero (invalid)")
	}

	return event.Installation.ID, nil
}

var ErrNoInstallation = errors.New("no installation in payload")
var ErrNoRepository = errors.New("no repository in payload")

// extractRepository extracts repository owner and name from a GitHub webhook payload.
// Returns (owner, repo, nil) if successfully extracted, or ("", "", error) otherwise.
func extractRepository(payload []byte) (owner, repo string, err error) {
	var event struct {
		Repository *struct {
			Owner *struct {
				Login string `json:"login"`
			} `json:"owner"`
			Name string `json:"name"`
		} `json:"repository"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return "", "", errors.Wrap(err, "failed to unmarshal repository from payload")
	}

	if event.Repository == nil {
		return "", "", ErrNoRepository
	}

	if event.Repository.Owner == nil || event.Repository.Owner.Login == "" {
		return "", "", errors.New("repository owner is missing or empty")
	}

	if event.Repository.Name == "" {
		return "", "", errors.New("repository name is missing or empty")
	}

	return event.Repository.Owner.Login, event.Repository.Name, nil
}

// extractInstallationIDWithFallback attempts multiple strategies to extract installation ID:
// Layer 1: Direct extraction from event.installation.id (fast path)
// Layer 2: Repository-based lookup via GitHub API (fallback for events without installation ID)
// Layer 3: Returns error to pass through to handler (final fallback)
func (h *InstallationFilterHandler) extractInstallationIDWithFallback(
	ctx context.Context,
	payload []byte,
) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// Layer 1: Try direct extraction from payload (fast path)
	installationID, err := extractInstallationID(payload)
	if err == nil && installationID > 0 {
		logger.Debug().
			Int64("installation_id", installationID).
			Msg("Installation ID extracted directly from payload (Layer 1)")
		return installationID, nil
	}

	// Layer 2: Try repository-based lookup as fallback
	logger.Debug().
		Err(err).
		Msg("Direct extraction failed, attempting repository-based lookup (Layer 2)")

	repoOwner, repoName, repoErr := extractRepository(payload)
	if repoErr != nil {
		// No repository info either, pass through to handler (Layer 3)
		logger.Debug().
			Err(err).
			Err(repoErr).
			Msg("Cannot extract installation ID or repository, passing to handler (Layer 3)")
		return 0, ErrNoInstallation
	}

	// Use InstallationsService to look up installation by repository
	// This will be cached by InstallationRegistry for future lookups
	return h.lookupInstallationByRepository(ctx, repoOwner, repoName)
}

// lookupInstallationByRepository queries GitHub API for installation by repository.
// Results are cached in InstallationRegistry to avoid repeated API calls.
// Returns (installationID, nil) if found, or (0, error) otherwise.
func (h *InstallationFilterHandler) lookupInstallationByRepository(
	ctx context.Context,
	owner, repo string,
) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// Check if we have an InstallationsService (required for lookup)
	if h.installationsService == nil {
		logger.Debug().
			Str("owner", owner).
			Str("repo", repo).
			Msg("No installations service available for repository lookup")
		return 0, errors.New("installations service not available")
	}

	// Look up installation via API
	logger.Debug().
		Str("owner", owner).
		Str("repo", repo).
		Msg("Looking up installation by repository via GitHub API")

	installation, err := h.installationsService.GetByRepository(ctx, owner, repo)
	if err != nil {
		// Check if it's a "not found" error (app not installed on this repository)
		if IsInstallationNotFoundError(err) {
			logger.Info().
				Str("owner", owner).
				Str("repo", repo).
				Msg("No installation found for repository (app not installed)")

			// We don't have an installation ID to cache the negative result
			// The event will pass through to handler which will handle appropriately
			return 0, ErrNoInstallation
		}

		// Other API errors (network, auth, etc.)
		logger.Warn().
			Err(err).
			Str("owner", owner).
			Str("repo", repo).
			Msg("Failed to lookup installation by repository")
		return 0, errors.Wrap(err, "failed to lookup installation by repository")
	}

	// Successfully found installation - cache the positive result
	installationID := installation.ID
	logger.Info().
		Int64("installation_id", installationID).
		Str("owner", owner).
		Str("repo", repo).
		Msg("Found installation via repository lookup, caching result")

	h.registry.MarkInstalled(installationID)
	return installationID, nil
}
