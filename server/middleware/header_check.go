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

package middleware

import (
	"html/template"
	"net/http"

	"github.com/bluekeyes/templatetree"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server/handler"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
)

// Header constants for routing decisions
const (
	HeaderGitHubEnterpriseHost = "X-GitHub-Enterprise-Host"
	HeaderDCPDestinationHost   = "x-dcp-destination-host"
	HeaderGitHubEvent          = "X-GitHub-Event"
	HeaderGitHubDelivery       = "X-GitHub-Delivery"
	HeaderGitHubHookID         = "X-GitHub-Hook-ID"
)

// Metrics for tracking routing decisions
var (
	routingDecisions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "policy_bot_routing_decisions_total",
			Help: "Total number of routing decisions made by middleware",
		},
		[]string{"route", "detection_method", "handler_type"},
	)

	routingLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "policy_bot_routing_latency_seconds",
			Help:    "Latency of routing decisions in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"route", "handler_type"},
	)
)

func init() {
	// Register metrics with the default prometheus registry
	prometheus.MustRegister(routingDecisions)
	prometheus.MustRegister(routingLatency)
}

// SelectWebhookDispatcher routes webhooks to appropriate dispatcher based on headers
func SelectWebhookDispatcher(enterprise, cloud http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := prometheus.NewTimer(routingLatency.WithLabelValues("", "webhook"))
		defer timer.ObserveDuration()

		ctx := r.Context()
		logger := zerolog.Ctx(ctx)

		// Extract headers for logging
		deliveryID := r.Header.Get(HeaderGitHubDelivery)
		eventType := r.Header.Get(HeaderGitHubEvent)
		hookID := r.Header.Get(HeaderGitHubHookID)
		enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
		dcpHost := r.Header.Get(HeaderDCPDestinationHost)

		// Create sub-logger with request details
		reqLogger := logger.With().
			Str("delivery_id", deliveryID).
			Str("event_type", eventType).
			Str("hook_id", hookID).
			Logger()

		var routeName string
		var detectionMethod string

		// Route based on headers
		if enterpriseHost != "" {
			routeName = "enterprise"
			detectionMethod = "enterprise_header"
			reqLogger.Info().
				Str("enterprise_host", enterpriseHost).
				Str("route", routeName).
				Msg("Routing webhook to enterprise dispatcher")
			routingDecisions.WithLabelValues(routeName, detectionMethod, "webhook").Inc()
			routingLatency.WithLabelValues(routeName, "webhook")
			enterprise.ServeHTTP(w, r)
			return
		}

		if dcpHost != "" {
			routeName = "cloud"
			detectionMethod = "dcp_header"
			reqLogger.Info().
				Str("dcp_host", dcpHost).
				Str("route", routeName).
				Msg("Routing webhook to cloud dispatcher via DCP header")
			routingDecisions.WithLabelValues(routeName, detectionMethod, "webhook").Inc()
			routingLatency.WithLabelValues(routeName, "webhook")
			cloud.ServeHTTP(w, r)
			return
		}

		// Default routing - use cloud dispatcher
		routeName = "cloud"
		detectionMethod = "default"
		reqLogger.Warn().
			Msg("No routing header found, using default cloud dispatcher")
		routingDecisions.WithLabelValues(routeName, detectionMethod, "webhook").Inc()
		routingLatency.WithLabelValues(routeName, "webhook")
		cloud.ServeHTTP(w, r)
	})
}

// SelectIndexHandler routes index page requests based on headers
func SelectIndexHandler(
	enterpriseBase handler.Base,
	cloudBase handler.Base,
	enterpriseConfig *githubapp.Config,
	cloudConfig *githubapp.Config,
	templates templatetree.Tree[*template.Template],
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := zerolog.Ctx(ctx)

		// Determine which handler to use based on headers
		var selectedBase handler.Base
		var selectedConfig *githubapp.Config
		var routeName string

		enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
		dcpHost := r.Header.Get(HeaderDCPDestinationHost)

		if enterpriseHost != "" {
			selectedBase = enterpriseBase
			selectedConfig = enterpriseConfig
			routeName = "enterprise"
			logger.Debug().
				Str("enterprise_host", enterpriseHost).
				Str("route", routeName).
				Msg("Routing index request to enterprise handler")
		} else if dcpHost != "" {
			selectedBase = cloudBase
			selectedConfig = cloudConfig
			routeName = "cloud"
			logger.Debug().
				Str("dcp_host", dcpHost).
				Str("route", routeName).
				Msg("Routing index request to cloud handler via DCP header")
		} else {
			// Default to cloud
			selectedBase = cloudBase
			selectedConfig = cloudConfig
			routeName = "cloud"
			logger.Debug().
				Str("route", routeName).
				Msg("Routing index request to cloud handler (default)")
		}

		// Record routing decision
		routingDecisions.WithLabelValues(routeName, "header_check", "index").Inc()

		// Create and serve the index handler
		indexHandler := &handler.Index{
			Base:         selectedBase,
			GithubConfig: selectedConfig,
			Templates:    templates,
		}

		indexHandler.ServeHTTP(w, r)
	}
}

