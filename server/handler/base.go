// Copyright 2018 Palantir Technologies, Inc.
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
	"reflect"
	"sync"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/pull"
	"github.com/pkg/errors"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

const (
	LogKeyGitHubSHA = "github_sha"

	// Metric keys for installation client creation
	MetricsKeyInstallationClientSuccess   = "installation.client.success"
	MetricsKeyInstallationClientFailure   = "installation.client.failure"
	MetricsKeyInstallationV4ClientSuccess = "installation.v4client.success"
	MetricsKeyInstallationV4ClientFailure = "installation.v4client.failure"

	// Metrics for reactive auth refresh
	MetricsKeyAuthRefreshAttempt  = "installation.auth_refresh.attempt"
	MetricsKeyAuthRefreshSuccess  = "installation.auth_refresh.success"
	MetricsKeyAuthRefreshFailure  = "installation.auth_refresh.failure"
	MetricsKeyAuthRefreshCacheHit = "installation.auth_refresh.cache_evicted"
)

// OrgClientCreator defines the interface for creating per-org rate-limited clients.
// This allows for proper mocking in tests.
type OrgClientCreator interface {
	NewOrgClient(ctx context.Context, owner string, installationID int64) (*github.Client, error)
	NewOrgV4Client(ctx context.Context, owner string, installationID int64) (*githubv4.Client, error)
}

type Base struct {
	// GitHub App Client Creation
	githubapp.ClientCreator
	Installations githubapp.InstallationsService

	// Configuration
	ConfigFetcher               *ConfigFetcher
	AutorRemediateConfigFetcher *ConfigFetcher
	BaseConfig                  *baseapp.HTTPConfig
	PullOpts                    *PullEvaluationOptions

	// Caching (environment-specific)
	// GHEC: Uses ClientCache (owner ID → clients with installation ID)
	// GHES: Uses InstallationManager (installation-based with circuit breaker)
	InstallationManager *InstallationManager // GHES: Centralized manager for installation client creation
	ClientCache         *ClientCache         // GHEC: Unified cache keyed by owner ID

	// Legacy Support
	InstallationIdMap map[int64]int64 // Legacy cache for backwards compatibility

	// Observability
	MetricsRegistry gometrics.Registry // Registry for recording metrics
	Logger          zerolog.Logger

	// App Identity
	AppID       int64  // GitHub App ID for multi-app event detection
	AppName     string // GitHub App name
	GithubCloud bool   // True for GHEC, false for GHES
	// Feature flags / behavior
	AuthRefreshEnabled *bool // default true; allows disabling reactive auth refresh if needed

	// Internal State
	DefaultFetchedConfig *FetchedConfig
	mu                   *sync.RWMutex
	clientSingleflight   singleflight.Group
}

func (base *Base) Initialize() {

	if reflect.ValueOf(base.Logger).IsZero() {
		base.Logger = zerolog.Nop()
	}

	if base.InstallationIdMap == nil {
		base.InstallationIdMap = make(map[int64]int64)
	}

	if base.mu == nil {
		base.mu = &sync.RWMutex{}
	}

	if base.DefaultFetchedConfig == nil {
		base.DefaultFetchedConfig = &FetchedConfig{}
	}

	// Default reactive auth refresh to true if not set
	if base.AuthRefreshEnabled == nil {
		def := true
		base.AuthRefreshEnabled = &def
	}

	// Initialize installation manager for GHES (has retry and circuit breaker logic)
	// CircuitBreaker is now created internally by InstallationManager (better encapsulation)
	// For GHEC, this is not used (we use GetClientsByOwner instead)
	if base.InstallationManager == nil {
		base.InstallationManager = NewInstallationManager(
			base.ClientCreator,
			nil, // No registry needed - simplified
			base.MetricsRegistry,
		)
	}

	// Initialize client cache for per-org client caching (unified cache for GHEC)
	// Keyed by owner ID (int64) for efficiency and immutability
	// Stores both clients and installation ID - no separate OrgMappingCache needed
	// TTL: 10 minutes (clients use 1-hour tokens, refresh earlier for safety)
	// Negative TTL: 2 minutes (shorter for non-existent installations)
	// MaxSize: 1000 orgs (reasonable for most deployments)
	// Integrated with MetricsRegistry for OTEL export to New Relic
	if base.ClientCache == nil {
		base.ClientCache = NewClientCacheWithOptions(
			10*time.Minute, // positiveTTL
			2*time.Minute,  // negativeTTL
			1000,           // maxSize
			base.MetricsRegistry,
		)
	}

}

