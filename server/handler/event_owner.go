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

import "github.com/google/go-github/v47/github"

// GetOwnerIDFromEvent extracts the repository owner ID from GitHub webhook events.
// Returns 0 if the event doesn't contain owner information.
//
// This function is used to enable ID-based caching of installations and clients,
// as owner IDs are immutable (unlike owner names which can change during renames).
//
// For owner names, use the existing pattern: event.GetRepo().GetOwner().GetLogin()
// For installation IDs, use: githubapp.GetInstallationIDFromEvent(&event)
func GetOwnerIDFromEvent(event interface{}) int64 {
	switch e := event.(type) {
	case *github.PullRequestEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.IssueCommentEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.StatusEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.CheckRunEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.CheckSuiteEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.PullRequestReviewEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.PullRequestReviewCommentEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.WorkflowRunEvent:
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *mergeGroupEvent:
		// Custom event type for merge_group webhook (defined in merge_group.go)
		if e.Repo != nil && e.Repo.Owner != nil {
			return e.Repo.Owner.GetID()
		}
	case *github.InstallationEvent:
		// Installation events use Installation.Account instead of Repo.Owner
		if e.Installation != nil && e.Installation.Account != nil {
			return e.Installation.Account.GetID()
		}
	case *github.InstallationRepositoriesEvent:
		// Installation repository events use Installation.Account
		if e.Installation != nil && e.Installation.Account != nil {
			return e.Installation.Account.GetID()
		}
	}
	return 0
}
