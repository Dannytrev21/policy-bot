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
	"encoding/json"
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

// TestSelectHandler_CloudDetection tests that cloud handlers are selected for cloud events
func TestSelectHandler_CloudDetection(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]interface{}
		source         string
		expectedEnv    string
		expectEnterprise bool
	}{
		{
			name: "cloud_no_host_header_defaults",
			headers: map[string]interface{}{
				"SomeOtherHeader": "value",
			},
			expectedEnv:    "cloud",
			expectEnterprise: false,
		},
		{
			name: "cloud_ghec_in_hostname",
			headers: map[string]interface{}{
				"Host": "ghec-12345.github.com",
			},
			expectedEnv:    "cloud",
			expectEnterprise: false,
		},
		{
			name: "enterprise_custom_host",
			headers: map[string]interface{}{
				"Host": "github.company.com",
			},
			expectedEnv:    "enterprise",
			expectEnterprise: true,
		},
		{
			name: "enterprise_ghes_host",
			headers: map[string]interface{}{
				"Host": "ghes.internal.net",
			},
			expectedEnv:    "enterprise",
			expectEnterprise: true,
		},
		{
			name:           "no_headers_defaults_to_cloud",
			headers:        nil,
			expectedEnv:    "cloud",
			expectEnterprise: false,
		},
		{
			name: "legacy_source_field_enterprise",
			source: "enterprise",
			headers: map[string]interface{}{},
			expectedEnv:    "enterprise",
			expectEnterprise: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloudHandler := &MockEventHandler{}
			cloudHandler.On("Handles").Return([]string{"pull_request"})

			enterpriseHandler := &MockEventHandler{}
			enterpriseHandler.On("Handles").Return([]string{"pull_request"})

			processor := NewProcessor(
				&ProcessorConfig{},
				&MockSQSClient{},
				[]githubapp.EventHandler{enterpriseHandler},
				[]githubapp.EventHandler{cloudHandler},
				&MockScheduler{},
				&MockScheduler{},
				&MockScheduler{},
				NewWorkerPoolManager(zerolog.Nop(), nil),
				zerolog.Nop(),
				nil,
			)

			sqsMsg := SQSMessage{
				EventType:  "pull_request",
				DeliveryID: "test-delivery-123",
				Headers:    tt.headers,
				Source:     tt.source,
			}

			handler, scheduler := processor.selectHandler(sqsMsg)

			// Verify handler and scheduler are selected (may be nil if no handler for event type)
			if tt.expectEnterprise {
				assert.NotNil(t, handler, "Should have an enterprise handler")
				assert.Equal(t, processor.enterpriseScheduler, scheduler, "Should select enterprise scheduler")
				// Verify it's from the enterprise handlers map
				_, exists := processor.enterpriseHandlers[sqsMsg.EventType]
				assert.True(t, exists, "Handler should be from enterprise handlers map")
			} else {
				assert.NotNil(t, handler, "Should have a cloud handler")
				assert.Equal(t, processor.cloudScheduler, scheduler, "Should select cloud scheduler")
				// Verify it's from the cloud handlers map
				_, exists := processor.cloudHandlers[sqsMsg.EventType]
				assert.True(t, exists, "Handler should be from cloud handlers map")
			}
		})
	}
}

// TestDetectSourceFromHeaders tests source detection logic
func TestDetectSourceFromHeaders(t *testing.T) {
	tests := []struct {
		name           string
		headers        map[string]interface{}
		source         string
		expectedSource string
	}{
		{
			name: "detect_cloud_from_ghec",
			headers: map[string]interface{}{
				"Host": "api.ghec.github.com",
			},
			expectedSource: "cloud",
		},
		{
			name: "detect_enterprise_from_custom_host",
			headers: map[string]interface{}{
				"Host": "github.mycompany.com",
			},
			expectedSource: "enterprise",
		},
		{
			name: "detect_enterprise_from_github_com",
			headers: map[string]interface{}{
				"Host": "api.github.com",
			},
			expectedSource: "enterprise", // No "ghec" in hostname = enterprise
		},
		{
			name:           "default_to_cloud_no_headers",
			headers:        nil,
			expectedSource: "cloud",
		},
		{
			name:           "legacy_source_field",
			source:         "enterprise",
			headers:        map[string]interface{}{},
			expectedSource: "enterprise",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewProcessor(
				&ProcessorConfig{},
				&MockSQSClient{},
				[]githubapp.EventHandler{},
				[]githubapp.EventHandler{},
				&MockScheduler{},
				&MockScheduler{},
				&MockScheduler{},
				NewWorkerPoolManager(zerolog.Nop(), nil),
				zerolog.Nop(),
				nil,
			)

			sqsMsg := SQSMessage{
				Headers: tt.headers,
				Source:  tt.source,
			}

			detectedSource := processor.detectSourceFromHeaders(sqsMsg)
			assert.Equal(t, tt.expectedSource, detectedSource)
		})
	}
}