// recordInstallationClientMetric records a metric for installation client creation
// using the go-metrics registry. This allows the metrics to be exported via OpenTelemetry.
func (b *Base) recordInstallationClientMetric(metricKey string) {
	if b.MetricsRegistry != nil {
		if counter := b.MetricsRegistry.Get(metricKey); counter != nil {
			if c, ok := counter.(interface{ Inc(int64) }); ok {
				c.Inc(1)
			}
		} else {
			// Register counter if it doesn't exist
			gometrics.GetOrRegisterCounter(metricKey, b.MetricsRegistry).Inc(1)
		}
	}
}

// recordAuthRefreshMetric increments auth-refresh-related counters using go-metrics.
func (b *Base) recordAuthRefreshMetric(metricKey string) {
	if b.MetricsRegistry == nil {
		return
	}
	if counter := b.MetricsRegistry.Get(metricKey); counter != nil {
		if c, ok := counter.(interface{ Inc(int64) }); ok {
			c.Inc(1)
			return
		}
	}
	gometrics.GetOrRegisterCounter(metricKey, b.MetricsRegistry).Inc(1)
}

// VerifyInstallation checks if the GitHub App is installed for the given installation ID.
// It returns true if the installation exists and is accessible, false otherwise.
// This method helps prevent 404 errors by verifying installation status before attempting
// to create installation clients for repositories where the app may not be installed.
//
// SIMPLIFIED: Directly calls GitHub API to verify installation (no caching overhead).
// For GHEC, this is rarely called since we use owner-based lookup.
// For GHES, the occasional verification call is acceptable.
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
	logger := zerolog.Ctx(ctx)

	// Create app client for API verification
	appClient, err := b.NewAppClient()
	if err != nil {
		logger.Warn().Err(err).
			Int64("installation_id", installationID).
			Msg("Failed to create app client for installation verification")
		return false
	}

	// Call GitHub API directly to verify installation exists
	_, _, err = appClient.Apps.GetInstallation(ctx, installationID)
	exists := err == nil

	if err != nil {
		logger.Debug().Err(err).
			Int64("installation_id", installationID).
			Msg("Installation verification failed or not found")
	}

	// Update legacy cache for backwards compatibility if installation exists
	if exists {
		b.mu.Lock()
		b.InstallationIdMap[installationID] = installationID
		b.mu.Unlock()
	}

	return exists
}

// IsOurApp checks if the given source app ID matches our app ID.
// This is used to distinguish events from our GitHub App vs external apps (Dependabot, Renovate, etc.).
//
// Special cases:
//   - If sourceAppID is 0 (missing from payload), assumes it's ours for backward compatibility
//   - If our AppID is 0 (not initialized), assumes all events are ours
//
// Performance: Simple int64 comparison, < 1ns, no allocations
func (b *Base) IsOurApp(sourceAppID int64) bool {
	// Backward compatibility: If sourceAppID is 0 (not in payload), assume it's ours
	if sourceAppID == 0 {
		return true
	}

	// If our AppID is not set, assume all events are ours (backward compatibility)
	if b.AppID == 0 {
		return true
	}

	return sourceAppID == b.AppID
}

// PostStatus posts a GitHub commit status with consistent logging.
func PostStatus(ctx context.Context, client *github.Client, owner, repo, ref string, status *github.RepoStatus) error {
	zerolog.Ctx(ctx).Info().Msgf("Setting %q status on %s to %s: %s", status.GetContext(), ref, status.GetState(), status.GetDescription())
	_, _, err := client.Repositories.CreateStatus(ctx, owner, repo, ref, status)
	return errors.WithStack(err)
}

func (b *Base) PreparePRContext(ctx context.Context, installationID int64, pr *github.PullRequest) (context.Context, zerolog.Logger) {
	ctx, logger := githubapp.PreparePRContext(ctx, installationID, pr.GetBase().GetRepo(), pr.GetNumber())

	logger = logger.With().Str(LogKeyGitHubSHA, pr.GetHead().GetSHA()).Logger()
	ctx = logger.WithContext(ctx)

	return ctx, logger
}

