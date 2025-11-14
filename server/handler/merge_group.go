// Copyright 2023 Palantir Technologies, Inc.
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

type mergeGroupPayload struct {
	HeadSHA    *string        `json:"head_sha,omitempty"`
	HeadRef    *string        `json:"head_ref,omitempty"`
	BaseSHA    *string        `json:"base_sha,omitempty"`
	BaseRef    *string        `json:"base_ref,omitempty"`
	HeadCommit *github.Commit `json:"head_commit,omitempty"`
}

func (m *mergeGroupPayload) GetBaseRef() string {
	if m == nil || m.BaseRef == nil {
		return ""
	}
	return *m.BaseRef
}

func (m *mergeGroupPayload) GetHeadSHA() string {
	if m == nil || m.HeadSHA == nil {
		return ""
	}
	return *m.HeadSHA
}

type mergeGroupEvent struct {
	Action       *string              `json:"action,omitempty"`
	Reason       *string              `json:"reason,omitempty"`
	MergeGroup   *mergeGroupPayload   `json:"merge_group,omitempty"`
	Repo         *github.Repository   `json:"repository,omitempty"`
	Org          *github.Organization `json:"organization,omitempty"`
	Installation *github.Installation `json:"installation,omitempty"`
	Sender       *github.User         `json:"sender,omitempty"`
}

func (e *mergeGroupEvent) GetAction() string {
	if e == nil || e.Action == nil {
		return ""
	}
	return *e.Action
}

func (e *mergeGroupEvent) GetMergeGroup() *mergeGroupPayload {
	if e == nil {
		return nil
	}
	return e.MergeGroup
}

func (e *mergeGroupEvent) GetRepo() *github.Repository {
	if e == nil {
		return nil
	}
	return e.Repo
}

func (e *mergeGroupEvent) GetInstallation() *github.Installation {
	if e == nil {
		return nil
	}
	return e.Installation
}

type MergeGroup struct {
	Base
}

func (h *MergeGroup) Handles() []string { return []string{"merge_group"} }

// Handle merge_group
// https://docs.github.com/webhooks-and-events/webhooks/webhook-events-and-payloads#merge_group
func (h *MergeGroup) Handle(ctx context.Context, eventType, devlieryID string, payload []byte) error {
	var event mergeGroupEvent

	if err := json.Unmarshal(payload, &event); err != nil {
		return errors.Wrap(err, "failed to parse merge group event payload")
	}

	if event.GetAction() != "checks_requested" {
		return nil
	}

	logger := zerolog.Ctx(ctx)
	installationID := githubapp.GetInstallationIDFromEvent(&event)
	owner := event.GetRepo().GetOwner().GetLogin()

	// Use cached client lookup instead of creating uncached client
	clients, err := h.GetClientsForEvent(ctx, owner, installationID)
	if err != nil {
		return err
	}

	repository := event.GetRepo().GetName()
	mergeGroup := event.GetMergeGroup()
	baseBranch := strings.TrimPrefix(mergeGroup.GetBaseRef(), "refs/heads/")
	headSHA := mergeGroup.GetHeadSHA()

	// If a PR is added to the merge queue, presumably the policy existed and was valid at the time of merge,
	// so we're just checking for the existance of a policy here and don't care about its validity.
	fetchedConfig := h.ConfigFetcher.ConfigForRepositoryBranch(ctx, clients.V3Client, owner, repository, baseBranch)
	if fetchedConfig.Config == nil {
		return nil
	}

	contextWithBranch := fmt.Sprintf("%s: %s", h.PullOpts.StatusCheckContext, baseBranch)
	state := "success"
	message := fmt.Sprintf("%s previously approved original pull request.", h.AppName)
	status := &github.RepoStatus{
		Context:     &contextWithBranch,
		State:       &state,
		Description: &message,
	}

	if err := PostStatus(ctx, clients.V3Client, owner, repository, headSHA, status); err != nil {
		logger.Err(errors.WithStack(err)).Msg("Failed to post status check for merge group")
	}

	if h.PullOpts.PostInsecureStatusChecks {
		status.Context = github.String(h.PullOpts.StatusCheckContext)
		if err := PostStatus(ctx, clients.V3Client, owner, repository, headSHA, status); err != nil {
			logger.Err(err).Msg("Failed to post insecure repo status")
		}
	}

	return nil
}
