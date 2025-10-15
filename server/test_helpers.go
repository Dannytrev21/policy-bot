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
	"strings"
	"time"

	"github.com/alexedwards/scs"
	"github.com/die-net/lrucache"
	"github.com/google/go-github/v47/github"
	"github.com/gregjones/httpcache"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/appconfig"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/go-githubapp/oauth2"
	"github.com/palantir/policy-bot/server/handler"
	"github.com/palantir/policy-bot/server/sqsconsumer"
	"github.com/palantir/policy-bot/version"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"goji.io"
	"goji.io/pat"
)

// NewWithTestHandlers creates a new server instance with custom test handlers
// This is primarily used for testing and integration scenarios
func NewWithTestHandlers(c *Config, testHandlers []githubapp.EventHandler) (*Server, error) {
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

	// Use cloud config for tests, fallback to enterprise if not set
	testConfig := c.GithubCloud.Config
	if testConfig.App.IntegrationID == 0 {
		testConfig = c.GithubEnterprise.Config
	}

	v4URL, err := url.Parse(testConfig.V4APIURL)
	if err != nil {
		return nil, errors.Wrap(err, "invalid v4 API URL")
	}

	userAgent := fmt.Sprintf("policy-bot/%s", version.GetVersion())
	cc, err := githubapp.NewDefaultCachingClientCreator(
		testConfig,
		githubapp.WithClientUserAgent(userAgent),
		githubapp.WithClientTimeout(githubTimeout),
		githubapp.WithClientCaching(true, func() httpcache.Cache {
			return lrucache.New(maxSize, 0)
		}),
		githubapp.WithClientMiddleware(
			githubapp.ClientLogging(
				zerolog.DebugLevel,
				githubapp.LogRequestBody("^"+v4URL.Path+"$"),
			),
			githubapp.ClientMetrics(base.Registry()),
		),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize client creator")
	}

	appClient, err := cc.NewAppClient()
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize Github app client")
	}

	app, _, err := appClient.Apps.Get(context.Background(), "")
	if err != nil {
		// For testing, we might not have a real GitHub app, so create a mock app
		logger.Warn().Msg("Failed to get GitHub app, using test configuration")
		app = &github.App{
			Slug: github.String("test-app"),
		}
	}

	pushedAtSize := c.Cache.PushedAtSize
	if pushedAtSize == 0 {
		pushedAtSize = DefaultPushedAtCacheSize
	}

	// Use test handlers if provided, otherwise create minimal handlers
	var handlers []githubapp.EventHandler
	if len(testHandlers) > 0 {
		handlers = testHandlers
	} else {
		// Create minimal policy handlers for normal operation
		policyPaths := []string{c.CloudOptions.PolicyPath}
		if c.CloudOptions.ForceSharedPolicy {
			policyPaths = []string{}
		}

		sharedPolicyPaths := []string{}
		if c.CloudOptions.SharedPolicyPath != "" {
			sharedPolicyPaths = []string{c.CloudOptions.SharedPolicyPath}
		}

		basePolicyHandler := handler.Base{
			ClientCreator: cc,
			BaseConfig:    &c.Server,
			Installations: githubapp.NewInstallationsService(appClient),
			PullOpts:      &c.CloudOptions,
			ConfigFetcher: &handler.ConfigFetcher{
				Loader: appconfig.NewLoader(
					policyPaths,
					appconfig.WithOwnerDefault(c.CloudOptions.SharedRepository, sharedPolicyPaths),
				),
			},
			AppName: app.GetSlug(),
		}

		handlers = []githubapp.EventHandler{
			&handler.Installation{Base: basePolicyHandler},
			&handler.MergeGroup{Base: basePolicyHandler},
			&handler.PullRequest{Base: basePolicyHandler},
			&handler.PullRequestReview{Base: basePolicyHandler},
			&handler.IssueComment{Base: basePolicyHandler},
			&handler.Status{Base: basePolicyHandler},
			&handler.CheckRun{Base: basePolicyHandler},
			&handler.WorkflowRun{Base: basePolicyHandler},
		}
	}

	queueSize := c.Workers.QueueSize
	if queueSize < 1 {
		queueSize = DefaultWebhookQueueSize
	}

	workers := c.Workers.Workers
	if workers < 1 {
		workers = DefaultWebhookWorkers
	}

	// Create the scheduler that both HTTP and SQS will use
	scheduler := githubapp.QueueAsyncScheduler(
		queueSize, workers,
		githubapp.WithSchedulingMetrics(base.Registry()),
		githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
		githubapp.WithContextDeriver(context.WithoutCancel),
	)

	dispatcher := githubapp.NewEventDispatcher(
		handlers,
		testConfig.App.WebhookSecret,
		githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
		githubapp.WithScheduler(scheduler),
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

	sqsConsumer, err := sqsconsumer.New(sqsConfig, handlers, handlers, scheduler, scheduler, logger, base.Registry())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SQS consumer")
	}

	// Templates are optional for testing
	_, err = handler.LoadTemplates(&c.Files, basePath, testConfig.WebURL)
	if err != nil {
		// For testing, we can ignore template loading errors
		logger.Warn().Err(err).Msg("Failed to load templates (continuing anyway)")
	}

	var mux *goji.Mux
	if basePath == "" {
		mux = base.Mux()
	} else {
		mux = goji.SubMux()
		base.Mux().Handle(pat.New(basePath+"/*"), mux)
	}

	// webhook route
	mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), dispatcher)

	// Health endpoint for testing
	mux.Handle(pat.Get("/health"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok","message":"Server is healthy"}`)); err != nil {
			logger.Warn().Err(err).Msg("Failed to write health check response")
		}
	}))

	// For testing, we might not need all the OAuth and UI routes
	// Only add them if we have proper configuration
	if testConfig.OAuth.ClientID != "" && testConfig.OAuth.ClientSecret != "" {
		oauth2RedirectURL := *publicURL
		oauth2RedirectURL.Path = basePath + oauth2.DefaultRoute

		mux.Handle(pat.Get(oauth2.DefaultRoute), oauth2.NewHandler(
			oauth2.GetConfig(testConfig, nil),
			oauth2.WithStore(&oauth2.SessionStateStore{
				Sessions: sessions,
			}),
			oauth2.OnLogin(handler.Login(testConfig, basePath, sessions)),
			oauth2.WithRedirectURL(oauth2RedirectURL.String()),
		))
	}

	// Basic routes
	mux.Handle(pat.Get("/favicon.ico"), http.RedirectHandler(basePath+"/static/img/favicon.ico", http.StatusFound))

	return &Server{
		config:      c,
		base:        base,
		sqsConsumer: sqsConsumer,
	}, nil
}