// NewEvalContext creates an evaluation context for a pull request.
// This is the main entry point for PR evaluation and policy checking.
//
// Flow:
//  1. Get GitHub API clients (v3 + v4) based on environment:
//     - GHEC: GetClientsByOwner() - owner-based lookup with per-org caching
//     - GHES: InstallationManager.GetClients() - installation-based lookup
//  2. Create pull.Context using the clients to fetch PR data (reviews, approvals, changes, etc.)
//  3. Fetch repository policy configuration
//  4. Return EvalContext containing clients, pull context, and config
//
// The returned EvalContext is then used to:
//   - Parse and validate policy configuration
//   - Evaluate policy rules against the PR
//   - Post commit status to GitHub (success/failure/pending)
//   - GitHub's automerge uses this status to auto-merge PRs that meet policy requirements
func (b *Base) NewEvalContext(ctx context.Context, installationID int64, loc pull.Locator) (*EvalContext, error) {
	// Start tracing span for eval context creation
	tracer := otel.Tracer("github.com/palantir/policy-bot/handler")
	ctx, span := tracer.Start(ctx, "Base.NewEvalContext",
		trace.WithAttributes(
			attribute.Int64("installation.id", installationID),
			attribute.String("repository.owner", loc.Owner),
			attribute.String("repository.name", loc.Repo),
			attribute.Int("pull_request.number", loc.Number),
		),
	)
	defer span.End()

	repoFullName := fmt.Sprintf("%s/%s", loc.Owner, loc.Repo)

	var clients *InstallationClients
	var err error

	// SIMPLIFICATION: For GHEC, use owner-based client lookup (per-org caching)
	// For GHES, use installation-based lookup (backward compatibility)
	if b.GithubCloud && loc.Owner != "" {
		// GHEC: Use simplified owner-based lookup with per-org caching
		// This is correct since there's ONE installation per org in GHEC
		span.SetAttributes(
			attribute.String("lookup.type", "owner_based"),
			attribute.String("lookup.owner", loc.Owner),
		)

		clients, err = b.GetClientsByOwner(ctx, loc.Owner)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed to get clients by owner")
			return nil, errors.Wrapf(err, "failed to get clients for owner %s", loc.Owner)
		}
		span.AddEvent("clients_retrieved_by_owner")

	} else {
		// GHES: Use installation-based lookup (requires installation ID)
		span.SetAttributes(
			attribute.String("lookup.type", "installation_based"),
			attribute.Int64("lookup.installation_id", installationID),
		)

		// Verify installation exists before attempting to create clients
		if !b.VerifyInstallation(ctx, installationID) {
			span.SetStatus(codes.Error, "installation not verified")
			span.SetAttributes(attribute.Bool("installation.verified", false))
			return nil, fmt.Errorf("installation %d not found or not accessible - app may not be installed on repository %s", installationID, repoFullName)
		}
		span.SetAttributes(attribute.Bool("installation.verified", true))

		// Use InstallationManager to create both v3 and v4 clients
		clients, err = b.InstallationManager.GetClients(ctx, installationID, repoFullName)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "failed to get installation clients")
			return nil, err
		}
		span.AddEvent("clients_retrieved_by_installation")
	}

	span.AddEvent("clients_created")

	mbrCtx := NewCrossOrgMembershipContext(ctx, clients.V3Client, loc.Owner, b.Installations, b.ClientCreator)
	prctx, err := pull.NewGitHubContext(ctx, mbrCtx, clients.V3Client, clients.V4Client, loc)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to create pull context")
		return nil, err
	}
	span.AddEvent("pull_context_created")

	baseBranch, _ := prctx.Branches()
	owner := prctx.RepositoryOwner()
	repository := prctx.RepositoryName()

	fetchedConfig := b.ConfigFetcher.ConfigForRepositoryBranch(ctx, clients.V3Client, owner, repository, baseBranch)
	span.SetAttributes(
		attribute.String("repository.base_branch", baseBranch),
		attribute.Bool("config.fetched", fetchedConfig.Config != nil),
	)

	span.SetStatus(codes.Ok, "eval context created successfully")
	return &EvalContext{
		Client:   clients.V3Client,
		V4Client: clients.V4Client,

		Options:   b.PullOpts,
		PublicURL: b.BaseConfig.PublicURL,

		PullContext: prctx,
		Config:      fetchedConfig,
	}, nil
}

func (b *Base) Evaluate(ctx context.Context, installationID int64, trigger common.Trigger, loc pull.Locator) error {
	evalCtx, err := b.NewEvalContext(ctx, installationID, loc)
	if err != nil {
		return errors.Wrap(err, "failed to create evaluation context")
	}
	return evalCtx.Evaluate(ctx, trigger)
}

