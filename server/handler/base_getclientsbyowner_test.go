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
	ownerID := int64(1001)
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Pre-populate client cache with owner ID as key
	expectedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	base.ClientCache.PutWithInstallationID(ownerID, expectedClients, installationID)

	// Execute - must provide owner ID for cache to work
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedClients, clients)

	// Verify no API calls were made (cache hit)
	assert.Empty(t, mockCreator.callLog, "Should not create new clients on cache hit")
}

func TestGetClientsByOwner_CacheMissWithOwnerID(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	ownerID := int64(1001)
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Execute - no cache, will do API lookup
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify per-org rate limiting was used
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgClient(%s, %d)", owner, installationID))
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgV4Client(%s, %d)", owner, installationID))

	// Verify client cache was populated with installation ID
	cachedClients, cachedInstID := base.ClientCache.GetWithInstallationID(ownerID)
	assert.NotNil(t, cachedClients, "Clients should be cached after creation")
	assert.Equal(t, installationID, cachedInstID, "Installation ID should be cached alongside clients")
}

func TestGetClientsByOwner_APILookup(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "new-org"
	ownerID := int64(9999)
	installationID := int64(99999)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Execute (no cache - will do API lookup)
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify per-org rate limiting was used
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgClient(%s, %d)", owner, installationID))
	assert.Contains(t, mockCreator.callLog, fmt.Sprintf("NewOrgV4Client(%s, %d)", owner, installationID))

	// Verify cache was populated with both clients and installation ID
	cachedClients, cachedInstID := base.ClientCache.GetWithInstallationID(ownerID)
	assert.NotNil(t, cachedClients, "Clients should be cached")
	assert.Equal(t, installationID, cachedInstID, "Installation ID should be cached alongside clients")
}

func TestGetClientsByOwner_InstallationNotFound(t *testing.T) {
	// Setup
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "nonexistent-org"
	ownerID := int64(8888)

	// Mock service configured for different owner - this one won't be found
	mockInstallations := newMockInstallationsServiceForOwner("different-owner", 12345)
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

	// Execute
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify
	assert.Error(t, err)
	assert.Nil(t, clients)
	assert.Contains(t, err.Error(), "failed to find installation")
	assert.Empty(t, mockCreator.callLog, "Should not create clients for missing installation")

	// Verify negative cache was set
	assert.True(t, base.ClientCache.IsNegativelyCached(ownerID), "Should cache negative result")
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
	ownerID1 := int64(1111)
	ownerID2 := int64(2222)
	installationID1 := int64(11111)
	installationID2 := int64(22222)

	// Mock service that handles both orgs
	mockInstallations := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			switch owner {
			case org1:
				return githubapp.Installation{ID: installationID1, Owner: org1, OwnerID: ownerID1}, nil
			case org2:
				return githubapp.Installation{ID: installationID2, Owner: org2, OwnerID: ownerID2}, nil
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
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute - get clients for both orgs
	clients1, err1 := base.GetClientsByOwner(ctx, org1, ownerID1)
	clients2, err2 := base.GetClientsByOwner(ctx, org2, ownerID2)

	// Verify both succeeded
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.NotNil(t, clients1)
	assert.NotNil(t, clients2)

	// Verify they're different client instances (check pointer addresses)
	assert.NotSame(t, clients1.V3Client, clients2.V3Client, "Different orgs should have different client pointers")

	// Verify both are cached separately by owner ID
	cached1, instID1 := base.ClientCache.GetWithInstallationID(ownerID1)
	cached2, instID2 := base.ClientCache.GetWithInstallationID(ownerID2)
	assert.NotNil(t, cached1)
	assert.NotNil(t, cached2)
	assert.Equal(t, clients1, cached1)
	assert.Equal(t, clients2, cached2)
	assert.Equal(t, installationID1, instID1, "Installation ID 1 should be cached")
	assert.Equal(t, installationID2, instID2, "Installation ID 2 should be cached")
}

// TestGetClientsByOwner_WithOwnerID_CacheHit tests owner ID-based cache lookup
func TestGetClientsByOwner_WithOwnerID_CacheHit(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	ownerID := int64(123456789)
	installationID := int64(987654321)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Pre-populate client cache with owner ID
	expectedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	base.ClientCache.PutWithInstallationID(ownerID, expectedClients, installationID)

	// Execute - should find clients by owner ID in cache
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify cache hit
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedClients, clients)

	// Verify no API calls were made (cache hit)
	assert.Empty(t, mockCreator.callLog, "Should not create new clients on cache hit")
}

