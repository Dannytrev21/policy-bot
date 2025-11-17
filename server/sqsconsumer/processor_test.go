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
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockSQSClient implements a mock SQS client for testing
type MockSQSClient struct {
	mock.Mock
}

func (m *MockSQSClient) DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*sqs.DeleteMessageOutput), args.Error(1)
}

func (m *MockSQSClient) SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	args := m.Called(ctx, params)
	return args.Get(0).(*sqs.SendMessageOutput), args.Error(1)
}

func (m *MockSQSClient) GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sqs.GetQueueAttributesOutput), args.Error(1)
}

func (m *MockSQSClient) ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	args := m.Called(ctx, params)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sqs.ReceiveMessageOutput), args.Error(1)
}

// MockEventHandler implements a mock GitHub event handler
type MockEventHandler struct {
	mock.Mock
}

func (m *MockEventHandler) Handles() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

func (m *MockEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	args := m.Called(ctx, eventType, deliveryID, payload)
	return args.Error(0)
}

// MockScheduler implements a mock scheduler
type MockScheduler struct {
	mock.Mock
}

func (m *MockScheduler) Schedule(ctx context.Context, d githubapp.Dispatch) error {
	args := m.Called(ctx, d)
	return args.Error(0)
}

func TestProcessor_Creation(t *testing.T) {
	config := &ProcessorConfig{
		EnableRetry: true,
		MaxRetries:  3,
	}

	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	processor := NewProcessor(
		config,
		&MockSQSClient{},
		[]githubapp.EventHandler{mockHandler},
		[]githubapp.EventHandler{mockHandler},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	assert.NotNil(t, processor)
	assert.Equal(t, config, processor.config)
}

func TestProcessor_Metrics(t *testing.T) {
	config := &ProcessorConfig{
		EnableRetry: true,
		MaxRetries:  3,
	}

	logger := zerolog.Nop()
	registry := metrics.NewRegistry()

	processor := NewProcessor(
		config,
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(logger, registry),
		logger,
		registry,
	)

	// Check that metrics initialization doesn't crash
	assert.NotNil(t, processor)

	// Test metrics recording
	startTime := time.Now()
	processor.recordMetrics("pull_request", "cloud", startTime, nil)
	processor.recordMetrics("pull_request", "enterprise", startTime, assert.AnError)
}

func TestProcessor_ContextValues(t *testing.T) {
	// Test that SQS processing adds appropriate context values
	ctx := context.Background()
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	value := ctx.Value(SQSEventSourceKey)
	assert.Equal(t, "sqs", value)
}

// TestProcessor_ParseMessage_StructuredFormat tests parsing our structured format
func TestProcessor_ParseMessage_StructuredFormat(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{
		"event_type": "pull_request",
		"delivery_id": "12345678-1234-1234-1234-123456789012",
		"headers": {
			"X-GitHub-Event": "pull_request",
			"Host": "api.github.com"
		},
		"payload": {"action": "opened"},
		"source": "sqs"
	}`

	message := createSQSMessage("msg-123", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("pull_request", message, sqsMsg)

	assert.NoError(t, err)
	assert.Equal(t, "pull_request", sqsMsg.EventType)
	assert.Equal(t, "12345678-1234-1234-1234-123456789012", sqsMsg.DeliveryID)
	assert.NotNil(t, sqsMsg.Headers)
	assert.NotNil(t, sqsMsg.Payload)
}

// TestProcessor_ParseMessage_WebhookFormat tests parsing GitHub webhook format
func TestProcessor_ParseMessage_WebhookFormat(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{
		"headers": {
			"X-GitHub-Event": "pull_request",
			"X-GitHub-Delivery": "webhook-delivery-456",
			"Host": "github.example.com"
		},
		"payload": {"action": "synchronize"}
	}`

	message := createSQSMessage("msg-456", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("pull_request", message, sqsMsg)

	assert.NoError(t, err)
	assert.Equal(t, "pull_request", sqsMsg.EventType)
	// DeliveryID should be extracted from X-GitHub-Delivery header for idempotency
	assert.Equal(t, "webhook-delivery-456", sqsMsg.DeliveryID)
	assert.NotNil(t, sqsMsg.Headers)
	assert.Contains(t, string(sqsMsg.Payload), "synchronize")
}

// TestProcessor_ParseMessage_RawPayload tests parsing raw payload
func TestProcessor_ParseMessage_RawPayload(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{"action": "opened", "number": 42}`

	message := createSQSMessage("msg-789", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("status", message, sqsMsg)

	assert.NoError(t, err)
	assert.Equal(t, "status", sqsMsg.EventType)
	// DeliveryID falls back to SQS MessageId when no X-GitHub-Delivery header
	assert.Equal(t, "msg-789", sqsMsg.DeliveryID)
	assert.Contains(t, string(sqsMsg.Payload), "action")
	assert.Contains(t, string(sqsMsg.Payload), "42")
}

// TestProcessor_ParseMessage_WebhookWithSeparatePayload tests webhook format with separate payload field
func TestProcessor_ParseMessage_WebhookWithSeparatePayload(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{
		"headers": {
			"X-GitHub-Event": "issues",
			"X-GitHub-Delivery": "issues-delivery-789",
			"Host": "github.company.com"
		},
		"payload": {
			"action": "closed",
			"issue": {
				"number": 123
			}
		}
	}`

	message := createSQSMessage("msg-webhook-payload", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("issues", message, sqsMsg)

	assert.NoError(t, err)
	assert.Equal(t, "issues", sqsMsg.EventType)
	// DeliveryID should be extracted from X-GitHub-Delivery header
	assert.Equal(t, "issues-delivery-789", sqsMsg.DeliveryID)
	assert.NotNil(t, sqsMsg.Headers)
	assert.Contains(t, string(sqsMsg.Payload), "closed")
	assert.Contains(t, string(sqsMsg.Payload), "123")
}

// TestProcessor_ParseMessage_MalformedJSON tests handling of malformed JSON
func TestProcessor_ParseMessage_MalformedJSON(t *testing.T) {
	processor := createTestProcessor()

	// Invalid JSON - but parseMessage is designed to be permissive and still return a message
	messageBody := `{invalid json here`

	message := createSQSMessage("msg-bad", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("pull_request", message, sqsMsg)

	// Should still succeed - treats as raw payload
	assert.NoError(t, err)
	assert.Equal(t, "pull_request", sqsMsg.EventType)
	assert.Equal(t, "msg-bad", sqsMsg.DeliveryID)
	assert.NotNil(t, sqsMsg.Payload)
}

// TestProcessor_ParseMessage_EmptyBody tests handling of empty message body
func TestProcessor_ParseMessage_EmptyBody(t *testing.T) {
	processor := createTestProcessor()

	messageBody := ``

	message := createSQSMessage("msg-empty", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("issue_comment", message, sqsMsg)

	assert.NoError(t, err)
	assert.Equal(t, "issue_comment", sqsMsg.EventType)
	assert.NotNil(t, sqsMsg.Payload)
}

// TestProcessor_ParseMessage_NilPayload tests handling when payload is nil
func TestProcessor_ParseMessage_NilPayload(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{
		"event_type": "pull_request",
		"delivery_id": "test-123"
	}`

	message := createSQSMessage("msg-nil", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("pull_request", message, sqsMsg)

	assert.NoError(t, err)
	assert.NotNil(t, sqsMsg.Payload)
	assert.Greater(t, len(sqsMsg.Payload), 0)
}

// TestProcessor_ParseMessage_XGitHubDeliveryOverridesJSONDeliveryID tests that X-GitHub-Delivery header
// takes precedence over JSON delivery_id field for idempotency
func TestProcessor_ParseMessage_XGitHubDeliveryOverridesJSONDeliveryID(t *testing.T) {
	processor := createTestProcessor()

	// JSON has delivery_id, but headers have X-GitHub-Delivery which should take precedence
	messageBody := `{
		"event_type": "pull_request",
		"delivery_id": "json-delivery-id-should-be-overridden",
		"headers": {
			"X-GitHub-Delivery": "72d3162e-cc78-11e3-81ab-4c9367dc0958",
			"X-GitHub-Event": "pull_request"
		},
		"payload": {"action": "opened"}
	}`

	message := createSQSMessage("sqs-message-id", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("pull_request", message, sqsMsg)

	assert.NoError(t, err)
	// X-GitHub-Delivery header should override JSON delivery_id
	assert.Equal(t, "72d3162e-cc78-11e3-81ab-4c9367dc0958", sqsMsg.DeliveryID)
}

// TestProcessor_ParseMessage_XGitHubDeliveryStableAcrossRetries tests that retry messages
// with same X-GitHub-Delivery but different SQS MessageId use the stable GitHub ID
func TestProcessor_ParseMessage_XGitHubDeliveryStableAcrossRetries(t *testing.T) {
	processor := createTestProcessor()

	// Simulate original message
	messageBody := `{
		"headers": {
			"X-GitHub-Delivery": "stable-github-delivery-id",
			"X-GitHub-Event": "issues"
		},
		"payload": {"action": "opened", "issue": {"number": 42}}
	}`

	// First message has one SQS MessageId
	message1 := createSQSMessage("first-sqs-message-id", messageBody)
	sqsMsg1 := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg1)
	err := processor.parseMessage("issues", message1, sqsMsg1)

	assert.NoError(t, err)
	assert.Equal(t, "stable-github-delivery-id", sqsMsg1.DeliveryID)

	// Retry message has different SQS MessageId but same payload
	message2 := createSQSMessage("second-sqs-message-id-after-retry", messageBody)
	sqsMsg2 := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg2)
	err = processor.parseMessage("issues", message2, sqsMsg2)

	assert.NoError(t, err)
	// Both should have same DeliveryID (stable for idempotency)
	assert.Equal(t, "stable-github-delivery-id", sqsMsg2.DeliveryID)
	assert.Equal(t, sqsMsg1.DeliveryID, sqsMsg2.DeliveryID)
}

// TestProcessor_ParseMessage_FallbackToSQSMessageIdWhenNoHeader tests fallback behavior
// when X-GitHub-Delivery header is not present
func TestProcessor_ParseMessage_FallbackToSQSMessageIdWhenNoHeader(t *testing.T) {
	processor := createTestProcessor()

	// Headers present but no X-GitHub-Delivery
	messageBody := `{
		"headers": {
			"X-GitHub-Event": "push",
			"Host": "github.com"
		},
		"payload": {"ref": "refs/heads/main"}
	}`

	message := createSQSMessage("fallback-sqs-message-id", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("push", message, sqsMsg)

	assert.NoError(t, err)
	// Should fall back to SQS MessageId
	assert.Equal(t, "fallback-sqs-message-id", sqsMsg.DeliveryID)
}

// TestProcessor_ParseMessage_EmptyXGitHubDeliveryFallsBack tests that empty X-GitHub-Delivery
// still falls back to SQS MessageId
func TestProcessor_ParseMessage_EmptyXGitHubDeliveryFallsBack(t *testing.T) {
	processor := createTestProcessor()

	messageBody := `{
		"headers": {
			"X-GitHub-Delivery": "",
			"X-GitHub-Event": "push"
		},
		"payload": {"ref": "refs/heads/main"}
	}`

	message := createSQSMessage("fallback-for-empty", messageBody)
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)
	err := processor.parseMessage("push", message, sqsMsg)

	assert.NoError(t, err)
	// Empty X-GitHub-Delivery should fall back to SQS MessageId
	assert.Equal(t, "fallback-for-empty", sqsMsg.DeliveryID)
}

// TestProcessor_DeleteMessage_Success tests successful message deletion
func TestProcessor_DeleteMessage_Success(t *testing.T) {
	mockSQS := &MockSQSClient{}
	processor := createTestProcessorWithSQS(mockSQS)

	receiptHandle := "test-receipt-handle"
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	mockSQS.On("DeleteMessage", mock.Anything, mock.MatchedBy(func(input *sqs.DeleteMessageInput) bool {
		return *input.ReceiptHandle == receiptHandle && *input.QueueUrl == queueURL
	})).Return(&sqs.DeleteMessageOutput{}, nil)

	err := processor.deleteMessage(context.Background(), queueURL, &receiptHandle, zerolog.Nop())
	assert.NoError(t, err)
	mockSQS.AssertExpectations(t)
}

// TestProcessor_DeleteMessage_Failure tests network failure during deletion
func TestProcessor_DeleteMessage_Failure(t *testing.T) {
	mockSQS := &MockSQSClient{}
	processor := createTestProcessorWithSQS(mockSQS)

	receiptHandle := "test-receipt-handle"
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	mockSQS.On("DeleteMessage", mock.Anything, mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, assert.AnError)

	err := processor.deleteMessage(context.Background(), queueURL, &receiptHandle, zerolog.Nop())
	assert.Error(t, err)
	mockSQS.AssertExpectations(t)
}

// TestProcessor_HandleRetry_Success tests successful retry with exponential backoff
func TestProcessor_HandleRetry_Success(t *testing.T) {
	mockSQS := &MockSQSClient{}
	config := &ProcessorConfig{
		EnableRetry: true,
		MaxRetries:  5,
	}

	processor := NewProcessor(
		config,
		mockSQS,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	message := createSQSMessage("msg-retry", `{"action": "test"}`)
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	sqsMsg := SQSMessage{
		EventType:  "pull_request",
		DeliveryID: "msg-retry",
		Payload:    []byte(`{"action": "test"}`),
		RetryCount: 1,
	}

	// Should send message with retry count incremented
	mockSQS.On("SendMessage", mock.Anything, mock.MatchedBy(func(input *sqs.SendMessageInput) bool {
		return *input.QueueUrl == queueURL && input.DelaySeconds > 0
	})).Return(&sqs.SendMessageOutput{}, nil)

	// Should delete original message after sending retry
	mockSQS.On("DeleteMessage", mock.Anything, mock.Anything).
		Return(&sqs.DeleteMessageOutput{}, nil)

	err := processor.handleRetry(context.Background(), queueURL, message, &sqsMsg, zerolog.Nop())

	assert.NoError(t, err)
	mockSQS.AssertExpectations(t)
}

// TestProcessor_HandleRetry_SendFails tests handling when retry send fails
func TestProcessor_HandleRetry_SendFails(t *testing.T) {
	mockSQS := &MockSQSClient{}
	config := &ProcessorConfig{
		EnableRetry: true,
		MaxRetries:  3,
	}

	processor := NewProcessor(
		config,
		mockSQS,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	message := createSQSMessage("msg-retry-fail", `{"action": "test"}`)
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	sqsMsg := SQSMessage{
		EventType:  "status",
		DeliveryID: "msg-retry-fail",
		Payload:    []byte(`{"action": "test"}`),
		RetryCount: 0,
	}

	// Send message fails
	mockSQS.On("SendMessage", mock.Anything, mock.Anything).
		Return(&sqs.SendMessageOutput{}, assert.AnError)

	err := processor.handleRetry(context.Background(), queueURL, message, &sqsMsg, zerolog.Nop())

	assert.Error(t, err)
	mockSQS.AssertExpectations(t)
}

// TestProcessor_ProcessViaScheduler_Success tests successful scheduler processing
func TestProcessor_ProcessViaScheduler_Success(t *testing.T) {
	mockScheduler := &MockScheduler{}
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	processor := NewProcessor(
		&ProcessorConfig{ProcessingMode: "scheduler"},
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{mockHandler},
		&MockScheduler{},
		mockScheduler,
		mockScheduler,
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	sqsMsg := SQSMessage{
		EventType:  "pull_request",
		DeliveryID: "test-123",
		Payload:    []byte(`{"action": "opened"}`),
	}

	mockScheduler.On("Schedule", mock.Anything, mock.MatchedBy(func(d githubapp.Dispatch) bool {
		return d.EventType == "pull_request" && d.DeliveryID == "test-123"
	})).Return(nil)

	err := processor.processViaScheduler(
		context.Background(),
		&sqsMsg,
		mockHandler,
		mockScheduler,
		sqsMsg.Payload,
		zerolog.Nop(),
	)

	assert.NoError(t, err)
	mockScheduler.AssertExpectations(t)
}

// TestProcessor_ProcessViaScheduler_Failure tests scheduler processing failure
func TestProcessor_ProcessViaScheduler_Failure(t *testing.T) {
	mockScheduler := &MockScheduler{}
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"status"})

	processor := NewProcessor(
		&ProcessorConfig{ProcessingMode: "scheduler"},
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{mockHandler},
		&MockScheduler{},
		mockScheduler,
		mockScheduler,
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	sqsMsg := SQSMessage{
		EventType:  "status",
		DeliveryID: "test-456",
		Payload:    []byte(`{"state": "success"}`),
	}

	expectedError := assert.AnError
	mockScheduler.On("Schedule", mock.Anything, mock.Anything).Return(expectedError)

	err := processor.processViaScheduler(
		context.Background(),
		&sqsMsg,
		mockHandler,
		mockScheduler,
		sqsMsg.Payload,
		zerolog.Nop(),
	)

	assert.Error(t, err)
	assert.Equal(t, expectedError, err)
	mockScheduler.AssertExpectations(t)
}

// Helper functions for tests

func createTestProcessor() *Processor {
	return NewProcessor(
		&ProcessorConfig{
			EnableRetry: true,
			MaxRetries:  3,
		},
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
}

func createTestProcessorWithSQS(sqsClient SQSClient) *Processor {
	return NewProcessor(
		&ProcessorConfig{
			EnableRetry: true,
			MaxRetries:  3,
		},
		sqsClient,
		[]githubapp.EventHandler{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		&MockScheduler{},
		&MockScheduler{},
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)
}

func createSQSMessage(messageID, body string) types.Message {
	return types.Message{
		MessageId: &messageID,
		Body:      &body,
		Attributes: map[string]string{
			"ApproximateReceiveCount": "1",
		},
	}
}