type installationLookupResult struct {
	clients        *InstallationClients
	installationID int64
}

func (b *Base) installationSingleflightKey(ownerID int64, ownerName, repo string, installationID int64) string {
	switch {
	case ownerID > 0:
		return fmt.Sprintf("owner-id:%d", ownerID)
	case installationID > 0:
		return fmt.Sprintf("installation:%d", installationID)
	default:
		return fmt.Sprintf("owner:%s|repo:%s", ownerName, repo)
	}
}

// resolveInstallationID resolves the installation ID using owner or repository lookup with caching semantics.
// It preserves the previous behavior:
//   - GHEC: owner-based lookup, negative-caches misses when ownerID is provided
//   - GHES: repository-based lookup (requires repo)
//   - Fallback to repo lookup on lookup errors when repo is provided
func (b *Base) resolveInstallationID(ctx context.Context, installationID int64, ownerID int64, ownerName string, repo string) (int64, error) {
	logger := zerolog.Ctx(ctx)

	// If caller already supplied a valid installation ID, use it.
	if installationID > 0 {
		return installationID, nil
	}

	var finalInstallationID int64
	var lookupErr error

	// Primary lookup
	if !b.GithubCloud {
		if repo == "" {
			return 0, fmt.Errorf("repository name required for GHES installation lookup")
		}
		repoFullName := fmt.Sprintf("%s/%s", ownerName, repo)
		var installation githubapp.Installation
		installation, lookupErr = b.Installations.GetByRepository(ctx, ownerName, repo)
		if lookupErr != nil {
			logger.Warn().Err(lookupErr).
				Str("owner", ownerName).
				Str("repo", repo).
				Str("repo_full_name", repoFullName).
				Msg("Failed to find installation for repository (GHES)")
		} else {
			finalInstallationID = installation.ID
			logger.Info().
				Str("owner", ownerName).
				Str("repo", repo).
				Int64("installation_id", finalInstallationID).
				Msg("Found installation via repository lookup (GHES)")
		}
	} else {
		var installation githubapp.Installation
		installation, lookupErr = b.Installations.GetByOwner(ctx, ownerName)
		if lookupErr != nil {
			logger.Warn().Err(lookupErr).
				Str("owner", ownerName).
				Msg("Failed to find installation for owner (GHEC)")
		} else {
			finalInstallationID = installation.ID
			logger.Info().
				Str("owner", ownerName).
				Int64("owner_id", ownerID).
				Int64("installation_id", finalInstallationID).
				Str("lookup_method", "GetByOwner").
				Msg("Found installation via owner lookup (GHEC)")
		}
	}

	// Fallback lookup if needed
	if lookupErr != nil && repo != "" {
		logger.Debug().
			Str("owner", ownerName).
			Str("repo", repo).
			Msg("Attempting fallback: repository-based installation lookup")

		installation, err := b.Installations.GetByRepository(ctx, ownerName, repo)
		if err != nil {
			logger.Error().Err(err).
				Str("owner", ownerName).
				Str("repo", repo).
				Msg("Fallback lookup also failed - installation not found")

			if b.ClientCache != nil && ownerID > 0 {
				b.ClientCache.PutNegative(ownerID)
			}

			return 0, errors.Wrapf(err, "failed to find installation for owner %s (tried both owner and repo lookup)", ownerName)
		}
		finalInstallationID = installation.ID
		logger.Info().
			Str("owner", ownerName).
			Str("repo", repo).
			Int64("installation_id", finalInstallationID).
			Msg("Found installation via fallback repository lookup")
	} else if lookupErr != nil {
		if b.ClientCache != nil && ownerID > 0 {
			b.ClientCache.PutNegative(ownerID)
		}
		return 0, errors.Wrap(lookupErr, "failed to find installation (no repo provided for fallback lookup)")
	}

	if finalInstallationID == 0 {
		return 0, fmt.Errorf("failed to obtain installation ID for owner %s", ownerName)
	}

	return finalInstallationID, nil
}

