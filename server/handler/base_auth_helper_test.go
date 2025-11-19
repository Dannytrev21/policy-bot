package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/go-github/v47/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseWithAuthRefresh_RetriesOnAuthError(t *testing.T) {
	base := &Base{}
	base.Initialize()

	initialClients := &InstallationClients{}
	refreshedClients := &InstallationClients{}
	authErr := &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}

	callCount := 0
	base.authRefreshOverride = func(ctx context.Context, owner string, ownerID int64, repo string, installationID int64, err error) (*InstallationClients, int64, error) {
		assert.Equal(t, "cloud-org", owner)
		assert.EqualValues(t, 12345, ownerID)
		assert.Equal(t, "repo", repo)
		assert.EqualValues(t, 42, installationID)
		return refreshedClients, installationID, nil
	}

	meta := AuthMetadata{
		Owner:          "cloud-org",
		OwnerID:        12345,
		Repo:           "repo",
		InstallationID: 42,
	}

	exec := func(clients *InstallationClients) error {
		callCount++
		if callCount == 1 {
			assert.Equal(t, initialClients, clients)
			return authErr
		}
		assert.Equal(t, refreshedClients, clients)
		return nil
	}

	resultClients, err := base.WithAuthRefresh(context.Background(), meta, initialClients, exec)
	require.NoError(t, err)
	assert.Equal(t, refreshedClients, resultClients)
	assert.Equal(t, 2, callCount, "API call should execute twice when refresh succeeds")
}

func TestBaseWithAuthRefresh_PassesThroughNonAuthErrors(t *testing.T) {
	base := &Base{}
	base.Initialize()

	initialClients := &InstallationClients{}
	expectedErr := assert.AnError
	base.authRefreshOverride = func(context.Context, string, int64, string, int64, error) (*InstallationClients, int64, error) {
		t.Fatalf("auth refresh should not be invoked for non-auth errors")
		return nil, 0, nil
	}

	meta := AuthMetadata{Owner: "org", Repo: "repo"}

	callCount := 0
	exec := func(clients *InstallationClients) error {
		callCount++
		return expectedErr
	}

	resultClients, err := base.WithAuthRefresh(context.Background(), meta, initialClients, exec)
	assert.Equal(t, initialClients, resultClients)
	assert.Equal(t, expectedErr, err)
	assert.Equal(t, 1, callCount)
}

func TestBaseWithAuthRefresh_RateLimitErrorSkipsRefresh(t *testing.T) {
	base := &Base{}
	base.Initialize()

	initialClients := &InstallationClients{}
	rateLimitErr := &github.RateLimitError{
		Response: &http.Response{StatusCode: http.StatusForbidden},
	}

	base.authRefreshOverride = func(context.Context, string, int64, string, int64, error) (*InstallationClients, int64, error) {
		t.Fatalf("rate limit errors should not trigger auth refresh")
		return nil, 0, nil
	}

	meta := AuthMetadata{Owner: "org", Repo: "repo"}

	callCount := 0
	exec := func(clients *InstallationClients) error {
		callCount++
		return rateLimitErr
	}

	resultClients, err := base.WithAuthRefresh(context.Background(), meta, initialClients, exec)
	assert.Equal(t, initialClients, resultClients)
	assert.Equal(t, rateLimitErr, err)
	assert.Equal(t, 1, callCount)
}

func TestBaseWithAuthRefresh_RefreshFailureReturnsError(t *testing.T) {
	base := &Base{}
	base.Initialize()

	initialClients := &InstallationClients{}
	authErr := &github.ErrorResponse{Response: &http.Response{StatusCode: 401}}
	refreshErr := assert.AnError

	base.authRefreshOverride = func(context.Context, string, int64, string, int64, error) (*InstallationClients, int64, error) {
		return nil, 0, refreshErr
	}

	meta := AuthMetadata{Owner: "org", Repo: "repo"}

	callCount := 0
	exec := func(clients *InstallationClients) error {
		callCount++
		return authErr
	}

	resultClients, err := base.WithAuthRefresh(context.Background(), meta, initialClients, exec)
	assert.Equal(t, initialClients, resultClients)
	var wrapped *AuthRefreshError
	require.Error(t, err)
	require.True(t, errors.As(err, &wrapped))
	assert.Equal(t, refreshErr, wrapped.Err)
	assert.Equal(t, 1, callCount, "should not retry when refresh fails")
}