// TestProcessMessage_CloudHandler tests end-to-end message processing for cloud
func TestProcessMessage_CloudHandler(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"pull_request"})
	// Note: In scheduler mode, the handler is not called directly - it's scheduled

	sqsClient := &MockSQSClient{}
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/pull_request" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	cloudScheduler := &MockScheduler{}
	cloudScheduler.On("Schedule", mock.Anything, mock.MatchedBy(func(d githubapp.Dispatch) bool {
		return d.EventType == "pull_request" &&
			d.DeliveryID == "test-delivery-123" &&
			string(d.Payload) == `{"action":"opened"}`
	})).Return(nil)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "scheduler", // Use scheduler mode
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		cloudScheduler,
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	sqsMessage := SQSMessage{
		EventType:  "pull_request",
		DeliveryID: "test-delivery-123",
		Headers: map[string]interface{}{
			"Host": "api.ghec.github.com", // Cloud host with ghec
		},
		Payload: json.RawMessage(`{"action":"opened"}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"pull_request",
		"https://sqs.us-east-1.amazonaws.com/123/pull_request",
		message,
	)

	assert.NoError(t, err)
	// Verify expectations (scheduler was called, message was deleted)
	cloudScheduler.AssertExpectations(t)
	sqsClient.AssertExpectations(t)
}

// TestProcessMessage_EnterpriseHandler tests end-to-end message processing for enterprise
func TestProcessMessage_EnterpriseHandler(t *testing.T) {
	enterpriseHandler := &MockEventHandler{}
	enterpriseHandler.On("Handles").Return([]string{"pull_request"})

	sqsClient := &MockSQSClient{}
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/pull_request" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	enterpriseScheduler := &MockScheduler{}
	enterpriseScheduler.On("Schedule", mock.Anything, mock.MatchedBy(func(d githubapp.Dispatch) bool {
		return d.EventType == "pull_request" &&
			d.DeliveryID == "test-delivery-123" &&
			string(d.Payload) == `{"action":"opened"}`
	})).Return(nil)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "scheduler",
		},
		sqsClient,
		[]githubapp.EventHandler{enterpriseHandler},
		[]githubapp.EventHandler{},
		enterpriseScheduler,
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	sqsMessage := SQSMessage{
		EventType:  "pull_request",
		DeliveryID: "test-delivery-123",
		Headers: map[string]interface{}{
			"Host": "github.company.com", // Enterprise host
		},
		Payload: json.RawMessage(`{"action":"opened"}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"pull_request",
		"https://sqs.us-east-1.amazonaws.com/123/pull_request",
		message,
	)

	assert.NoError(t, err)
	enterpriseScheduler.AssertExpectations(t)
	sqsClient.AssertExpectations(t)
}

// TestProcessMessage_DirectMode tests direct processing mode
func TestProcessMessage_DirectMode(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"status"})
	cloudHandler.On("Handle", mock.Anything, "status", "test-delivery-123", []byte(`{"state":"success"}`)).Return(nil)

	sqsClient := &MockSQSClient{}
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/status" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	registry := metrics.NewRegistry()
	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), registry)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct", // Direct mode
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		workerPoolMgr,
		zerolog.Nop(),
		registry,
	)

	// Set worker count for status events
	processor.SetQueueWorkers(map[string]int{
		"status": 5,
	})

	sqsMessage := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-delivery-123",
		Headers:    nil, // No Host header defaults to cloud
		Payload:    json.RawMessage(`{"state":"success"}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"status",
		"https://sqs.us-east-1.amazonaws.com/123/status",
		message,
	)

	assert.NoError(t, err)
	cloudHandler.AssertExpectations(t)
	sqsClient.AssertExpectations(t)

	// Verify worker pool was created
	stats := workerPoolMgr.GetStats()
	assert.Contains(t, stats, "status")
	assert.Equal(t, 5, stats["status"].Capacity)
}

// TestProcessMessage_ContextEnrichment tests that context is properly enriched
func TestProcessMessage_ContextEnrichment(t *testing.T) {
	var capturedCtx context.Context

	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"pull_request"})
	cloudHandler.On("Handle", mock.Anything, "pull_request", "test-delivery-123", []byte(`{}`)).
		Run(func(args mock.Arguments) {
			capturedCtx = args.Get(0).(context.Context)
		}).
		Return(nil)

	sqsClient := &MockSQSClient{}
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/pull_request" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	registry := metrics.NewRegistry()
	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), registry)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct",
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		workerPoolMgr,
		zerolog.Nop(),
		registry,
	)

	sqsMessage := SQSMessage{
		EventType:  "pull_request",
		DeliveryID: "test-delivery-123",
		Headers:    nil, // No Host header defaults to cloud
		Payload:    json.RawMessage(`{}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"pull_request",
		"https://sqs.us-east-1.amazonaws.com/123/pull_request",
		message,
	)

	require.NoError(t, err)
	require.NotNil(t, capturedCtx, "Context should be passed to handler")

	// Verify context values
	assert.Equal(t, "sqs", capturedCtx.Value(SQSEventSourceKey))
	assert.Equal(t, "cloud", capturedCtx.Value(SQSEventEnvironment))
	assert.Equal(t, "pull_request", capturedCtx.Value(SQSQueueName))
	assert.Equal(t, "msg-123", capturedCtx.Value(SQSMessageID))
	assert.Equal(t, "receipt-123", capturedCtx.Value(SQSReceiptHandle))
}

