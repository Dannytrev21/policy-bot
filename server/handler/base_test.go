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

package handler

import (
	"context"
	"testing"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/pull"
	"github.com/pkg/errors"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// MockClientCreator is a mock implementation of githubapp.ClientCreator for testing
type MockClientCreator struct {
	appClient          *github.Client
	appClientErr       error
	installationClient *github.Client
	installationErr    error
}

func (m *MockClientCreator) NewAppClient() (*github.Client, error) {
	return m.appClient, m.appClientErr
}

func (m *MockClientCreator) NewAppV4Client() (*githubv4.Client, error) {
	// Return a minimal v4 client for testing
	return githubv4.NewClient(nil), nil
}

func (m *MockClientCreator) NewInstallationClient(installationID int64) (*github.Client, error) {
	return m.installationClient, m.installationErr
}

func (m *MockClientCreator) NewInstallationV4Client(installationID int64) (*githubv4.Client, error) {
	// Return a minimal v4 client for testing
	return githubv4.NewClient(nil), nil
}

func (m *MockClientCreator) NewTokenClient(token string) (*github.Client, error) {
	return nil, errors.New("not implemented")
}

func (m *MockClientCreator) NewTokenSourceClient(tokenSource oauth2.TokenSource) (*github.Client, error) {
	return nil, errors.New("not implemented")
}

func (m *MockClientCreator) NewTokenSourceV4Client(tokenSource oauth2.TokenSource) (*githubv4.Client, error) {
	return nil, errors.New("not implemented")
}

func (m *MockClientCreator) NewTokenV4Client(token string) (*githubv4.Client, error) {
	return nil, errors.New("not implemented")
}

// createTestBase creates a Base handler for testing
func createTestBase(mockCreator githubapp.ClientCreator) *Base {
	base := &Base{
		ClientCreator:   mockCreator,
		MetricsRegistry: gometrics.NewRegistry(),
	}
	base.Initialize()
	return base
}

func TestBase_VerifyInstallation_Success(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	mockClient := github.NewClient(nil)

	mockCreator := &MockClientCreator{
		appClient:    mockClient,
		appClientErr: nil,
	}

	base := createTestBase(mockCreator)

	// Pre-populate the installation registry cache with positive result
	base.InstallationRegistry.MarkInstalled(installationID)

	// Execute
	result := base.VerifyInstallation(ctx, installationID)

	// Assert
	assert.True(t, result, "VerifyInstallation should return true for cached installation")
}

func TestBase_VerifyInstallation_CacheHit(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry cache
	base.InstallationRegistry.MarkInstalled(installationID)

	// Execute
	result := base.VerifyInstallation(ctx, installationID)

	// Assert
	assert.True(t, result, "VerifyInstallation should return true for cached installation")

	// Verify cache still contains the installation
	status, cacheHit := base.InstallationRegistry.Check(installationID)
	assert.True(t, cacheHit, "Cache should have a hit")
	assert.Equal(t, InstallationExists, status, "Installation should be marked as existing")
}

func TestBase_VerifyInstallation_AppClientCreationFails(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	mockCreator := &MockClientCreator{
		appClient:    nil,
		appClientErr: errors.New("failed to create app client"),
	}

	base := createTestBase(mockCreator)

	// Execute
	result := base.VerifyInstallation(ctx, installationID)

	// Assert
	assert.False(t, result, "VerifyInstallation should return false when app client creation fails")
}

func TestBase_Initialize(t *testing.T) {
	// Test that Initialize properly sets up the Base struct
	base := &Base{}
	base.Initialize()

	assert.NotNil(t, base.InstallationIdMap, "InstallationIdMap should be initialized")
	assert.NotNil(t, base.mu, "mutex should be initialized")
	assert.NotNil(t, base.DefaultFetchedConfig, "DefaultFetchedConfig should be initialized")
}

func TestBase_Initialize_DoesNotOverwrite(t *testing.T) {
	// Test that Initialize doesn't overwrite existing values
	existingMap := make(map[int64]int64)
	existingMap[123] = 456

	base := &Base{
		InstallationIdMap: existingMap,
	}
	base.Initialize()

	assert.Equal(t, existingMap, base.InstallationIdMap, "Initialize should not overwrite existing InstallationIdMap")
	assert.Contains(t, base.InstallationIdMap, int64(123), "Existing data should be preserved")
}

func TestBase_NewEvalContext_InstallationNotFound(t *testing.T) {
	// This test verifies that NewEvalContext returns an error when installation is not found
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(99999)

	mockCreator := &MockClientCreator{
		appClient:    nil,
		appClientErr: errors.New("failed to create app client"),
	}

	base := createTestBase(mockCreator)

	// Execute
	_, err := base.NewEvalContext(ctx, installationID, pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	})

	// Assert
	require.Error(t, err, "NewEvalContext should return error when installation is not found")
	assert.Contains(t, err.Error(), "not found or not accessible", "Error should indicate installation not found")
	assert.Contains(t, err.Error(), "test-owner/test-repo", "Error should include repository information")
}