// TestGetClientsByOwner_APILookupCachesInstallationID tests that installation ID is cached after API lookup
func TestGetClientsByOwner_APILookupCachesInstallationID(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	ownerID := int64(999999)
	installationID := int64(12345)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Execute - fresh lookup, should make API call
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify installation ID is cached alongside clients
	_, cachedInstID := base.ClientCache.GetWithInstallationID(ownerID)
	assert.Equal(t, installationID, cachedInstID, "Installation ID should be cached")
}

// TestGetClientsByOwner_UnifiedCache tests that clients and installation ID are cached together
func TestGetClientsByOwner_UnifiedCache(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "new-org"
	ownerID := int64(111222333)
	installationID := int64(444555666)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Execute - fresh lookup, should make API call
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

	// Verify clients created successfully
	require.NoError(t, err)
	assert.NotNil(t, clients)

	// Verify clients and installation ID are cached together
	cachedClients, cachedInstID := base.ClientCache.GetWithInstallationID(ownerID)
	assert.NotNil(t, cachedClients, "Clients should be cached")
	assert.Equal(t, installationID, cachedInstID, "Installation ID should be cached alongside clients")
}

// TestGetClientsByOwner_WithoutOwnerID_NoCaching tests that calls without ownerID don't use cache
func TestGetClientsByOwner_WithoutOwnerID_NoCaching(t *testing.T) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "legacy-org"
	installationID := int64(77777)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Execute WITHOUT ownerID parameter - won't use cache
	clients, err := base.GetClientsByOwner(ctx, owner) // No ownerID parameter

	// Verify clients created (but not cached since no owner ID)
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Note: Without owner ID, cache is not used (owner ID is required for caching)
	// This is by design - owner ID provides immutability for cache keys
}

// TestGetClientsForEvent_WithOwnerID tests owner ID forwarding through GetClientsForEvent
func TestGetClientsForEvent_WithOwnerID(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	ownerID := int64(555666777)
	installationID := int64(888999)

	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// Pre-populate client cache by owner ID
	expectedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	base.ClientCache.PutWithInstallationID(ownerID, expectedClients, installationID)

	// Execute with owner ID parameter
	clients, err := base.GetClientsForEvent(ctx, owner, 0, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedClients, clients)
}

func TestGetClientsByOwner_FallbackToNonRateLimitedCreator(t *testing.T) {
	// Setup with standard MockClientCreator (not rate-limited)
	ctx := zerolog.New(nil).WithContext(context.Background())
	owner := "test-org"
	ownerID := int64(7777)
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
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute with owner ID
	clients, err := base.GetClientsByOwner(ctx, owner, ownerID)

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
	ownerID := int64(12345)
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
		GithubCloud:     true, // GHEC mode
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute - should use GetClientsByOwner (cached path) with owner ID
	clients, err := base.GetClientsForEvent(ctx, owner, installationID, ownerID)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.NotNil(t, clients.V3Client)
	assert.NotNil(t, clients.V4Client)

	// Verify cache was populated by owner ID
	cachedClients := base.ClientCache.Get(ownerID)
	assert.NotNil(t, cachedClients, "Clients should be cached by owner ID")
}

func TestGetClientsForEvent_GHEC_HitsCacheOnSecondCall(t *testing.T) {
	ctx := context.Background()
	owner := "test-org"
	ownerID := int64(12345)
	installationID := int64(12345)

	// Setup mocks
	mockInstallations := newMockInstallationsServiceForOwner(owner, installationID)
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

	// First call - populates cache
	clients1, err := base.GetClientsForEvent(ctx, owner, installationID, ownerID)
	require.NoError(t, err)

	// Second call - should hit cache
	clients2, err := base.GetClientsForEvent(ctx, owner, installationID, ownerID)
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

	// Create installation manager for GHES
	// CircuitBreaker is now created internally by InstallationManager
	installationManager := NewInstallationManager(
		mockCreator,
		nil, // registry parameter (deprecated)
		gometrics.NewRegistry(),
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

// Tests for retrieveClientAndInstallationId

func TestRetrieveClientAndInstallationId_CacheHit(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)
	expectedInstallationID := int64(67890)

	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Pre-populate cache
	expectedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	base.ClientCache.PutWithInstallationID(ownerID, expectedClients, expectedInstallationID)

	// Execute
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")

	// Verify cache hit
	require.NoError(t, err)
	assert.Equal(t, expectedClients, clients)
	assert.Equal(t, expectedInstallationID, installID)
	assert.Len(t, mockCreator.callLog, 0, "No API calls should be made on cache hit")
}

func TestRetrieveClientAndInstallationId_GHEC_OwnerLookup(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)
	expectedInstallationID := int64(67890)

	mockInstallations := newMockInstallationsServiceForOwner(ownerName, expectedInstallationID)
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

	// Execute (no installation ID provided, should use owner lookup)
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedInstallationID, installID)

	// Verify cached
	cachedClients, cachedInstallID := base.ClientCache.GetWithInstallationID(ownerID)
	assert.Equal(t, clients, cachedClients)
	assert.Equal(t, expectedInstallationID, cachedInstallID)
}

