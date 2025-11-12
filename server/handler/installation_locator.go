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
	"bytes"
	"context"
	"sync"
	"sync/atomic"

	"github.com/google/go-github/v47/github"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// LookupStrategy determines which lookup approach to use
type LookupStrategy int

const (
	// StrategyWebhook uses direct ID lookup only (fail fast for webhooks)
	StrategyWebhook LookupStrategy = iota

	// StrategySQS uses full smart lookup (ID → owner:repo → API)
	StrategySQS
)

// InstallationLocator provides efficient installation lookup with
// optimized SQS event processing and minimal allocations
type InstallationLocator struct {
	registry      *InstallationRegistry
	logger        zerolog.Logger
	clientFactory func(ctx context.Context) (*github.Client, error)
	circuitBreaker *CircuitBreaker

	// Concurrency control using channel-based semaphore
	apiSemaphore chan struct{}

	// Deduplication using mutex and map
	lookupMu       sync.Mutex
	lookupInFlight map[string]chan lookupResult

	// String building optimization
	keyBuilderPool sync.Pool

	// Metrics
	metrics *LocatorMetrics
}

type lookupResult struct {
	installationID int64
	err            error
}

// LocatorMetrics tracks lookup performance with atomic operations
type LocatorMetrics struct {
	DirectHits    int64 `json:"direct_hits"`
	CompoundHits  int64 `json:"compound_hits"`
	APICalls      int64 `json:"api_calls"`
	CircuitOpens  int64 `json:"circuit_opens"`
	Cancellations int64 `json:"cancellations"`
}

// LookupRequest contains parameters for installation lookup
type LookupRequest struct {
	// Primary lookup key - installation ID from event
	InstallationID int64

	// Fallback lookup keys
	Owner string
	Repo  string

	// Strategy to use for this lookup
	Strategy LookupStrategy

	// Event type (for logging and metrics)
	EventType string
}

// LookupResult contains the result of an installation lookup
type LookupResult struct {
	// Installation ID (may differ from request if looked up via repo)
	InstallationID int64

	// Whether the installation exists and is valid
	Exists bool

	// How the installation was found
	Source LookupSource

	// Any error that occurred during lookup
	Error error
}

// LookupSource indicates how an installation was found
type LookupSource int

const (
	// SourceCacheID found via installation ID in cache
	SourceCacheID LookupSource = iota

	// SourceCacheRepo found via owner:repo in cache
	SourceCacheRepo

	// SourceAPI found via GitHub API call
	SourceAPI

	// SourceNotFound not found anywhere
	SourceNotFound
)

// NewInstallationLocator creates an installation locator with optimized settings
func NewInstallationLocator(
	registry *InstallationRegistry,
	logger zerolog.Logger,
	clientFactory func(ctx context.Context) (*github.Client, error),
) *InstallationLocator {
	// Create channel-based semaphore for API concurrency control (max 10 concurrent)
	apiSem := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		apiSem <- struct{}{}
	}

	return &InstallationLocator{
		registry:       registry,
		logger:         logger.With().Str("component", "installation_locator").Logger(),
		clientFactory:  clientFactory,
		circuitBreaker: NewCircuitBreaker(),
		apiSemaphore:   apiSem,
		lookupInFlight: make(map[string]chan lookupResult),
		keyBuilderPool: sync.Pool{
			New: func() interface{} {
				return &bytes.Buffer{}
			},
		},
		metrics: &LocatorMetrics{},
	}
}

// Lookup attempts to find an installation using the appropriate strategy
func (l *InstallationLocator) Lookup(ctx context.Context, req LookupRequest) LookupResult {
	// Early return if context is already cancelled
	select {
	case <-ctx.Done():
		atomic.AddInt64(&l.metrics.Cancellations, 1)
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceNotFound,
			Error:          ctx.Err(),
		}
	default:
	}

	logger := l.logger.With().
		Int64("installation_id", req.InstallationID).
		Str("owner", req.Owner).
		Str("repo", req.Repo).
		Str("event_type", req.EventType).
		Logger()

	// Apply strategy-specific behavior
	switch req.Strategy {
	case StrategyWebhook:
		return l.lookupWebhook(ctx, req, logger)
	case StrategySQS:
		return l.lookupSQS(ctx, req, logger)
	default:
		logger.Error().Msg("Unknown lookup strategy")
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceNotFound,
			Error:          errors.New("unknown lookup strategy"),
		}
	}
}

