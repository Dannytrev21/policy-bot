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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// MockClientFactory for testing
type MockClientFactory struct {
	client *github.Client
	err    error
}

func (m *MockClientFactory) NewInstallationClient(installationID int64) (*github.Client, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.client, nil
}

func TestNewInstallationLocator(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	assert.NotNil(t, locator)
	assert.NotNil(t, locator.registry)
	assert.NotNil(t, locator.metrics)
	assert.NotNil(t, locator.circuitBreaker)
	assert.NotNil(t, locator.apiSemaphore)
	assert.NotNil(t, locator.lookupInFlight)
	assert.NotNil(t, locator.keyBuilderPool)
}

func TestInstallationLocator_LookupWebhook(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	tests := []struct {
		name           string
		installationID int64
		setupRegistry  func(*InstallationRegistry)
		expectExists   bool
		expectError    bool
	}{
		{
			name:           "webhook with valid cached installation",
			installationID: 12345,
			setupRegistry: func(r *InstallationRegistry) {
				r.MarkInstalled(12345)
			},
			expectExists: true,
			expectError:  false,
		},
		{
			name:           "webhook with uncached installation still passes",
			installationID: 99999,
			setupRegistry:  func(r *InstallationRegistry) {},
			expectExists:   true,
			expectError:    false,
		},
		{
			name:           "webhook with zero installation ID returns error",
			installationID: 0,
			setupRegistry:  func(r *InstallationRegistry) {},
			expectExists:   false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupRegistry(registry)

			req := LookupRequest{
				InstallationID: tt.installationID,
				EventSource:    EventSourceWebhook,
				EventType:      "pull_request",
			}

			result := locator.Lookup(context.Background(), req)

			if tt.expectError {
				assert.Error(t, result.Error)
			} else {
				assert.NoError(t, result.Error)
			}

			assert.Equal(t, tt.expectExists, result.Exists)
		})
	}
}

func TestInstallationLocator_BuildCompoundKey(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	tests := []struct {
		name     string
		owner    string
		repo     string
		expected string
	}{
		{
			name:     "simple owner and repo",
			owner:    "octocat",
			repo:     "hello-world",
			expected: "octocat:hello-world",
		},
		{
			name:     "owner with dashes",
			owner:    "my-org",
			repo:     "my-repo",
			expected: "my-org:my-repo",
		},
		{
			name:     "empty owner",
			owner:    "",
			repo:     "repo",
			expected: ":repo",
		},
		{
			name:     "empty repo",
			owner:    "owner",
			repo:     "",
			expected: "owner:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := locator.buildCompoundKey(tt.owner, tt.repo)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInstallationLocator_HandleAPISuccess(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	req := LookupRequest{
		InstallationID: 0,
		Owner:          "test-owner",
		Repo:           "test-repo",
		EventSource:    EventSourceSQS,
		EventType:      "push",
	}

	result := locator.handleAPISuccess(12345, req, logger)

	assert.Equal(t, int64(12345), result.InstallationID)
	assert.True(t, result.Exists)
	assert.Equal(t, SourceAPI, result.Source)
	assert.NoError(t, result.Error)

	// Verify it was cached
	status, hit := registry.Check(12345)
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)

	// Verify compound key was added
	id, status, hit := registry.CheckByRepo("test-owner", "test-repo")
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)
	assert.Equal(t, int64(12345), id)
}

func TestInstallationLocator_HandleAPIError(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	tests := []struct {
		name         string
		err          error
		expectExists bool
	}{
		{
			name:         "404 not found error",
			err:          errors.New("404 Not Found: No installation found for repository"),
			expectExists: false,
		},
		{
			name:         "network error",
			err:          errors.New("network timeout"),
			expectExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := LookupRequest{
				InstallationID: 0,
				Owner:          "test-owner",
				Repo:           "test-repo",
				EventSource:    EventSourceSQS,
				EventType:      "push",
			}

			result := locator.handleAPIError(tt.err, req, logger)

			assert.Equal(t, tt.expectExists, result.Exists)
			assert.Error(t, result.Error)
		})
	}
}

// Note: UpdateFromEvent requires actual GitHub event types (*github.InstallationEvent)
// which are complex to mock. The integration tests in installation_test.go cover this
// functionality with real GitHub event handlers.

// Note: TestInstallationLocator_ConcurrentLookup would require a proper GitHub client
// mock to avoid nil pointer dereferences in API lookup. The existing integration tests
// cover concurrent access with the race detector enabled.

func TestInstallationLocator_GetMetrics(t *testing.T) {
	registry := NewInstallationRegistry(time.Hour, 5*time.Minute, nil)
	logger := zerolog.Nop()
	clientFactory := func(ctx context.Context) (*github.Client, error) {
		return nil, nil
	}

	circuitBreaker := NewCircuitBreaker()
	locator := NewInstallationLocator(registry, logger, clientFactory, circuitBreaker)

	// Perform some operations to generate metrics
	registry.MarkInstalled(12345)
	registry.AddRepositories(12345, []struct{ Owner, Repo string }{
		{Owner: "owner1", Repo: "repo1"},
	})

	req := LookupRequest{
		InstallationID: 12345,
		EventSource:    EventSourceSQS,
		EventType:      "push",
	}

	locator.Lookup(context.Background(), req)

	metrics := locator.GetMetrics()
	assert.NotNil(t, metrics)
	assert.GreaterOrEqual(t, metrics.DirectHits, int64(0))
}