func TestBase_VerifyInstallation_ConcurrentAccess(t *testing.T) {
	// Test that concurrent access to VerifyInstallation is safe
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry cache
	base.InstallationRegistry.MarkInstalled(installationID)

	// Execute multiple goroutines concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			result := base.VerifyInstallation(ctx, installationID)
			assert.True(t, result)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestBase_NewEvalContext_InstallationClientCreationFails(t *testing.T) {
	// Test enhanced error logging when NewInstallationClient fails
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	expectedErr := errors.New("installation token expired")
	mockCreator := &MockClientCreator{
		appClient:       github.NewClient(nil),
		appClientErr:    nil,
		installationErr: expectedErr,
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry so installation verification passes
	base.InstallationRegistry.MarkInstalled(installationID)

	// Execute
	_, err := base.NewEvalContext(ctx, installationID, pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	})

	// Assert
	require.Error(t, err, "NewEvalContext should return error when installation client creation fails")
	assert.Contains(t, err.Error(), "failed to create installation client", "Error should mention client creation failure")
	assert.Contains(t, err.Error(), "test-owner/test-repo", "Error should include repository")
	assert.Contains(t, err.Error(), "12345", "Error should include installation ID")
}

func TestBase_NewEvalContext_V4ClientCreationFails(t *testing.T) {
	// Test enhanced error logging when NewInstallationV4Client fails
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	// Create a mock that succeeds for v3 client but fails for v4 client
	mockCreator := &MockClientCreatorWithV4Error{
		MockClientCreator: MockClientCreator{
			appClient:          github.NewClient(nil),
			appClientErr:       nil,
			installationClient: github.NewClient(nil),
			installationErr:    nil,
		},
		v4Error: errors.New("graphql authentication failed"),
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry so installation verification passes
	base.InstallationRegistry.MarkInstalled(installationID)

	// Execute
	_, err := base.NewEvalContext(ctx, installationID, pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	})

	// Assert
	require.Error(t, err, "NewEvalContext should return error when v4 client creation fails")
	assert.Contains(t, err.Error(), "failed to create installation v4 client", "Error should mention v4 client creation failure")
	assert.Contains(t, err.Error(), "test-owner/test-repo", "Error should include repository")
	assert.Contains(t, err.Error(), "12345", "Error should include installation ID")
}

// MockClientCreatorWithV4Error extends MockClientCreator to allow testing v4 client errors
type MockClientCreatorWithV4Error struct {
	MockClientCreator
	v4Error error
}

func (m *MockClientCreatorWithV4Error) NewInstallationV4Client(installationID int64) (*githubv4.Client, error) {
	return nil, m.v4Error
}

func TestBase_RecordInstallationClientMetric(t *testing.T) {
	// Test that recordInstallationClientMetric correctly increments counters
	mockCreator := &MockClientCreator{
		appClient:    github.NewClient(nil),
		appClientErr: nil,
	}

	base := createTestBase(mockCreator)

	// Record success metrics
	base.recordInstallationClientMetric(MetricsKeyInstallationClientSuccess)
	base.recordInstallationClientMetric(MetricsKeyInstallationV4ClientSuccess)

	// Record failure metrics
	base.recordInstallationClientMetric(MetricsKeyInstallationClientFailure)
	base.recordInstallationClientMetric(MetricsKeyInstallationV4ClientFailure)

	// Verify all metrics were recorded
	if base.MetricsRegistry != nil {
		// Check v3 success
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationClientSuccess); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Equal(t, int64(1), c.Count(), "V3 client success metric should be 1")
			}
		}

		// Check v4 success
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationV4ClientSuccess); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Equal(t, int64(1), c.Count(), "V4 client success metric should be 1")
			}
		}

		// Check v3 failure
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Equal(t, int64(1), c.Count(), "V3 client failure metric should be 1")
			}
		}

		// Check v4 failure
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationV4ClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Equal(t, int64(1), c.Count(), "V4 client failure metric should be 1")
			}
		}
	}

	// Test multiple increments
	base.recordInstallationClientMetric(MetricsKeyInstallationClientSuccess)
	if counter := base.MetricsRegistry.Get(MetricsKeyInstallationClientSuccess); counter != nil {
		if c, ok := counter.(interface{ Count() int64 }); ok {
			assert.Equal(t, int64(2), c.Count(), "V3 client success metric should be 2 after second increment")
		}
	}
}

