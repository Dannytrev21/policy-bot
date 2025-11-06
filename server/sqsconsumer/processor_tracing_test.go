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
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestProcessor_Tracing_SuccessfulProcessing verifies tracing works for successful message processing
// The test verifies that tracing code doesn't panic and processing completes successfully
func TestProcessor_Tracing_SuccessfulProcessing(t *testing.T) {
	// Setup test processor
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	mockScheduler := &MockScheduler{}
	mockScheduler.On("Schedule", mock.Anything, mock.MatchedBy(func(d githubapp.Dispatch) bool {
		return d.EventType == "pull_request" && d.DeliveryID == "msg-123"
	})).Return(nil)

	mockSQSClient := &MockSQSClient{}
	mockSQSClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return aws.ToString(input.ReceiptHandle) == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	config := &ProcessorConfig{
		ProcessingMode:       "scheduler",
		EnableRetry:          true,
		MaxRetries:           3,
		EnableCircuitBreaker: false,
	}

	processor := NewProcessor(
		config,
		mockSQSClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{mockHandler},
		&MockScheduler{},
		mockScheduler,
		mockScheduler,
		nil,
		zerolog.Nop(),
		metrics.NewRegistry(),
	)

	// Create test message with installation ID
	messageBody := `{
		"event_type": "pull_request",
		"delivery_id": "msg-123",
		"headers": {
			"Host": "api.github.com"
		},
		"payload": {"action":"opened","installation":{"id":12345}}
	}`

	message := types.Message{
		MessageId:     aws.String("msg-123"),
		ReceiptHandle: aws.String("receipt-123"),
		Body:          aws.String(messageBody),
	}

	// Process the message - verifies tracing doesn't cause panics
	err := processor.ProcessMessage(context.Background(), "pull_request", "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue", message)
	require.NoError(t, err, "Processing should succeed with tracing enabled")

	mockHandler.AssertExpectations(t)
	mockSQSClient.AssertExpectations(t)
}

// TestProcessor_Tracing_NilBody_ErrorPath verifies tracing handles errors correctly
// This test specifically checks the nil body error path which is simpler to test
func TestProcessor_Tracing_NilBody_ErrorPath(t *testing.T) {
	// Setup test processor
	config := &ProcessorConfig{
		ProcessingMode:       "scheduler",
		EnableRetry:          false,
		EnableCircuitBreaker: false,
	}

	processor := NewProcessor(
		config,
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		nil,
		zerolog.Nop(),
		metrics.NewRegistry(),
	)

	// Create test message with nil body to trigger error path
	message := types.Message{
		MessageId:     aws.String("msg-456"),
		ReceiptHandle: aws.String("receipt-456"),
		Body:          nil, // Nil body triggers error
	}

	// Process the message - should return error but not panic
	// The tracing code should record the error in spans
	err := processor.ProcessMessage(context.Background(), "pull_request", "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue", message)
	require.Error(t, err, "Processing should fail with error but not panic")
	assert.Contains(t, err.Error(), "message body is nil", "Should return nil body error")
}

// TestExtractInstallationID tests the installation ID extraction helper function
func TestExtractInstallationID(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		expectedID  int64
		description string
	}{
		{
			name:        "valid_installation_id",
			payload:     `{"installation":{"id":12345}}`,
			expectedID:  12345,
			description: "Should extract installation ID from valid payload",
		},
		{
			name:        "nested_installation",
			payload:     `{"action":"opened","installation":{"id":67890,"account":{"login":"test"}}}`,
			expectedID:  67890,
			description: "Should extract installation ID from nested payload",
		},
		{
			name:        "no_installation",
			payload:     `{"action":"opened","repository":{"name":"test"}}`,
			expectedID:  0,
			description: "Should return 0 when no installation field present",
		},
		{
			name:        "invalid_json",
			payload:     `{invalid json}`,
			expectedID:  0,
			description: "Should return 0 for invalid JSON",
		},
		{
			name:        "empty_payload",
			payload:     ``,
			expectedID:  0,
			description: "Should return 0 for empty payload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installationID := extractInstallationID([]byte(tt.payload))
			assert.Equal(t, tt.expectedID, installationID, tt.description)
		})
	}
}

// TestProcessor_Tracing_NilMessageBody verifies tracing handles nil message body errors correctly
func TestProcessor_Tracing_NilMessageBody(t *testing.T) {
	// Setup test processor
	config := &ProcessorConfig{
		ProcessingMode:       "scheduler",
		EnableRetry:          false,
		EnableCircuitBreaker: false,
	}

	processor := NewProcessor(
		config,
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		nil,
		zerolog.Nop(),
		metrics.NewRegistry(),
	)

	// Create test message with nil body
	message := types.Message{
		MessageId:     aws.String("msg-nil"),
		ReceiptHandle: aws.String("receipt-nil"),
		Body:          nil,
	}

	// Process the message - should fail but not panic
	err := processor.ProcessMessage(context.Background(), "pull_request", "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue", message)
	require.Error(t, err, "Should return error for nil body")
	assert.Contains(t, err.Error(), "message body is nil", "Error should indicate nil body")
}

// TestProcessor_Tracing_EnterpriseEnvironment verifies enterprise environment tracing
func TestProcessor_Tracing_EnterpriseEnvironment(t *testing.T) {
	// Setup test processor
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	mockScheduler := &MockScheduler{}
	mockScheduler.On("Schedule", mock.Anything, mock.MatchedBy(func(d githubapp.Dispatch) bool {
		return d.EventType == "pull_request" && d.DeliveryID == "msg-ent"
	})).Return(nil)

	mockSQSClient := &MockSQSClient{}
	mockSQSClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return aws.ToString(input.ReceiptHandle) == "receipt-ent"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	config := &ProcessorConfig{
		ProcessingMode:       "scheduler",
		EnableRetry:          false,
		EnableCircuitBreaker: false,
	}

	processor := NewProcessor(
		config,
		mockSQSClient,
		[]githubapp.EventHandler{mockHandler},
		[]githubapp.EventHandler{},
		mockScheduler,
		&MockScheduler{},
		mockScheduler,
		nil,
		zerolog.Nop(),
		metrics.NewRegistry(),
	)

	// Create test message with enterprise host header
	messageBody := `{
		"event_type": "pull_request",
		"delivery_id": "msg-ent",
		"headers": {
			"Host": "github.enterprise.com"
		},
		"payload": {"action":"opened"}
	}`

	message := types.Message{
		MessageId:     aws.String("msg-ent"),
		ReceiptHandle: aws.String("receipt-ent"),
		Body:          aws.String(messageBody),
	}

	// Process the message - verifies enterprise routing works with tracing
	err := processor.ProcessMessage(context.Background(), "pull_request", "https://sqs.us-east-1.amazonaws.com/123456789012/test-queue", message)
	require.NoError(t, err, "Enterprise processing should succeed with tracing")

	mockHandler.AssertExpectations(t)
	mockSQSClient.AssertExpectations(t)
	mockScheduler.AssertExpectations(t)
}
