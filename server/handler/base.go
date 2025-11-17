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
)

const (
	LogKeyGitHubSHA = "github_sha"

	// Metric keys for installation client creation
	MetricsKeyInstallationClientSuccess   = "installation.client.success"
	MetricsKeyInstallationClientFailure   = "installation.client.failure"
	MetricsKeyInstallationV4ClientSuccess = "installation.v4client.success"
	MetricsKeyInstallationV4ClientFailure = "installation.v4client.failure"
)

// OrgClientCreator defines the interface for creating per-org rate-limited clients.
// This allows for proper mocking in tests.
type OrgClientCreator interface {
	NewOrgClient(ctx context.Context, owner string, installationID int64) (*github.Client, error)
	NewOrgV4Client(ctx context.Context, owner string, installationID int64) (*githubv4.Client, error)
}

type Base struct {
	githubapp.ClientCreator

	Installations               githubapp.InstallationsService
	ConfigFetcher               *ConfigFetcher
	AutorRemediateConfigFetcher *ConfigFetcher
	BaseConfig                  *baseapp.HTTPConfig
	PullOpts                    *PullEvaluationOptions
	InstallationIdMap           map[int64]int64 // Legacy cache, kept for backwards compatibility
	CircuitBreaker              *CircuitBreaker      // Shared circuit breaker for tracking API failures
	InstallationManager         *InstallationManager // GHES: Centralized manager for installation client creation (has retry/circuit breaker logic)
	OrgMappingCache             *MappingCache        // Organization → Installation ID mapping cache (GHEC)
	ClientCache                 *ClientCache         // Owner/org → InstallationClients cache (per-org caching for GHEC)
	MetricsRegistry             gometrics.Registry   // Registry for recording metrics
	AppID                       int64                // Our GitHub App ID for multi-app event detection
	DefaultInstallationID       int64                // GHEC optimization: single installation ID (set during init)
	GithubCloud                 bool
	mu                          *sync.RWMutex
	Logger                      zerolog.Logger

	AppName              string
	DefaultFetchedConfig *FetchedConfig
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

	// Initialize shared circuit breaker for InstallationManager
	// This ensures consistent failure tracking across GitHub API calls for GHES
	if base.CircuitBreaker == nil {
		base.CircuitBreaker = NewCircuitBreaker()
	}

	// Initialize installation manager for GHES (has retry and circuit breaker logic)
	// For GHEC, this is not used (we use GetClientsByOwner instead)
	if base.InstallationManager == nil {
		base.InstallationManager = NewInstallationManager(
			base.ClientCreator,
			nil, // No registry needed - simplified
			base.MetricsRegistry,
			base.CircuitBreaker,
		)
	}

	// Initialize mapping caches for organization lookups (used by GHEC)
	// Repository mapping cache removed (no longer needed after filter removal)
	// Integrated with MetricsRegistry for OTEL export to New Relic
	if base.OrgMappingCache == nil {
		base.OrgMappingCache = NewMappingCacheWithMetrics(
			1*time.Hour,  // positiveTTL
			5*time.Minute, // negativeTTL
			10000,         // maxSize
			1*time.Minute, // cleanupInterval
			base.MetricsRegistry,
		)
	}

	// Initialize client cache for per-org client caching (correct for GHEC)
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
//  - Parse and validate policy configuration
//  - Evaluate policy rules against the PR
//  - Post commit status to GitHub (success/failure/pending)
//  - GitHub's automerge uses this status to auto-merge PRs that meet policy requirements
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

// InvalidateInstallationCaches removes all cache entries associated with an installation.
// This should be called when the GitHub App is uninstalled.
func (b *Base) InvalidateInstallationCaches(installationID int64, owner string, repos []string) {
	logger := zerolog.Logger{}

	// Remove organization mapping if provided
	if owner != "" && b.OrgMappingCache != nil {
		orgKey := "org:" + owner
		b.OrgMappingCache.Remove(orgKey)
		logger.Debug().
			Str("org_key", orgKey).
			Int64("installation_id", installationID).
			Msg("Removed organization mapping from cache")
	}

	// Invalidate cached clients for this owner
	// This prevents stale cached clients from being used after installation deletion/suspension
	if owner != "" && b.ClientCache != nil {
		b.ClientCache.Invalidate(owner)
		logger.Debug().
			Str("owner", owner).
			Int64("installation_id", installationID).
			Msg("Invalidated client cache for owner")
	}

	logger.Info().
		Int64("installation_id", installationID).
		Str("owner", owner).
		Int("repos_count", len(repos)).
		Msg("Invalidated installation caches")
}

// PopulateInstallationCaches adds cache entries when an installation is created or repositories are added.
func (b *Base) PopulateInstallationCaches(installationID int64, owner string, repos []string) {
	logger := zerolog.Logger{}

	// Add organization mapping if provided
	if owner != "" && b.OrgMappingCache != nil {
		orgKey := "org:" + owner
		b.OrgMappingCache.Set(orgKey, installationID)
		logger.Debug().
			Str("org_key", orgKey).
			Int64("installation_id", installationID).
			Msg("Populated organization mapping in cache")
	}

	logger.Info().
		Int64("installation_id", installationID).
		Str("owner", owner).
		Int("repos_count", len(repos)).
		Msg("Populated installation caches")
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
// Lookup Strategy (flexible fallback):
//  1. Check ClientCache by owner (fast path, ~100ns)
//  2. Check OrgMappingCache for owner→installationID mapping (tries owner ID first if provided, then owner name)
//  3. Try Installations.GetByOwner(owner) - works for org or user
//  4. Cache both the installation ID and the clients for future requests
//
// For GHEC: Uses owner-level installation lookup (ONE installation per org)
// For GHES: Returns error (use repository-based lookup instead)
//
// Optional ownerID parameter: If provided, enables ID-based cache lookups (preferred for immutability).
// This is backward compatible - existing calls without ownerID continue to work.
//
// Performance: Cache hit returns in ~100ns. Cache miss requires 1 API call.
func (b *Base) GetClientsByOwner(ctx context.Context, owner string, ownerID ...int64) (*InstallationClients, error) {
	logger := zerolog.Ctx(ctx)

	// Validate input
	if owner == "" {
		return nil, fmt.Errorf("owner cannot be empty")
	}

	// Step 1: Check client cache first (fast path for GHEC)
	// This includes both positive cache (clients exist) and negative cache (installation not found)
	if b.ClientCache != nil {
		if clients := b.ClientCache.Get(owner); clients != nil {
			logger.Debug().
				Str("owner", owner).
				Msg("Client cache hit for owner")
			return clients, nil
		}

		// Check for negative cache entry (cached "not found")
		if b.ClientCache.IsNegativelyCached(owner) {
			logger.Debug().
				Str("owner", owner).
				Msg("Negative cache hit - installation not found (cached)")
			return nil, fmt.Errorf("installation not found for owner %s (negatively cached)", owner)
		}
	}

	logger.Debug().
		Str("owner", owner).
		Msg("Client cache miss - looking up installation")

	// Step 2: Check org mapping cache for owner→installationID
	// Try owner ID first (if provided), then fall back to owner name
	var installationID int64
	var foundInOrgCache bool

	if b.OrgMappingCache != nil {
		// Try owner ID-based lookup first (preferred - immutable)
		if len(ownerID) > 0 && ownerID[0] > 0 {
			idKey := b.OrgMappingCache.BuildOwnerIDCacheKey(ownerID[0])
			if idKey != "" {
				if cachedID, found := b.OrgMappingCache.Get(idKey); found {
					installationID = cachedID
					foundInOrgCache = true
					logger.Debug().
						Str("owner", owner).
						Int64("owner_id", ownerID[0]).
						Int64("installation_id", installationID).
						Msg("Found installation ID in org mapping cache by owner ID")
				}
			}
		}

		// Fall back to owner name-based lookup (backward compatibility)
		if !foundInOrgCache {
			orgKey := b.OrgMappingCache.BuildOrgCacheKey(owner)
			if orgKey != "" {
				if cachedID, found := b.OrgMappingCache.Get(orgKey); found {
					installationID = cachedID
					foundInOrgCache = true
					logger.Debug().
						Str("owner", owner).
						Int64("installation_id", installationID).
						Msg("Found installation ID in org mapping cache by owner name")
				}
			}
		}
	}

	// Step 3: If not in cache, look up via Installations service
	if !foundInOrgCache {
		if !b.GithubCloud {
			// For GHES: Use repository-level lookup
			// Note: This requires a repository name, which we don't have here.
			// For GHES, callers should use the existing InstallationManager.GetClients() with installation ID
			return nil, fmt.Errorf("GetClientsByOwner requires owner-level installation lookup (GHEC only). For GHES, use repository-based lookup")
		}

		// For GHEC: Use GetByOwner for org-level installation lookup
		// This works for both organizations and users
		// In GHEC, there's typically ONE installation per org (max 2)
		installation, lookupErr := b.Installations.GetByOwner(ctx, owner)
		if lookupErr != nil {
			logger.Warn().Err(lookupErr).
				Str("owner", owner).
				Msg("Failed to find installation for owner (org or user)")

			// Cache negative result to avoid repeated API calls for non-existent installations
			if b.ClientCache != nil {
				b.ClientCache.PutNegative(owner)
				logger.Debug().
					Str("owner", owner).
					Msg("Cached negative result (installation not found)")
			}

			return nil, errors.Wrapf(lookupErr, "failed to find installation for owner %s (org or user)", owner)
		}
		installationID = installation.ID

		logger.Info().
			Str("owner", owner).
			Int64("installation_id", installationID).
			Str("lookup_method", "GetByOwner").
			Msg("Found installation via owner lookup (GHEC)")

		// Cache the owner→installationID mapping for faster future lookups
		// Cache by both owner ID (if available) and owner name
		if b.OrgMappingCache != nil {
			// Cache by owner ID (preferred - immutable)
			if len(ownerID) > 0 && ownerID[0] > 0 {
				idKey := b.OrgMappingCache.BuildOwnerIDCacheKey(ownerID[0])
				if idKey != "" {
					b.OrgMappingCache.Set(idKey, installationID)
					logger.Debug().
						Str("owner", owner).
						Int64("owner_id", ownerID[0]).
						Int64("installation_id", installationID).
						Msg("Cached ownerID→installation mapping")
				}
			}

			// Also cache by owner name (backward compatibility)
			orgKey := b.OrgMappingCache.BuildOrgCacheKey(owner)
			if orgKey != "" {
				b.OrgMappingCache.Set(orgKey, installationID)
				logger.Debug().
					Str("owner", owner).
					Int64("installation_id", installationID).
					Msg("Cached owner name→installation mapping")
			}
		}
	}

	// Step 4: Create clients with per-org rate limiting
	clients, err := b.createClientsForOwner(ctx, owner, installationID)
	if err != nil {
		logger.Error().Err(err).
			Str("owner", owner).
			Int64("installation_id", installationID).
			Msg("Failed to create clients for owner")
		return nil, errors.Wrapf(err, "failed to create clients for owner %s (installation %d)", owner, installationID)
	}

	// Step 5: Cache clients by owner (correct for GHEC where there's ONE installation per org)
	if b.ClientCache != nil {
		b.ClientCache.Put(owner, clients)
		logger.Debug().
			Str("owner", owner).
			Int64("installation_id", installationID).
			Msg("Cached clients by owner")
	}

	return clients, nil
}

// GetClientsForEvent retrieves installation clients using cached lookups when possible.
// This method provides a unified interface for handlers to get clients efficiently.
//
// For GHEC: Uses owner-based lookup with ClientCache and OrgMappingCache (supports owner ID-based caching)
// For GHES: Uses installation-based lookup with InstallationManager's cache
//
// This ensures all client creation benefits from the caching infrastructure.
// Handlers should use this method instead of calling NewInstallationClient directly.
//
// Optional ownerID parameter: If provided, enables ID-based cache lookups (preferred for immutability).
// This is backward compatible - existing calls without ownerID continue to work.
func (b *Base) GetClientsForEvent(ctx context.Context, owner string, installationID int64, ownerID ...int64) (*InstallationClients, error) {
	// For GHEC, use owner-based lookup (benefits from ClientCache + OrgMappingCache)
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
