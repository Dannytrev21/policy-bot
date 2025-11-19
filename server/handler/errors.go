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
	"net"
	"net/url"
	"strings"

	"github.com/google/go-github/v47/github"
	"github.com/pkg/errors"
)

// classifyGitHubError inspects common GitHub client errors and returns:
//   - status code (0 if not an HTTP error)
//   - isRateLimit: true when the error is a rate limit error (should not trigger auth refresh)
//   - isAuthRelated: true when the error suggests token/installation problems (401/403/404/410/422)
//
// This keeps auth/installation handling reactive to actual failures and avoids unnecessary token creation.
func classifyGitHubError(err error) (status int, isRateLimit bool, isAuthRelated bool) {
	if err == nil {
		return 0, false, false
	}

	var rlErr *github.RateLimitError
	if errors.As(err, &rlErr) && rlErr.Response != nil {
		return rlErr.Response.StatusCode, true, false
	}

	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		status = er.Response.StatusCode
		switch status {
		case 401, 403, 404, 410, 422:
			return status, false, true
		default:
			return status, false, false
		}
	}

	return 0, false, false
}

// IsRetryableError determines if an error is transient and should be retried.
// It returns true for network errors, 5xx server errors, and rate limiting errors.
// It returns false for 404 (not found), 401/403 (authentication), and other client errors.
//
// This function is used by both the InstallationManager for GitHub API retries
// and the SQS processor for determining whether to retry failed messages.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()

	// Network-related errors are retryable
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// URL errors (connection issues) are retryable
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	// Check for common retryable error patterns
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"TLS handshake timeout",
		"no such host",
		"temporary failure",
		"500", // Internal Server Error
		"502", // Bad Gateway
		"503", // Service Unavailable
		"504", // Gateway Timeout
		"429", // Too Many Requests (rate limit)
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			return true
		}
	}

	// Check for non-retryable error patterns
	// These are permanent failures that won't be fixed by retrying
	nonRetryablePatterns := []string{
		"404",       // Not Found - installation doesn't exist
		"401",       // Unauthorized - bad credentials
		"403",       // Forbidden - insufficient permissions
		"422",       // Unprocessable Entity - bad request
		"not found", // Generic not found
		"unauthorized",
		"forbidden",
		"bad request",
	}

	for _, pattern := range nonRetryablePatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			return false
		}
	}

	// Default to not retrying unknown errors
	return false
}

// IsInstallationNotFoundError checks if an error indicates that a GitHub App
// installation was not found (404 error).
//
// This is used to identify cases where the app is not installed on a repository,
// which should not trigger retries as the condition won't change.
func IsInstallationNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "404") ||
		strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "installation") && strings.Contains(errMsg, "not accessible")
}

// IsAuthenticationError checks if an error is related to authentication or authorization
// (401 or 403 errors).
//
// These errors indicate configuration problems that won't be fixed by retrying.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "401") ||
		strings.Contains(errMsg, "403") ||
		strings.Contains(errMsg, "unauthorized") ||
		strings.Contains(errMsg, "forbidden")
}

// AuthRefreshError is returned when the auth refresh helper fails to recover
// a GitHub client and needs the caller to take further action.
// Permanent indicates whether the failure is non-retryable (installation deleted).
type AuthRefreshError struct {
	Permanent bool
	Err       error
}

func (e *AuthRefreshError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *AuthRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