func TestRetrieveClientAndInstallationId_GHES_RepoLookup(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)
	repoName := "test-repo"
	expectedInstallationID := int64(67890)

	mockInstallations := &MockInstallationsService{
		getByRepositoryFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			if owner == ownerName && repo == repoName {
				return githubapp.Installation{
					ID:    expectedInstallationID,
					Owner: ownerName,
				}, nil
			}
			return githubapp.Installation{}, githubapp.InstallationNotFound(owner + "/" + repo)
		},
	}
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

	// Execute (GHES uses repo-based lookup)
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, repoName)

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedInstallationID, installID)
}

func TestRetrieveClientAndInstallationId_FallbackToRepoLookup(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)
	repoName := "test-repo"
	expectedInstallationID := int64(67890)

	// Owner lookup fails, but repo lookup succeeds
	mockInstallations := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			return githubapp.Installation{}, fmt.Errorf("owner lookup failed")
		},
		getByRepositoryFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			if owner == ownerName && repo == repoName {
				return githubapp.Installation{
					ID:    expectedInstallationID,
					Owner: ownerName,
				}, nil
			}
			return githubapp.Installation{}, githubapp.InstallationNotFound(owner + "/" + repo)
		},
	}
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

	// Execute (owner lookup fails, fallback to repo lookup)
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, repoName)

	// Verify fallback worked
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, expectedInstallationID, installID)
}

func TestRetrieveClientAndInstallationId_NegativeCaching(t *testing.T) {
	ctx := context.Background()
	ownerName := "nonexistent-org"
	ownerID := int64(99999)

	// All lookups fail
	mockInstallations := &MockInstallationsService{
		getByOwnerFunc: func(ctx context.Context, owner string) (githubapp.Installation, error) {
			return githubapp.Installation{}, githubapp.InstallationNotFound(owner)
		},
		getByRepositoryFunc: func(ctx context.Context, owner, repo string) (githubapp.Installation, error) {
			return githubapp.Installation{}, githubapp.InstallationNotFound(owner + "/" + repo)
		},
	}
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

	// Execute - should fail and cache negative result
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "some-repo")

	// Verify failure and negative caching
	require.Error(t, err)
	assert.Nil(t, clients)
	assert.Equal(t, int64(0), installID)
	assert.True(t, base.ClientCache.IsNegativelyCached(ownerID), "Should cache negative result")
}

func TestRetrieveClientAndInstallationId_ProvidedInstallationID(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)
	providedInstallationID := int64(67890)

	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		GithubCloud:     true,
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute with pre-provided installation ID (no lookup needed)
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, providedInstallationID, ownerID, ownerName, "")

	// Verify
	require.NoError(t, err)
	assert.NotNil(t, clients)
	assert.Equal(t, providedInstallationID, installID)
}

func TestRetrieveClientAndInstallationId_GHES_RequiresRepo(t *testing.T) {
	ctx := context.Background()
	ownerName := "test-org"
	ownerID := int64(12345)

	mockCreator := NewMockRateLimitedClientCreator()

	base := &Base{
		ClientCreator:   mockCreator,
		ClientCache:     NewClientCache(10*time.Minute, 1000),
		GithubCloud:     false, // GHES
		MetricsRegistry: gometrics.NewRegistry(),
		Logger:          zerolog.Nop(),
	}
	base.Initialize()

	// Execute without repo (should fail for GHES)
	clients, installID, err := base.retrieveClientAndInstallationId(ctx, 0, ownerID, ownerName, "")

	// Verify error
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repository name required")
	assert.Nil(t, clients)
	assert.Equal(t, int64(0), installID)
}