// lookupWebhook implements webhook-specific lookup (ID only, fail fast)
func (l *InstallationLocator) lookupWebhook(
	ctx context.Context,
	req LookupRequest,
	logger zerolog.Logger,
) LookupResult {
	// Webhook strategy: Only use direct installation ID
	if req.InstallationID > 0 {
		status, cached := l.registry.Check(req.InstallationID)
		if cached && status == InstallationExists {
			atomic.AddInt64(&l.metrics.DirectHits, 1)
			logger.Debug().Msg("Installation found via direct ID")
			return LookupResult{
				InstallationID: req.InstallationID,
				Exists:         true,
				Source:         SourceCacheID,
			}
		}

		// Not in cache, but we have an ID - return it (will be validated by client creation)
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         true,
			Source:         SourceCacheID,
		}
	}

	// No installation ID - pass through (webhook behavior)
	return LookupResult{
		InstallationID: 0,
		Exists:         false,
		Source:         SourceNotFound,
		Error:          ErrNoInstallation,
	}
}

// lookupSQS implements SQS-specific smart lookup with all fallbacks
func (l *InstallationLocator) lookupSQS(
	ctx context.Context,
	req LookupRequest,
	logger zerolog.Logger,
) LookupResult {
	// Check if event should bypass cache
	classifier := &EventClassifier{
		classifications: map[string]EventClassification{
			"check_run":    EventNoCache,
			"check_suite":  EventNoCache,
			"status":       EventNoCache,
			"workflow_run": EventNoCache,
			"workflow_job": EventNoCache,
		},
	}

	if class, ok := classifier.classifications[req.EventType]; ok && class == EventNoCache {
		logger.Debug().Msg("Event bypasses cache - no lookup needed")
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         true,
			Source:         SourceCacheID,
		}
	}

	// Method 1: Direct installation ID
	if req.InstallationID > 0 {
		if status, hit := l.registry.Check(req.InstallationID); hit {
			if status == InstallationExists {
				atomic.AddInt64(&l.metrics.DirectHits, 1)
				logger.Debug().Msg("Installation found via direct ID")
				return LookupResult{
					InstallationID: req.InstallationID,
					Exists:         true,
					Source:         SourceCacheID,
				}
			} else if status == InstallationNotFound {
				atomic.AddInt64(&l.metrics.DirectHits, 1)
				logger.Debug().Msg("Installation confirmed not installed via direct ID")
				return LookupResult{
					InstallationID: req.InstallationID,
					Exists:         false,
					Source:         SourceCacheID,
				}
			}
		}
	}

	// Method 2: Compound key lookup (owner:repo)
	if req.Owner != "" && req.Repo != "" {
		installID, status, hit := l.registry.CheckByRepo(req.Owner, req.Repo)
		if hit {
			if status == InstallationExists {
				atomic.AddInt64(&l.metrics.CompoundHits, 1)
				logger.Debug().
					Int64("found_id", installID).
					Msg("Installation found via compound key")
				return LookupResult{
					InstallationID: installID,
					Exists:         true,
					Source:         SourceCacheRepo,
				}
			} else if status == InstallationNotFound {
				atomic.AddInt64(&l.metrics.CompoundHits, 1)
				logger.Debug().
					Str("owner", req.Owner).
					Str("repo", req.Repo).
					Msg("Installation confirmed not installed via compound key")
				return LookupResult{
					InstallationID: installID,
					Exists:         false,
					Source:         SourceCacheRepo,
				}
			}
		}
	}

	// Method 3: API lookup with circuit breaker and semaphore
	if !l.circuitBreaker.Allow() {
		atomic.AddInt64(&l.metrics.CircuitOpens, 1)
		logger.Warn().Msg("Circuit breaker open, skipping API lookup")
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceNotFound,
			Error:          errors.New("circuit breaker open"),
		}
	}

	// Use channel-based semaphore to limit concurrent API calls
	select {
	case <-l.apiSemaphore:
		defer func() { l.apiSemaphore <- struct{}{} }()
	case <-ctx.Done():
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceNotFound,
			Error:          ctx.Err(),
		}
	}

	result := l.apiLookupWithDedup(ctx, req, logger)

	// Update circuit breaker
	if result.Error != nil {
		l.circuitBreaker.RecordFailure()
	} else {
		l.circuitBreaker.RecordSuccess()
	}

	return result
}

