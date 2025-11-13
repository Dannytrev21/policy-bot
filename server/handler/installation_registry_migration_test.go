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
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestPhase8_LegacyCacheRemoved verifies that the legacy cache field has been
// completely removed from InstallationRegistry, eliminating the dual cache redundancy
func TestPhase8_LegacyCacheRemoved(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Use reflection to verify the legacy cache field no longer exists
	registryType := reflect.TypeOf(registry).Elem()

	// Check that there's NO field named "cache" (legacy)
	for i := 0; i < registryType.NumField(); i++ {
		field := registryType.Field(i)
		assert.NotEqual(t, "cache", field.Name, "Legacy 'cache' field should be removed")
	}

	// Verify the new 'installations' field exists
	_, installationsExists := registryType.FieldByName("installations")
	assert.True(t, installationsExists, "Should have 'installations' field")

	// Verify the 'repoIndex' field exists
	_, repoIndexExists := registryType.FieldByName("repoIndex")
	assert.True(t, repoIndexExists, "Should have 'repoIndex' field")

	t.Log("✅ Phase 8: Legacy cache successfully removed from InstallationRegistry")
}

// TestPhase8_BackwardCompatibility verifies that all public methods still work
// the same way after removing the legacy cache
func TestPhase8_BackwardCompatibility(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	t.Run("Check/MarkInstalled compatibility", func(t *testing.T) {
		// Initially unknown
		status, hit := registry.Check(installationID)
		assert.Equal(t, InstallationUnknown, status)
		assert.False(t, hit)

		// Mark as installed
		registry.MarkInstalled(installationID)

		// Should be cached
		status, hit = registry.Check(installationID)
		assert.Equal(t, InstallationExists, status)
		assert.True(t, hit, "Check should return cached result after MarkInstalled")
	})

	t.Run("Check/MarkNotInstalled compatibility", func(t *testing.T) {
		notFoundID := int64(99999)

		// Mark as not installed
		registry.MarkNotInstalled(notFoundID)

		// Should be cached
		status, hit := registry.Check(notFoundID)
		assert.Equal(t, InstallationNotFound, status)
		assert.True(t, hit, "Check should return cached result after MarkNotInstalled")
	})

	t.Run("Remove compatibility", func(t *testing.T) {
		removeID := int64(54321)

		// Mark as installed
		registry.MarkInstalled(removeID)
		status, hit := registry.Check(removeID)
		assert.True(t, hit)

		// Remove
		registry.Remove(removeID)

		// Should be unknown now
		status, hit = registry.Check(removeID)
		assert.Equal(t, InstallationUnknown, status)
		assert.False(t, hit, "Check should return unknown after Remove")
	})

	t.Run("Clear compatibility", func(t *testing.T) {
		// Create a fresh registry for this test
		clearRegistry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

		// Add multiple entries
		clearRegistry.MarkInstalled(1)
		clearRegistry.MarkInstalled(2)
		clearRegistry.MarkNotInstalled(3)
		assert.Equal(t, 3, clearRegistry.GetCacheSize())

		// Clear
		clearRegistry.Clear()

		// All should be unknown
		status1, hit1 := clearRegistry.Check(1)
		status2, hit2 := clearRegistry.Check(2)
		status3, hit3 := clearRegistry.Check(3)

		assert.Equal(t, InstallationUnknown, status1)
		assert.Equal(t, InstallationUnknown, status2)
		assert.Equal(t, InstallationUnknown, status3)
		assert.False(t, hit1)
		assert.False(t, hit2)
		assert.False(t, hit3)
		assert.Equal(t, 0, clearRegistry.GetCacheSize())
	})

	t.Log("✅ Phase 8: All public methods maintain backward compatibility")
}