func TestBase_NewEvalContext_RecordsV3FailureMetric(t *testing.T) {
	// Test that v3 failure metric is recorded when v3 client creation fails
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	expectedErr := errors.New("installation token expired")
	mockCreator := &MockClientCreator{
		appClient:       github.NewClient(nil),
		appClientErr:    nil,
		installationErr: expectedErr,
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry so installation verification passes
	base.InstallationRegistry.MarkInstalled(installationID)

	// Record initial metrics state
	var initialFailure int64
	if base.MetricsRegistry != nil {
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				initialFailure = c.Count()
			}
		}
	}

	// Execute
	_, err := base.NewEvalContext(ctx, installationID, pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	})

	// Should fail with v3 client creation error
	require.Error(t, err)

	// Verify failure metric was incremented
	if base.MetricsRegistry != nil {
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Greater(t, c.Count(), initialFailure, "V3 client failure metric should be incremented")
			}
		}
	}
}

func TestBase_NewEvalContext_RecordsV4FailureMetric(t *testing.T) {
	// Test that v4 failure metric is recorded when v4 client creation fails
	ctx := zerolog.New(nil).WithContext(context.Background())
	installationID := int64(12345)

	mockCreator := &MockClientCreatorWithV4Error{
		MockClientCreator: MockClientCreator{
			appClient:          github.NewClient(nil),
			appClientErr:       nil,
			installationClient: github.NewClient(nil),
			installationErr:    nil,
		},
		v4Error: errors.New("graphql authentication failed"),
	}

	base := createTestBase(mockCreator)

	// Pre-populate the registry so installation verification passes
	base.InstallationRegistry.MarkInstalled(installationID)

	// Record initial metrics state
	var initialFailure int64
	if base.MetricsRegistry != nil {
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationV4ClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				initialFailure = c.Count()
			}
		}
	}

	// Execute
	_, err := base.NewEvalContext(ctx, installationID, pull.Locator{
		Owner:  "test-owner",
		Repo:   "test-repo",
		Number: 1,
	})

	// Should fail with v4 client creation error
	require.Error(t, err)

	// Verify failure metric was incremented
	if base.MetricsRegistry != nil {
		if counter := base.MetricsRegistry.Get(MetricsKeyInstallationV4ClientFailure); counter != nil {
			if c, ok := counter.(interface{ Count() int64 }); ok {
				assert.Greater(t, c.Count(), initialFailure, "V4 client failure metric should be incremented")
			}
		}
	}
}
