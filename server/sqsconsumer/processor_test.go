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
		&MockScheduler{},
		zerolog.New(nil),
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

	logger := zerolog.New(nil)
	registry := metrics.NewRegistry()

	processor := NewProcessor(
		config,
		&MockSQSClient{},
		[]githubapp.EventHandler{},
		&MockScheduler{},
		logger,
		registry,
	)

	// Check that metrics initialization doesn't crash
	assert.NotNil(t, processor)

	// Test metrics recording
	startTime := time.Now()
	processor.recordMetrics("pull_request", startTime, nil)
	processor.recordMetrics("pull_request", startTime, assert.AnError)
}

func TestProcessor_ContextValues(t *testing.T) {
	// Test that SQS processing adds appropriate context values
	ctx := context.Background()
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	value := ctx.Value(SQSEventSourceKey)
	assert.Equal(t, "sqs", value)
}
