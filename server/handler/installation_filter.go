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
	"sync"
	"sync/atomic"
	"time"

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

	// Phase 4: Lookup method effectiveness metrics (SQS events only)
	MetricsKeyLookupMethodDirect    = "installation.lookup.method.direct"
	MetricsKeyLookupMethodRepoCache = "installation.lookup.method.repo_cache"
	MetricsKeyLookupMethodOrgCache  = "installation.lookup.method.org_cache"
	MetricsKeyLookupMethodRepoAPI   = "installation.lookup.method.repo_api"
	MetricsKeyLookupMethodOrgAPI    = "installation.lookup.method.org_api"
	MetricsKeyLookupAllFailed       = "installation.lookup.all_failed"

	// SQS event source detection - must match sqsconsumer package
	SQSEventSourceKey = "sqs_event_source"
)

// InstallationFilterHandler is a decorator that wraps an EventHandler to filter
// events from non-installed repositories before they are processed.
// This reduces queue pressure and processing overhead for events that would be
// rejected anyway.
type InstallationFilterHandler struct {
	wrapped              githubapp.EventHandler
	registry             *InstallationRegistry
	repoCache            *MappingCache // Repository → Installation ID mapping
	orgCache             *MappingCache // Organization → Installation ID mapping
	installationsService githubapp.InstallationsService
	metrics              *InstallationFilterMetrics
	metricsRegistry      gometrics.Registry

	// Enhanced components for improved filtering
	locator    *InstallationLocator
	classifier *EventClassifier

	// Phase 8 Step 4: Configuration to control filtering behavior per event source
	filterConfig *FilterConfig
}

// FilterConfig controls whether installation filtering is applied per event source
type FilterConfig struct {
	WebhookFilteringEnabled bool // Enable filtering for webhook events (default: false)
	SQSFilteringEnabled     bool // Enable filtering for SQS events (default: true)
}

// InstallationFilterMetrics tracks filtering statistics
// Using atomic operations for thread safety
type InstallationFilterMetrics struct {
	Filtered int64 // Events filtered out (app not installed)
	Passed   int64 // Events passed through to handler
}

