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
	"net/http/httptest"
	"testing"

	"github.com/palantir/go-githubapp/githubapp"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockSQSConfig implements the SQSConfig interface for testing
type MockSQSConfig struct {
	enabledEvents map[string]map[string]bool // eventType -> environment -> enabled
}

func (m *MockSQSConfig) IsEventEnabledForEnvironment(eventType, environment string) bool {
	if m.enabledEvents == nil {
		return true // Default to enabled if not configured
	}
	if envMap, ok := m.enabledEvents[eventType]; ok {
		return envMap[environment]
	}
	return true // Default to enabled if event not in map
}

func TestFilterWebhookEvents(t *testing.T) {
	tests := []struct {
		name               string
		eventType          string
		host               string
		headers            map[string]string
		enabledEvents      map[string]map[string]bool
		expectSkipped      bool
		expectStatusCode   int
		expectHandlerCalled bool
		description        string
	}{
		{
			name:      "ghec_status_disabled",
			eventType: "status",
			host:      "github.com",
			headers:   map[string]string{},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      false, // Disabled for GHEC
					"enterprise": true,
				},
			},
			expectSkipped:       true,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: false,
			description:         "Should skip status webhook from GHEC when disabled",
		},
		{
			name:      "ghec_status_enabled",
			eventType: "status",
			host:      "github.com",
			headers:   map[string]string{},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      true, // Enabled for GHEC
					"enterprise": true,
				},
			},
			expectSkipped:       false,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: true,
			description:         "Should pass status webhook from GHEC when enabled",
		},
		{
			name:      "ghes_status_enabled",
			eventType: "status",
			host:      "github.enterprise.com",
			headers: map[string]string{
				"X-GitHub-Enterprise-Host": "github.enterprise.com",
			},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      false, // Disabled for GHEC
					"enterprise": true,  // Enabled for GHES
				},
			},
			expectSkipped:       false,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: true,
			description:         "Should pass status webhook from GHES when enabled",
		},
		{
			name:      "ghes_status_disabled",
			eventType: "status",
			host:      "github.enterprise.com",
			headers: map[string]string{
				"X-GitHub-Enterprise-Host": "github.enterprise.com",
			},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      true,
					"enterprise": false, // Disabled for GHES
				},
			},
			expectSkipped:       true,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: false,
			description:         "Should skip status webhook from GHES when disabled",
		},
		{
			name:      "pull_request_always_enabled",
			eventType: "pull_request",
			host:      "github.com",
			headers:   map[string]string{},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      false, // Status disabled
					"enterprise": true,
				},
				"pull_request": {
					"cloud":      true, // Pull request enabled
					"enterprise": true,
				},
			},
			expectSkipped:       false,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: true,
			description:         "Should pass pull_request webhook when enabled",
		},
		{
			name:      "unknown_event_defaults_to_enabled",
			eventType: "unknown_event",
			host:      "github.com",
			headers:   map[string]string{},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      false,
					"enterprise": true,
				},
			},
			expectSkipped:       false,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: true,
			description:         "Should pass unknown event types (defaults to enabled)",
		},
		{
			name:      "no_event_header_passes_through",
			eventType: "", // Empty event type
			host:      "github.com",
			headers:   map[string]string{},
			enabledEvents: map[string]map[string]bool{
				"status": {
					"cloud":      false,
					"enterprise": true,
				},
			},
			expectSkipped:       false,
			expectStatusCode:    http.StatusOK,
			expectHandlerCalled: true,
			description:         "Should pass through when no event header present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Track whether the handler was called
			handlerCalled := false
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			// Create mock SQS config
			mockSQS := &MockSQSConfig{
				enabledEvents: tt.enabledEvents,
			}

			// Create mock GitHub config
			githubConfig := &githubapp.Config{
				V3APIURL: "https://api.github.com",
			}

			// Create metrics registry for testing
			metricsRegistry := gometrics.NewRegistry()

			// Create filter config
			filterConfig := EventFilterConfig{
				SQSConfig:       mockSQS,
				GithubConfig:    githubConfig,
				MetricsRegistry: metricsRegistry,
				Logger:          zerolog.Nop(),
			}

			// Apply middleware
			handler := FilterWebhookEvents(filterConfig)(nextHandler)

			// Create test request
			req := httptest.NewRequest("POST", "http://"+tt.host+"/api/github/hook", nil)
			if tt.eventType != "" {
				req.Header.Set("X-GitHub-Event", tt.eventType)
			}
			req.Header.Set("X-GitHub-Delivery", "test-delivery-123")

			// Add custom headers
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			// Create response recorder
			rr := httptest.NewRecorder()

			// Execute handler
			handler.ServeHTTP(rr, req)

			// Verify status code
			assert.Equal(t, tt.expectStatusCode, rr.Code, tt.description)

			// Verify handler was called/not called as expected
			assert.Equal(t, tt.expectHandlerCalled, handlerCalled, tt.description)

			// Verify metrics were recorded
			if tt.expectSkipped {
				skippedCounter := metricsRegistry.Get(MetricsKeyWebhookEventsSkipped)
				require.NotNil(t, skippedCounter, "Skipped counter should exist")
				assert.Equal(t, int64(1), skippedCounter.(gometrics.Counter).Count(), "Skipped counter should be incremented")
			} else if tt.eventType != "" {
				passedCounter := metricsRegistry.Get(MetricsKeyWebhookEventsPassed)
				require.NotNil(t, passedCounter, "Passed counter should exist")
				assert.Equal(t, int64(1), passedCounter.(gometrics.Counter).Count(), "Passed counter should be incremented")
			}
		})
	}
}