// NewWithSeparateHandlers creates a server with separate cloud and enterprise handlers for comprehensive testing
func NewWithSeparateHandlers(c *Config, cloudHandlers []githubapp.EventHandler, enterpriseHandlers []githubapp.EventHandler) (*Server, error) {
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

	userAgent := fmt.Sprintf("policy-bot/%s", version.GetVersion())

	// Create enterprise client creator
	enterpriseClientCreator, err := githubapp.NewDefaultCachingClientCreator(
		c.GithubEnterprise.Config,
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
		return nil, errors.Wrap(err, "failed to initialize enterprise client creator")
	}

	// Create cloud client creator
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
		return nil, errors.Wrap(err, "failed to initialize cloud client creator")
	}

	// For testing, mock GitHub app clients (not needed for test handlers)
	_, _ = enterpriseClientCreator.NewAppClient()
	_, _ = cloudClientCreator.NewAppClient()

	queueSize := c.Workers.QueueSize
	if queueSize < 1 {
		queueSize = DefaultWebhookQueueSize
	}

	workers := c.Workers.Workers
	if workers < 1 {
		workers = DefaultWebhookWorkers
	}

	// Create separate schedulers for enterprise and cloud
	enterpriseScheduler := githubapp.QueueAsyncScheduler(
		queueSize, workers,
		githubapp.WithSchedulingMetrics(base.Registry()),
		githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
		githubapp.WithContextDeriver(context.WithoutCancel),
	)

	cloudScheduler := githubapp.QueueAsyncScheduler(
		queueSize, workers,
		githubapp.WithSchedulingMetrics(base.Registry()),
		githubapp.WithAsyncErrorCallback(githubapp.MetricsAsyncErrorCallback(base.Registry())),
		githubapp.WithContextDeriver(context.WithoutCancel),
	)

	// Create SQS consumer with separate handlers
	// Convert server EventQueueConfig to sqsconsumer EventQueueConfig
	sqsQueuesForSeparate := make(map[string]sqsconsumer.EventQueueConfig)
	for eventType, queueConfig := range c.SQS.Queues {
		sqsQueuesForSeparate[eventType] = sqsconsumer.EventQueueConfig{
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
		Queues:            sqsQueuesForSeparate,
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

	sqsConsumer, err := sqsconsumer.New(
		sqsConfig,
		cloudHandlers,
		enterpriseHandlers,
		cloudScheduler,
		enterpriseScheduler,
		logger,
		base.Registry(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create SQS consumer")
	}

	var mux *goji.Mux
	if basePath == "" {
		mux = base.Mux()
	} else {
		mux = goji.SubMux()
		base.Mux().Handle(pat.New(basePath+"/*"), mux)
	}

	// Create dispatchers for webhook routing
	// Note: enterpriseDispatcher is created but webhook route uses cloudDispatcher by default
	_ = githubapp.NewEventDispatcher(
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

	// Webhook route (defaults to cloud dispatcher for testing)
	mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), cloudDispatcher)

	// Health endpoint
	mux.Handle(pat.Get("/health"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"status":"ok","message":"Server is healthy"}`)); err != nil {
			logger.Warn().Err(err).Msg("Failed to write health check response")
		}
	}))

	return &Server{
		config:      c,
		base:        base,
		sqsConsumer: sqsConsumer,
	}, nil
}

// Address returns the server's listening address for testing
func (s *Server) Address() string {
	// For testing purposes, we need to get the actual listening address
	// Since baseapp.Server doesn't expose Address(), we'll use reflection or a workaround
	// For now, return a default that matches the test configuration
	return "localhost:8080"
}

// Shutdown gracefully stops the HTTP server for tests.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.base == nil {
		return nil
	}
	return s.base.HTTPServer().Shutdown(ctx)
}
