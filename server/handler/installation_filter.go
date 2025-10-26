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
	wrapped         githubapp.EventHandler
	registry        *InstallationRegistry
	metrics         *InstallationFilterMetrics
	metricsRegistry gometrics.Registry
}

// InstallationFilterMetrics tracks filtering statistics
// Using atomic operations for thread safety
type InstallationFilterMetrics struct {
	Filtered int64 // Events filtered out (app not installed)
	Passed   int64 // Events passed through to handler
}

// NewInstallationFilterHandler creates a new filtering wrapper around an event handler
// The metricsRegistry parameter can be nil for testing, but should be provided in production
// for metrics export via OTEL.
func NewInstallationFilterHandler(handler githubapp.EventHandler, registry *InstallationRegistry, metricsRegistry gometrics.Registry) *InstallationFilterHandler {
	h := &InstallationFilterHandler{
		wrapped:         handler,
		registry:        registry,
		metrics:         &InstallationFilterMetrics{},
		metricsRegistry: metricsRegistry,
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

	// Extract installation ID from payload
	installationID, err := extractInstallationID(payload)
	if err != nil {
		// If we can't extract installation ID, pass through to handler
		// (it will handle the error appropriately)
		logger.Debug().
			Err(err).
			Str("event_type", eventType).
			Str("delivery_id", deliveryID).
			Msg("Could not extract installation ID, passing to handler")

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

// extractInstallationID extracts the installation ID from a GitHub webhook payload
func extractInstallationID(payload []byte) (int64, error) {
	// Parse payload to extract installation.id
	var event struct {
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return 0, err
	}

	if event.Installation == nil {
		return 0, ErrNoInstallation
	}

	return event.Installation.ID, nil
}

var ErrNoInstallation = errors.New("no installation in payload")
