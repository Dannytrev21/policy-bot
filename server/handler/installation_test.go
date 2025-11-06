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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBase_PopulateInstallationCaches tests that caches are populated correctly
func TestBase_PopulateInstallationCaches(t *testing.T) {
	base := &Base{}
	base.Initialize()

	installationID := int64(12345)
	owner := "test-org"
	repos := []string{"repo1", "repo2", "repo3"}

	// Populate caches
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify organization cache
	orgKey := "org:" + owner
	cachedID, found := base.OrgMappingCache.Get(orgKey)
	assert.True(t, found, "Organization should be in cache")
	assert.Equal(t, installationID, cachedID, "Installation ID should match")

	// Verify repository caches
	for _, repo := range repos {
		repoKey := owner + "/" + repo
		cachedID, found := base.RepoMappingCache.Get(repoKey)
		assert.True(t, found, "Repository %s should be in cache", repoKey)
		assert.Equal(t, installationID, cachedID, "Installation ID should match for %s", repoKey)
	}

	// Verify installation registry
	status, found := base.InstallationRegistry.Check(installationID)
	assert.True(t, found, "Installation should be in registry")
	assert.Equal(t, InstallationExists, status, "Installation should be marked as existing")
}

// TestBase_InvalidateInstallationCaches tests that caches are cleared correctly
func TestBase_InvalidateInstallationCaches(t *testing.T) {
	base := &Base{}
	base.Initialize()

	installationID := int64(12345)
	owner := "test-org"
	repos := []string{"repo1", "repo2"}

	// First populate the caches
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify caches are populated
	orgKey := "org:" + owner
	_, found := base.OrgMappingCache.Get(orgKey)
	require.True(t, found, "Organization should be in cache before invalidation")

	// Invalidate caches
	base.InvalidateInstallationCaches(installationID, owner, repos)

	// Verify organization cache is cleared
	_, found = base.OrgMappingCache.Get(orgKey)
	assert.False(t, found, "Organization should be removed from cache")

	// Verify repository caches are cleared
	for _, repo := range repos {
		repoKey := owner + "/" + repo
		_, found := base.RepoMappingCache.Get(repoKey)
		assert.False(t, found, "Repository %s should be removed from cache", repoKey)
	}
}

// TestBase_AddRepositoriesToCache tests adding specific repositories
func TestBase_AddRepositoriesToCache(t *testing.T) {
	base := &Base{}
	base.Initialize()

	installationID := int64(67890)
	owner := "another-org"
	repos := []string{"new-repo1", "new-repo2"}

	// Add repositories
	base.AddRepositoriesToCache(installationID, owner, repos)

	// Verify repositories are in cache
	for _, repo := range repos {
		repoKey := owner + "/" + repo
		cachedID, found := base.RepoMappingCache.Get(repoKey)
		assert.True(t, found, "Repository %s should be in cache", repoKey)
		assert.Equal(t, installationID, cachedID, "Installation ID should match for %s", repoKey)
	}
}

// TestBase_RemoveRepositoriesFromCache tests removing specific repositories
func TestBase_RemoveRepositoriesFromCache(t *testing.T) {
	base := &Base{}
	base.Initialize()

	installationID := int64(11111)
	owner := "remove-org"
	repos := []string{"remove-repo1", "remove-repo2"}

	// First add the repositories
	base.AddRepositoriesToCache(installationID, owner, repos)

	// Verify they're in cache
	for _, repo := range repos {
		repoKey := owner + "/" + repo
		_, found := base.RepoMappingCache.Get(repoKey)
		require.True(t, found, "Repository %s should be in cache before removal", repoKey)
	}

	// Remove repositories
	base.RemoveRepositoriesFromCache(owner, repos)

	// Verify they're removed
	for _, repo := range repos {
		repoKey := owner + "/" + repo
		_, found := base.RepoMappingCache.Get(repoKey)
		assert.False(t, found, "Repository %s should be removed from cache", repoKey)
	}
}

// TestBase_CacheLifecycle_NilCaches tests that methods handle nil caches gracefully
func TestBase_CacheLifecycle_NilCaches(t *testing.T) {
	base := &Base{}
	// Don't call Initialize() - caches will be nil

	installationID := int64(99999)
	owner := "test-org"
	repos := []string{"repo1"}

	// These should not panic
	assert.NotPanics(t, func() {
		base.PopulateInstallationCaches(installationID, owner, repos)
	}, "PopulateInstallationCaches should not panic with nil caches")

	assert.NotPanics(t, func() {
		base.InvalidateInstallationCaches(installationID, owner, repos)
	}, "InvalidateInstallationCaches should not panic with nil caches")

	assert.NotPanics(t, func() {
		base.AddRepositoriesToCache(installationID, owner, repos)
	}, "AddRepositoriesToCache should not panic with nil caches")

	assert.NotPanics(t, func() {
		base.RemoveRepositoriesFromCache(owner, repos)
	}, "RemoveRepositoriesFromCache should not panic with nil caches")
}

// TestBase_CacheLifecycle_EmptyInputs tests handling of empty owner/repos
func TestBase_CacheLifecycle_EmptyInputs(t *testing.T) {
	base := &Base{}
	base.Initialize()

	installationID := int64(55555)

	// Test with empty owner
	base.PopulateInstallationCaches(installationID, "", []string{"repo1"})
	base.InvalidateInstallationCaches(installationID, "", []string{"repo1"})

	// Test with empty repos
	base.PopulateInstallationCaches(installationID, "owner", []string{})
	base.InvalidateInstallationCaches(installationID, "owner", []string{})

	// Test with both empty
	base.PopulateInstallationCaches(installationID, "", []string{})
	base.InvalidateInstallationCaches(installationID, "", []string{})
}

