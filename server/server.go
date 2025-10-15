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

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alexedwards/scs"
	"github.com/bluekeyes/hatpear"
	"github.com/c2h5oh/datasize"
	"github.com/die-net/lrucache"
	"github.com/gregjones/httpcache"
	"github.com/palantir/go-baseapp/baseapp"
	datadog "github.com/palantir/go-baseapp/baseapp/datadog"
	"github.com/palantir/go-githubapp/appconfig"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/go-githubapp/oauth2"
	"github.com/palantir/policy-bot/server/handler"
	"github.com/palantir/policy-bot/server/middleware"
	"github.com/palantir/policy-bot/server/sqsconsumer"
	"github.com/palantir/policy-bot/version"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"goji.io"
	"goji.io/pat"
)

const (
	DefaultSessionLifetime = 24 * time.Hour
	DefaultGitHubTimeout   = 10 * time.Second

	DefaultWebhookWorkers   = 10
	DefaultWebhookQueueSize = 100

	DefaultHTTPCacheSize     = 50 * datasize.MB
	DefaultPushedAtCacheSize = 100_000
	DefaultPolicyBotRoute    = "/policy-bot"
)

type Server struct {
	config      *Config
	base        *baseapp.Server
	sqsConsumer sqsconsumer.Consumer
}

