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
	"net/http"
	"testing"

	"github.com/palantir/go-githubapp/githubapp"
	"github.com/stretchr/testify/assert"
)

func TestDetectEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		headers     map[string]string
		config      *githubapp.Config
		expected    Environment
		description string
	}{
		{
			name:        "github_com_host",
			host:        "github.com",
			headers:     map[string]string{},
			config:      nil,
			expected:    EnvironmentGHEC,
			description: "Should detect GHEC from github.com host",
		},
		{
			name:        "githubapp_com_host",
			host:        "example.githubapp.com",
			headers:     map[string]string{},
			config:      nil,
			expected:    EnvironmentGHEC,
			description: "Should detect GHEC from githubapp.com host",
		},
		{
			name:        "api_github_com_host",
			host:        "api.github.com",
			headers:     map[string]string{},
			config:      nil,
			expected:    EnvironmentGHEC,
			description: "Should detect GHEC from api.github.com host",
		},
		{
			name:        "ghes_enterprise_header",
			host:        "github.enterprise.com",
			headers:     map[string]string{"X-GitHub-Enterprise-Host": "github.enterprise.com"},
			config:      nil,
			expected:    EnvironmentGHES,
			description: "Should detect GHES from X-GitHub-Enterprise-Host header",
		},
		{
			name:     "ghec_from_v3_api_url",
			host:     "unknown.example.com",
			headers:  map[string]string{},
			config:   &githubapp.Config{V3APIURL: "https://api.github.com"},
			expected: EnvironmentGHEC,
			description: "Should detect GHEC from V3APIURL containing api.github.com",
		},
		{
			name:     "ghec_from_v4_api_url",
			host:     "unknown.example.com",
			headers:  map[string]string{},
			config:   &githubapp.Config{V4APIURL: "https://api.github.com/graphql"},
			expected: EnvironmentGHEC,
			description: "Should detect GHEC from V4APIURL containing api.github.com",
		},
		{
			name:     "ghes_from_api_urls",
			host:     "github.enterprise.com",
			headers:  map[string]string{},
			config:   &githubapp.Config{V3APIURL: "https://github.enterprise.com/api/v3", V4APIURL: "https://github.enterprise.com/api/graphql"},
			expected: EnvironmentGHES,
			description: "Should detect GHES from enterprise API URLs",
		},
		{
			name:        "default_to_ghes",
			host:        "unknown.example.com",
			headers:     map[string]string{},
			config:      nil,
			expected:    EnvironmentGHES,
			description: "Should default to GHES for unknown hosts",
		},
		{
			name:        "self_hosted_domain",
			host:        "github.mycompany.internal",
			headers:     map[string]string{},
			config:      nil,
			expected:    EnvironmentGHES,
			description: "Should detect GHES for self-hosted domain",
		},
		{
			name:     "ghes_with_custom_port",
			host:     "github.enterprise.com:443",
			headers:  map[string]string{},
			config:   &githubapp.Config{V3APIURL: "https://github.enterprise.com:443/api/v3"},
			expected: EnvironmentGHES,
			description: "Should detect GHES with custom port",
		},
		{
			name:        "priority_host_over_header",
			host:        "github.com",
			headers:     map[string]string{"X-GitHub-Enterprise-Host": "should-be-ignored"},
			config:      nil,
			expected:    EnvironmentGHEC,
			description: "Should prioritize Host header over enterprise header when Host is github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create request with specified host
			req, err := http.NewRequest("POST", "http://"+tt.host+"/api/github/hook", nil)
			assert.NoError(t, err)

			// Add custom headers
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			// Test environment detection
			result := DetectEnvironment(req, tt.config)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

func TestEnvironment_String(t *testing.T) {
	tests := []struct {
		env      Environment
		expected string
	}{
		{EnvironmentGHEC, "cloud"},
		{EnvironmentGHES, "enterprise"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.env.String())
		})
	}
}

// Benchmark environment detection to ensure minimal overhead
func BenchmarkDetectEnvironment(b *testing.B) {
	req, _ := http.NewRequest("POST", "http://github.com/api/github/hook", nil)
	config := &githubapp.Config{V3APIURL: "https://api.github.com"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DetectEnvironment(req, config)
	}
}

func BenchmarkDetectEnvironment_GHES(b *testing.B) {
	req, _ := http.NewRequest("POST", "http://github.enterprise.com/api/github/hook", nil)
	req.Header.Set("X-GitHub-Enterprise-Host", "github.enterprise.com")
	config := &githubapp.Config{V3APIURL: "https://github.enterprise.com/api/v3"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DetectEnvironment(req, config)
	}
}