// NewInstallationFilterHandler creates a new filtering wrapper around an event handler
// The installationsService parameter can be nil for testing or when repository-based fallback is not needed.
// The repoCache and orgCache parameters are the shared mapping caches from Base - if nil, new caches will be created.
// The metricsRegistry parameter can be nil for testing, but should be provided in production
// for metrics export via OTEL.
// The filterConfig parameter controls filtering behavior per event source (webhook vs SQS).
// If nil, default behavior is: webhook filtering disabled, SQS filtering enabled.
func NewInstallationFilterHandler(
	handler githubapp.EventHandler,
	registry *InstallationRegistry,
	installationsService githubapp.InstallationsService,
	metricsRegistry gometrics.Registry,
	repoCache *MappingCache,
	orgCache *MappingCache,
	locator *InstallationLocator,
	filterConfig *FilterConfig,
) *InstallationFilterHandler {
	// Use provided caches or create new ones for testing
	if repoCache == nil {
		repoCache = NewMappingCache(1*time.Hour, 5*time.Minute)
	}
	if orgCache == nil {
		orgCache = NewMappingCache(1*time.Hour, 5*time.Minute)
	}

	// Use default filter config if not provided
	if filterConfig == nil {
		filterConfig = &FilterConfig{
			WebhookFilteringEnabled: false, // Default: no filtering for webhooks
			SQSFilteringEnabled:     true,  // Default: filtering enabled for SQS
		}
	}

	h := &InstallationFilterHandler{
		wrapped:              handler,
		registry:             registry,
		repoCache:            repoCache,
		orgCache:             orgCache,
		installationsService: installationsService,
		metrics:              &InstallationFilterMetrics{},
		metricsRegistry:      metricsRegistry,
		locator:              locator,
		classifier:           NewEventClassifier(),
		filterConfig:         filterConfig,
	}

	// Register metrics counters if registry is provided
	if metricsRegistry != nil {
		gometrics.GetOrRegisterCounter(MetricsKeyFilterEventsFiltered, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterEventsPassed, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterCacheHitsPositive, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyFilterCacheHitsNegative, metricsRegistry)

		// Phase 4: Register lookup method metrics
		gometrics.GetOrRegisterCounter(MetricsKeyLookupMethodDirect, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyLookupMethodRepoCache, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyLookupMethodOrgCache, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyLookupMethodRepoAPI, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyLookupMethodOrgAPI, metricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyLookupAllFailed, metricsRegistry)
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

	// Phase 8 Step 4: Check if filtering is enabled for this event source
	isSQS := false
	if eventSource, ok := ctx.Value(SQSEventSourceKey).(string); ok && eventSource == "sqs" {
		isSQS = true
	}

	// Check if filtering is enabled for this event source
	filteringEnabled := (isSQS && h.filterConfig.SQSFilteringEnabled) ||
		(!isSQS && h.filterConfig.WebhookFilteringEnabled)

	if !filteringEnabled {
		// Filtering disabled for this event source - pass through directly
		logger.Debug().
			Str("event_type", eventType).
			Str("delivery_id", deliveryID).
			Bool("is_sqs", isSQS).
			Bool("webhook_filtering_enabled", h.filterConfig.WebhookFilteringEnabled).
			Bool("sqs_filtering_enabled", h.filterConfig.SQSFilteringEnabled).
			Msg("Installation filtering disabled for this event source - passing through")

		h.recordPassedEvent()
		return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
	}

	// Filtering is enabled - proceed with normal filtering logic
	logger.Debug().
		Str("event_type", eventType).
		Str("delivery_id", deliveryID).
		Bool("is_sqs", isSQS).
		Msg("Installation filtering enabled - proceeding with lookup")

	// Use enhanced handling if locator is available
	if h.locator != nil && h.classifier != nil {
		return h.handleEnhanced(ctx, eventType, deliveryID, payload)
	}

	// Extract installation ID from payload with fallback strategies
	// Layer 1: Direct extraction from installation.id
	// Layer 2: Repository-based lookup via GitHub API
	// Layer 3: Pass through to handler (if both fail)
	installationID, err := h.extractInstallationIDWithFallback(ctx, payload)
	if err != nil {
		// Check if this is a definitive "not installed" result (negative cache)
		if errors.Is(err, ErrInstallationNotInstalled) {
			logger.Info().
				Str("event_type", eventType).
				Str("delivery_id", deliveryID).
				Msg("Event filtered - app definitively not installed (SQS negative cache)")

			h.recordFilteredEvent()
			return nil
		}

		// For other errors (ErrNoInstallation, etc.), pass through to handler
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

// handleEnhanced uses the new InstallationLocator and EventClassifier for improved filtering
func (h *InstallationFilterHandler) handleEnhanced(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	logger := zerolog.Ctx(ctx).With().
		Str("event_type", eventType).
		Str("delivery_id", deliveryID).
		Logger()

	// Step 1: Classify the event
	classification := h.classifier.Classify(eventType)

	// Step 2: Check if event should bypass caching
	if classification == EventNoCache {
		logger.Debug().Msg("Event type bypasses caching - passing to handler")
		h.recordPassedEvent()
		return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
	}

	// Step 3: Extract identifiers
	ids, err := extractIdentifiers(payload)
	if err != nil {
		logger.Debug().Err(err).Msg("Failed to extract identifiers - passing to handler")
		h.recordPassedEvent()
		return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
	}
	defer putIdentifiers(ids)

	// Step 4: Determine event source
	eventSource := EventSourceWebhook
	if sqsSource, ok := ctx.Value(SQSEventSourceKey).(string); ok && sqsSource == "sqs" {
		eventSource = EventSourceSQS
		logger.Debug().Msg("Processing SQS event")
	}

	// Step 5: Create lookup request
	lookupReq := LookupRequest{
		InstallationID: ids.InstallationID,
		Owner:          ids.OwnerLogin,
		Repo:           ids.RepoName,
		EventSource:    eventSource,
		EventType:      eventType,
	}

	// Step 6: Perform lookup
	result := h.locator.Lookup(ctx, lookupReq)

	// Step 7: Handle result
	if result.Error != nil {
		// Check if it's a definitive "not installed" result
		if errors.Is(result.Error, ErrInstallationNotInstalled) || result.Source == SourceAPI && !result.Exists {
			logger.Info().
				Int64("installation_id", result.InstallationID).
				Str("source", getSourceName(result.Source)).
				Msg("Event filtered - app not installed")

			h.recordFilteredEvent()
			return nil // Successfully filtered
		}

		// Other errors - pass through to handler
		logger.Debug().
			Err(result.Error).
			Msg("Lookup error - passing to handler")
		h.recordPassedEvent()
		return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
	}

	// Step 8: Check if installation exists
	if !result.Exists {
		logger.Info().
			Int64("installation_id", result.InstallationID).
			Str("source", getSourceName(result.Source)).
			Msg("Event filtered - installation not found")

		h.recordFilteredEvent()
		return nil // Successfully filtered
	}

	// Step 9: Installation exists - pass to handler
	logger.Debug().
		Int64("installation_id", result.InstallationID).
		Str("source", getSourceName(result.Source)).
		Msg("Installation found - passing to handler")

	h.recordPassedEvent()
	return h.wrapped.Handle(ctx, eventType, deliveryID, payload)
}

// getSourceName returns a human-readable name for a lookup source
func getSourceName(source LookupSource) string {
	switch source {
	case SourceCacheID:
		return "cache_id"
	case SourceCacheRepo:
		return "cache_repo"
	case SourceAPI:
		return "api"
	case SourceNotFound:
		return "not_found"
	default:
		return "unknown"
	}
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

// recordLookupMethod records which lookup method successfully found the installation ID.
// This is only called for SQS events to track the effectiveness of different lookup strategies.
// Phase 4: Metrics and Observability
func (h *InstallationFilterHandler) recordLookupMethod(metricKey string) {
	if h.metricsRegistry == nil {
		return
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
var ErrInstallationNotInstalled = errors.New("installation definitively not installed (negative cache)")

// ExtractedIdentifiers holds all possible installation lookup keys extracted from a webhook payload.
// This supports multiple lookup strategies when the direct installation ID is missing or invalid.
//
// Performance: Instances are pooled via identifiersPool to reduce allocations in the hot path
// (one allocation per event without pooling).
type ExtractedIdentifiers struct {
	InstallationID int64  // Direct installation.id from payload
	OwnerLogin     string // organization.login or repository.owner.login
	OwnerID        int64  // organization.id or repository.owner.id
	RepoName       string // repository.name
	RepoID         int64  // repository.id
	AccountLogin   string // installation.account.login (for installation events)
	AccountID      int64  // installation.account.id
}

// identifiersPool is a sync.Pool for reusing ExtractedIdentifiers instances
// to reduce allocations in the hot path (200 events/sec × 1 allocation = 200 allocs/sec saved)
var identifiersPool = sync.Pool{
	New: func() interface{} {
		return &ExtractedIdentifiers{}
	},
}

// getIdentifiers retrieves an ExtractedIdentifiers from the pool
func getIdentifiers() *ExtractedIdentifiers {
	return identifiersPool.Get().(*ExtractedIdentifiers)
}

// putIdentifiers returns an ExtractedIdentifiers to the pool after clearing sensitive data
func putIdentifiers(ids *ExtractedIdentifiers) {
	// Clear all fields to avoid retaining references
	*ids = ExtractedIdentifiers{}
	identifiersPool.Put(ids)
}

// MappingCache has been extracted to mapping_cache.go for better code organization

// buildRepoCacheKey builds a cache key for repository mapping lookups.
// Format: "owner/repo"
// This helper reduces allocations by reusing a single string builder pattern.
func buildRepoCacheKey(owner, repo string) string {
	// Pre-calculate capacity: len(owner) + 1 (for '/') + len(repo)
	// This avoids reallocation during concatenation
	capacity := len(owner) + 1 + len(repo)
	key := make([]byte, 0, capacity)
	key = append(key, owner...)
	key = append(key, '/')
	key = append(key, repo...)
	return string(key)
}

// buildOrgCacheKey builds a cache key for organization mapping lookups.
// Format: "org:orgname"
// This helper reduces allocations for repeated org cache operations.
func buildOrgCacheKey(org string) string {
	// Pre-calculate capacity: 4 (for 'org:') + len(org)
	capacity := 4 + len(org)
	key := make([]byte, 0, capacity)
	key = append(key, "org:"...)
	key = append(key, org...)
	return string(key)
}

// extractIdentifiers extracts all possible identifiers from a GitHub webhook payload.
// This function attempts to extract multiple identifiers to support fallback lookup strategies.
// It does not fail on partial extraction - it returns whatever identifiers it can find.
//
// Performance: Uses sync.Pool to reuse ExtractedIdentifiers instances.
// IMPORTANT: Caller must call putIdentifiers(ids) when done to return to pool.
func extractIdentifiers(payload []byte) (*ExtractedIdentifiers, error) {
	// Get from pool instead of allocating
	ids := getIdentifiers()

	// Try to extract installation.id and installation.account
	var installationEvent struct {
		Installation *struct {
			ID      int64 `json:"id"`
			Account *struct {
				Login string `json:"login"`
				ID    int64  `json:"id"`
			} `json:"account"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(payload, &installationEvent); err == nil {
		if installationEvent.Installation != nil {
			ids.InstallationID = installationEvent.Installation.ID
			if installationEvent.Installation.Account != nil {
				ids.AccountLogin = installationEvent.Installation.Account.Login
				ids.AccountID = installationEvent.Installation.Account.ID
			}
		}
	}

	// Try to extract repository info
	var repoEvent struct {
		Repository *struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Owner *struct {
				Login string `json:"login"`
				ID    int64  `json:"id"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &repoEvent); err == nil {
		if repoEvent.Repository != nil {
			ids.RepoID = repoEvent.Repository.ID
			ids.RepoName = repoEvent.Repository.Name
			if repoEvent.Repository.Owner != nil {
				ids.OwnerLogin = repoEvent.Repository.Owner.Login
				ids.OwnerID = repoEvent.Repository.Owner.ID
			}
		}
	}

	// Try to extract organization info (for org-level events without repository)
	// Only use this if we don't already have owner info from repository
	var orgEvent struct {
		Organization *struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(payload, &orgEvent); err == nil {
		if orgEvent.Organization != nil {
			// Only use org info if we don't have owner info from repository
			// Repository owner is more specific and preferred
			if ids.OwnerLogin == "" {
				ids.OwnerLogin = orgEvent.Organization.Login
				ids.OwnerID = orgEvent.Organization.ID
			}
		}
	}

	return ids, nil
}

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

// lookupInstallationWithSmartCache performs multi-method lookup with proper cache management for SQS events.
// CRITICAL: If ANY method succeeds → Installation exists → Positive cache
//
//	Only if ALL methods fail → Installation doesn't exist → Negative indicators
//
// Lookup priority:
// 1. Direct installation ID from payload (if valid)
// 2. Repository mapping cache (if repo info available)
// 3. Organization mapping cache (if org info available)
// 4. Repository API lookup (if repo info available)
// 5. Organization API lookup (if org info available)
func (h *InstallationFilterHandler) lookupInstallationWithSmartCache(
	ctx context.Context,
	ids *ExtractedIdentifiers,
) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// Check if this is an SQS event for Phase 4 metrics recording
	// Only record metrics for SQS events to track lookup method effectiveness
	isSQSEvent := false
	if eventSource, ok := ctx.Value(SQSEventSourceKey).(string); ok && eventSource == "sqs" {
		isSQSEvent = true
	}

	// Method 1: Direct installation ID from payload
	if ids.InstallationID > 0 {
		// Check if this ID is in positive cache
		if status, cached := h.registry.Check(ids.InstallationID); cached {
			if status == InstallationExists {
				logger.Debug().
					Int64("installation_id", ids.InstallationID).
					Msg("Using direct installation ID (registry cache hit)")
				if isSQSEvent {
					h.recordLookupMethod(MetricsKeyLookupMethodDirect)
				}
				return ids.InstallationID, nil
			}
			// If negative cache, continue to other methods
			// (installation might have been added since negative cache entry)
		}

		// Direct ID found but not cached - return it and let handler cache it
		logger.Debug().
			Int64("installation_id", ids.InstallationID).
			Msg("Using direct installation ID from payload")
		if isSQSEvent {
			h.recordLookupMethod(MetricsKeyLookupMethodDirect)
		}
		return ids.InstallationID, nil
	}

	// Method 2: Check repository mapping cache
	if ids.OwnerLogin != "" && ids.RepoName != "" {
		cacheKey := buildRepoCacheKey(ids.OwnerLogin, ids.RepoName)
		if cachedID, found := h.repoCache.Get(cacheKey); found {
			if cachedID > 0 {
				logger.Debug().
					Str("cache_key", cacheKey).
					Int64("installation_id", cachedID).
					Msg("Repository mapping cache hit (positive)")
				h.registry.MarkInstalled(cachedID) // Ensure positive cache in registry
				if isSQSEvent {
					h.recordLookupMethod(MetricsKeyLookupMethodRepoCache)
				}
				return cachedID, nil
			}
			// Negative cache hit - installation doesn't exist for this repo
			logger.Debug().
				Str("cache_key", cacheKey).
				Msg("Repository mapping cache hit (negative)")
			return 0, ErrInstallationNotInstalled
		}
	}

	// Method 3: Check organization mapping cache
	if ids.OwnerLogin != "" {
		cacheKey := buildOrgCacheKey(ids.OwnerLogin)
		if cachedID, found := h.orgCache.Get(cacheKey); found {
			if cachedID > 0 {
				logger.Debug().
					Str("cache_key", cacheKey).
					Int64("installation_id", cachedID).
					Msg("Organization mapping cache hit (positive)")
				h.registry.MarkInstalled(cachedID) // Ensure positive cache in registry
				if isSQSEvent {
					h.recordLookupMethod(MetricsKeyLookupMethodOrgCache)
				}
				return cachedID, nil
			}
			// Negative cache hit - installation doesn't exist for this org
			logger.Debug().
				Str("cache_key", cacheKey).
				Msg("Organization mapping cache hit (negative)")
			return 0, ErrInstallationNotInstalled
		}
	}

	// Method 4: Repository API lookup
	if ids.OwnerLogin != "" && ids.RepoName != "" {
		logger.Debug().
			Str("owner", ids.OwnerLogin).
			Str("repo", ids.RepoName).
			Msg("Attempting repository-based installation lookup (API call)")

		installationID, err := h.lookupInstallationByRepository(ctx, ids.OwnerLogin, ids.RepoName)
		if err == nil {
			// SUCCESS! Mark as positive in ALL caches
			h.registry.MarkInstalled(installationID) // ✅ POSITIVE CACHE
			h.repoCache.Set(buildRepoCacheKey(ids.OwnerLogin, ids.RepoName), installationID)
			// Also cache at org level if single installation per org (common in GHEC)
			h.orgCache.Set(buildOrgCacheKey(ids.OwnerLogin), installationID)

			logger.Info().
				Int64("installation_id", installationID).
				Str("method", "repository_api_lookup").
				Msg("Found installation via repository lookup")
			if isSQSEvent {
				h.recordLookupMethod(MetricsKeyLookupMethodRepoAPI)
			}
			return installationID, nil
		}

		// Repository lookup failed - try org lookup before giving up
		if !IsInstallationNotFoundError(err) {
			// Network error or other transient failure - don't cache negative
			logger.Warn().
				Err(err).
				Str("owner", ids.OwnerLogin).
				Str("repo", ids.RepoName).
				Msg("Repository lookup failed with transient error, will try organization lookup")
		}
	}

	// Method 5: Organization API lookup
	if ids.OwnerLogin != "" {
		logger.Debug().
			Str("org", ids.OwnerLogin).
			Msg("Attempting organization-based installation lookup (API call)")

		installationID, err := h.lookupInstallationByOrganization(ctx, ids.OwnerLogin)
		if err == nil {
			// SUCCESS! Mark as positive
			h.registry.MarkInstalled(installationID) // ✅ POSITIVE CACHE
			h.orgCache.Set(buildOrgCacheKey(ids.OwnerLogin), installationID)

			logger.Info().
				Int64("installation_id", installationID).
				Str("method", "organization_api_lookup").
				Msg("Found installation via organization lookup")
			if isSQSEvent {
				h.recordLookupMethod(MetricsKeyLookupMethodOrgAPI)
			}
			return installationID, nil
		}

		// Organization lookup also failed
		if !IsInstallationNotFoundError(err) {
			// Network error or other transient failure - don't cache negative
			logger.Warn().
				Err(err).
				Str("org", ids.OwnerLogin).
				Msg("Organization lookup failed with transient error")
			return 0, ErrNoInstallation
		}
	}

	// ALL METHODS FAILED - Installation genuinely doesn't exist
	// Cache negative results in mapping caches
	if ids.OwnerLogin != "" && ids.RepoName != "" {
		h.repoCache.SetNotFound(buildRepoCacheKey(ids.OwnerLogin, ids.RepoName))
	}
	if ids.OwnerLogin != "" {
		h.orgCache.SetNotFound(buildOrgCacheKey(ids.OwnerLogin))
	}

	logger.Warn().
		Str("owner", ids.OwnerLogin).
		Str("repo", ids.RepoName).
		Str("account", ids.AccountLogin).
		Msg("All installation lookup methods failed - app not installed")

	// Record metric for complete lookup failure (SQS events only)
	if isSQSEvent {
		h.recordLookupMethod(MetricsKeyLookupAllFailed)
	}

	return 0, ErrInstallationNotInstalled
}

// extractInstallationIDWithFallback attempts multiple strategies to extract installation ID.
// For SQS events: Uses enhanced smart lookup with mapping caches and multiple API methods
// For webhook events: Uses simple direct + repository fallback (existing behavior)
//
// Layer 1: Direct extraction from event.installation.id (fast path)
// Layer 2: Repository-based lookup via GitHub API (fallback for events without installation ID)
// Layer 3: Returns error to pass through to handler (final fallback)
func (h *InstallationFilterHandler) extractInstallationIDWithFallback(
	ctx context.Context,
	payload []byte,
) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// Check if this is an SQS event - use enhanced lookup for SQS only
	if eventSource, ok := ctx.Value(SQSEventSourceKey).(string); ok && eventSource == "sqs" {
		logger.Debug().Msg("SQS event detected - using enhanced multi-method lookup")

		// Extract all possible identifiers for comprehensive lookup
		ids, err := extractIdentifiers(payload)
		if err != nil {
			logger.Debug().
				Err(err).
				Msg("Failed to extract identifiers from SQS event")
			return 0, ErrNoInstallation
		}
		// Return identifiers to pool when done (reduces allocation pressure)
		defer putIdentifiers(ids)

		// Use smart lookup with caching for SQS events
		return h.lookupInstallationWithSmartCache(ctx, ids)
	}

	// Webhook event - use existing simple fallback (Phase 1 behavior)
	logger.Debug().Msg("Webhook event detected - using simple extraction")

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

			// Return the original error so smart lookup can detect it's a 404
			return 0, err
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

// lookupInstallationByOrganization queries GitHub API for installation by organization.
// Results are cached to avoid repeated API calls.
// Returns (installationID, nil) if found, or (0, error) otherwise.
func (h *InstallationFilterHandler) lookupInstallationByOrganization(
	ctx context.Context,
	org string,
) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// Check if we have an InstallationsService (required for lookup)
	if h.installationsService == nil {
		logger.Debug().
			Str("org", org).
			Msg("No installations service available for organization lookup")
		return 0, errors.New("installations service not available")
	}

	// Look up installation via API
	logger.Debug().
		Str("org", org).
		Msg("Looking up installation by organization via GitHub API")

	installation, err := h.installationsService.GetByOwner(ctx, org)
	if err != nil {
		// Check if it's a "not found" error (app not installed for this organization)
		if IsInstallationNotFoundError(err) {
			logger.Info().
				Str("org", org).
				Msg("No installation found for organization (app not installed)")
			// Return the original error so smart lookup can detect it's a 404
			return 0, err
		}

		// Other API errors (network, auth, etc.)
		logger.Warn().
			Err(err).
			Str("org", org).
			Msg("Failed to lookup installation by organization")
		return 0, errors.Wrap(err, "failed to lookup installation by organization")
	}

	// Successfully found installation - cache the positive result
	installationID := installation.ID
	logger.Info().
		Int64("installation_id", installationID).
		Str("org", org).
		Msg("Found installation via organization lookup, caching result")

	h.registry.MarkInstalled(installationID)
	return installationID, nil
}

// ============================================================================
// Phase 3: Cache Lifecycle Management
// ============================================================================
//
// NOTE: Cache lifecycle management methods (PopulateInstallationCaches,
// InvalidateInstallationCaches, etc.) have been moved to the Base struct
// since the caches are now shared from Base. These methods are called by
// the Installation handler to manage cache lifecycle events.
// See base.go for the implementation.