// InvalidateInstallationCaches removes all cache entries associated with an installation.
// This should be called when the GitHub App is uninstalled.
// ownerID is optional but recommended - if provided, enables cache invalidation by owner ID.
func (b *Base) InvalidateInstallationCaches(installationID int64, owner string, repos []string, ownerID ...int64) {
	logger := zerolog.Logger{}

	// Invalidate cached clients for this owner by owner ID
	// This prevents stale cached clients from being used after installation deletion/suspension
	if b.ClientCache != nil && len(ownerID) > 0 && ownerID[0] > 0 {
		b.ClientCache.Invalidate(ownerID[0])
		logger.Debug().
			Str("owner", owner).
			Int64("owner_id", ownerID[0]).
			Int64("installation_id", installationID).
			Msg("Invalidated client cache for owner (by ID)")
	}

	logger.Info().
		Int64("installation_id", installationID).
		Str("owner", owner).
		Int("repos_count", len(repos)).
		Msg("Invalidated installation caches")
}

// PopulateInstallationCaches adds cache entries when an installation is created or repositories are added.
// Note: This is now a no-op since ClientCache is populated on-demand during GetClientsByOwner.
// The cache is unified - installation ID is stored alongside clients when first accessed.
func (b *Base) PopulateInstallationCaches(installationID int64, owner string, repos []string) {
	logger := zerolog.Logger{}

	// No-op: ClientCache is populated on-demand in GetClientsByOwner
	// The mapping (ownerID → clients + installationID) is created when first accessed

	logger.Info().
		Int64("installation_id", installationID).
		Str("owner", owner).
		Int("repos_count", len(repos)).
		Msg("Installation cache population delegated to on-demand lookup")
}

// RemoveRepositoriesFromCache removes specific repositories from the cache.
// SIMPLIFIED: No-op after repo cache removal. Kept for backward compatibility.
func (b *Base) RemoveRepositoriesFromCache(owner string, repos []string) {
	// No-op: Repository mapping cache removed during simplification
}

// AddRepositoriesToCache adds specific repositories to the cache.
// SIMPLIFIED: No-op after repo cache removal. Kept for backward compatibility.
func (b *Base) AddRepositoriesToCache(installationID int64, owner string, repos []string) {
	// No-op: Repository mapping cache removed during simplification
}