// buildCompoundKey uses a pooled buffer for efficient string building
func (l *InstallationLocator) buildCompoundKey(owner, repo string) string {
	buf := l.keyBuilderPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		l.keyBuilderPool.Put(buf)
	}()

	buf.Grow(len(owner) + 1 + len(repo))
	buf.WriteString(owner)
	buf.WriteByte(':')
	buf.WriteString(repo)

	return buf.String()
}

// apiLookupWithDedup deduplicates concurrent API calls for the same repo
func (l *InstallationLocator) apiLookupWithDedup(
	ctx context.Context,
	req LookupRequest,
	logger zerolog.Logger,
) LookupResult {
	key := l.buildCompoundKey(req.Owner, req.Repo)

	// Check if there's already a lookup in flight
	l.lookupMu.Lock()
	if ch, exists := l.lookupInFlight[key]; exists {
		l.lookupMu.Unlock()
		logger.Debug().Msg("Waiting for in-flight API call")

		select {
		case result := <-ch:
			if result.err != nil {
				return l.handleAPIError(result.err, req, logger)
			}
			return l.handleAPISuccess(result.installationID, req, logger)
		case <-ctx.Done():
			return LookupResult{
				InstallationID: req.InstallationID,
				Exists:         false,
				Source:         SourceNotFound,
				Error:          ctx.Err(),
			}
		}
	}

	// Create channel for this lookup
	ch := make(chan lookupResult, 1)
	l.lookupInFlight[key] = ch
	l.lookupMu.Unlock()

	// Perform the actual API lookup in a goroutine
	go func() {
		defer func() {
			l.lookupMu.Lock()
			delete(l.lookupInFlight, key)
			l.lookupMu.Unlock()
			close(ch)
		}()

		atomic.AddInt64(&l.metrics.APICalls, 1)
		l.registry.RecordAPICall()

		client, err := l.clientFactory(ctx)
		if err != nil {
			ch <- lookupResult{err: errors.Wrap(err, "failed to create client")}
			return
		}

		installation, _, err := client.Apps.FindRepositoryInstallation(ctx, req.Owner, req.Repo)
		if err != nil {
			ch <- lookupResult{err: err}
			return
		}

		ch <- lookupResult{installationID: installation.GetID()}
	}()

	// Wait for lookup to complete
	select {
	case result := <-ch:
		if result.err != nil {
			return l.handleAPIError(result.err, req, logger)
		}
		return l.handleAPISuccess(result.installationID, req, logger)
	case <-ctx.Done():
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceNotFound,
			Error:          ctx.Err(),
		}
	}
}

// handleAPIError processes API lookup errors
func (l *InstallationLocator) handleAPIError(
	err error,
	req LookupRequest,
	logger zerolog.Logger,
) LookupResult {
	if isNotFoundError(err) {
		if req.InstallationID > 0 {
			l.registry.MarkNotInstalled(req.InstallationID)
		}
		logger.Debug().Msg("Installation not found via API")
		return LookupResult{
			InstallationID: req.InstallationID,
			Exists:         false,
			Source:         SourceAPI,
		}
	}

	logger.Error().Err(err).Msg("API lookup failed")
	return LookupResult{
		InstallationID: req.InstallationID,
		Exists:         false,
		Source:         SourceNotFound,
		Error:          err,
	}
}

// handleAPISuccess processes successful API lookups
func (l *InstallationLocator) handleAPISuccess(
	installationID int64,
	req LookupRequest,
	logger zerolog.Logger,
) LookupResult {
	l.registry.MarkInstalled(installationID)
	if req.Owner != "" && req.Repo != "" {
		l.registry.AddRepositories(installationID, []struct{ Owner, Repo string }{
			{Owner: req.Owner, Repo: req.Repo},
		})
	}

	logger.Info().
		Int64("installation_id", installationID).
		Msg("Installation found via API and cached")

	return LookupResult{
		InstallationID: installationID,
		Exists:         true,
		Source:         SourceAPI,
	}
}

