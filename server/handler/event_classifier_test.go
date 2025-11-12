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

	"github.com/stretchr/testify/assert"
)

func TestEventClassifier_Classify(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name      string
		eventType string
		want      EventClassification
	}{
		// Events that always have installation ID
		{
			name:      "installation event",
			eventType: "installation",
			want:      EventWithInstallation,
		},
		{
			name:      "pull_request event",
			eventType: "pull_request",
			want:      EventWithInstallation,
		},
		{
			name:      "push event",
			eventType: "push",
			want:      EventWithInstallation,
		},
		{
			name:      "issues event",
			eventType: "issues",
			want:      EventWithInstallation,
		},

		// Events that should bypass cache
		{
			name:      "check_run event",
			eventType: "check_run",
			want:      EventNoCache,
		},
		{
			name:      "check_suite event",
			eventType: "check_suite",
			want:      EventNoCache,
		},
		{
			name:      "status event",
			eventType: "status",
			want:      EventNoCache,
		},

		// Events that may have installation ID
		{
			name:      "ping event",
			eventType: "ping",
			want:      EventMaybeInstallation,
		},
		{
			name:      "meta event",
			eventType: "meta",
			want:      EventMaybeInstallation,
		},

		// Events that never have installation ID
		{
			name:      "organization event",
			eventType: "organization",
			want:      EventNoInstallation,
		},
		{
			name:      "fork event",
			eventType: "fork",
			want:      EventNoInstallation,
		},
		{
			name:      "star event",
			eventType: "star",
			want:      EventNoInstallation,
		},

		// Unknown event (defaults to maybe)
		{
			name:      "unknown event",
			eventType: "some_new_event_type",
			want:      EventMaybeInstallation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifier.Classify(tt.eventType))
		})
	}
}

func TestEventClassifier_ShouldCache(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name      string
		eventType string
		want      bool
	}{
		{
			name:      "pull_request should cache",
			eventType: "pull_request",
			want:      true,
		},
		{
			name:      "push should cache",
			eventType: "push",
			want:      true,
		},
		{
			name:      "check_run should not cache",
			eventType: "check_run",
			want:      false,
		},
		{
			name:      "check_suite should not cache",
			eventType: "check_suite",
			want:      false,
		},
		{
			name:      "status should not cache",
			eventType: "status",
			want:      false,
		},
		{
			name:      "unknown event should cache",
			eventType: "new_event_type",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifier.ShouldCache(tt.eventType))
		})
	}
}

func TestEventClassifier_ExpectsInstallationID(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name      string
		eventType string
		want      bool
	}{
		{
			name:      "installation expects ID",
			eventType: "installation",
			want:      true,
		},
		{
			name:      "pull_request expects ID",
			eventType: "pull_request",
			want:      true,
		},
		{
			name:      "ping maybe has ID",
			eventType: "ping",
			want:      false,
		},
		{
			name:      "organization never has ID",
			eventType: "organization",
			want:      false,
		},
		{
			name:      "check_run no cache",
			eventType: "check_run",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifier.ExpectsInstallationID(tt.eventType))
		})
	}
}

func TestEventClassifier_MightHaveInstallationID(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name      string
		eventType string
		want      bool
	}{
		{
			name:      "installation might have ID",
			eventType: "installation",
			want:      true,
		},
		{
			name:      "pull_request might have ID",
			eventType: "pull_request",
			want:      true,
		},
		{
			name:      "ping might have ID",
			eventType: "ping",
			want:      true,
		},
		{
			name:      "organization never has ID",
			eventType: "organization",
			want:      false,
		},
		{
			name:      "fork never has ID",
			eventType: "fork",
			want:      false,
		},
		{
			name:      "check_run no cache",
			eventType: "check_run",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifier.MightHaveInstallationID(tt.eventType))
		})
	}
}

func TestEventClassifier_ExtractEventMetadata(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name           string
		eventType      string
		installationID int64
		owner          string
		repo           string
		wantCache      bool
		wantExpects    bool
	}{
		{
			name:           "pull_request with installation",
			eventType:      "pull_request",
			installationID: 12345,
			owner:          "owner",
			repo:           "repo",
			wantCache:      true,
			wantExpects:    true,
		},
		{
			name:           "check_run should not cache",
			eventType:      "check_run",
			installationID: 12345,
			owner:          "owner",
			repo:           "repo",
			wantCache:      false,
			wantExpects:    false,
		},
		{
			name:           "organization event no installation",
			eventType:      "organization",
			installationID: 0,
			owner:          "org",
			repo:           "",
			wantCache:      true,
			wantExpects:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := classifier.ExtractEventMetadata(tt.eventType, tt.installationID, tt.owner, tt.repo)

			assert.Equal(t, tt.installationID, metadata.InstallationID)
			assert.Equal(t, tt.owner, metadata.Owner)
			assert.Equal(t, tt.repo, metadata.Repo)
			assert.Equal(t, tt.eventType, metadata.EventType)
			assert.Equal(t, tt.wantCache, metadata.ShouldCache)
			assert.Equal(t, tt.wantExpects, metadata.ExpectsInstallation)
		})
	}
}

func TestEventClassifier_IsReliableForCaching(t *testing.T) {
	classifier := NewEventClassifier()

	tests := []struct {
		name      string
		eventType string
		want      bool
	}{
		{
			name:      "installation is reliable",
			eventType: "installation",
			want:      true,
		},
		{
			name:      "pull_request is reliable",
			eventType: "pull_request",
			want:      true,
		},
		{
			name:      "ping is not reliable",
			eventType: "ping",
			want:      false,
		},
		{
			name:      "organization is not reliable",
			eventType: "organization",
			want:      false,
		},
		{
			name:      "check_run is not reliable",
			eventType: "check_run",
			want:      false,
		},
		{
			name:      "unknown is not reliable",
			eventType: "unknown_event",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifier.IsReliableForCaching(tt.eventType))
		})
	}
}

func TestGetClassificationName(t *testing.T) {
	tests := []struct {
		name           string
		classification EventClassification
		want           string
	}{
		{
			name:           "with installation",
			classification: EventWithInstallation,
			want:           "with_installation",
		},
		{
			name:           "maybe installation",
			classification: EventMaybeInstallation,
			want:           "maybe_installation",
		},
		{
			name:           "no installation",
			classification: EventNoInstallation,
			want:           "no_installation",
		},
		{
			name:           "no cache",
			classification: EventNoCache,
			want:           "no_cache",
		},
		{
			name:           "unknown",
			classification: EventClassification(999),
			want:           "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetClassificationName(tt.classification))
		})
	}
}