// New instantiates a new Server.
// Callers must then invoke Start to run the Server.
func New(c *Config) (*Server, error) {
	logger := baseapp.NewLogger(baseapp.LoggingConfig{
		Level:  c.Logging.Level,
		Pretty: c.Logging.Text,
	})

	lifetime, _ := time.ParseDuration(c.Sessions.Lifetime)
	if lifetime == 0 {
		lifetime = DefaultSessionLifetime
	}

	publicURL, err := url.Parse(c.Server.PublicURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed parse public URL")
	}
	if publicURL.Scheme == "" || publicURL.Host == "" {
		return nil, errors.Errorf("public URL must contain a scheme and a host: %s", c.Server.PublicURL)
	}

	basePath := strings.TrimSuffix(publicURL.Path, "/")
	forceTLS := publicURL.Scheme == "https"

	sessions := scs.NewCookieManager(c.Sessions.Key)
	sessions.Name("policy-bot")
	sessions.Lifetime(lifetime)
	sessions.Persist(true)
	sessions.HttpOnly(true)
	sessions.Secure(forceTLS)

	base, err := baseapp.NewServer(c.Server, baseapp.DefaultParams(logger, "policybot.")...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize base server")
	}

	maxSize := int64(DefaultHTTPCacheSize)
	if c.Cache.MaxSize != 0 {
		maxSize = int64(c.Cache.MaxSize)
	}

	githubTimeout := c.Workers.GithubTimeout
	if githubTimeout == 0 {
		githubTimeout = DefaultGitHubTimeout
	}

	// Use enterprise config for V4 URL, fallback to cloud if not set
	enterpriseV4URL := c.GithubEnterprise.V4APIURL
	if enterpriseV4URL == "" {
		enterpriseV4URL = c.GithubCloud.V4APIURL
	}
	if enterpriseV4URL == "" {
		return nil, errors.New("no GitHub v4 API URL configured: must set v4_api_url in github_enterprise or github_cloud")
	}

	environmentProxy := os.Getenv("AWS_PROXY")

	if len(environmentProxy) == 0 {
		environmentProxy = os.Getenv("HTTP_PROXY")
	}

	proxyURL, err := url.Parse(environmentProxy)
	if err != nil {
		return nil, errors.Wrap(err, "invalid proxy URL")
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}

	userAgent := fmt.Sprintf("policy-bot/%s", version.GetVersion())
	enterpriseClientCreator, err := githubapp.NewDefaultCachingClientCreator(
		c.GithubEnterprise.Config,
		githubapp.WithClientUserAgent(userAgent),
		githubapp.WithClientTimeout(githubTimeout),
		githubapp.WithTransport(transport),
		githubapp.WithClientCaching(true, func() httpcache.Cache {
			return lrucache.New(maxSize, 0)
		}),
		githubapp.WithClientMiddleware(
			githubapp.ClientLogging(zerolog.DebugLevel),
			githubapp.ClientMetrics(base.Registry()),
		),
	)

	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize enterprise client creator")
	}

	cloudClientCreator, err := githubapp.NewDefaultCachingClientCreator(
		c.GithubCloud.Config,
		githubapp.WithClientUserAgent(userAgent),
		githubapp.WithClientTimeout(githubTimeout),
		githubapp.WithClientCaching(true, func() httpcache.Cache {
			return lrucache.New(maxSize, 0)
		}),
		githubapp.WithClientMiddleware(
			githubapp.ClientLogging(zerolog.DebugLevel),
			githubapp.ClientMetrics(base.Registry()),
		),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize client creator")
	}

	enterpriseAppClient, err := enterpriseClientCreator.NewAppClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize Github app client")
	}

	enterpriseApp, _, err := enterpriseAppClient.Apps.Get(context.Background(), "")
	if err != nil {
		return nil, errors.Wrap(err, "failed to get configured GitHub app")
	}

	cloudAppClient, err := cloudClientCreator.NewAppClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize Github app client")
	}

	cloudApp, _, err := cloudAppClient.Apps.Get(context.Background(), "")
	if err != nil {
		return nil, errors.Wrap(err, "failed to get configured GitHub app")
	}

	pushedAtSize := c.Cache.PushedAtSize
	if pushedAtSize == 0 {
		pushedAtSize = DefaultPushedAtCacheSize
	}

	// policyPaths := []string{c.Options.PolicyPath}
	// if c.Options.ForceSharedPolicy {
	// 	policyPaths = []string{}
	// }

	// sharedPolicyPaths := []string{}
	// if c.Options.SharedPolicyPath != nil {
	// 	sharedPolicyPaths = []string{*c.Options.SharedPolicyPath}
	// }

	enterpriseBasePolicyHandler := handler.Base{
		ClientCreator:     enterpriseClientCreator,
		BaseConfig:        &c.Server,
		Installations:     githubapp.NewInstallationsService(enterpriseAppClient),
		InstallationIdMap: make(map[int64]int64),

		PullOpts: &c.EnterpriseOptions,
		ConfigFetcher: &handler.ConfigFetcher{
			Loader: appconfig.NewLoader(
				[]string{c.EnterpriseOptions.PolicyPath},
				appconfig.WithOwnerDefault(c.EnterpriseOptions.SharedRepository, []string{c.EnterpriseOptions.SharedPolicyPath}),
			),
		},

		AppName: enterpriseApp.GetSlug(),
	}

	cloudBasePolicyHandler := handler.Base{
		ClientCreator: cloudClientCreator,
		BaseConfig:    &c.Server,
		Installations: githubapp.NewInstallationsService(cloudAppClient),

		PullOpts: &c.CloudOptions,
		ConfigFetcher: &handler.ConfigFetcher{
			Loader: appconfig.NewLoader(
				[]string{c.CloudOptions.PolicyPath},
				appconfig.WithOwnerDefault(c.CloudOptions.SharedRepository, []string{c.CloudOptions.SharedPolicyPath}),
			),
		},

		AppName: cloudApp.GetSlug(),
	}

	queueSize := c.Workers.QueueSize
	if queueSize < 1 {
		queueSize = DefaultWebhookQueueSize
	}

	workers := c.Workers.Workers
	if workers < 1 {
		workers = DefaultWebhookWorkers
	}

	enterpriseHandlers := []githubapp.EventHandler{
		&handler.Installation{Base: enterpriseBasePolicyHandler},
		&handler.MergeGroup{Base: enterpriseBasePolicyHandler},
		&handler.PullRequest{Base: enterpriseBasePolicyHandler},
		&handler.PullRequestReview{Base: enterpriseBasePolicyHandler},
		&handler.IssueComment{Base: enterpriseBasePolicyHandler},
		&handler.Status{Base: enterpriseBasePolicyHandler},
		&handler.CheckRun{Base: enterpriseBasePolicyHandler},
		&handler.WorkflowRun{Base: enterpriseBasePolicyHandler},
	}

	cloudHandlers := []githubapp.EventHandler{
		&handler.Installation{Base: cloudBasePolicyHandler},
		&handler.MergeGroup{Base: cloudBasePolicyHandler},
		&handler.PullRequest{Base: cloudBasePolicyHandler},
		&handler.PullRequestReview{Base: cloudBasePolicyHandler},
		&handler.IssueComment{Base: cloudBasePolicyHandler},
		&handler.Status{Base: cloudBasePolicyHandler},
		&handler.CheckRun{Base: cloudBasePolicyHandler},
		&handler.WorkflowRun{Base: cloudBasePolicyHandler},
	}

	// Create the scheduler that both HTTP and SQS will use
	cloudScheduler := githubapp.QueueAsyncScheduler(
		queueSize, workers,
		githubapp.WithSchedulingMetrics(base.Registry()),
		githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
		githubapp.WithContextDeriver(context.WithoutCancel),
	)

	enterpriseScheduler := githubapp.QueueAsyncScheduler(
		queueSize, workers,
		githubapp.WithSchedulingMetrics(base.Registry()),
		githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
		githubapp.WithContextDeriver(context.WithoutCancel),
	)

	enterpriseDispatcher := githubapp.NewEventDispatcher(
		enterpriseHandlers,
		c.GithubEnterprise.App.WebhookSecret,
		githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
		githubapp.WithScheduler(enterpriseScheduler),
	)

	cloudDispatcher := githubapp.NewEventDispatcher(
		cloudHandlers,
		c.GithubCloud.App.WebhookSecret,
		githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
		githubapp.WithScheduler(cloudScheduler),
	)

	// Create SQS consumer using the same scheduler and handlers
	// Convert server EventQueueConfig to sqsconsumer EventQueueConfig
	sqsQueues := make(map[string]sqsconsumer.EventQueueConfig)
	for eventType, queueConfig := range c.SQS.Queues {
		sqsQueues[eventType] = sqsconsumer.EventQueueConfig{
			EastRegionURL:     queueConfig.EastRegionURL,
			WestRegionURL:     queueConfig.WestRegionURL,
			EventRouting:      queueConfig.EventRouting,
			GHECEnabled:       queueConfig.GHECEnabled,
			GHESEnabled:       queueConfig.GHESEnabled,
			QueueWorkers:      queueConfig.QueueWorkers,
			VisibilityTimeout: queueConfig.VisibilityTimeout,
			MaxRetries:        queueConfig.MaxRetries,
		}
	}

	sqsConfig := &sqsconsumer.Config{
		Enabled:           c.SQS.Enabled,
		Region:            c.SQS.Region,
		EndpointURL:       c.SQS.EndpointURL,
		ProcessingMode:    c.SQS.ProcessingMode,
		Queues:            sqsQueues,
		WorkersPerQueue:   c.SQS.WorkersPerQueue,
		MaxMessages:       c.SQS.MaxMessages,
		VisibilityTimeout: c.SQS.VisibilityTimeout,
		WaitTimeSeconds:   c.SQS.WaitTimeSeconds,
		ShutdownTimeout:   c.SQS.ShutdownTimeout,
		EnableRetry:       c.SQS.EnableRetry,
		MaxRetries:        c.SQS.MaxRetries,
		DLQ: sqsconsumer.DLQConfig{
			Enabled:         c.SQS.DLQ.Enabled,
			MaxReceiveCount: c.SQS.DLQ.MaxReceiveCount,
			QueueSuffix:     c.SQS.DLQ.QueueSuffix,
		},
	}

	sqsConsumer, err := sqsconsumer.New(sqsConfig, cloudHandlers, enterpriseHandlers, cloudScheduler, enterpriseScheduler, logger, base.Registry())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SQS consumer")
	}

	// Use cloud WebURL for templates, fallback to enterprise if cloud not set
	webURL := c.GithubCloud.WebURL
	if webURL == "" {
		webURL = c.GithubEnterprise.WebURL
	}
	if webURL == "" {
		return nil, errors.New("no GitHub web URL configured: must set web_url in github_cloud or github_enterprise")
	}

	templates, err := handler.LoadTemplates(&c.Files, basePath, webURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load templates")
	}

	var mux *goji.Mux
	if basePath == "" {
		mux = base.Mux()
	} else {
		mux = goji.SubMux()
		base.Mux().Handle(pat.New(basePath+"/*"), mux)
	}

	// ============================================================================
	// Route Configuration:
	//
	// Header-based routing (via middleware):
	//   - /api/github/hook -> webhooks (enterprise/cloud based on headers)
	//   - / -> index page (enterprise/cloud based on headers)
	//   - /api/simulate/* -> simulation (enterprise/cloud based on headers)
	//
	// Path-based routing (explicit):
	//   - /details/ghes/* -> GitHub Enterprise Server details
	//   - /details/ghec/* -> GitHub Enterprise Cloud details
	//
	// Shared routes:
	//   - /api/health -> combined health check
	//   - /api/metrics -> Prometheus metrics
	//   - /api/validate -> policy validation utility
	//   - /static/* -> static assets
	//   - /oauth/callback -> OAuth callback (session-based)
	//
	// Routing priority:
	//   1. X-GitHub-Enterprise-Host header -> enterprise
	//   2. x-dcp-destination-host header -> cloud
	//   3. source query parameter -> enterprise/cloud
	//   4. Default -> cloud
	// ============================================================================

	// Webhook endpoint uses header-based routing:
	// - X-GitHub-Enterprise-Host header -> enterprise dispatcher
	// - x-dcp-destination-host header -> cloud dispatcher
	// - No header -> defaults to cloud dispatcher
	mux.Handle(pat.Post(githubapp.DefaultWebhookRoute),
		middleware.SelectWebhookDispatcher(enterpriseDispatcher, cloudDispatcher))

	enterpriseSimulateHandler := &handler.Simulate{
		Base: enterpriseBasePolicyHandler,
	}

	cloudSimulateHandler := &handler.Simulate{
		Base: cloudBasePolicyHandler,
	}

	// additional API routes
	mux.Handle(pat.Get("/api/health"), handler.Health())
	mux.Handle(pat.Get("/api/metrics"), handler.Metrics(base.Registry(), c.Prometheus))

	// Policy validation endpoint - shared utility, no source separation needed
	mux.Handle(pat.Put("/api/validate"), handler.Validate())

	// Policy simulation endpoint - routes based on headers or source param
	mux.Handle(pat.Post("/api/simulate/:owner/:repo/:number"),
		middleware.SelectAPIHandler(
			hatpear.Try(enterpriseSimulateHandler),
			hatpear.Try(cloudSimulateHandler)))

	oauth2RedirectURL := *publicURL
	oauth2RedirectURL.Path = basePath + oauth2.DefaultRoute

	// Use cloud config for OAuth, fallback to enterprise if not set
	oauthConfig := c.GithubCloud.Config
	if oauthConfig.App.IntegrationID == 0 {
		oauthConfig = c.GithubEnterprise.Config
	}
	if oauthConfig.App.IntegrationID == 0 {
		return nil, errors.New("no GitHub app configured: must set app.integration_id in github_cloud or github_enterprise")
	}

	ghecAuthPath := basePath + "/api/github/auth/ghec"
	ghesAuthPath := basePath + "/api/github/auth/ghes"

	// OAuth callback is shared between enterprise and cloud
	// Session state determines which GitHub instance to authenticate with
	mux.Handle(pat.Get(ghesAuthPath), oauth2.NewHandler(
		oauth2.GetConfig(c.GithubEnterprise.Config, nil),
		oauth2.ForceTLS(forceTLS),
		oauth2.WithStore(&oauth2.SessionStateStore{
			Sessions: sessions,
		}),
		oauth2.OnLogin(handler.Login(c.GithubEnterprise.Config, basePath, sessions)),
		oauth2.WithRedirectURL(oauth2RedirectURL.String()),
	))

	mux.Handle(pat.Get(ghecAuthPath), oauth2.NewHandler(
		oauth2.GetConfig(c.GithubCloud.Config, nil),
		oauth2.ForceTLS(forceTLS),
		oauth2.WithStore(&oauth2.SessionStateStore{
			Sessions: sessions,
		}),
		oauth2.OnLogin(handler.Login(c.GithubCloud.Config, basePath, sessions)),
		oauth2.WithRedirectURL(oauth2RedirectURL.String()),
	))

	// additional client routes
	mux.Handle(pat.Get("/favicon.ico"), http.RedirectHandler(basePath+"/static/img/favicon.ico", http.StatusFound))
	mux.Handle(pat.Get("/static/*"), handler.Static(basePath+"/static/", &c.Files))

	// Index page uses header-based routing to display appropriate GitHub App info
	mux.Handle(pat.Get("/"), middleware.SelectIndexHandler(enterpriseBasePolicyHandler, cloudBasePolicyHandler, &c.GithubEnterprise.Config, &c.GithubCloud.Config, templates))

	enterpriseDetailsHandler := handler.Details{
		Base:      enterpriseBasePolicyHandler,
		Sessions:  sessions,
		Templates: templates,
	}

	cloudDetailsHandler := handler.Details{
		Base:      cloudBasePolicyHandler,
		Sessions:  sessions,
		Templates: templates,
	}

	// Details pages use explicit path separation:
	// - /details/ghes/* for GitHub Enterprise Server
	// - /details/ghec/* for GitHub Enterprise Cloud
	details := goji.SubMux()
	details.Use(handler.RequireLogin(sessions, basePath))

	details.Handle(pat.Get("/ghes/:owner/:repo/:number"), hatpear.Try(&enterpriseDetailsHandler))
	details.Handle(pat.Get("/ghes/:owner/:repo/:number/reviewers"), hatpear.Try(&handler.DetailsReviewers{
		Details: enterpriseDetailsHandler,
	}))

	details.Handle(pat.Get("/ghec/:owner/:repo/:number"), hatpear.Try(&cloudDetailsHandler))
	details.Handle(pat.Get("/ghec/:owner/:repo/:number/reviewers"), hatpear.Try(&handler.DetailsReviewers{
		Details: cloudDetailsHandler,
	}))

	mux.Handle(pat.New("/details/*"), details)

	return &Server{
		config:      c,
		base:        base,
		sqsConsumer: sqsConsumer,
	}, nil
}

// Start is blocking and long-running
func (s *Server) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if s.config.Datadog.Address != "" {
		if err := datadog.StartEmitter(s.base, s.config.Datadog); err != nil {
			return err
		}
	}

	// Start SQS consumer if enabled (non-blocking)
	if s.config.SQS.Enabled {
		if err := s.sqsConsumer.Start(ctx); err != nil {
			return errors.Wrap(err, "failed to start SQS consumer")
		}

		// Set up graceful shutdown for SQS consumer
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			if sqsErr := s.sqsConsumer.Stop(shutdownCtx); sqsErr != nil {
				logger := baseapp.NewLogger(baseapp.LoggingConfig{
					Level:  s.config.Logging.Level,
					Pretty: s.config.Logging.Text,
				})
				logger.Error().Err(sqsErr).Msg("Error stopping SQS consumer")
			}
		}()
	}

	// Start the HTTP server (this blocks until shutdown)
	// Both HTTP and SQS now run in parallel - SQS consumers are already running in goroutines
	return s.base.Start()
}