// GetClientsByOwner retrieves installation clients for a given owner/org with caching.
// This is the simplified, preferred method for GHEC where there's ONE installation per org (max 2).
//
// Token Management Strategy (Reactive Approach):
// This function does NOT proactively validate tokens. Token lifecycle is managed by
// ghinstallation.Transport. If an installation becomes invalid (deleted/suspended),
// the first API call will fail with 401/403/404/410/422, at which point callers should
// use handleAuthFailure() to invalidate cache and recreate clients.
//
// Lookup Strategy (simplified single cache):
//  1. Check ClientCache by owner ID (fast path, ~100ns) - returns clients and installation ID
//  2. If cache miss, call Installations.GetByOwner(owner) to get installation
//  3. Create clients for the installation (ghinstallation.Transport handles token creation)
//  4. Cache clients with installation ID in ClientCache
//  5. Return clients
//
// For GHEC: Uses owner-level installation lookup (ONE installation per org)
// For GHES: Returns error (use repository-based lookup instead)
//
// IMPORTANT: ownerID is now required for efficient caching (int64 keys are faster and immutable).
// If ownerID is not provided (0 or empty), the function will still work but won't use the cache.
//
// Performance: Cache hit returns in ~100ns. Cache miss requires 1 API call.
func (b *Base) GetClientsByOwner(ctx context.Context, owner string, ownerID ...int64) (*InstallationClients, error) {
	logger := zerolog.Ctx(ctx)

	// Validate input
	if owner == "" {
		return nil, fmt.Errorf("owner cannot be empty")
	}

	// Extract owner ID if provided
	var actualOwnerID int64
	if len(ownerID) > 0 && ownerID[0] > 0 {
		actualOwnerID = ownerID[0]
	}

	// Step 1: Check client cache first (fast path for GHEC)
	// Cache is keyed by owner ID (int64) for efficiency and immutability
	if b.ClientCache != nil && actualOwnerID > 0 {
		if clients := b.ClientCache.Get(actualOwnerID); clients != nil {
			logger.Debug().
				Str("owner", owner).
				Int64("owner_id", actualOwnerID).
				Msg("Client cache hit for owner (by ID)")
			return clients, nil
		}

		// Check for negative cache entry (cached "not found")
		if b.ClientCache.IsNegativelyCached(actualOwnerID) {
			logger.Debug().
				Str("owner", owner).
				Int64("owner_id", actualOwnerID).
				Msg("Negative cache hit - installation not found (cached)")
			return nil, fmt.Errorf("installation not found for owner %s (negatively cached)", owner)
		}
	}

	logger.Debug().
		Str("owner", owner).
		Int64("owner_id", actualOwnerID).
		Msg("Client cache miss - looking up installation")

	// Step 2: Look up installation via Installations service
	if !b.GithubCloud {
		// For GHES: Use repository-level lookup
		// Note: This requires a repository name, which we don't have here.
		// For GHES, callers should use the existing InstallationManager.GetClients() with installation ID
		return nil, fmt.Errorf("GetClientsByOwner requires owner-level installation lookup (GHEC only). For GHES, use repository-based lookup")
	}

	lookupKey := b.installationSingleflightKey(actualOwnerID, owner, "", 0)
	result, err, _ := b.clientSingleflight.Do(lookupKey, func() (interface{}, error) {
		// Re-check cache inside singleflight in case another goroutine populated it.
		if b.ClientCache != nil && actualOwnerID > 0 {
			if cachedClients, cachedInstallationID := b.ClientCache.GetWithInstallationID(actualOwnerID); cachedClients != nil {
				logger.Debug().
					Str("owner", owner).
					Int64("owner_id", actualOwnerID).
					Int64("installation_id", cachedInstallationID).
					Msg("Client cache hit during singleflight")
				return &installationLookupResult{
					clients:        cachedClients,
					installationID: cachedInstallationID,
				}, nil
			}
			if b.ClientCache.IsNegativelyCached(actualOwnerID) {
				return nil, fmt.Errorf("installation not found for owner %s (negatively cached)", owner)
			}
		}

		installationID, resolveErr := b.resolveInstallationID(ctx, 0, actualOwnerID, owner, "")
		if resolveErr != nil {
			return nil, resolveErr
		}

		clients, createErr := b.createClientsForOwner(ctx, owner, installationID)
		if createErr != nil {
			logger.Error().Err(createErr).
				Str("owner", owner).
				Int64("installation_id", installationID).
				Msg("Failed to create clients for owner")
			return nil, errors.Wrapf(createErr, "failed to create clients for owner %s (installation %d)", owner, installationID)
		}

		if b.ClientCache != nil && actualOwnerID > 0 {
			b.ClientCache.PutWithInstallationID(actualOwnerID, clients, installationID)
			logger.Debug().
				Str("owner", owner).
				Int64("owner_id", actualOwnerID).
				Int64("installation_id", installationID).
				Msg("Cached clients by owner ID (with installation ID)")
		}

		return &installationLookupResult{
			clients:        clients,
			installationID: installationID,
		}, nil
	})

	if err != nil {
		return nil, err
	}

	res := result.(*installationLookupResult)
	return res.clients, nil
}

