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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockInstallationsService is a mock implementation of githubapp.InstallationsService
type MockInstallationsService struct {
	getByOwnerFunc      func(ctx context.Context, owner string) (githubapp.Installation, error)
	getByRepositoryFunc func(ctx context.Context, owner, repo string) (githubapp.Installation, error)
	listAllFunc         func(ctx context.Context) ([]githubapp.Installation, error)
}

func (m *MockInstallationsService) GetByOwner(ctx context.Context, owner string) (githubapp.Installation, error) {
	if m.getByOwnerFunc != nil {
		return m.getByOwnerFunc(ctx, owner)
	}
	return githubapp.Installation{}, githubapp.InstallationNotFound(owner)
}

func (m *MockInstallationsService) GetByRepository(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
	if m.getByRepositoryFunc != nil {
		return m.getByRepositoryFunc(ctx, owner, repo)
	}
	return githubapp.Installation{}, githubapp.InstallationNotFound(owner + "/" + repo)
}

func (m *MockInstallationsService) ListAll(ctx context.Context) ([]githubapp.Installation, error) {
	if m.listAllFunc != nil {
		return m.listAllFunc(ctx)
	}
	return nil, nil
}

// newMockInstallationsService creates a configured mock installations service for testing
func newMockInstallationsServiceForOwner(owner string, installationID int64) *MockInstallationsService {
	return &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, o string) (githubapp.Installation, error) {
			if o == owner {
				return githubapp.Installation{
					ID:      installationID,
					Owner:   owner,
					OwnerID: installationID + 1000,
				}, nil
			}
			return githubapp.Installation{}, githubapp.InstallationNotFound(o)
		},
	}
}

// MockRateLimitedClientCreator extends MockClientCreator with per-org rate limiting
type MockRateLimitedClientCreator struct {
	MockClientCreator
	orgClients   map[string]*github.Client
	orgV4Clients map[string]*githubv4.Client
	callLog      []string // Track which methods were called
}

func NewMockRateLimitedClientCreator() *MockRateLimitedClientCreator {
	return &MockRateLimitedClientCreator{
		MockClientCreator: MockClientCreator{
			installationClient: github.NewClient(nil),
		},
		orgClients:   make(map[string]*github.Client),
		orgV4Clients: make(map[string]*githubv4.Client),
		callLog:      []string{},
	}
}

func (m *MockRateLimitedClientCreator) NewOrgClient(ctx context.Context, owner string, installationID int64) (*github.Client, error) {
	m.callLog = append(m.callLog, fmt.Sprintf("NewOrgClient(%s, %d)", owner, installationID))

	// Always create a fresh client - caching is handled by ClientCache
	client := github.NewClient(nil)
	m.orgClients[owner] = client // Track for debugging
	return client, nil
}

func (m *MockRateLimitedClientCreator) NewOrgV4Client(ctx context.Context, owner string, installationID int64) (*githubv4.Client, error) {
	m.callLog = append(m.callLog, fmt.Sprintf("NewOrgV4Client(%s, %d)", owner, installationID))

	// Always create a fresh client - caching is handled by ClientCache
	client := githubv4.NewClient(nil)
	m.orgV4Clients[owner] = client // Track for debugging
	return client, nil
}

func TestGetClientsByOwner_ClientCacheHit(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Pre-populate client cache
	expectedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	base.ClientCache.Put(owner, expectedClients)

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedClients, clients)

	// Verify no API calls were made (cache hit)
	assert.Empty(t, mockCreator.callLog, "Should not create new clients on cache hit")
}

func TestGetClientsByOwner_OrgMappingCacheHit(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Pre-populate org mapping cache (but not client cache)
	orgKey := "org:" + owner
	base.OrgMappingCache.Set(orgKey, installationID)

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify per-org rate limiting was used
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgClient(%s, %d)", owner, installationID))
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgV4Client(%s, %d)", owner, installationID))

	// Verify client cache was populated
	cachedClients := base.ClientCache.Get(owner)
	assert.NotNil(t, cachedClients, "Clients should be cached after creation")
}

func TestGetClientsByOwner_APILookup(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "new-org"
	installationID := int64(99999)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute (no cache - will do API lookup)
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify per-org rate limiting was used
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgClient(%s, %d)", owner, installationID))
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgV4Client(%s, %d)", owner, installationID))

	// Verify both caches were populated
	cachedClients := base.ClientCache.Get(owner)
	assert.NotNil(t, cachedClients, "Clients should be cached")

	orgKey := "org:" + owner
	cachedID, found := base.OrgMappingCache.Get(orgKey)
	assert.True(t, found, "Org mapping should be cached")
	assert.Equal(t, installationID, cachedID)
}

func TestGetClientsByOwner_InstallationNotFound(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "nonexistent-org"

	// Mock service configured for different owner - this one won't be found
	mockInstallations := newMockInstallationsServiceForOwner("different-owner", 12345)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify
	assert.Error(t, err)
	assert.Nil(t, clients)
	assert.Contains(t, err.Error(), "failed to find installation")
	assert.Empty(t, mockCreator.callLog, "Should not create clients for missing installation")
}

func TestGetClientsByOwner_EmptyOwner(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())

	mockInstallations := newMockInstallationsServiceForOwner("any-owner", 12345)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute with empty owner
	clients, err := base.GetClientsByOwner(ctx, "")

	// Verify
	assert.Error(t, err)
	assert.Nil(t, clients)
	assert.Contains(t, err.Error(), "owner cannot be empty")
}