// TestPhase8_MemoryImprovement documents that we're now using a single map
// instead of dual maps for the same data
func TestPhase8_MemoryImprovement(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Add 100 entries
	for i := int64(1); i <= 100; i++ {
		registry.MarkInstalled(i)
	}

	// Verify we have 100 entries
	assert.Equal(t, 100, registry.GetCacheSize())

	// Use reflection to count internal maps
	registryValue := reflect.ValueOf(registry).Elem()
	mapCount := 0
	mapNames := []string{}

	for i := 0; i < registryValue.NumField(); i++ {
		field := registryValue.Field(i)
		if field.Kind() == reflect.Map {
			mapCount++
			fieldName := registryValue.Type().Field(i).Name
			mapNames = append(mapNames, fieldName)
		}
	}

	// Should have exactly 2 maps: installations and repoIndex
	// (Before Phase 8, we had 3: cache, installations, repoIndex)
	assert.Equal(t, 2, mapCount, "Should have exactly 2 maps after Phase 8")
	assert.Contains(t, mapNames, "installations", "Should have 'installations' map")
	assert.Contains(t, mapNames, "repoIndex", "Should have 'repoIndex' map")
	assert.NotContains(t, mapNames, "cache", "Should NOT have legacy 'cache' map")

	t.Logf("✅ Phase 8: Memory improvement verified")
	t.Logf("   - Before: 3 maps (cache + installations + repoIndex)")
	t.Logf("   - After:  2 maps (installations + repoIndex)")
	t.Logf("   - Savings: ~50%% reduction in map overhead")
}

// TestPhase8_EnhancedFeatures verifies that new InstallationRecord features work
func TestPhase8_EnhancedFeatures(t *testing.T) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)
	installationID := int64(12345)

	t.Run("GetInstallation returns full record", func(t *testing.T) {
		// Before Phase 8, MarkNotInstalled only updated legacy cache
		// After Phase 8, it creates an InstallationRecord
		registry.MarkNotInstalled(installationID)

		// GetInstallation should now return the record
		record, exists := registry.GetInstallation(installationID)
		assert.True(t, exists, "Phase 8: GetInstallation should find MarkNotInstalled entries")
		assert.NotNil(t, record)
		assert.Equal(t, InstallationNotFound, record.Status)
	})

	t.Run("MarkInstalled creates full record", func(t *testing.T) {
		installedID := int64(54321)
		registry.MarkInstalled(installedID)

		// GetInstallation should return the record
		record, exists := registry.GetInstallation(installedID)
		assert.True(t, exists, "Phase 8: GetInstallation should find MarkInstalled entries")
		assert.NotNil(t, record)
		assert.Equal(t, InstallationExists, record.Status)
		assert.NotNil(t, record.Repositories, "Should have initialized Repositories map")
	})

	t.Run("Repository tracking integration", func(t *testing.T) {
		repoID := int64(99999)

		// Mark as installed and add repositories
		registry.MarkInstalled(repoID)
		registry.AddRepositories(repoID, []struct{ Owner, Repo string }{
			{Owner: "testowner", Repo: "testrepo"},
		})

		// Check by repo should work
		foundID, status, hit := registry.CheckByRepo("testowner", "testrepo")
		assert.True(t, hit, "Should find by repo")
		assert.Equal(t, repoID, foundID)
		assert.Equal(t, InstallationExists, status)

		// GetInstallation should show the repository
		record, exists := registry.GetInstallation(repoID)
		assert.True(t, exists)
		assert.True(t, record.HasRepository("testowner", "testrepo"))
	})

	t.Log("✅ Phase 8: Enhanced InstallationRecord features working correctly")
}

// TestPhase8_ExpiredEntryCleanup verifies that expired entry cleanup now also
// cleans up repo index entries (which wasn't possible with legacy cache)
func TestPhase8_ExpiredEntryCleanup(t *testing.T) {
	// Use very short TTL for testing
	registry := NewInstallationRegistry(50*time.Millisecond, 50*time.Millisecond, nil)
	installationID := int64(12345)

	// Mark as installed and add repository
	registry.MarkInstalled(installationID)
	registry.AddRepositories(installationID, []struct{ Owner, Repo string }{
		{Owner: "owner", Repo: "repo"},
	})

	// Verify both installation and repo index have entries
	status, hit := registry.Check(installationID)
	assert.True(t, hit)
	assert.Equal(t, InstallationExists, status)

	foundID, foundStatus, foundHit := registry.CheckByRepo("owner", "repo")
	assert.True(t, foundHit)
	assert.Equal(t, installationID, foundID)
	assert.Equal(t, InstallationExists, foundStatus)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// Check installation (should trigger cleanup)
	status, hit = registry.Check(installationID)
	assert.False(t, hit, "Should be expired")
	assert.Equal(t, InstallationUnknown, status)

	// Repo index should also be cleaned up (Phase 8 improvement)
	_, _, repoHit := registry.CheckByRepo("owner", "repo")
	assert.False(t, repoHit, "Phase 8: Repo index should be cleaned up when installation expires")

	t.Log("✅ Phase 8: Expired entry cleanup properly removes repo index entries")
}