// retrieveClientAndInstallationId retrieves both the installation client and installation ID
// using a cache-first approach. This is the primary method for obtaining installation information.
//
// Token Management Strategy (Reactive Approach):
// This function does NOT proactively create or validate tokens. Token management is handled
// by ghinstallation.Transport, which automatically refreshes tokens 1 minute before expiry.
// If tokens become invalid (installation deleted/suspended), auth failures (401/403/404/410/422)
// will be detected during actual API calls, triggering cache invalidation and client recreation.
//
// Flow:
// 1. Check cache by owner ID for cached installation ID and clients
// 2. If cache miss, use API to get installation ID:
//   - GHEC: Use GetByOwner for org-level lookup
//   - GHES: Use GetByRepository for repo-level lookup (requires repo parameter)
//     3. Create installation clients using the installation ID
//     (ghinstallation.Transport creates and caches tokens automatically)
//     4. Cache clients with installation ID for future requests
//     5. On error, fallback to repo-based lookup to obtain installation ID
//
// Note: Auth failures should be handled by callers using handleAuthFailure()
// to trigger cache invalidation and client recreation when needed.
//
// Returns: InstallationClients, installation ID, error
func (b *Base) retrieveClientAndInstallationId(ctx context.Context, installationID int64, ownerID int64, ownerName string, repo string) (*InstallationClients, int64, error) {
	logger := zerolog.Ctx(ctx)

	// Step 1: Check cache by owner ID first (fast path)
	if b.ClientCache != nil && ownerID > 0 {
		if clients, cachedInstallationID := b.ClientCache.GetWithInstallationID(ownerID); clients != nil {
			logger.Debug().
				Str("owner", ownerName).
				Int64("owner_id", ownerID).
				Int64("installation_id", cachedInstallationID).
				Msg("Cache hit - retrieved clients and installation ID from cache")
			return clients, cachedInstallationID, nil
		}

		// Check for negative cache entry
		if b.ClientCache.IsNegativelyCached(ownerID) {
			logger.Debug().
				Str("owner", ownerName).
				Int64("owner_id", ownerID).
				Msg("Negative cache hit - installation not found (cached)")
			return nil, 0, fmt.Errorf("installation not found for owner %s (negatively cached)", ownerName)
		}
	}

	logger.Debug().
		Str("owner", ownerName).
		Int64("owner_id", ownerID).
		Int64("provided_installation_id", installationID).
		Str("repo", repo).
		Msg("Cache miss - looking up installation")

	lookupKey := b.installationSingleflightKey(ownerID, ownerName, repo, installationID)
	result, err, _ := b.clientSingleflight.Do(lookupKey, func() (interface{}, error) {
		// Double-check cache inside singleflight to avoid duplicate work
		if b.ClientCache != nil && ownerID > 0 {
			if cachedClients, cachedInstallationID := b.ClientCache.GetWithInstallationID(ownerID); cachedClients != nil {
				logger.Debug().
					Str("owner", ownerName).
					Int64("owner_id", ownerID).
					Int64("installation_id", cachedInstallationID).
					Msg("Cache hit during singleflight")
				return &installationLookupResult{
					clients:        cachedClients,
					installationID: cachedInstallationID,
				}, nil
			}

			if b.ClientCache.IsNegativelyCached(ownerID) {
				return nil, fmt.Errorf("installation not found for owner %s (negatively cached)", ownerName)
			}
		}

		finalInstallationID, resolveErr := b.resolveInstallationID(ctx, installationID, ownerID, ownerName, repo)
		if resolveErr != nil {
			return nil, resolveErr
		}

		clients, createErr := b.createClientsForOwner(ctx, ownerName, finalInstallationID)
		if createErr != nil {
			logger.Error().Err(createErr).
				Str("owner", ownerName).
				Int64("installation_id", finalInstallationID).
				Msg("Failed to create clients for installation")
			return nil, errors.Wrapf(createErr, "failed to create clients for owner %s (installation %d)", ownerName, finalInstallationID)
		}

		if b.ClientCache != nil && ownerID > 0 {
			b.ClientCache.PutWithInstallationID(ownerID, clients, finalInstallationID)
			logger.Debug().
				Str("owner", ownerName).
				Int64("owner_id", ownerID).
				Int64("installation_id", finalInstallationID).
				Msg("Cached clients and installation ID")
		}

		return &installationLookupResult{
			clients:        clients,
			installationID: finalInstallationID,
		}, nil
	})

	if err != nil {
		return nil, 0, err
	}

	res := result.(*installationLookupResult)
	return res.clients, res.installationID, nil
}

// HandleAuthFailure handles authentication/installation failures by invalidating cache and recreating clients.
// It is reactive: it only runs after an API call fails with auth-ish codes (401/403/404/410/422).
// Rate limit errors (403 via RateLimitError) are passed through without cache mutation.
func (b *Base) HandleAuthFailure(ctx context.Context, owner string, ownerID int64, repo string, installationID int64, authErr error) (*InstallationClients, int64, error) {
	// If feature flag disabled, pass through error
	if b.AuthRefreshEnabled != nil && !*b.AuthRefreshEnabled {
		return nil, 0, authErr
	}

	status, isRateLimit, isAuth := classifyGitHubError(authErr)
	if !isAuth || isRateLimit {
		// Not an auth/installation problem; do not mutate cache.
		return nil, 0, authErr
	}

	b.recordAuthRefreshMetric(MetricsKeyAuthRefreshAttempt)

	// Clear stale cache entry before re-resolving.
	if b.ClientCache != nil && ownerID > 0 {
		b.ClientCache.Invalidate(ownerID)
		b.recordAuthRefreshMetric(MetricsKeyAuthRefreshCacheHit)
	}

	// Permanent removal (404/410) → negative cache and return error
	if status == 404 || status == 410 {
		if b.ClientCache != nil && ownerID > 0 {
			b.ClientCache.PutNegative(ownerID)
		}
		b.recordAuthRefreshMetric(MetricsKeyAuthRefreshFailure)
		return nil, 0, errors.Wrapf(authErr, "installation not found or removed for owner %s", owner)
	}

	// For 401/403/422, attempt to re-resolve installation ID and recreate clients.
	clients, refreshedID, err := b.retrieveClientAndInstallationId(ctx, 0, ownerID, owner, repo)
	if err != nil {
		b.recordAuthRefreshMetric(MetricsKeyAuthRefreshFailure)
		return nil, 0, err
	}
	b.recordAuthRefreshMetric(MetricsKeyAuthRefreshSuccess)
	return clients, refreshedID, nil
}

