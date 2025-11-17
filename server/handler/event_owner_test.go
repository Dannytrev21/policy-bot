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

	"github.com/google/go-github/v47/github"
	"github.com/stretchr/testify/assert"
)

func TestGetOwnerIDFromEvent(t *testing.T) {
	tests := []struct {
		name     string
		event    interface{}
		expected int64
	}{
		{
			name: "PullRequestEvent with owner",
			event: &github.PullRequestEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(12345),
					},
				},
			},
			expected: 12345,
		},
		{
			name: "IssueCommentEvent with owner",
			event: &github.IssueCommentEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(67890),
					},
				},
			},
			expected: 67890,
		},
		{
			name: "StatusEvent with owner",
			event: &github.StatusEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(11111),
					},
				},
			},
			expected: 11111,
		},
		{
			name: "CheckRunEvent with owner",
			event: &github.CheckRunEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(22222),
					},
				},
			},
			expected: 22222,
		},
		{
			name: "CheckSuiteEvent with owner",
			event: &github.CheckSuiteEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(33333),
					},
				},
			},
			expected: 33333,
		},
		{
			name: "PullRequestReviewEvent with owner",
			event: &github.PullRequestReviewEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(44444),
					},
				},
			},
			expected: 44444,
		},
		{
			name: "PullRequestReviewCommentEvent with owner",
			event: &github.PullRequestReviewCommentEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(55555),
					},
				},
			},
			expected: 55555,
		},
		{
			name: "WorkflowRunEvent with owner",
			event: &github.WorkflowRunEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(66666),
					},
				},
			},
			expected: 66666,
		},
		{
			name: "mergeGroupEvent with owner",
			event: &mergeGroupEvent{
				Repo: &github.Repository{
					Owner: &github.User{
						ID: github.Int64(77777),
					},
				},
			},
			expected: 77777,
		},
		{
			name: "InstallationEvent with account",
			event: &github.InstallationEvent{
				Installation: &github.Installation{
					Account: &github.User{
						ID: github.Int64(88888),
					},
				},
			},
			expected: 88888,
		},
		{
			name: "InstallationRepositoriesEvent with account",
			event: &github.InstallationRepositoriesEvent{
				Installation: &github.Installation{
					Account: &github.User{
						ID: github.Int64(99999),
					},
				},
			},
			expected: 99999,
		},
		{
			name:     "PullRequestEvent with nil Repo",
			event:    &github.PullRequestEvent{},
			expected: 0,
		},
		{
			name: "PullRequestEvent with nil Owner",
			event: &github.PullRequestEvent{
				Repo: &github.Repository{},
			},
			expected: 0,
		},
		{
			name: "InstallationEvent with nil Installation",
			event: &github.InstallationEvent{
			},
			expected: 0,
		},
		{
			name: "InstallationEvent with nil Account",
			event: &github.InstallationEvent{
				Installation: &github.Installation{},
			},
			expected: 0,
		},
		{
			name:     "Unsupported event type",
			event:    &github.PushEvent{}, // Not in our switch case
			expected: 0,
		},
		{
			name:     "Nil event",
			event:    nil,
			expected: 0,
		},
		{
			name:     "Non-pointer event",
			event:    "string",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetOwnerIDFromEvent(tt.event)
			assert.Equal(t, tt.expected, result, "owner ID mismatch")
		})
	}
}

// TestGetOwnerIDFromEvent_RealWorldScenarios tests scenarios that might occur in production
func TestGetOwnerIDFromEvent_RealWorldScenarios(t *testing.T) {
	t.Run("Organization owner", func(t *testing.T) {
		event := &github.PullRequestEvent{
			Repo: &github.Repository{
				Owner: &github.User{
					ID:    github.Int64(123456789),
					Login: github.String("example-org"),
					Type:  github.String("Organization"),
				},
			},
		}
		result := GetOwnerIDFromEvent(event)
		assert.Equal(t, int64(123456789), result)
	})

	t.Run("User owner", func(t *testing.T) {
		event := &github.PullRequestEvent{
			Repo: &github.Repository{
				Owner: &github.User{
					ID:    github.Int64(987654321),
					Login: github.String("example-user"),
					Type:  github.String("User"),
				},
			},
		}
		result := GetOwnerIDFromEvent(event)
		assert.Equal(t, int64(987654321), result)
	})

	t.Run("Owner with zero ID", func(t *testing.T) {
		event := &github.PullRequestEvent{
			Repo: &github.Repository{
				Owner: &github.User{
					ID: github.Int64(0),
				},
			},
		}
		result := GetOwnerIDFromEvent(event)
		assert.Equal(t, int64(0), result)
	})

	t.Run("Multiple sequential calls same event", func(t *testing.T) {
		event := &github.PullRequestEvent{
			Repo: &github.Repository{
				Owner: &github.User{
					ID: github.Int64(111222333),
				},
			},
		}
		// Call multiple times to ensure consistency
		for i := 0; i < 5; i++ {
			result := GetOwnerIDFromEvent(event)
			assert.Equal(t, int64(111222333), result)
		}
	})
}

// BenchmarkGetOwnerIDFromEvent ensures the function has minimal overhead
func BenchmarkGetOwnerIDFromEvent(b *testing.B) {
	event := &github.PullRequestEvent{
		Repo: &github.Repository{
			Owner: &github.User{
				ID:    github.Int64(12345),
				Login: github.String("test-org"),
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetOwnerIDFromEvent(event)
	}
}

// BenchmarkGetOwnerIDFromEvent_NilChecks benchmarks worst-case scenario with nil checks
func BenchmarkGetOwnerIDFromEvent_NilChecks(b *testing.B) {
	event := &github.PullRequestEvent{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetOwnerIDFromEvent(event)
	}
}

// BenchmarkGetOwnerIDFromEvent_InstallationEvent benchmarks InstallationEvent path
func BenchmarkGetOwnerIDFromEvent_InstallationEvent(b *testing.B) {
	event := &github.InstallationEvent{
		Installation: &github.Installation{
			Account: &github.User{
				ID: github.Int64(99999),
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetOwnerIDFromEvent(event)
	}
}