func TestFilterWebhookEvents_NilConfig(t *testing.T) {
	// Handler should pass through when SQS config is nil
	handlerCalled := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	filterConfig := EventFilterConfig{
		SQSConfig:    nil, // Nil config
		GithubConfig: &githubapp.Config{V3APIURL: "https://api.github.com"},
		Logger:       zerolog.Nop(),
	}

	handler := FilterWebhookEvents(filterConfig)(nextHandler)

	req := httptest.NewRequest("POST", "http://github.com/api/github/hook", nil)
	req.Header.Set("X-GitHub-Event", "status")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, handlerCalled, "Handler should be called when SQS config is nil")
}

func TestFilterWebhookEvents_Metrics(t *testing.T) {
	mockSQS := &MockSQSConfig{
		enabledEvents: map[string]map[string]bool{
			"status": {
				"cloud": false,
			},
		},
	}

	metricsRegistry := gometrics.NewRegistry()

	filterConfig := EventFilterConfig{
		SQSConfig:       mockSQS,
		GithubConfig:    &githubapp.Config{V3APIURL: "https://api.github.com"},
		MetricsRegistry: metricsRegistry,
		Logger:          zerolog.Nop(),
	}

	handler := FilterWebhookEvents(filterConfig)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Send 3 status webhooks (should be skipped)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "http://github.com/api/github/hook", nil)
		req.Header.Set("X-GitHub-Event", "status")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Verify global counter
	skippedCounter := metricsRegistry.Get(MetricsKeyWebhookEventsSkipped)
	require.NotNil(t, skippedCounter)
	assert.Equal(t, int64(3), skippedCounter.(gometrics.Counter).Count())

	// Verify per-event counter
	perEventCounter := metricsRegistry.Get(MetricsKeyWebhookEventsSkipped + ".status.cloud")
	require.NotNil(t, perEventCounter)
	assert.Equal(t, int64(3), perEventCounter.(gometrics.Counter).Count())
}

// Benchmark the middleware to ensure minimal overhead
func BenchmarkFilterWebhookEvents_Pass(b *testing.B) {
	mockSQS := &MockSQSConfig{
		enabledEvents: map[string]map[string]bool{
			"pull_request": {
				"cloud": true,
			},
		},
	}

	filterConfig := EventFilterConfig{
		SQSConfig:    mockSQS,
		GithubConfig: &githubapp.Config{V3APIURL: "https://api.github.com"},
		Logger:       zerolog.Nop(),
	}

	handler := FilterWebhookEvents(filterConfig)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "http://github.com/api/github/hook", nil)
	req.Header.Set("X-GitHub-Event", "pull_request")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}
}

func BenchmarkFilterWebhookEvents_Skip(b *testing.B) {
	mockSQS := &MockSQSConfig{
		enabledEvents: map[string]map[string]bool{
			"status": {
				"cloud": false,
			},
		},
	}

	filterConfig := EventFilterConfig{
		SQSConfig:    mockSQS,
		GithubConfig: &githubapp.Config{V3APIURL: "https://api.github.com"},
		Logger:       zerolog.Nop(),
	}

	handler := FilterWebhookEvents(filterConfig)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "http://github.com/api/github/hook", nil)
	req.Header.Set("X-GitHub-Event", "status")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}
}