// isNotFoundError checks if an error represents a 404 Not Found response
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	if ghErr, ok := err.(*github.ErrorResponse); ok {
		return ghErr.Response != nil && ghErr.Response.StatusCode == 404
	}

	cause := errors.Cause(err)
	if cause != err {
		return isNotFoundError(cause)
	}

	return false
}

// UpdateFromEvent updates the registry based on GitHub webhook events
func (l *InstallationLocator) UpdateFromEvent(ctx context.Context, eventType string, payload interface{}) {
	logger := l.logger.With().Str("event_type", eventType).Logger()

	switch eventType {
	case "installation":
		l.handleInstallationEvent(payload, logger)
	case "installation_repositories":
		l.handleInstallationRepositoriesEvent(payload, logger)
	}
}

// handleInstallationEvent processes installation created/deleted events
func (l *InstallationLocator) handleInstallationEvent(payload interface{}, logger zerolog.Logger) {
	event, ok := payload.(*github.InstallationEvent)
	if !ok {
		logger.Error().Msg("Invalid installation event payload")
		return
	}

	installation := event.GetInstallation()
	installID := installation.GetID()
	action := event.GetAction()

	logger = logger.With().
		Int64("installation_id", installID).
		Str("action", action).
		Logger()

	switch action {
	case "created":
		l.registry.MarkInstalled(installID)
		logger.Info().Msg("Installation created and cached")

	case "deleted":
		l.registry.Remove(installID)
		logger.Info().Msg("Installation deleted from cache")
	}
}

// handleInstallationRepositoriesEvent processes repository added/removed events
func (l *InstallationLocator) handleInstallationRepositoriesEvent(payload interface{}, logger zerolog.Logger) {
	event, ok := payload.(*github.InstallationRepositoriesEvent)
	if !ok {
		logger.Error().Msg("Invalid installation_repositories event payload")
		return
	}

	installation := event.GetInstallation()
	installID := installation.GetID()
	action := event.GetAction()

	logger = logger.With().
		Int64("installation_id", installID).
		Str("action", action).
		Logger()

	switch action {
	case "added":
		var repoList []struct{ Owner, Repo string }
		for _, repo := range event.RepositoriesAdded {
			owner := repo.GetOwner().GetLogin()
			name := repo.GetName()
			if owner != "" && name != "" {
				repoList = append(repoList, struct{ Owner, Repo string }{
					Owner: owner,
					Repo:  name,
				})
			}
		}
		if len(repoList) > 0 {
			l.registry.AddRepositories(installID, repoList)
			logger.Info().
				Int("repos_added", len(repoList)).
				Msg("Repositories added to installation")
		}

	case "removed":
		var repoList []struct{ Owner, Repo string }
		for _, repo := range event.RepositoriesRemoved {
			owner := repo.GetOwner().GetLogin()
			name := repo.GetName()
			if owner != "" && name != "" {
				repoList = append(repoList, struct{ Owner, Repo string }{
					Owner: owner,
					Repo:  name,
				})
			}
		}
		if len(repoList) > 0 {
			l.registry.RemoveRepositories(installID, repoList)
			logger.Info().
				Int("repos_removed", len(repoList)).
				Msg("Repositories removed from installation")
		}
	}
}

// GetMetrics returns current metrics (thread-safe via atomics)
func (l *InstallationLocator) GetMetrics() LocatorMetrics {
	return LocatorMetrics{
		DirectHits:    atomic.LoadInt64(&l.metrics.DirectHits),
		CompoundHits:  atomic.LoadInt64(&l.metrics.CompoundHits),
		APICalls:      atomic.LoadInt64(&l.metrics.APICalls),
		CircuitOpens:  atomic.LoadInt64(&l.metrics.CircuitOpens),
		Cancellations: atomic.LoadInt64(&l.metrics.Cancellations),
	}
}