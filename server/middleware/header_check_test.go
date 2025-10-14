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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectSource(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		query    string
		expected string
	}{
		{
			name:     "enterprise_header",
			headers:  map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
			expected: "enterprise",
		},
		{
			name:     "dcp_header",
			headers:  map[string]string{HeaderDCPDestinationHost: "github.com"},
			expected: "cloud",
		},
		{
			name:     "no_headers_default",
			headers:  map[string]string{},
			expected: "cloud",
		},
		{
			name:     "query_param_enterprise",
			query:    "?source=enterprise",
			expected: "enterprise",
		},
		{
			name:     "query_param_ghes",
			query:    "?source=ghes",
			expected: "enterprise",
		},
		{
			name:     "query_param_cloud",
			query:    "?source=cloud",
			expected: "cloud",
		},
		{
			name:     "query_param_ghec",
			query:    "?source=ghec",
			expected: "cloud",
		},
		{
			name:     "header_priority_over_query",
			headers:  map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
			query:    "?source=cloud",
			expected: "enterprise",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test"+tt.query, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := DetectSource(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsEnterpriseRequest(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected bool
	}{
		{
			name:     "enterprise_request",
			headers:  map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
			expected: true,
		},
		{
			name:     "cloud_request",
			headers:  map[string]string{HeaderDCPDestinationHost: "github.com"},
			expected: false,
		},
		{
			name:     "default_cloud",
			headers:  map[string]string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := IsEnterpriseRequest(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractGitHubHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/webhook", nil)
	req.Header.Set(HeaderGitHubDelivery, "12345-67890")
	req.Header.Set(HeaderGitHubEvent, "pull_request")
	req.Header.Set(HeaderGitHubHookID, "123456")
	req.Header.Set(HeaderGitHubEnterpriseHost, "ghes.example.com")
	req.Header.Set(HeaderDCPDestinationHost, "github.com")

	headers := ExtractGitHubHeaders(req)

	assert.Equal(t, "12345-67890", headers["delivery_id"])
	assert.Equal(t, "pull_request", headers["event_type"])
	assert.Equal(t, "123456", headers["hook_id"])
	assert.Equal(t, "ghes.example.com", headers["enterprise_host"])
	assert.Equal(t, "github.com", headers["dcp_host"])
}

func TestSelectWebhookDispatcher(t *testing.T) {
	// Create mock handlers that track which was called
	enterpriseCalled := false
	cloudCalled := false

	enterpriseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterpriseCalled = true
		w.WriteHeader(http.StatusOK)
	})

	cloudHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusOK)
	})

	dispatcher := SelectWebhookDispatcher(enterpriseHandler, cloudHandler)

	tests := []struct {
		name             string
		headers          map[string]string
		expectEnterprise bool
		expectCloud      bool
	}{
		{
			name:             "routes_to_enterprise",
			headers:          map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
			expectEnterprise: true,
			expectCloud:      false,
		},
		{
			name:             "routes_to_cloud_dcp",
			headers:          map[string]string{HeaderDCPDestinationHost: "github.com"},
			expectEnterprise: false,
			expectCloud:      true,
		},
		{
			name:             "routes_to_cloud_default",
			headers:          map[string]string{},
			expectEnterprise: false,
			expectCloud:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset flags
			enterpriseCalled = false
			cloudCalled = false

			req := httptest.NewRequest("POST", "/api/github/hook", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			dispatcher.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectEnterprise, enterpriseCalled, "enterprise handler called state")
			assert.Equal(t, tt.expectCloud, cloudCalled, "cloud handler called state")
			assert.Equal(t, http.StatusOK, rr.Code)
		})
	}
}

func TestSelectAPIHandler(t *testing.T) {
	// Create mock handlers that track which was called
	enterpriseCalled := false
	cloudCalled := false

	enterpriseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterpriseCalled = true
		w.WriteHeader(http.StatusOK)
	})

	cloudHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusOK)
	})

	apiHandler := SelectAPIHandler(enterpriseHandler, cloudHandler)

	tests := []struct {
		name             string
		headers          map[string]string
		query            string
		expectEnterprise bool
		expectCloud      bool
	}{
		{
			name:             "routes_to_enterprise_via_header",
			headers:          map[string]string{HeaderGitHubEnterpriseHost: "ghes.example.com"},
			expectEnterprise: true,
			expectCloud:      false,
		},
		{
			name:             "routes_to_enterprise_via_query_enterprise",
			query:            "?source=enterprise",
			expectEnterprise: true,
			expectCloud:      false,
		},
		{
			name:             "routes_to_enterprise_via_query_ghes",
			query:            "?source=ghes",
			expectEnterprise: true,
			expectCloud:      false,
		},
		{
			name:             "routes_to_cloud_via_header",
			headers:          map[string]string{HeaderDCPDestinationHost: "github.com"},
			expectEnterprise: false,
			expectCloud:      true,
		},
		{
			name:             "routes_to_cloud_via_query_cloud",
			query:            "?source=cloud",
			expectEnterprise: false,
			expectCloud:      true,
		},
		{
			name:             "routes_to_cloud_via_query_ghec",
			query:            "?source=ghec",
			expectEnterprise: false,
			expectCloud:      true,
		},
		{
			name:             "routes_to_cloud_default",
			expectEnterprise: false,
			expectCloud:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset flags
			enterpriseCalled = false
			cloudCalled = false

			req := httptest.NewRequest("POST", "/api/validate"+tt.query, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rr := httptest.NewRecorder()
			apiHandler.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectEnterprise, enterpriseCalled, "enterprise handler called state")
			assert.Equal(t, tt.expectCloud, cloudCalled, "cloud handler called state")
			assert.Equal(t, http.StatusOK, rr.Code)
		})
	}
}

func TestChain(t *testing.T) {
	// Create a simple handler
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("final")); err != nil {
			t.Fatalf("failed to write response: %v", err)
		}
	})

	// Create middleware that adds headers
	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware-1", "true")
			next.ServeHTTP(w, r)
		})
	}

	middleware2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Middleware-2", "true")
			next.ServeHTTP(w, r)
		})
	}

	// Chain the middleware
	chained := Chain(finalHandler, middleware1, middleware2)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	chained.ServeHTTP(rr, req)

	// Check that both middleware ran
	assert.Equal(t, "true", rr.Header().Get("X-Middleware-1"))
	assert.Equal(t, "true", rr.Header().Get("X-Middleware-2"))
	assert.Equal(t, "final", rr.Body.String())
}
