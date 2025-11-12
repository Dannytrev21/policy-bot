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

func TestInstallationRecord_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "not expired",
			expiresAt: time.Now().Add(1 * time.Hour),
			want:      false,
		},
		{
			name:      "expired",
			expiresAt: time.Now().Add(-1 * time.Hour),
			want:      true,
		},
		{
			name:      "just about to expire",
			expiresAt: time.Now().Add(1 * time.Millisecond),
			want:      false, // Not expired if in the future
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &InstallationRecord{
				ExpiresAt: tt.expiresAt,
			}
			assert.Equal(t, tt.want, r.IsExpired())
		})
	}
}

func TestInstallationRecord_HasRepository(t *testing.T) {
	record := &InstallationRecord{
		InstallationID: 12345,
		Status:         InstallationExists,
		Repositories: map[string]bool{
			"owner1:repo1": true,
			"owner2:repo2": true,
		},
	}

	tests := []struct {
		name  string
		owner string
		repo  string
		want  bool
	}{
		{
			name:  "repository exists",
			owner: "owner1",
			repo:  "repo1",
			want:  true,
		},
		{
			name:  "repository does not exist",
			owner: "owner3",
			repo:  "repo3",
			want:  false,
		},
		{
			name:  "partial match owner only",
			owner: "owner1",
			repo:  "repo3",
			want:  false,
		},
		{
			name:  "partial match repo only",
			owner: "owner3",
			repo:  "repo1",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, record.HasRepository(tt.owner, tt.repo))
		})
	}
}

func TestInstallationRecord_AddRepository(t *testing.T) {
	t.Run("add to empty repositories", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
		}

		record.AddRepository("owner", "repo")

		require.NotNil(t, record.Repositories)
		assert.True(t, record.Repositories["owner:repo"])
		assert.Equal(t, 1, record.GetRepositoryCount())
	})

	t.Run("add to existing repositories", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
			Repositories: map[string]bool{
				"owner1:repo1": true,
			},
		}

		record.AddRepository("owner2", "repo2")

		assert.True(t, record.Repositories["owner1:repo1"])
		assert.True(t, record.Repositories["owner2:repo2"])
		assert.Equal(t, 2, record.GetRepositoryCount())
	})

	t.Run("add duplicate repository", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
			Repositories: map[string]bool{
				"owner:repo": true,
			},
		}

		record.AddRepository("owner", "repo")

		assert.True(t, record.Repositories["owner:repo"])
		assert.Equal(t, 1, record.GetRepositoryCount())
	})
}

func TestInstallationRecord_RemoveRepository(t *testing.T) {
	t.Run("remove from existing repositories", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
			Repositories: map[string]bool{
				"owner1:repo1": true,
				"owner2:repo2": true,
			},
		}

		record.RemoveRepository("owner1", "repo1")

		assert.False(t, record.Repositories["owner1:repo1"])
		assert.True(t, record.Repositories["owner2:repo2"])
		assert.Equal(t, 1, record.GetRepositoryCount())
	})

	t.Run("remove from nil repositories", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
		}

		// Should not panic
		record.RemoveRepository("owner", "repo")

		assert.Equal(t, 0, record.GetRepositoryCount())
	})

	t.Run("remove non-existent repository", func(t *testing.T) {
		record := &InstallationRecord{
			InstallationID: 12345,
			Repositories: map[string]bool{
				"owner:repo": true,
			},
		}

		record.RemoveRepository("other", "repo")

		assert.True(t, record.Repositories["owner:repo"])
		assert.Equal(t, 1, record.GetRepositoryCount())
	})
}

func TestInstallationRecord_GetRepositoryCount(t *testing.T) {
	tests := []struct {
		name         string
		repositories map[string]bool
		want         int
	}{
		{
			name:         "nil repositories",
			repositories: nil,
			want:         0,
		},
		{
			name:         "empty repositories",
			repositories: map[string]bool{},
			want:         0,
		},
		{
			name: "multiple repositories",
			repositories: map[string]bool{
				"owner1:repo1": true,
				"owner2:repo2": true,
				"owner3:repo3": true,
			},
			want: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := &InstallationRecord{
				Repositories: tt.repositories,
			}
			assert.Equal(t, tt.want, record.GetRepositoryCount())
		})
	}
}