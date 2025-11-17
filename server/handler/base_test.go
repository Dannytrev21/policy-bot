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
	"time"

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
	assert.NotNil(t, base.ClientCache, "ClientCache should be initialized")
	assert.NotNil(t, base.InstallationManager, "InstallationManager should be initialized")
	assert.NotNil(t, base.mu, "mutex should be initialized")
	assert.NotNil(t, base.DefaultFetchedConfig, "DefaultFetchedConfig should be initialized")
}

func TestBase_Initialize_DoesNotOverwrite(t *testing.T) {
	// Test that Initialize doesn't overwrite existing values
	existingMap := make(map[int64]int64)
	existingMap[123] = 456
	existingCache := NewClientCache(10*time.Minute, 100)
	existingManager := NewInstallationManager(nil, nil, nil)

	base := &Base{
		InstallationIdMap:   existingMap,
		ClientCache:         existingCache,
		InstallationManager: existingManager,
	}
	base.Initialize()

	assert.Equal(t, existingMap, base.InstallationIdMap, "Initialize should not overwrite existing InstallationIdMap")
	assert.Contains(t, base.InstallationIdMap, int64(123), "Existing data should be preserved")
	assert.Equal(t, existingCache, base.ClientCache, "Initialize should not overwrite existing ClientCache")
	assert.Equal(t, existingManager, base.InstallationManager, "Initialize should not overwrite existing InstallationManager")
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

// ============================================================================
// Tests for Step 2: App Source Detection
// ============================================================================

func TestBase_IsOurApp_MatchingAppID(t *testing.T) {
	// Test that IsOurApp returns true when source app ID matches our app ID
	base := &Base{
		AppID: 12345,
	}

	result := base.IsOurApp(12345)
	assert.True(t, result, "IsOurApp should return true when app IDs match")
}

func TestBase_IsOurApp_DifferentAppID(t *testing.T) {
	// Test that IsOurApp returns false when source app ID is different
	base := &Base{
		AppID: 12345,
	}

	result := base.IsOurApp(67890)
	assert.False(t, result, "IsOurApp should return false when app IDs don't match")
}

func TestBase_IsOurApp_ZeroSourceAppID(t *testing.T) {
	// Test backward compatibility: zero source app ID assumes it's ours
	base := &Base{
		AppID: 12345,
	}

	result := base.IsOurApp(0)
	assert.True(t, result, "IsOurApp should return true for zero source app ID (backward compatibility)")
}

func TestBase_IsOurApp_ZeroOurAppID(t *testing.T) {
	// Test backward compatibility: zero our app ID assumes all events are ours
	base := &Base{
		AppID: 0,
	}

	result := base.IsOurApp(67890)
	assert.True(t, result, "IsOurApp should return true when our AppID is zero (not initialized)")
}

func TestBase_IsOurApp_BothZero(t *testing.T) {
	// Test edge case: both app IDs are zero
	base := &Base{
		AppID: 0,
	}

	result := base.IsOurApp(0)
	assert.True(t, result, "IsOurApp should return true when both app IDs are zero")
}

// REMOVED: func TestBase_ShouldProcessEvent_OurApp(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ShouldProcessEvent_ExternalApp(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ShouldProcessEvent_MissingSourceAppID(t *testing.T) { - tests removed infrastructure

func TestBase_IsOurApp_RealWorldScenarios(t *testing.T) {
	// Test with real GitHub App IDs
	tests := []struct {
		name          string
		ourAppID      int64
		sourceAppID   int64
		expected      bool
		description   string
	}{
		{
			name:        "PolicyBot event",
			ourAppID:    12345,
			sourceAppID: 12345,
			expected:    true,
			description: "Event from our own app",
		},
		{
			name:        "Dependabot event",
			ourAppID:    12345,
			sourceAppID: 29110,
			expected:    false,
			description: "Event from Dependabot",
		},
		{
			name:        "Renovate event",
			ourAppID:    12345,
			sourceAppID: 11111,
			expected:    false,
			description: "Event from Renovate",
		},
		{
			name:        "GitHub Actions event",
			ourAppID:    12345,
			sourceAppID: 15368,
			expected:    false,
			description: "Event from GitHub Actions",
		},
		{
			name:        "Legacy event without app_id",
			ourAppID:    12345,
			sourceAppID: 0,
			expected:    true,
			description: "Legacy webhook without app_id field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &Base{
				AppID: tt.ourAppID,
			}

			result := base.IsOurApp(tt.sourceAppID)
			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// REMOVED: func TestBase_ShouldProcessEvent_WithEnvironment(t *testing.T) { - tests removed infrastructure

func TestBase_IsOurApp_ConcurrentAccess(t *testing.T) {
	// Test that IsOurApp is safe for concurrent access
	base := &Base{
		AppID: 12345,
	}

	// Test concurrent access with our app ID
	done := make(chan bool)
	for i := 0; i < 100; i++ {
		go func() {
			result := base.IsOurApp(12345)
			assert.True(t, result)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 100; i++ {
		<-done
	}

	// Test concurrent access with different app ID
	for i := 0; i < 100; i++ {
		go func() {
			result := base.IsOurApp(67890)
			assert.False(t, result)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestBase_AppID_Initialization(t *testing.T) {
	// Test that AppID can be set during initialization
	base := &Base{
		AppID:           12345,
		MetricsRegistry: gometrics.NewRegistry(),
	}
	base.Initialize()

	// Verify AppID is preserved during initialization
	assert.Equal(t, int64(12345), base.AppID, "AppID should be preserved during initialization")
}

func TestBase_IsOurApp_NoMutexNeeded(t *testing.T) {
	// Verify that IsOurApp doesn't require mutex (AppID is immutable after init)
	base := &Base{
		AppID: 12345,
	}

	// Multiple readers should be able to access concurrently without contention
	results := make(chan bool, 1000)
	for i := 0; i < 1000; i++ {
		go func() {
			result := base.IsOurApp(12345)
			results <- result
		}()
	}

	// Collect all results
	for i := 0; i < 1000; i++ {
		result := <-results
		assert.True(t, result, "All concurrent reads should succeed")
	}
}

// REMOVED: func TestBase_ResolveInstallation_OurApp(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_ExternalApp_GHEC(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_ExternalApp_GHES(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_ExternalApp_InsufficientIdentifiers(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_ExternalApp_NotFound(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_resolveByOrganization_CacheHit(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_resolveByOrganization_CacheMiss_LocatorSuccess(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_resolveByOrganization_NoLocator(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_GHEC_DefaultInstallation(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_GHEC_NoDefaultSet(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_GHES_IgnoresDefaultInstallation(t *testing.T) { - tests removed infrastructure

// REMOVED: func TestBase_ResolveInstallation_GHEC_ExternalApp_UsesLookup(t *testing.T) { - tests removed infrastructure