// SelectAPIHandler routes API requests to appropriate handler based on headers or path
func SelectAPIHandler(enterprise, cloud http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := zerolog.Ctx(ctx)

		// Check for explicit routing headers
		enterpriseHost := r.Header.Get(HeaderGitHubEnterpriseHost)
		dcpHost := r.Header.Get(HeaderDCPDestinationHost)

		// Additional check: look for "source" query parameter
		source := r.URL.Query().Get("source")

		var selectedHandler http.Handler
		var routeName string
		var detectionMethod string

		// Priority order: headers first, then query param
		if enterpriseHost != "" {
			selectedHandler = enterprise
			routeName = "enterprise"
			detectionMethod = "enterprise_header"
			logger.Debug().
				Str("enterprise_host", enterpriseHost).
				Str("route", routeName).
				Str("path", r.URL.Path).
				Msg("Routing API request to enterprise handler")
		} else if source == "enterprise" || source == "ghes" {
			selectedHandler = enterprise
			routeName = "enterprise"
			detectionMethod = "query_param"
			logger.Debug().
				Str("source_param", source).
				Str("route", routeName).
				Str("path", r.URL.Path).
				Msg("Routing API request to enterprise handler via query param")
		} else if dcpHost != "" {
			selectedHandler = cloud
			routeName = "cloud"
			detectionMethod = "dcp_header"
			logger.Debug().
				Str("dcp_host", dcpHost).
				Str("route", routeName).
				Str("path", r.URL.Path).
				Msg("Routing API request to cloud handler via DCP header")
		} else if source == "cloud" || source == "ghec" {
			selectedHandler = cloud
			routeName = "cloud"
			detectionMethod = "query_param"
			logger.Debug().
				Str("source_param", source).
				Str("route", routeName).
				Str("path", r.URL.Path).
				Msg("Routing API request to cloud handler via query param")
		} else {
			// Default to cloud
			selectedHandler = cloud
			routeName = "cloud"
			detectionMethod = "default"
			logger.Debug().
				Str("route", routeName).
				Str("path", r.URL.Path).
				Msg("Routing API request to cloud handler (default)")
		}

		// Record routing decision
		routingDecisions.WithLabelValues(routeName, detectionMethod, "api").Inc()

		selectedHandler.ServeHTTP(w, r)
	})
}

// DetectSource determines if a request is from enterprise or cloud
func DetectSource(r *http.Request) string {
	// Check headers first
	if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
		return "enterprise"
	}
	if r.Header.Get(HeaderDCPDestinationHost) != "" {
		return "cloud"
	}

	// Check query parameters
	source := r.URL.Query().Get("source")
	if source == "enterprise" || source == "ghes" {
		return "enterprise"
	}
	if source == "cloud" || source == "ghec" {
		return "cloud"
	}

	// Default
	return "cloud"
}

// IsEnterpriseRequest checks if the request should be routed to enterprise
func IsEnterpriseRequest(r *http.Request) bool {
	return DetectSource(r) == "enterprise"
}

// ExtractGitHubHeaders extracts common GitHub webhook headers for logging
func ExtractGitHubHeaders(r *http.Request) map[string]string {
	return map[string]string{
		"delivery_id":     r.Header.Get(HeaderGitHubDelivery),
		"event_type":      r.Header.Get(HeaderGitHubEvent),
		"hook_id":         r.Header.Get(HeaderGitHubHookID),
		"enterprise_host": r.Header.Get(HeaderGitHubEnterpriseHost),
		"dcp_host":        r.Header.Get(HeaderDCPDestinationHost),
	}
}

// Chain combines multiple middleware into a single handler
func Chain(handler http.Handler, middleware ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

// WithLogging adds request logging to a handler
func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := zerolog.Ctx(r.Context())
		logger.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("remote_addr", r.RemoteAddr).
			Msg("Handling request")
		next.ServeHTTP(w, r)
	})
}