// TestProcessMessage_NoHandlerForEventType tests behavior when no handler exists
func TestProcessMessage_NoHandlerForEventType(t *testing.T) {
	sqsClient := &MockSQSClient{}
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/unknown_event" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	processor := NewProcessor(
		&ProcessorConfig{},
		sqsClient,
		[]githubapp.EventHandler{}, // No handlers
		[]githubapp.EventHandler{}, // No handlers
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	sqsMessage := SQSMessage{
		EventType:  "unknown_event",
		DeliveryID: "test-delivery-123",
		Payload:    json.RawMessage(`{}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"unknown_event",
		"https://sqs.us-east-1.amazonaws.com/123/unknown_event",
		message,
	)

	// Should delete the message since we can't process it
	assert.NoError(t, err)
	sqsClient.AssertExpectations(t)
}

// TestProcessMessage_HandlerError tests error handling when handler fails
func TestProcessMessage_HandlerError(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"status"})
	handlerErr := assert.AnError
	cloudHandler.On("Handle", mock.Anything, "status", "test-delivery-123", []byte(`{"state":"error"}`)).Return(handlerErr)

	sqsClient := &MockSQSClient{}
	// Message should NOT be deleted when handler fails

	registry := metrics.NewRegistry()
	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), registry)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct",
			EnableRetry:    false, // Disable retry for this test
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		workerPoolMgr,
		zerolog.Nop(),
		registry,
	)

	sqsMessage := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-delivery-123",
		Payload:    json.RawMessage(`{"state":"error"}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"status",
		"https://sqs.us-east-1.amazonaws.com/123/status",
		message,
	)

	// Should return error from handler
	assert.Error(t, err)
	cloudHandler.AssertExpectations(t)
	// sqsClient should NOT have DeleteMessage called
	sqsClient.AssertNotCalled(t, "DeleteMessage")
}

// TestProcessMessage_RetryLogic tests retry with exponential backoff
func TestProcessMessage_RetryLogic(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"status"})
	cloudHandler.On("Handle", mock.Anything, "status", "test-delivery-123", []byte(`{}`)).Return(assert.AnError)

	sqsClient := &MockSQSClient{}

	// Expect retry message to be sent
	sqsClient.On("SendMessage", mock.Anything, mock.MatchedBy(func(input *sqs.SendMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/status" &&
			input.DelaySeconds > 0
	})).Return(&sqs.SendMessageOutput{}, nil)

	// Expect original message to be deleted after retry is queued
	sqsClient.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.QueueUrl == "https://sqs.us-east-1.amazonaws.com/123/status" &&
			*input.ReceiptHandle == "receipt-123"
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	registry := metrics.NewRegistry()
	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), registry)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct",
			EnableRetry:    true,
			MaxRetries:     3,
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		workerPoolMgr,
		zerolog.Nop(),
		registry,
	)

	sqsMessage := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-delivery-123",
		RetryCount: 0, // First attempt
		Payload:    json.RawMessage(`{}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"status",
		"https://sqs.us-east-1.amazonaws.com/123/status",
		message,
	)

	// Should succeed (retry was queued and original deleted)
	assert.NoError(t, err)
	cloudHandler.AssertExpectations(t)
	sqsClient.AssertExpectations(t)
}

// TestProcessMessage_MaxRetriesExceeded tests behavior when max retries exceeded
func TestProcessMessage_MaxRetriesExceeded(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"status"})
	cloudHandler.On("Handle", mock.Anything, "status", "test-delivery-123", []byte(`{}`)).Return(assert.AnError)

	sqsClient := &MockSQSClient{}

	registry := metrics.NewRegistry()
	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), registry)

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct",
			EnableRetry:    true,
			MaxRetries:     3,
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		workerPoolMgr,
		zerolog.Nop(),
		registry,
	)

	sqsMessage := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-delivery-123",
		RetryCount: 3, // Already at max
		Payload:    json.RawMessage(`{}`),
	}

	messageBody, _ := json.Marshal(sqsMessage)
	messageID := "msg-123"
	receiptHandle := "receipt-123"

	message := types.Message{
		Body:          aws.String(string(messageBody)),
		MessageId:     &messageID,
		ReceiptHandle: &receiptHandle,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"status",
		"https://sqs.us-east-1.amazonaws.com/123/status",
		message,
	)

	// Should return error (no more retries)
	assert.Error(t, err)
	cloudHandler.AssertExpectations(t)
	// Message should NOT be deleted (will go to DLQ via SQS)
	sqsClient.AssertNotCalled(t, "DeleteMessage")
}

// TestProcessMessage_NilBody tests handling of nil message body
func TestProcessMessage_NilBody(t *testing.T) {
	processor := NewProcessor(
		&ProcessorConfig{},
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	messageID := "msg-123"
	message := types.Message{
		Body:      nil, // Nil body
		MessageId: &messageID,
	}

	err := processor.ProcessMessage(
		context.Background(),
		"status",
		"https://sqs.us-east-1.amazonaws.com/123/status",
		message,
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "message body is nil")
}

// TestProcessViaDirect_WorkerPoolError tests error from worker pool manager
func TestProcessViaDirect_WorkerPoolError(t *testing.T) {
	cloudHandler := &MockEventHandler{}
	cloudHandler.On("Handles").Return([]string{"status"})

	sqsClient := &MockSQSClient{}

	processor := NewProcessor(
		&ProcessorConfig{
			ProcessingMode: "direct",
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{cloudHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		nil, // Nil worker pool manager
		zerolog.Nop(),
		nil,
	)

	sqsMessage := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-delivery-123",
		Payload:    json.RawMessage(`{}`),
	}

	err := processor.processViaDirect(
		context.Background(),
		sqsMessage,
		cloudHandler,
		[]byte(`{}`),
		zerolog.Nop(),
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "worker pool manager not initialized")
}

// TestParseMessage tests message parsing with various formats
func TestParseMessage(t *testing.T) {
	tests := []struct {
		name           string
		messageBody    string
		expectedEvent  string
		expectedPayload string
		expectError    bool
	}{
		{
			name: "structured_sqs_message",
			messageBody: `{
				"event_type": "pull_request",
				"delivery_id": "abc-123",
				"headers": {"Host": "api.github.com"},
				"payload": {"action": "opened"}
			}`,
			expectedEvent:  "pull_request",
			expectedPayload: `{"action":"opened"}`,
			expectError:    false,
		},
		{
			name: "webhook_with_headers",
			messageBody: `{
				"headers": {"Host": "github.company.com"},
				"payload": {"action": "closed"}
			}`,
			expectedEvent:  "status",
			expectedPayload: `{"action":"closed"}`,
			expectError:    false,
		},
		{
			name:           "raw_payload",
			messageBody:    `{"action": "synchronize"}`,
			expectedEvent:  "status",
			expectedPayload: `{"action": "synchronize"}`,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewProcessor(
				&ProcessorConfig{},
				&MockSQSClient{},
				[]githubapp.EventHandler{},
				[]githubapp.EventHandler{},
				&MockScheduler{},
				&MockScheduler{},
				&MockScheduler{},
				NewWorkerPoolManager(zerolog.Nop(), nil),
				zerolog.Nop(),
				nil,
			)

			messageID := "test-msg-id"
			message := types.Message{
				Body:      aws.String(tt.messageBody),
				MessageId: &messageID,
			}

			sqsMsg, err := processor.parseMessage("status", message)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.expectedEvent != "" {
					assert.Equal(t, tt.expectedEvent, sqsMsg.EventType)
				}
				if tt.expectedPayload != "" {
					assert.JSONEq(t, tt.expectedPayload, string(sqsMsg.Payload))
				}
			}
		})
	}
}
