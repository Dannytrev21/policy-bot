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
	"errors"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "network error",
			err:      &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			expected: true,
		},
		{
			name:     "URL error",
			err:      &url.Error{Op: "Get", Err: errors.New("timeout")},
			expected: true,
		},
		{
			name:     "500 Internal Server Error",
			err:      errors.New("500 Internal Server Error"),
			expected: true,
		},
		{
			name:     "502 Bad Gateway",
			err:      errors.New("502 Bad Gateway"),
			expected: true,
		},
		{
			name:     "503 Service Unavailable",
			err:      errors.New("503 Service Unavailable"),
			expected: true,
		},
		{
			name:     "504 Gateway Timeout",
			err:      errors.New("504 Gateway Timeout"),
			expected: true,
		},
		{
			name:     "429 Too Many Requests",
			err:      errors.New("429 Too Many Requests"),
			expected: true,
		},
		{
			name:     "connection refused",
			err:      errors.New("dial tcp 127.0.0.1:443: connection refused"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("read: connection reset by peer"),
			expected: true,
		},
		{
			name:     "timeout error",
			err:      errors.New("request timeout"),
			expected: true,
		},
		{
			name:     "TLS handshake timeout",
			err:      errors.New("TLS handshake timeout"),
			expected: true,
		},
		{
			name:     "no such host",
			err:      errors.New("dial tcp: lookup api.github.com: no such host"),
			expected: true,
		},
		{
			name:     "404 Not Found",
			err:      errors.New("404 Not Found"),
			expected: false,
		},
		{
			name:     "401 Unauthorized",
			err:      errors.New("401 Unauthorized"),
			expected: false,
		},
		{
			name:     "403 Forbidden",
			err:      errors.New("403 Forbidden"),
			expected: false,
		},
		{
			name:     "422 Unprocessable Entity",
			err:      errors.New("422 Unprocessable Entity"),
			expected: false,
		},
		{
			name:     "installation not found",
			err:      errors.New("installation 12345 not found"),
			expected: false,
		},
		{
			name:     "bad request",
			err:      errors.New("bad request: invalid payload"),
			expected: false,
		},
		{
			name:     "unknown error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryableError(tt.err)
			assert.Equal(t, tt.expected, result, "IsRetryableError(%v) = %v, want %v", tt.err, result, tt.expected)
		})
	}
}

func TestIsInstallationNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "404 error",
			err:      errors.New("404 Not Found"),
			expected: true,
		},
		{
			name:     "not found error",
			err:      errors.New("resource not found"),
			expected: true,
		},
		{
			name:     "installation not found",
			err:      errors.New("installation 12345 not found"),
			expected: true,
		},
		{
			name:     "installation not accessible",
			err:      errors.New("installation 12345 not accessible"),
			expected: true,
		},
		{
			name:     "GitHub 404 response",
			err:      errors.New("GET https://api.github.com/app/installations/12345: 404 Not Found []"),
			expected: true,
		},
		{
			name:     "500 error",
			err:      errors.New("500 Internal Server Error"),
			expected: false,
		},
		{
			name:     "401 error",
			err:      errors.New("401 Unauthorized"),
			expected: false,
		},
		{
			name:     "random error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsInstallationNotFoundError(tt.err)
			assert.Equal(t, tt.expected, result, "IsInstallationNotFoundError(%v) = %v, want %v", tt.err, result, tt.expected)
		})
	}
}

func TestIsAuthenticationError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "401 Unauthorized",
			err:      errors.New("401 Unauthorized"),
			expected: true,
		},
		{
			name:     "403 Forbidden",
			err:      errors.New("403 Forbidden"),
			expected: true,
		},
		{
			name:     "unauthorized error",
			err:      errors.New("unauthorized: invalid token"),
			expected: true,
		},
		{
			name:     "forbidden error",
			err:      errors.New("forbidden: insufficient permissions"),
			expected: true,
		},
		{
			name:     "GitHub 401 response",
			err:      errors.New("GET https://api.github.com/app/installations/12345: 401 Bad credentials []"),
			expected: true,
		},
		{
			name:     "GitHub 403 response",
			err:      errors.New("GET https://api.github.com/app/installations/12345: 403 Resource not accessible by integration []"),
			expected: true,
		},
		{
			name:     "404 error",
			err:      errors.New("404 Not Found"),
			expected: false,
		},
		{
			name:     "500 error",
			err:      errors.New("500 Internal Server Error"),
			expected: false,
		},
		{
			name:     "random error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsAuthenticationError(tt.err)
			assert.Equal(t, tt.expected, result, "IsAuthenticationError(%v) = %v, want %v", tt.err, result, tt.expected)
		})
	}
}

// TestErrorClassificationConsistency ensures that the error classification functions
// are mutually exclusive for the important error types
func TestErrorClassificationConsistency(t *testing.T) {
	errors := []error{
		errors.New("404 Not Found"),
		errors.New("401 Unauthorized"),
		errors.New("403 Forbidden"),
		errors.New("500 Internal Server Error"),
		errors.New("connection refused"),
	}

	for _, err := range errors {
		retryable := IsRetryableError(err)
		notFound := IsInstallationNotFoundError(err)
		auth := IsAuthenticationError(err)

		// An error should not be both retryable and a permanent error (not found or auth)
		if retryable {
			assert.False(t, notFound, "Error %v should not be both retryable and not found", err)
			assert.False(t, auth, "Error %v should not be both retryable and auth", err)
		}

		// Not found and auth errors should be mutually exclusive
		if notFound {
			assert.False(t, auth, "Error %v should not be both not found and auth", err)
		}
	}
}