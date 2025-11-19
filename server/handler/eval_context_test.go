package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v47/github"
	"github.com/palantir/policy-bot/pull"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
)

func TestEvalContextPostStatus_RefreshesAfterAuthError(t *testing.T) {
	base := &Base{}
	base.Initialize()

	initialClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}
	refreshedClients := &InstallationClients{
		V3Client: github.NewClient(nil),
		V4Client: githubv4.NewClient(nil),
	}

	authErr := &github.ErrorResponse{Response: &http.Response{StatusCode: http.StatusUnauthorized}}
	postCalls := 0
	refreshCalls := 0

	base.authRefreshOverride = func(ctx context.Context, owner string, ownerID int64, repo string, installationID int64, err error) (*InstallationClients, int64, error) {
		refreshCalls++
		return refreshedClients, installationID, nil
	}

	ec := &EvalContext{
		Base:                base,
		InstallationID:      99,
		Client:              initialClients.V3Client,
		V4Client:            initialClients.V4Client,
		OwnerID:             123,
		installationClients: initialClients,
		postStatusFunc: func(ctx context.Context, client *github.Client, owner, repo, sha string, status *github.RepoStatus) error {
			postCalls++
			if postCalls == 1 {
				return authErr
			}
			return nil
		},
		Options: &PullEvaluationOptions{
			StatusCheckContext: "policy-bot",
		},
		PublicURL: "https://policy-bot",
		PullContext: &stubPullContext{
			owner:      "acme",
			repo:       "widgets",
			number:     7,
			title:      "Test",
			author:     "user",
			createdAt:  time.Now(),
			open:       true,
			headSHA:    "abc123",
			baseBranch: "main",
		},
	}

	ec.PostStatus(context.Background(), "success", "ok")

	assert.Equal(t, 2, postCalls, "postStatusFunc should be invoked twice (retry after refresh)")
	assert.Equal(t, 1, refreshCalls, "auth refresh should run once")
	assert.Equal(t, refreshedClients.V3Client, ec.Client)
	assert.Equal(t, refreshedClients, ec.installationClients)
}

// stubPullContext implements pull.Context with minimal behavior for tests.
type stubPullContext struct {
	owner      string
	repo       string
	number     int
	title      string
	author     string
	createdAt  time.Time
	open       bool
	headSHA    string
	baseBranch string
}

func (s *stubPullContext) IsTeamMember(team, user string) (bool, error)     { return false, nil }
func (s *stubPullContext) IsOrgMember(org, user string) (bool, error)       { return false, nil }
func (s *stubPullContext) TeamMembers(team string) ([]string, error)        { return nil, nil }
func (s *stubPullContext) OrganizationMembers(org string) ([]string, error) { return nil, nil }
func (s *stubPullContext) RepositoryOwner() string                          { return s.owner }
func (s *stubPullContext) RepositoryName() string                           { return s.repo }
func (s *stubPullContext) Number() int                                      { return s.number }
func (s *stubPullContext) Title() string                                    { return s.title }
func (s *stubPullContext) Body() (*pull.Body, error)                        { return nil, nil }
func (s *stubPullContext) Author() string                                   { return s.author }
func (s *stubPullContext) CreatedAt() time.Time                             { return s.createdAt }
func (s *stubPullContext) IsOpen() bool                                     { return s.open }
func (s *stubPullContext) IsClosed() bool                                   { return !s.open }
func (s *stubPullContext) HeadSHA() string                                  { return s.headSHA }
func (s *stubPullContext) Branches() (string, string)                       { return s.baseBranch, "feature" }
func (s *stubPullContext) ChangedFiles() ([]*pull.File, error)              { return nil, nil }
func (s *stubPullContext) Commits() ([]*pull.Commit, error)                 { return nil, nil }
func (s *stubPullContext) Comments() ([]*pull.Comment, error)               { return nil, nil }
func (s *stubPullContext) Reviews() ([]*pull.Review, error)                 { return nil, nil }
func (s *stubPullContext) IsDraft() bool                                    { return false }
func (s *stubPullContext) RepositoryCollaborators() ([]*pull.Collaborator, error) {
	return nil, nil
}
func (s *stubPullContext) CollaboratorPermission(user string) (pull.Permission, error) {
	return pull.PermissionNone, nil
}
func (s *stubPullContext) Teams() (map[string]pull.Permission, error) {
	return map[string]pull.Permission{}, nil
}
func (s *stubPullContext) RequestedReviewers() ([]*pull.Reviewer, error) { return nil, nil }
func (s *stubPullContext) LatestStatuses() (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *stubPullContext) Labels() ([]string, error) { return nil, nil }
