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
	"fmt"
	"time"
)

// InstallationRecord represents enhanced cached installation data
// that tracks both installation status and repository associations
type InstallationRecord struct {
	// InstallationID is the GitHub installation ID
	InstallationID int64

	// Status indicates the current cache status
	Status InstallationStatus

	// ExpiresAt is when this cache entry expires
	ExpiresAt time.Time

	// Repositories associated with this installation (owner:repo format)
	// Pre-allocated strings for performance (avoids repeated concatenation)
	Repositories map[string]bool

	// LastUpdated tracks when repository list was last refreshed
	LastUpdated time.Time

	// Account information (cached to avoid API calls)
	AccountLogin string
	AccountType  string // "User" or "Organization"
}

// IsExpired checks if this record has expired
func (r *InstallationRecord) IsExpired() bool {
	return time.Now().After(r.ExpiresAt)
}

// HasRepository checks if this installation includes a specific repository
func (r *InstallationRecord) HasRepository(owner, repo string) bool {
	key := fmt.Sprintf("%s:%s", owner, repo)
	return r.Repositories != nil && r.Repositories[key]
}

// AddRepository adds a repository to this installation's tracked repos
func (r *InstallationRecord) AddRepository(owner, repo string) {
	if r.Repositories == nil {
		r.Repositories = make(map[string]bool)
	}
	key := fmt.Sprintf("%s:%s", owner, repo)
	r.Repositories[key] = true
}

// RemoveRepository removes a repository from this installation's tracked repos
func (r *InstallationRecord) RemoveRepository(owner, repo string) {
	if r.Repositories == nil {
		return
	}
	key := fmt.Sprintf("%s:%s", owner, repo)
	delete(r.Repositories, key)
}

// GetRepositoryCount returns the number of repositories tracked for this installation
func (r *InstallationRecord) GetRepositoryCount() int {
	if r.Repositories == nil {
		return 0
	}
	return len(r.Repositories)
}