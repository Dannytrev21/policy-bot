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

package middleware

import (
	"net/http"

	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server/handler"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

// Metrics for tracking filtered events
const (
	MetricsKeyWebhookEventsSkipped = "github.webhook.events.skipped"
	MetricsKeyWebhookEventsPassed  = "github.webhook.events.passed"
)

// EventFilterConfig holds configuration for webhook event filtering
type EventFilterConfig struct {
	// SQS config used to check IsEventEnabledForEnvironment
	SQSConfig interface {
		IsEventEnabledForEnvironment(eventType, environment string) bool
	}

	// GitHub config for environment detection
	GithubConfig *githubapp.Config

	// Metrics registry for recording filtered events
	MetricsRegistry gometrics.Registry

	// Logger for filtered events
	Logger zerolog.Logger
}

// FilterWebhookEvents is middleware that filters webhook events based on environment configuration.
// It uses the existing SQSConfig.IsEventEnabledForEnvironment logic to determine if a webhook
// should be processed or skipped.
//
// Architecture:
//   [Webhook] → [FilterWebhookEvents] → [Check IsEventEnabledForEnvironment]
//                                     ↓
//                              [Disabled for env?]
//                                     ↓
//                  YES → Skip (200 OK)  |  NO → Continue to dispatcher
//
// This middleware enables selective webhook filtering for GHEC while maintaining
// SQS event processing (SQS path bypasses this middleware entirely).
func FilterWebhookEvents(config EventFilterConfig) func(http.Handler) http.Handler {
	// Register metrics if registry is provided
	if config.MetricsRegistry != nil {
		gometrics.GetOrRegisterCounter(MetricsKeyWebhookEventsSkipped, config.MetricsRegistry)
		gometrics.GetOrRegisterCounter(MetricsKeyWebhookEventsPassed, config.MetricsRegistry)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			logger := zerolog.Ctx(ctx)

			// Extract event type from header
			eventType := r.Header.Get(HeaderGitHubEvent)
			if eventType == "" {
				// No event type header - pass through (will be handled by dispatcher)
				next.ServeHTTP(w, r)
				return
			}

			// Detect environment (GHEC vs GHES)
			environment := handler.DetectEnvironment(r, config.GithubConfig)

			// Check if this event is enabled for this environment
			if config.SQSConfig != nil && !config.SQSConfig.IsEventEnabledForEnvironment(eventType, environment.String()) {
				// Event is disabled for this environment - skip processing
				logger.Info().
					Str("event_type", eventType).
					Str("environment", environment.String()).
					Str("delivery_id", r.Header.Get(HeaderGitHubDelivery)).
					Msg("Webhook event skipped - disabled for environment")

				// Record metric
				recordSkippedWebhookEvent(config.MetricsRegistry, eventType, environment.String())

				// Return 200 OK to prevent GitHub retries
				// GitHub expects a 2xx response to acknowledge receipt
				w.WriteHeader(http.StatusOK)
				return
			}

			// Event is enabled - pass through to next handler
			recordPassedWebhookEvent(config.MetricsRegistry, eventType, environment.String())
			next.ServeHTTP(w, r)
		})
	}
}

// recordSkippedWebhookEvent records metrics for a skipped webhook event
func recordSkippedWebhookEvent(registry gometrics.Registry, eventType, environment string) {
	if registry == nil {
		return
	}

	// Increment global skipped counter
	if counter := registry.Get(MetricsKeyWebhookEventsSkipped); counter != nil {
		if c, ok := counter.(gometrics.Counter); ok {
			c.Inc(1)
		}
	}

	// Increment per-event-type counter for better granularity
	perEventKey := MetricsKeyWebhookEventsSkipped + "." + eventType + "." + environment
	if counter := gometrics.GetOrRegisterCounter(perEventKey, registry); counter != nil {
		counter.Inc(1)
	}
}

// recordPassedWebhookEvent records metrics for a passed webhook event
func recordPassedWebhookEvent(registry gometrics.Registry, eventType, environment string) {
	if registry == nil {
		return
	}

	// Increment global passed counter
	if counter := registry.Get(MetricsKeyWebhookEventsPassed); counter != nil {
		if c, ok := counter.(gometrics.Counter); ok {
			c.Inc(1)
		}
	}

	// Increment per-event-type counter for better granularity
	perEventKey := MetricsKeyWebhookEventsPassed + "." + eventType + "." + environment
	if counter := gometrics.GetOrRegisterCounter(perEventKey, registry); counter != nil {
		counter.Inc(1)
	}
}