func TestGetClientsByOwner_GHES_ReturnsError(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"

	mockInstallations := &MockInstallationsService{}
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		GithubCloud:     false, // GHES
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify
	assert.Error(t, err)
	assert.Nil(t, clients)
	assert.Contains(t, err.Error(), "GHEC only")
}

func TestGetClientsByOwner_MultipleOrgs_CachedSeparately(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	org1 := "org-one"
	org2 := "org-two"
	installationID1 := int64(11111)
	installationID2 := int64(22222)

	// Mock service that handles both orgs
	mockInstallations := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			switch owner {
			case org1:
				return githubapp.Installation{ID: installationID1, Owner: org1, OwnerID: installationID1 + 1000}, nil
			case org2:
				return githubapp.Installation{ID: installationID2, Owner: org2, OwnerID: installationID2 + 1000}, nil
			default:
				return githubapp.Installation{}, githubapp.InstallationNotFound(owner)
			}
		},
	}

	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute - get clients for both orgs
	clients1, err1 := base.GetClientsByOwner(ctx, org1)
	clients2, err2 := base.GetClientsByOwner(ctx, org2)

	// Verify both succeeded
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.NotNil(t, clients1)
	assert.NotNil(t, clients2)

	// Verify they're different client instances (check pointer addresses)
	assert.NotSame(t, clients1.V3Client, clients2.V3Client, "Different orgs should have different client pointers")

	// Verify both are cached separately
	cached1 := base.ClientCache.Get(org1)
	cached2 := base.ClientCache.Get(org2)
	assert.NotNil(t, cached1)
	assert.NotNil(t, cached2)
	assert.Equal(t, clients1, cached1)
	assert.Equal(t, clients2, cached2)

	// Verify org mappings are cached separately
	orgKey1 := "org:" + org1
	orgKey2 := "org:" + org2
	cachedID1, found1 := base.OrgMappingCache.Get(orgKey1)
	cachedID2, found2 := base.OrgMappingCache.Get(orgKey2)
	assert.True(t, found1)
	assert.True(t, found2)
	assert.Equal(t, installationID1, cachedID1)
	assert.Equal(t, installationID2, cachedID2)
}

func TestGetClientsByOwner_FallbackToNonRateLimitedCreator(t *testing.T) {
	// Setup with standard MockClientCreator (not rate-limited)
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)

	// Use standard mock creator (not RateLimitedClientCreator)
	mockCreator := &MockClientCreator{
		installationClient: github.NewClient(nil),
	}

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner)

	// Verify - should still work, just without per-org rate limiting
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)
}

// Tests for GetClientsForEvent (Step 2.1)

func TestGetClientsForEvent_GHEC_UsesCachedLookup(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	installationID := int64(12345)

	// Setup mock installations service
	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)

	// Setup mock client creator using standard pattern
	mockCreator := NewMockRateLimitedClientCreator()

	// Setup base with GHEC configuration
	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true, // GHEC mode
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute - should use GetClientsByOwner (cached path)
	clients, err := base.GetClientsForEvent(ctx, owner, installationID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify cache was populated
	cachedClients := base.ClientCache.Get(owner)
	assert.NotNil(t, cachedClients, "Clients should be cached")
}

func TestGetClientsForEvent_GHEC_HitsCacheOnSecondCall(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	installationID := int64(12345)

	// Setup mocks
	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		Installations:   mockInstallations,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		OrgMappingCache: NewMappingCache(1*time.Hour, 5*time.Minute),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// First call - populates cache
	clients1, err := base.GetClientsForEvent(ctx, owner, installationID)
	require.NoError(t, err)

	// Second call - should hit cache
	clients2, err := base.GetClientsForEvent(ctx, owner, installationID)
	require.NoError(t, err)

	// Verify both calls succeeded
	assert.NotNil(t, clients1)
	assert.NotNil(t, clients2)

	// Verify cache metrics show a hit
	hits, misses, _, _ := base.ClientCache.GetMetrics()
	assert.GreaterOrEqual(t, hits, int64(1), "Should have at least one cache hit")
	assert.GreaterOrEqual(t, misses, int64(1), "Should have at least one cache miss (first call)")
}

func TestGetClientsForEvent_GHES_UsesInstallationManager(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	installationID := int64(67890)

	// Setup mock client creator
	mockCreator := &MockClientCreator{
		installationClient: github.NewClient(nil),
	}

	// Create circuit breaker
	circuitBreaker := NewCircuitBreaker()

	// Create installation manager for GHES
	installationManager := NewInstallationManager(
		mockCreator,
		nil, // registry parameter (deprecated)
		gometrics.NewRegistry(),
		circuitBreaker,
	)

	// Setup base with GHES configuration
	base := &Base{
		ClientCreator:       mockCreator,
		InstallationManager: installationManager,
		GithubCloud:         false, // GHES mode
		MetricsRegistry:     gometrics.NewRegistry(),
		Logger:              zerolog.Nop(),
	}
	base.Initialize()

	// Execute - should use InstallationManager.GetClients
	clients, err := base.GetClientsForEvent(ctx, owner, installationID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)
}

func TestGetClientsForEvent_FallbackPath(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	installationID := int64(99999)

	// Setup mock client creator (no InstallationManager, no caching)
	mockCreator := &MockClientCreator{
		installationClient: github.NewClient(nil),
	}

	// Setup base without GHEC or InstallationManager (legacy path)
	base := &Base{
		ClientCreator:   mockCreator,
		GithubCloud:     false,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute - should use fallback createClientsForOwner
	clients, err := base.GetClientsForEvent(ctx, owner, installationID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)
}