// TestInstallation_Handle_CreatedAction tests installation created event cache population
// Note: This test only verifies that the cache population is called.
// Full integration testing requires a complete ClientCreator setup.
func TestInstallation_Handle_CreatedAction_CachePopulation(t *testing.T) {
	// This test verifies the cache population logic by directly testing
	// the Base methods, since the full handler requires ClientCreator setup
	base := &Base{}
	base.Initialize()

	installationID := int64(123)
	owner := "test-org"
	repos := []string{"repo1", "repo2"}

	// Simulate what the handler does for "created" action
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify caches were populated
	orgKey := "org:test-org"
	cachedID, found := base.OrgMappingCache.Get(orgKey)
	assert.True(t, found, "Organization should be in cache")
	assert.Equal(t, installationID, cachedID)

	repoKey := "test-org/repo1"
	cachedID, found = base.RepoMappingCache.Get(repoKey)
	assert.True(t, found, "Repository should be in cache")
	assert.Equal(t, installationID, cachedID)
}

// TestInstallation_Handle_DeletedAction tests installation deleted event cache invalidation
func TestInstallation_Handle_DeletedAction_CacheInvalidation(t *testing.T) {
	// This test verifies the cache invalidation logic by directly testing
	// the Base methods
	base := &Base{}
	base.Initialize()

	// First create the installation
	installationID := int64(456)
	owner := "delete-org"
	repos := []string{"repo1", "repo2"}
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify it's in cache
	orgKey := "org:" + owner
	_, found := base.OrgMappingCache.Get(orgKey)
	require.True(t, found, "Organization should be in cache before deletion")

	// Simulate what the handler does for "deleted" action
	base.InvalidateInstallationCaches(installationID, owner, repos)

	// Verify caches were cleared
	_, found = base.OrgMappingCache.Get(orgKey)
	assert.False(t, found, "Organization should be removed from cache")

	repoKey := "delete-org/repo1"
	_, found = base.RepoMappingCache.Get(repoKey)
	assert.False(t, found, "Repository should be removed from cache")
}

// TestInstallation_Handle_RepositoriesAdded tests repositories added event cache population
func TestInstallation_Handle_RepositoriesAdded_CachePopulation(t *testing.T) {
	// This test verifies the cache population logic for added repositories
	base := &Base{}
	base.Initialize()

	installationID := int64(789)
	owner := "add-org"
	repos := []string{"new-repo1", "new-repo2"}

	// Simulate what the handler does for "added" action
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify new repositories are in cache
	repoKey := "add-org/new-repo1"
	cachedID, found := base.RepoMappingCache.Get(repoKey)
	assert.True(t, found, "New repository should be in cache")
	assert.Equal(t, installationID, cachedID)
}

// TestInstallation_Handle_RepositoriesRemoved tests repositories removed event cache removal
func TestInstallation_Handle_RepositoriesRemoved_CacheRemoval(t *testing.T) {
	// This test verifies the cache removal logic for removed repositories
	base := &Base{}
	base.Initialize()

	// First add repositories
	installationID := int64(999)
	owner := "remove-org"
	repos := []string{"old-repo1", "old-repo2"}
	base.AddRepositoriesToCache(installationID, owner, repos)

	// Verify they're in cache
	repoKey := "remove-org/old-repo1"
	_, found := base.RepoMappingCache.Get(repoKey)
	require.True(t, found, "Repository should be in cache before removal")

	// Simulate what the handler does for "removed" action
	base.RemoveRepositoriesFromCache(owner, repos)

	// Verify repositories are removed from cache
	_, found = base.RepoMappingCache.Get(repoKey)
	assert.False(t, found, "Repository should be removed from cache")
}

// TestBase_CacheLifecycle_TTLRespected tests that cache TTLs are respected
func TestBase_CacheLifecycle_TTLRespected(t *testing.T) {
	base := &Base{
		RepoMappingCache: NewMappingCache(100*time.Millisecond, 50*time.Millisecond),
		OrgMappingCache:  NewMappingCache(100*time.Millisecond, 50*time.Millisecond),
	}
	base.Initialize()

	installationID := int64(77777)
	owner := "ttl-org"
	repos := []string{"ttl-repo"}

	// Populate caches
	base.PopulateInstallationCaches(installationID, owner, repos)

	// Verify immediately available
	repoKey := owner + "/" + repos[0]
	_, found := base.RepoMappingCache.Get(repoKey)
	assert.True(t, found, "Repository should be in cache immediately")

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Verify expired
	_, found = base.RepoMappingCache.Get(repoKey)
	assert.False(t, found, "Repository should expire after TTL")
}

// TestBase_CacheLifecycle_ConcurrentOperations tests thread safety
func TestBase_CacheLifecycle_ConcurrentOperations(t *testing.T) {
	base := &Base{}
	base.Initialize()

	done := make(chan bool)

	// Run concurrent operations
	for i := 0; i < 10; i++ {
		go func(id int) {
			installationID := int64(1000 + id)
			owner := "concurrent-org"
			repos := []string{"repo1", "repo2"}

			for j := 0; j < 10; j++ {
				base.PopulateInstallationCaches(installationID, owner, repos)
				base.AddRepositoriesToCache(installationID, owner, repos)
				base.RemoveRepositoriesFromCache(owner, repos)
				base.InvalidateInstallationCaches(installationID, owner, repos)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic - this test mainly checks for race conditions
}
