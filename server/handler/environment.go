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
	"strings"

	"github.com/palantir/go-githubapp/githubapp"
)

// Environment represents GitHub deployment type
// Values match SQSConfig.IsEventEnabledForEnvironment expectations
type Environment string

const (
	// EnvironmentGHEC represents GitHub Enterprise Cloud (github.com)
	EnvironmentGHEC Environment = "cloud"

	// EnvironmentGHES represents GitHub Enterprise Server (self-hosted)
	EnvironmentGHES Environment = "enterprise"
)

// DetectEnvironment determines if a webhook request is from GHEC or GHES.
//
// Detection logic (in priority order):
//  1. Check Host header for github.com/githubapp.com → GHEC
//  2. Check X-GitHub-Enterprise-Host header (present in GHES) → GHES
//  3. Check API URLs from config → GHEC if api.github.com
//  4. Default to GHES for self-hosted installations
//
// This function is optimized for the hot path (every webhook) with minimal allocations.
// Uses string.Contains instead of regex for performance.
func DetectEnvironment(req *http.Request, config *githubapp.Config) Environment {
	// Layer 1: Check Host header (fastest, most reliable for webhooks)
	// GitHub webhooks set Host to the URL where the app is registered
	host := req.Host
	if strings.Contains(host, "github.com") || strings.Contains(host, "githubapp.com") {
		return EnvironmentGHEC
	}

	// Layer 2: Check for GHES-specific header (added by GitHub Enterprise)
	// This header is only present in GHES webhooks
	if req.Header.Get("X-GitHub-Enterprise-Host") != "" {
		return EnvironmentGHES
	}

	// Layer 3: Check API URLs from configuration
	// If configured to talk to api.github.com, it's GHEC
	if config != nil {
		if strings.Contains(config.V3APIURL, "api.github.com") ||
			strings.Contains(config.V4APIURL, "api.github.com") {
			return EnvironmentGHEC
		}
	}

	// Layer 4: Default to GHES for self-hosted installations
	// Conservative default - if we can't determine, assume enterprise
	return EnvironmentGHES
}

// String returns the string representation of the Environment
func (e Environment) String() string {
	return string(e)
}
