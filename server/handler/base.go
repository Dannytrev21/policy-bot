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
	"strings"
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

type Base struct {
	githubapp.ClientCreator

	Installations               githubapp.InstallationsService
	ConfigFetcher               *ConfigFetcher
	AutorRemediateConfigFetcher *ConfigFetcher
	BaseConfig                  *baseapp.HTTPConfig
	PullOpts                    *PullEvaluationOptions
	InstallationIdMap           map[int64]int64 // Legacy cache, kept for backwards compatibility
	InstallationRegistry        *InstallationRegistry
	InstallationManager         *InstallationManager // Centralized manager for installation client creation
	MetricsRegistry             gometrics.Registry   // Registry for recording metrics
	GithubCloud                 bool
	mu                          *sync.RWMutex

	AppName              string
	DefaultFetchedConfig *FetchedConfig
}

func (base *Base) Initialize() {

	if base.InstallationIdMap == nil {
		base.InstallationIdMap = make(map[int64]int64)
	}

	if base.mu == nil {
		base.mu = &sync.RWMutex{}
	}

	if base.DefaultFetchedConfig == nil {
		base.DefaultFetchedConfig = &FetchedConfig{}
	}

	// Initialize installation registry with TTL-based caching
	// Positive cache: 1 hour (installations rarely change)
	// Negative cache: 5 minutes (allow faster detection of new installations)
	if base.InstallationRegistry == nil {
		base.InstallationRegistry = NewInstallationRegistry(1*time.Hour, 5*time.Minute, base.MetricsRegistry)
	}

	// Initialize installation manager if not already set
	// The manager centralizes client creation logic and will be extended with
	// retry and circuit breaker patterns in future phases
	if base.InstallationManager == nil {
		base.InstallationManager = NewInstallationManager(
			base.ClientCreator,
			base.InstallationRegistry,
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
// The method uses a TTL-based cache that stores both positive (installed) and negative
// (not installed) results to minimize API calls to GitHub.
func (b *Base) VerifyInstallation(ctx context.Context, installationID int64) bool {
	logger := zerolog.Ctx(ctx)

	// Check the installation registry cache first
	status, cacheHit := b.InstallationRegistry.Check(installationID)
	if cacheHit {
		switch status {
		case InstallationExists:
			logger.Debug().
				Int64("installation_id", installationID).
				Msg("Installation found in cache (positive)")
			return true
		case InstallationNotFound:
			logger.Debug().
				Int64("installation_id", installationID).
				Msg("Installation found in cache (negative - not installed)")
			return false
		}
	}

	// Cache miss - verify installation via GitHub API
	logger.Debug().
		Int64("installation_id", installationID).
		Msg("Installation cache miss - verifying via API")

	appClient, err := b.NewAppClient()
	if err != nil {
		logger.Warn().Err(err).
			Int64("installation_id", installationID).
			Msg("Failed to create app client for installation verification")
		return false
	}

	b.InstallationRegistry.RecordAPICall()
	installation, resp, err := appClient.Apps.GetInstallation(ctx, installationID)
	if err != nil {
		// Check if it's a 404 (installation not found) - this is expected for repos where app isn't installed
		if resp != nil && resp.StatusCode == 404 || strings.Contains(err.Error(), "404") {
			logger.Info().
				Int64("installation_id", installationID).
				Msg("Installation not found - app may not be installed on this repository")

			// Cache negative result to avoid repeated API calls
			b.InstallationRegistry.MarkNotInstalled(installationID)
			return false
		}
		// Other errors are unexpected and should be logged as warnings
		logger.Warn().Err(err).
			Int64("installation_id", installationID).
			Msg("Failed to verify installation")
		return false
	}

	// Cache the valid installation (positive result)
	b.InstallationRegistry.MarkInstalled(installationID)

	// Also update legacy cache for backwards compatibility
	b.mu.Lock()
	b.InstallationIdMap[installationID] = installation.GetID()
	b.mu.Unlock()

	logger.Debug().
		Int64("installation_id", installationID).
		Msg("Installation verified and cached")

	return true
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

	// Verify installation exists before attempting to create clients
	// This populates the cache which the InstallationManager will use
	if !b.VerifyInstallation(ctx, installationID) {
		span.SetStatus(codes.Error, "installation not verified")
		span.SetAttributes(attribute.Bool("installation.verified", false))
		return nil, fmt.Errorf("installation %d not found or not accessible - app may not be installed on repository %s", installationID, repoFullName)
	}
	span.SetAttributes(attribute.Bool("installation.verified", true))

	// Use InstallationManager to create both v3 and v4 clients
	// The manager handles verification, client creation, metrics, and error logging
	// Note: GetClients will create its own child span
	clients, err := b.InstallationManager.GetClients(ctx, installationID, repoFullName)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to get installation clients")
		return nil, err
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
