// Copyright 2018 Palantir Technologies, Inc.
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

package sqsconsumer

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestProcessor_DetectSourceFromHeaders(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	tests := []struct {
		name           string
		message        SQSMessage
		expectedSource string
		description    string
	}{
		{
			name: "ghec_in_host",
			message: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "github.ghec.example.com",
				},
			},
			expectedSource: "cloud",
			description:    "Host containing 'ghec' should route to cloud",
		},
		{
			name: "GHEC_uppercase_in_host",
			message: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "GITHUB.GHEC.COMPANY.COM",
				},
			},
			expectedSource: "cloud",
			description:    "Host containing 'GHEC' (uppercase) should route to cloud",
		},
		{
			name: "ghes_host",
			message: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "github.enterprise.example.com",
				},
			},
			expectedSource: "enterprise",
			description:    "Host without 'ghec' should route to enterprise",
		},
		{
			name: "enterprise_server_host",
			message: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "ghes.company.local",
				},
			},
			expectedSource: "enterprise",
			description:    "Enterprise server host should route to enterprise",
		},
		{
			name:           "no_headers_default_cloud",
			message:        SQSMessage{},
			expectedSource: "cloud",
			description:    "No headers should default to cloud",
		},
		{
			name: "empty_headers_default_cloud",
			message: SQSMessage{
				Headers: map[string]interface{}{},
			},
			expectedSource: "cloud",
			description:    "Empty headers should default to cloud",
		},
		{
			name: "legacy_source_field_enterprise",
			message: SQSMessage{
				Source: "enterprise",
			},
			expectedSource: "enterprise",
			description:    "Legacy source field should still work for backward compatibility",
		},
		{
			name: "headers_override_legacy_source",
			message: SQSMessage{
				Headers: map[string]interface{}{
					"Host": "github.ghec.example.com",
				},
				Source: "enterprise", // Legacy field says enterprise
			},
			expectedSource: "cloud", // But Host header should take precedence
			description:    "Host header should override legacy source field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processor.detectSourceFromHeaders(tt.message)
			assert.Equal(t, tt.expectedSource, result, tt.description)
		})
	}
}

func TestProcessor_ParseMessage_WithHeaders(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	tests := []struct {
		name          string
		messageBody   string
		eventType     string
		expectHeaders bool
		expectedHost  string
		description   string
	}{
		{
			name: "github_webhook_with_headers",
			messageBody: `{
				"headers": {
					"Host": "github.ghec.company.com",
					"X-GitHub-Event": "pull_request",
					"X-GitHub-Delivery": "12345-67890"
				},
				"payload": {
					"action": "opened",
					"pull_request": {
						"id": 123,
						"number": 456
					}
				}
			}`,
			eventType:     "pull_request",
			expectHeaders: true,
			expectedHost:  "github.ghec.company.com",
			description:   "Should parse GitHub webhook with headers correctly",
		},
		{
			name: "structured_sqs_message_with_headers",
			messageBody: `{
				"event_type": "pull_request",
				"delivery_id": "abc-123",
				"headers": {
					"Host": "ghes.internal.company.com"
				},
				"payload": {
					"action": "closed"
				}
			}`,
			eventType:     "pull_request",
			expectHeaders: true,
			expectedHost:  "ghes.internal.company.com",
			description:   "Should parse structured SQS message with headers",
		},
		{
			name: "raw_github_payload_no_headers",
			messageBody: `{
				"action": "synchronize",
				"pull_request": {
					"id": 789,
					"number": 101
				}
			}`,
			eventType:     "pull_request",
			expectHeaders: false,
			description:   "Should handle raw GitHub payload without headers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := types.Message{
				Body:      &tt.messageBody,
				MessageId: aws.String("test-message-id"),
			}

			result, err := processor.parseMessage(tt.eventType, message)

			assert.NoError(t, err)
			assert.Equal(t, tt.eventType, result.EventType)

			if tt.expectHeaders {
				assert.NotNil(t, result.Headers, "Headers should be present")
				if host, ok := result.Headers["Host"].(string); ok {
					assert.Equal(t, tt.expectedHost, host, "Host header should match")
				}
			} else {
				if result.Headers != nil {
					assert.Empty(t, result.Headers, "Headers should be empty for raw payload")
				}
			}

			// Verify payload is preserved
			assert.NotNil(t, result.Payload, "Payload should always be present")
		})
	}
}

func TestProcessor_RealGitHubWebhookFormat(t *testing.T) {
	processor := &Processor{
		logger: zerolog.New(nil),
	}

	// Test with actual GitHub webhook JSON structure as it would appear in SQS
	githubWebhook := `{
		"headers": {
			"Host": "github.ghec.mycompany.com",
			"X-GitHub-Event": "pull_request",
			"X-GitHub-Delivery": "12345-67890-abcdef",
			"X-GitHub-Hook-ID": "987654321",
			"Content-Type": "application/json"
		},
		"payload": {
			"action": "opened",
			"number": 42,
			"pull_request": {
				"id": 123456789,
				"number": 42,
				"state": "open",
				"title": "Add new feature",
				"user": {
					"login": "developer"
				},
				"head": {
					"ref": "feature-branch",
					"sha": "abc123def456"
				},
				"base": {
					"ref": "main",
					"sha": "def456abc123"
				}
			},
			"repository": {
				"id": 987654321,
				"name": "my-repo",
				"full_name": "mycompany/my-repo"
			}
		}
	}`

	message := types.Message{
		Body:      &githubWebhook,
		MessageId: aws.String("sqs-message-id-12345"),
	}

	// Parse the message
	sqsMsg, err := processor.parseMessage("pull_request", message)
	assert.NoError(t, err)

	// Verify headers were extracted
	assert.NotNil(t, sqsMsg.Headers)
	assert.Equal(t, "github.ghec.mycompany.com", sqsMsg.Headers["Host"])
	assert.Equal(t, "pull_request", sqsMsg.Headers["X-GitHub-Event"])

	// Detect source from headers
	source := processor.detectSourceFromHeaders(sqsMsg)
	assert.Equal(t, "cloud", source, "GHEC host should route to cloud")

	// Verify payload is preserved
	var payload map[string]interface{}
	err = json.Unmarshal(sqsMsg.Payload, &payload)
	assert.NoError(t, err)
	assert.Equal(t, "opened", payload["action"])
	assert.Equal(t, float64(42), payload["number"])
}
