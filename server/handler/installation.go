// Copyright 2022 Palantir Technologies, Inc.
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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

type Installation struct {
	Base
}

func (h *Installation) Handles() []string {
	return []string{"installation", "installation_repositories"}
}

// Handle installation, installation_repositories
// https://docs.github.com/en/developers/webhooks-and-events/webhooks/webhook-events-and-payloads#installation
// https://docs.github.com/en/developers/webhooks-and-events/webhooks/webhook-events-and-payloads#installation_repositories
func (h *Installation) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	var action string
	var installationID int64
	var repositories []*github.Repository
	var owner string
	var locatorPayload interface{}

	switch eventType {
	case "installation":
		var event github.InstallationEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return errors.Wrap(err, "failed to parse installation event payload")
		}

		action = event.GetAction()
		installationID = githubapp.GetInstallationIDFromEvent(&event)
		repositories = event.Repositories
		if event.Installation != nil && event.Installation.Account != nil {
			owner = event.Installation.Account.GetLogin()
		}
		locatorPayload = &event

	case "installation_repositories":
		var event github.InstallationRepositoriesEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return errors.Wrap(err, "failed to parse installation repositories event payload")
		}

		action = event.GetAction()
		installationID = githubapp.GetInstallationIDFromEvent(&event)
		if event.Installation != nil && event.Installation.Account != nil {
			owner = event.Installation.Account.GetLogin()
		}

		// Handle both added and removed repositories
		if action == "added" {
			repositories = event.RepositoriesAdded
		} else if action == "removed" {
			repositories = event.RepositoriesRemoved
		}
		locatorPayload = &event
	}

	logger := zerolog.Ctx(ctx)

	// Keep the installation locator registry in sync whenever we do receive lifecycle events
	if h.InstallationLocator != nil && locatorPayload != nil {
		h.InstallationLocator.UpdateFromEvent(ctx, eventType, locatorPayload)
	}

	// Extract repository names for cache operations
	repoNames := make([]string, 0, len(repositories))
	for _, repo := range repositories {
		repoNames = append(repoNames, repo.GetName())
	}

	switch action {
	case "created", "added":
		// Populate caches with installation, organization, and repository mappings
		h.PopulateInstallationCaches(installationID, owner, repoNames)
		logger.Debug().
			Int64("installation_id", installationID).
			Str("owner", owner).
			Int("repos_count", len(repoNames)).
			Msg("Populated installation caches (created/added)")

		client, err := h.NewInstallationClient(installationID)
		if err != nil {
			return err
		}
		for _, repo := range repositories {
			h.postRepoInstallationStatus(ctx, client, repo)
		}

	case "deleted":
		// Clear all cache entries when installation is deleted
		h.InvalidateInstallationCaches(installationID, owner, repoNames)
		logger.Info().
			Int64("installation_id", installationID).
			Str("owner", owner).
			Int("repos_count", len(repoNames)).
			Msg("Invalidated installation caches (deleted)")

	case "removed":
		// Remove specific repositories from cache when they're removed from the installation
		h.RemoveRepositoriesFromCache(owner, repoNames)
		logger.Info().
			Str("owner", owner).
			Int("repos_count", len(repoNames)).
			Msg("Removed repositories from cache (removed)")
	}

	return nil
}

func (h *Installation) postRepoInstallationStatus(ctx context.Context, client *github.Client, r *github.Repository) {
	logger := zerolog.Ctx(ctx)

	repoFullName := strings.Split(r.GetFullName(), "/")
	owner, repo := repoFullName[0], repoFullName[1]
	// We must make this extra call because the installation event
	// returns a partial repository object that doesn't include all
	// the data we need for the repo status context (branch & SHA)
	repository, _, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return
	}

	defaultBranch := repository.GetDefaultBranch()
	branch, _, err := client.Repositories.GetBranch(ctx, owner, repo, defaultBranch, false)
	if err != nil {
		return
	}

	head := branch.GetCommit().GetSHA()
	contextWithBranch := fmt.Sprintf("%s: %s", h.PullOpts.StatusCheckContext, defaultBranch)
	state := "success"
	message := fmt.Sprintf("%s successfully installed.", h.AppName)
	status := &github.RepoStatus{
		Context:     &contextWithBranch,
		State:       &state,
		Description: &message,
	}
	if err := PostStatus(ctx, client, owner, repo, head, status); err != nil {
		logger.Err(errors.WithStack(err)).Msg("Failed to post repo status")
	}
}
