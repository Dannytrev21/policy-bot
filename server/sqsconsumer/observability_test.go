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
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestProcessor_Phase3_EnhancedMetrics tests the enhanced metrics with environment context
func TestProcessor_Phase3_EnhancedMetrics(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		environment string
		err         error
		wantMetrics []string
	}{
		{
			name:        "cloud_success_metrics",
			eventType:   "pull_request",
			environment: "cloud",
			err:         nil,
			wantMetrics: []string{
				"sqs.processing.time.pull_request",
				"sqs.processing.time.cloud.pull_request",
				"sqs.messages.processed.pull_request",
				"sqs.messages.processed.cloud.pull_request",
			},
		},
		{
			name:        "enterprise_success_metrics",
			eventType:   "status",
			environment: "enterprise",
			err:         nil,
			wantMetrics: []string{
				"sqs.processing.time.status",
				"sqs.processing.time.enterprise.status",
				"sqs.messages.processed.status",
				"sqs.messages.processed.enterprise.status",
			},
		},
		{
			name:        "cloud_failure_metrics",
			eventType:   "pull_request",
			environment: "cloud",
			err:         assert.AnError,
			wantMetrics: []string{
				"sqs.processing.time.pull_request",
				"sqs.processing.time.cloud.pull_request",
				"sqs.messages.failed.pull_request",
				"sqs.messages.failed.cloud.pull_request",
			},
		},
		{
			name:        "enterprise_failure_metrics",
			eventType:   "status",
			environment: "enterprise",
			err:         assert.AnError,
			wantMetrics: []string{
				"sqs.processing.time.status",
				"sqs.processing.time.enterprise.status",
				"sqs.messages.failed.status",
				"sqs.messages.failed.enterprise.status",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := metrics.NewRegistry()
			logger := zerolog.New(nil).Level(zerolog.Disabled)

			// Create empty handler lists
			var enterpriseHandlers []githubapp.EventHandler
			var cloudHandlers []githubapp.EventHandler

			processor := NewProcessor(
				&ProcessorConfig{},
				nil, // No SQS client needed for this test
				enterpriseHandlers,
				cloudHandlers,
				&MockScheduler{},
				&MockScheduler{},
				&MockScheduler{},
				logger,
				registry,
			)

			startTime := time.Now()
			processor.recordMetrics(tt.eventType, tt.environment, startTime, tt.err)

			// Verify all expected metrics exist
			for _, metricName := range tt.wantMetrics {
				assert.NotNil(t, registry.Get(metricName), "Expected metric %s to exist", metricName)
			}
		})
	}
}

// TestProcessor_Phase3_ContextEnrichment tests the enhanced context values
func TestProcessor_Phase3_ContextEnrichment(t *testing.T) {
	ctx := context.Background()

	// Add all context values as ProcessMessage does
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")
	ctx = context.WithValue(ctx, SQSEventEnvironment, "cloud")
	ctx = context.WithValue(ctx, SQSQueueName, "pull_request")
	ctx = context.WithValue(ctx, SQSMessageID, "test-message-id")
	ctx = context.WithValue(ctx, SQSReceiptHandle, "test-receipt-handle")

	// Verify all context values
	assert.Equal(t, "sqs", ctx.Value(SQSEventSourceKey))
	assert.Equal(t, "cloud", ctx.Value(SQSEventEnvironment))
	assert.Equal(t, "pull_request", ctx.Value(SQSQueueName))
	assert.Equal(t, "test-message-id", ctx.Value(SQSMessageID))
	assert.Equal(t, "test-receipt-handle", ctx.Value(SQSReceiptHandle))
}

// MockSQSClientForHealth implements SQSClient for health check testing
type MockSQSClientForHealth struct {
	GetQueueAttributesFunc func(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

func (m *MockSQSClientForHealth) DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return nil, nil
}

func (m *MockSQSClientForHealth) SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return nil, nil
}

func (m *MockSQSClientForHealth) GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	if m.GetQueueAttributesFunc != nil {
		return m.GetQueueAttributesFunc(ctx, params, optFns...)
	}
	return &sqs.GetQueueAttributesOutput{}, nil
}

// TestConsumer_Phase3_QueueHealthStruct tests the QueueHealth struct
func TestConsumer_Phase3_QueueHealthStruct(t *testing.T) {
	// Test that QueueHealth structure is properly defined
	health := QueueHealth{
		QueueName:        "pull_request",
		QueueURL:         "https://sqs.us-east-1.amazonaws.com/123/pr-queue",
		Status:           "healthy",
		ApproxMessages:   10,
		ApproxDelayed:    2,
		ApproxNotVisible: 5,
		CheckedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	assert.Equal(t, "pull_request", health.QueueName)
	assert.Equal(t, "healthy", health.Status)
	assert.Equal(t, int64(10), health.ApproxMessages)
	assert.Equal(t, int64(2), health.ApproxDelayed)
	assert.Equal(t, int64(5), health.ApproxNotVisible)
	assert.NotEmpty(t, health.CheckedAt)
}

// TestConsumer_Phase3_DLQConfig tests DLQ configuration
func TestConsumer_Phase3_DLQConfig(t *testing.T) {
	tests := []struct {
		name      string
		dlqConfig DLQConfig
	}{
		{
			name: "dlq_enabled_with_suffix",
			dlqConfig: DLQConfig{
				Enabled:         true,
				MaxReceiveCount: 3,
				QueueSuffix:     "-dlq",
			},
		},
		{
			name: "dlq_disabled",
			dlqConfig: DLQConfig{
				Enabled: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				DLQ: tt.dlqConfig,
			}

			assert.Equal(t, tt.dlqConfig.Enabled, config.DLQ.Enabled)
			if tt.dlqConfig.Enabled {
				assert.Equal(t, tt.dlqConfig.MaxReceiveCount, config.DLQ.MaxReceiveCount)
				assert.Equal(t, tt.dlqConfig.QueueSuffix, config.DLQ.QueueSuffix)
			}
		})
	}
}

// TestConsumer_Phase3_NoOpConsumerDetailedHealth tests that noOpConsumer implements DetailedHealth
func TestConsumer_Phase3_NoOpConsumerDetailedHealth(t *testing.T) {
	noop := &noOpConsumer{}
	ctx := context.Background()

	health, err := noop.DetailedHealth(ctx)

	assert.NoError(t, err)
	assert.NotNil(t, health)
	assert.Equal(t, 0, len(health))
}