// GetClientsForEvent retrieves installation clients using cached lookups when possible.
// This method provides a unified interface for handlers to get clients efficiently.
//
// For GHEC: Uses owner-based lookup with ClientCache (unified cache keyed by owner ID)
// For GHES: Uses installation-based lookup with InstallationManager's cache
//
// This ensures all client creation benefits from the caching infrastructure.
// Handlers should use this method instead of calling NewInstallationClient directly.
//
// Optional ownerID parameter: If provided, enables ID-based cache lookups (preferred for immutability).
// This is backward compatible - existing calls without ownerID continue to work.
func (b *Base) GetClientsForEvent(ctx context.Context, owner string, installationID int64, ownerID ...int64) (*InstallationClients, error) {
	// For GHEC, use owner-based lookup (benefits from unified ClientCache)
	if b.GithubCloud && owner != "" {
		return b.GetClientsByOwner(ctx, owner, ownerID...)
	}

	// For GHES, use InstallationManager which has its own caching
	if b.InstallationManager != nil {
		repoFullName := fmt.Sprintf("%s/*", owner) // Placeholder - actual repo may not be known yet
		return b.InstallationManager.GetClients(ctx, installationID, repoFullName)
	}

	// Fallback: Create clients directly (no caching - legacy path)
	return b.createClientsForOwner(ctx, owner, installationID)
}

// createClientsForOwner creates both v3 and v4 installation clients for an owner/org
// with per-org rate limiting (if RateLimitedClientCreator is being used).
func (b *Base) createClientsForOwner(ctx context.Context, owner string, installationID int64) (*InstallationClients, error) {
	logger := zerolog.Ctx(ctx)

	// Check if the ClientCreator supports per-org rate limiting
	// If so, use the new per-org rate limiting methods
	if rlcc, ok := b.ClientCreator.(OrgClientCreator); ok {
		// Use per-org rate limiting (correct for GHEC)
		v3Client, err := rlcc.NewOrgClient(ctx, owner, installationID)
		if err != nil {
			b.recordInstallationClientMetric(MetricsKeyInstallationClientFailure)
			return nil, errors.Wrapf(err, "failed to create v3 client for owner %s", owner)
		}
		b.recordInstallationClientMetric(MetricsKeyInstallationClientSuccess)

		v4Client, err := rlcc.NewOrgV4Client(ctx, owner, installationID)
		if err != nil {
			b.recordInstallationClientMetric(MetricsKeyInstallationV4ClientFailure)
			return nil, errors.Wrapf(err, "failed to create v4 client for owner %s", owner)
		}
		b.recordInstallationClientMetric(MetricsKeyInstallationV4ClientSuccess)

		logger.Debug().
			Str("owner", owner).
			Int64("installation_id", installationID).
			Msg("Created clients with per-org rate limiting")

		return &InstallationClients{
			V3Client: v3Client,
			V4Client: v4Client,
		}, nil
	}

	// Fallback: Use standard client creation (no per-org rate limiting)
	v3Client, err := b.ClientCreator.NewInstallationClient(installationID)
	if err != nil {
		b.recordInstallationClientMetric(MetricsKeyInstallationClientFailure)
		return nil, errors.Wrapf(err, "failed to create v3 client for installation %d", installationID)
	}
	b.recordInstallationClientMetric(MetricsKeyInstallationClientSuccess)

	v4Client, err := b.ClientCreator.NewInstallationV4Client(installationID)
	if err != nil {
		b.recordInstallationClientMetric(MetricsKeyInstallationV4ClientFailure)
		return nil, errors.Wrapf(err, "failed to create v4 client for installation %d", installationID)
	}
	b.recordInstallationClientMetric(MetricsKeyInstallationV4ClientSuccess)

	logger.Debug().
		Int64("installation_id", installationID).
		Msg("Created clients with standard rate limiting")

	return &InstallationClients{
		V3Client: v3Client,
		V4Client: v4Client,
	}, nil
}
