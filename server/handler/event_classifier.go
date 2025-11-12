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

// EventClassification categorizes GitHub events based on installation ID availability
// and caching behavior
type EventClassification int

const (
	// EventWithInstallation - Event always has installation ID
	EventWithInstallation EventClassification = iota

	// EventMaybeInstallation - Event may or may not have installation ID
	EventMaybeInstallation

	// EventNoInstallation - Event never has installation ID
	EventNoInstallation

	// EventNoCache - Event should never be cached (e.g., check_run)
	EventNoCache
)

// EventClassifier provides event classification logic
type EventClassifier struct {
	// Map of event types to their classifications
	classifications map[string]EventClassification
}

// NewEventClassifier creates a new event classifier with GitHub event knowledge
func NewEventClassifier() *EventClassifier {
	return &EventClassifier{
		classifications: map[string]EventClassification{
			// Events that always have installation ID
			"installation":              EventWithInstallation,
			"installation_repositories": EventWithInstallation,
			"installation_target":       EventWithInstallation,

			// Events that definitely have installation ID when from installed apps
			"issues":              EventWithInstallation,
			"issue_comment":       EventWithInstallation,
			"pull_request":        EventWithInstallation,
			"pull_request_review": EventWithInstallation,
			"push":                EventWithInstallation,
			"create":              EventWithInstallation,
			"delete":              EventWithInstallation,
			"release":             EventWithInstallation,
			"deployment":          EventWithInstallation,
			"deployment_status":   EventWithInstallation,
			"repository":          EventWithInstallation,
			"repository_dispatch": EventWithInstallation,
			"workflow_dispatch":   EventWithInstallation,
			"workflow_run":        EventWithInstallation,
			"workflow_job":        EventWithInstallation,

			// Events that should bypass cache due to incomplete data
			"check_run":   EventNoCache, // Can poison cache with incomplete installation data
			"check_suite": EventNoCache, // Can poison cache with incomplete installation data
			"status":      EventNoCache, // Legacy status events, use commit status instead

			// Events that may not have installation ID
			"ping":                 EventMaybeInstallation, // Setup/test event
			"meta":                 EventMaybeInstallation, // Webhook meta events
			"marketplace_purchase": EventMaybeInstallation, // Marketplace events
			"sponsorship":          EventMaybeInstallation, // Sponsorship events

			// Events that never have installation ID (org/user level)
			"organization":        EventNoInstallation,
			"org_block":           EventNoInstallation,
			"membership":          EventNoInstallation,
			"member":              EventNoInstallation,
			"public":              EventNoInstallation,
			"team":                EventNoInstallation,
			"team_add":            EventNoInstallation,
			"fork":                EventNoInstallation, // Forks don't inherit installations
			"gollum":              EventNoInstallation, // Wiki events
			"page_build":          EventNoInstallation, // GitHub Pages
			"project":             EventNoInstallation, // Projects (deprecated)
			"project_card":        EventNoInstallation,
			"project_column":      EventNoInstallation,
			"security_advisory":   EventNoInstallation, // Security advisories
			"security_and_analysis": EventNoInstallation, // Security settings
			"star":                EventNoInstallation, // Star events
			"watch":               EventNoInstallation, // Watch events
		},
	}
}

// Classify returns the classification for a given event type
func (ec *EventClassifier) Classify(eventType string) EventClassification {
	if classification, exists := ec.classifications[eventType]; exists {
		return classification
	}

	// Default to assuming the event might have an installation ID
	// This is safer than assuming it doesn't
	return EventMaybeInstallation
}

// ShouldCache returns whether an event should be cached
func (ec *EventClassifier) ShouldCache(eventType string) bool {
	classification := ec.Classify(eventType)
	return classification != EventNoCache
}

// ExpectsInstallationID returns whether an event is expected to have an installation ID
func (ec *EventClassifier) ExpectsInstallationID(eventType string) bool {
	classification := ec.Classify(eventType)
	return classification == EventWithInstallation
}

// MightHaveInstallationID returns whether an event might have an installation ID
func (ec *EventClassifier) MightHaveInstallationID(eventType string) bool {
	classification := ec.Classify(eventType)
	return classification == EventWithInstallation || classification == EventMaybeInstallation
}

// EventMetadata contains extracted metadata from a GitHub event
type EventMetadata struct {
	// Installation ID from the event (0 if not present)
	InstallationID int64

	// Repository information
	Owner string
	Repo  string

	// Event type
	EventType string

	// Whether this event should be cached
	ShouldCache bool

	// Whether this event is expected to have an installation ID
	ExpectsInstallation bool
}

// ExtractEventMetadata extracts metadata from a raw GitHub event payload
// This is a helper function that would be called during event processing
func (ec *EventClassifier) ExtractEventMetadata(eventType string, installationID int64, owner, repo string) EventMetadata {
	classification := ec.Classify(eventType)

	return EventMetadata{
		InstallationID:      installationID,
		Owner:               owner,
		Repo:                repo,
		EventType:           eventType,
		ShouldCache:         classification != EventNoCache,
		ExpectsInstallation: classification == EventWithInstallation,
	}
}

// IsReliableForCaching checks if an event is reliable for populating the cache
// Some events may have partial or unreliable installation data
func (ec *EventClassifier) IsReliableForCaching(eventType string) bool {
	classification := ec.Classify(eventType)

	// Only events that definitely have installation IDs are reliable
	// Events that "maybe" have IDs or shouldn't be cached are unreliable
	return classification == EventWithInstallation
}

// GetClassificationName returns a human-readable name for a classification
func GetClassificationName(c EventClassification) string {
	switch c {
	case EventWithInstallation:
		return "with_installation"
	case EventMaybeInstallation:
		return "maybe_installation"
	case EventNoInstallation:
		return "no_installation"
	case EventNoCache:
		return "no_cache"
	default:
		return "unknown"
	}
}