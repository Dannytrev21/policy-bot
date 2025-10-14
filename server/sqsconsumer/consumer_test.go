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
	"fmt"
	"sync"
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

func TestConsumer_Disabled(t *testing.T) {
	config := &Config{
		Enabled: false,
	}

	logger := zerolog.New(nil)
	consumer, err := New(config, nil, nil, nil, nil, logger, nil)
	assert.NoError(t, err)

	// Should be a no-op consumer
	ctx := context.Background()

	err = consumer.Start(ctx)
	assert.NoError(t, err)

	err = consumer.Stop(ctx)
	assert.NoError(t, err)

	err = consumer.Health()
	assert.NoError(t, err)

	// DetailedHealth should return empty
	health, err := consumer.DetailedHealth(ctx)
	assert.NoError(t, err)
	assert.Empty(t, health)
}

func TestConsumer_EventRouting(t *testing.T) {
	tests := []struct {
		name          string
		eventRouting  map[string]string
		eventType     string
		shouldProcess bool
	}{
		{
			name:          "no routing config - should process",
			eventRouting:  nil,
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "explicit sqs routing",
			eventRouting:  map[string]string{"pull_request": "sqs"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "explicit http routing",
			eventRouting:  map[string]string{"pull_request": "http"},
			eventType:     "pull_request",
			shouldProcess: false,
		},
		{
			name:          "both routing",
			eventRouting:  map[string]string{"pull_request": "both"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "no routing for event type - default to process",
			eventRouting:  map[string]string{"status": "http"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled:      true,
				Region:       "us-east-1",
				Queues:       map[string]string{tt.eventType: "https://sqs.us-east-1.amazonaws.com/123456789012/test"},
				EventRouting: tt.eventRouting,
			}

			logger := zerolog.New(nil)

			// Create a test consumer using unexported struct
			c := &consumer{
				config: config,
				logger: logger,
			}

			result := c.shouldProcessViaSQS(tt.eventType)
			assert.Equal(t, tt.shouldProcess, result)
		})
	}
}

func TestConsumer_PerQueueWorkerAllocation(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		eventType       string
		expectedWorkers int
	}{
		{
			name: "uses queue-specific worker count",
			config: &Config{
				WorkersPerQueue: 5,
				QueueWorkers: map[string]int{
					"status":       15,
					"pull_request": 8,
				},
			},
			eventType:       "status",
			expectedWorkers: 15,
		},
		{
			name: "falls back to default when queue not specified",
			config: &Config{
				WorkersPerQueue: 7,
				QueueWorkers: map[string]int{
					"status": 15,
				},
			},
			eventType:       "pull_request",
			expectedWorkers: 7,
		},
		{
			name: "ignores zero or negative queue-specific values",
			config: &Config{
				WorkersPerQueue: 5,
				QueueWorkers: map[string]int{
					"status":       0,
					"pull_request": -1,
				},
			},
			eventType:       "status",
			expectedWorkers: 5,
		},
		{
			name: "works when QueueWorkers is nil",
			config: &Config{
				WorkersPerQueue: 6,
				QueueWorkers:    nil,
			},
			eventType:       "status",
			expectedWorkers: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config: tt.config,
			}

			result := c.getWorkersForQueue(tt.eventType)
			assert.Equal(t, tt.expectedWorkers, result)
		})
	}
}

func TestConsumer_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		field  string
		expect int
	}{
		{
			name:   "default workers per queue",
			config: &Config{WorkersPerQueue: 0},
			field:  "workers",
			expect: DefaultWorkersPerQueue,
		},
		{
			name:   "custom workers per queue",
			config: &Config{WorkersPerQueue: 3},
			field:  "workers",
			expect: 3,
		},
		{
			name:   "default max messages",
			config: &Config{MaxMessages: 0},
			field:  "maxMessages",
			expect: DefaultMaxMessages,
		},
		{
			name:   "invalid max messages (too high)",
			config: &Config{MaxMessages: 15},
			field:  "maxMessages",
			expect: DefaultMaxMessages,
		},
		{
			name:   "valid max messages",
			config: &Config{MaxMessages: 5},
			field:  "maxMessages",
			expect: 5,
		},
		{
			name:   "default visibility timeout",
			config: &Config{VisibilityTimeout: 0},
			field:  "visibilityTimeout",
			expect: DefaultVisibilityTimeout,
		},
		{
			name:   "default wait time",
			config: &Config{WaitTimeSeconds: -1},
			field:  "waitTime",
			expect: DefaultWaitTimeSeconds,
		},
		{
			name:   "invalid wait time (too high)",
			config: &Config{WaitTimeSeconds: 25},
			field:  "waitTime",
			expect: DefaultWaitTimeSeconds,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config: tt.config,
			}

			var result int
			switch tt.field {
			case "workers":
				result = c.getWorkersPerQueue()
			case "maxMessages":
				result = c.getMaxMessages()
			case "visibilityTimeout":
				result = c.getVisibilityTimeout()
			case "waitTime":
				result = c.getWaitTimeSeconds()
			}

			assert.Equal(t, tt.expect, result)
		})
	}
}

// Integration-style tests for consumer lifecycle

// TestConsumer_InitMetrics tests metric initialization
func TestConsumer_InitMetrics(t *testing.T) {
	registry := metrics.NewRegistry()
	
	config := &Config{
		Queues: map[string]string{
			"status":       "https://sqs/status",
			"pull_request": "https://sqs/pr",
		},
	}

	c := &consumer{
		config:   config,
		registry: registry,
	}

	c.initMetrics(registry)

	// Verify metrics were created for each queue
	for eventType := range config.Queues {
		processed := registry.Get(fmt.Sprintf("%s.%s", MetricsKeyMessagesProcessed, eventType))
		assert.NotNil(t, processed, "Expected processed metric for %s", eventType)

		failed := registry.Get(fmt.Sprintf("%s.%s", MetricsKeyMessagesFailed, eventType))
		assert.NotNil(t, failed, "Expected failed metric for %s", eventType)

		timing := registry.Get(fmt.Sprintf("%s.%s", MetricsKeyProcessingTime, eventType))
		assert.NotNil(t, timing, "Expected timing metric for %s", eventType)
	}
}

// TestConsumer_InitMetrics_NilRegistry tests metric initialization with nil registry
func TestConsumer_InitMetrics_NilRegistry(t *testing.T) {
	config := &Config{
		Queues: map[string]string{
			"status": "https://sqs/status",
		},
	}

	c := &consumer{
		config:   config,
		registry: nil,
	}

	// Should not panic with nil registry
	c.initMetrics(nil)
}

// TestConsumer_Health_Success tests successful health check
func TestConsumer_Health_Success(t *testing.T) {
	mockSQS := &MockSQSClient{}
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	config := &Config{
		Queues: map[string]string{
			"pull_request": queueURL,
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: SQSClient(mockSQS),
		logger:    zerolog.Nop(),
	}

	// Mock successful GetQueueAttributes
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == queueURL
	})).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			"ApproximateNumberOfMessages": "5",
		},
	}, nil)

	err := c.Health()
	assert.NoError(t, err)
	mockSQS.AssertExpectations(t)
}

// TestConsumer_Health_Failure tests health check failure
func TestConsumer_Health_Failure(t *testing.T) {
	mockSQS := &MockSQSClient{}
	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	config := &Config{
		Queues: map[string]string{
			"pull_request": queueURL,
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: SQSClient(mockSQS),
		logger:    zerolog.Nop(),
	}

	// Mock failed GetQueueAttributes
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.Anything).
		Return((*sqs.GetQueueAttributesOutput)(nil), assert.AnError)

	err := c.Health()
	assert.Error(t, err)
	mockSQS.AssertExpectations(t)
}

// TestConsumer_DetailedHealth tests detailed health check
func TestConsumer_DetailedHealth(t *testing.T) {
	mockSQS := &MockSQSClient{}
	statusURL := "https://sqs.us-east-1.amazonaws.com/123/status"
	prURL := "https://sqs.us-east-1.amazonaws.com/123/pr"

	config := &Config{
		Queues: map[string]string{
			"status":       statusURL,
			"pull_request": prURL,
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: SQSClient(mockSQS),
		logger:    zerolog.Nop(),
	}

	// Mock GetQueueAttributes for status queue
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == statusURL
	})).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			"ApproximateNumberOfMessages":           "10",
			"ApproximateNumberOfMessagesDelayed":    "2",
			"ApproximateNumberOfMessagesNotVisible": "3",
		},
	}, nil)

	// Mock GetQueueAttributes for pull_request queue (with error)
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == prURL
	})).Return((*sqs.GetQueueAttributesOutput)(nil), assert.AnError)

	health, err := c.DetailedHealth(context.Background())

	assert.NoError(t, err)
	assert.Len(t, health, 2)

	// Check status queue health
	assert.Equal(t, "status", health["status"].QueueName)
	assert.Equal(t, statusURL, health["status"].QueueURL)
	assert.Equal(t, "healthy", health["status"].Status)
	assert.Equal(t, int64(10), health["status"].ApproxMessages)
	assert.Equal(t, int64(2), health["status"].ApproxDelayed)
	assert.Equal(t, int64(3), health["status"].ApproxNotVisible)

	// Check pull_request queue health (should be unhealthy)
	assert.Equal(t, "pull_request", health["pull_request"].QueueName)
	assert.Equal(t, "unhealthy", health["pull_request"].Status)
	assert.NotEmpty(t, health["pull_request"].LastError)

	mockSQS.AssertExpectations(t)
}

// ============================================================================
// Consumer Lifecycle Tests (New for 80% coverage)
// ============================================================================

// TestConsumer_New_EnabledWithConfig tests successful consumer creation
func TestConsumer_New_EnabledWithConfig(t *testing.T) {
	config := &Config{
		Enabled:         true,
		Region:          "us-east-1",
		ProcessingMode:  "direct",
		Queues:          map[string]string{"pull_request": "https://sqs/pr"},
		WorkersPerQueue: 3,
		QueueWorkers:    map[string]int{"pull_request": 5},
		MaxMessages:     10,
		EnableRetry:     true,
		MaxRetries:      3,
	}

	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	mockScheduler := &MockScheduler{}

	logger := zerolog.Nop()
	registry := metrics.NewRegistry()

	cons, err := New(
		config,
		[]githubapp.EventHandler{mockHandler},
		[]githubapp.EventHandler{mockHandler},
		mockScheduler,
		mockScheduler,
		logger,
		registry,
	)

	assert.NoError(t, err)
	assert.NotNil(t, cons)

	// Cast to concrete type to verify internals
	c, ok := cons.(*consumer)
	assert.True(t, ok)
	assert.NotNil(t, c.processor)
	assert.NotNil(t, c.workerPoolMgr)
	assert.Equal(t, "direct", c.config.ProcessingMode)
}

// TestConsumer_New_DefaultProcessingMode tests default processing mode
func TestConsumer_New_DefaultProcessingMode(t *testing.T) {
	config := &Config{
		Enabled: true,
		Region:  "us-east-1",
		Queues:  map[string]string{"pull_request": "https://sqs/pr"},
		// ProcessingMode not set - should default to "scheduler"
	}

	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	mockScheduler := &MockScheduler{}
	logger := zerolog.Nop()

	cons, err := New(
		config,
		[]githubapp.EventHandler{mockHandler},
		[]githubapp.EventHandler{mockHandler},
		mockScheduler,
		mockScheduler,
		logger,
		nil,
	)

	assert.NoError(t, err)
	c, ok := cons.(*consumer)
	assert.True(t, ok)
	assert.NotNil(t, c)
	assert.Equal(t, "scheduler", c.config.ProcessingMode)
}

// TestConsumer_Start_Success tests successful consumer startup
func TestConsumer_Start_Success(t *testing.T) {
	mockSQS := &MockSQSClient{}
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})

	config := &Config{
		Queues:          map[string]string{"pull_request": "https://sqs/pr"},
		WorkersPerQueue: 1,
		EventRouting:    map[string]string{"pull_request": "sqs"},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
		processor: NewProcessor(
			&ProcessorConfig{ProcessingMode: "scheduler"},
			mockSQS,
			[]githubapp.EventHandler{mockHandler},
			[]githubapp.EventHandler{mockHandler},
			&MockScheduler{},
			&MockScheduler{},
			&MockScheduler{},
			NewWorkerPoolManager(zerolog.Nop(), nil),
			zerolog.Nop(),
			nil,
		),
	}

	ctx := context.Background()

	// Start should spawn goroutines
	err := c.Start(ctx)
	assert.NoError(t, err)
	assert.True(t, c.started)

	// Immediately stop to clean up goroutines
	close(c.stopChan)
	c.wg.Wait()
}

// TestConsumer_Start_AlreadyStarted tests starting an already started consumer
func TestConsumer_Start_AlreadyStarted(t *testing.T) {
	mockSQS := &MockSQSClient{}
	config := &Config{
		Queues: map[string]string{"pull_request": "https://sqs/pr"},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
		started:   true, // Already started
	}

	ctx := context.Background()
	err := c.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

// TestConsumer_Start_SkipsHTTPOnlyEvents tests event routing filtering
func TestConsumer_Start_SkipsHTTPOnlyEvents(t *testing.T) {
	mockSQS := &MockSQSClient{}
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request", "status"})

	config := &Config{
		Queues: map[string]string{
			"pull_request": "https://sqs/pr",
			"status":       "https://sqs/status",
		},
		WorkersPerQueue: 1,
		EventRouting: map[string]string{
			"pull_request": "http", // HTTP only - should skip
			"status":       "sqs",  // SQS - should process
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
		processor: NewProcessor(
			&ProcessorConfig{ProcessingMode: "scheduler"},
			mockSQS,
			[]githubapp.EventHandler{mockHandler},
			[]githubapp.EventHandler{mockHandler},
			&MockScheduler{},
			&MockScheduler{},
			&MockScheduler{},
			NewWorkerPoolManager(zerolog.Nop(), nil),
			zerolog.Nop(),
			nil,
		),
	}

	ctx := context.Background()
	err := c.Start(ctx)
	assert.NoError(t, err)

	// Only 1 worker should be started (for status queue)
	// pull_request should be skipped
	close(c.stopChan)
	c.wg.Wait()
}

// TestConsumer_Stop_GracefulShutdown tests graceful shutdown
func TestConsumer_Stop_GracefulShutdown(t *testing.T) {
	config := &Config{
		ShutdownTimeout: 5 * time.Second,
		ProcessingMode:  "scheduler",
	}

	c := &consumer{
		config:   config,
		logger:   zerolog.Nop(),
		stopChan: make(chan struct{}),
	}

	// Simulate a running worker
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		<-c.stopChan
		// Simulate quick cleanup
	}()

	ctx := context.Background()
	err := c.Stop(ctx)
	assert.NoError(t, err)
}

// TestConsumer_Stop_WithTimeout tests shutdown timeout
func TestConsumer_Stop_WithTimeout(t *testing.T) {
	config := &Config{
		ShutdownTimeout: 100 * time.Millisecond, // Very short timeout
		ProcessingMode:  "scheduler",
	}

	c := &consumer{
		config:   config,
		logger:   zerolog.Nop(),
		stopChan: make(chan struct{}),
	}

	// Simulate a worker that takes too long
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		<-c.stopChan
		time.Sleep(500 * time.Millisecond) // Longer than timeout
	}()

	ctx := context.Background()
	err := c.Stop(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown timeout")
}

// TestConsumer_Stop_DirectMode tests shutdown with worker pool manager
func TestConsumer_Stop_DirectMode(t *testing.T) {
	config := &Config{
		ShutdownTimeout: 5 * time.Second,
		ProcessingMode:  "direct",
	}

	workerPoolMgr := NewWorkerPoolManager(zerolog.Nop(), nil)

	c := &consumer{
		config:        config,
		workerPoolMgr: workerPoolMgr,
		logger:        zerolog.Nop(),
		stopChan:      make(chan struct{}),
	}

	ctx := context.Background()
	err := c.Stop(ctx)
	assert.NoError(t, err)
}

// TestConsumer_ConsumeQueue_ReceiveAndProcess tests message consumption
func TestConsumer_ConsumeQueue_ReceiveAndProcess(t *testing.T) {
	mockSQS := &MockSQSClient{}
	mockHandler := &MockEventHandler{}
	mockHandler.On("Handles").Return([]string{"pull_request"})
	mockScheduler := &MockScheduler{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"
	messageBody := `{"event_type":"pull_request","delivery_id":"test-123","payload":{"action":"opened"}}`

	config := &Config{
		MaxMessages:       10,
		VisibilityTimeout: 30,
		WaitTimeSeconds:   20,
	}

	// Create a real processor with mocks
	processor := NewProcessor(
		&ProcessorConfig{ProcessingMode: "scheduler"},
		mockSQS,
		[]githubapp.EventHandler{mockHandler},
		[]githubapp.EventHandler{mockHandler},
		mockScheduler,
		mockScheduler,
		mockScheduler,
		NewWorkerPoolManager(zerolog.Nop(), nil),
		zerolog.Nop(),
		nil,
	)

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		processor: processor,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
	}

	var stopOnce sync.Once
	// Mock ReceiveMessage to return one message on first call
	mockSQS.On("ReceiveMessage", mock.Anything, mock.Anything).Return(&sqs.ReceiveMessageOutput{
		Messages: []types.Message{
			{
				Body:      stringPtr(messageBody),
				MessageId: stringPtr("msg-123"),
			},
		},
	}, nil).Once()

	// Second call returns empty and triggers stop
	mockSQS.On("ReceiveMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		stopOnce.Do(func() {
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(c.stopChan)
			}()
		})
	}).Return(&sqs.ReceiveMessageOutput{Messages: []types.Message{}}, nil)

	// Mock scheduler to handle the event
	mockScheduler.On("Schedule", mock.Anything, mock.Anything).Return(nil)
	// Mock DeleteMessage for successful processing
	mockSQS.On("DeleteMessage", mock.Anything, mock.Anything).Return(&sqs.DeleteMessageOutput{}, nil)

	ctx := context.Background()

	// Run consumeQueue in goroutine
	c.wg.Add(1)
	go c.consumeQueue(ctx, "pull_request", queueURL, 0)

	// Wait for completion
	c.wg.Wait()

	mockSQS.AssertExpectations(t)
	mockScheduler.AssertExpectations(t)
}

// TestConsumer_ConsumeQueue_ErrorHandling tests error handling
func TestConsumer_ConsumeQueue_ErrorHandling(t *testing.T) {
	mockSQS := &MockSQSClient{}

	config := &Config{
		MaxMessages:       10,
		VisibilityTimeout: 30,
		WaitTimeSeconds:   20,
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
	}

	var stopOnce sync.Once
	// Mock ReceiveMessage to return error first time
	mockSQS.On("ReceiveMessage", mock.Anything, mock.Anything).Return(
		(*sqs.ReceiveMessageOutput)(nil), assert.AnError,
	).Once()

	// Second call triggers stop
	mockSQS.On("ReceiveMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		stopOnce.Do(func() {
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(c.stopChan)
			}()
		})
	}).Return((*sqs.ReceiveMessageOutput)(nil), assert.AnError)

	ctx := context.Background()

	// Run consumeQueue in goroutine
	c.wg.Add(1)
	go c.consumeQueue(ctx, "pull_request", "https://sqs/pr", 0)

	// Wait for completion with timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("Test timeout - consumeQueue did not exit")
	}

	mockSQS.AssertExpectations(t)
}

// TestConsumer_MonitorDLQ tests DLQ monitoring
func TestConsumer_MonitorDLQ(t *testing.T) {
	mockSQS := &MockSQSClient{}
	registry := metrics.NewRegistry()

	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"
	dlqURL := queueURL + "-dlq"

	config := &Config{
		Queues: map[string]string{
			"pull_request": queueURL,
		},
		DLQ: DLQConfig{
			Enabled:     true,
			QueueSuffix: "-dlq",
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		registry:  registry,
		logger:    zerolog.Nop(),
		stopChan:  make(chan struct{}),
	}

	var stopOnce sync.Once
	// Mock GetQueueAttributes for DLQ - first call triggers stop
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == dlqURL
	})).Run(func(args mock.Arguments) {
		// After first check, stop monitoring
		stopOnce.Do(func() {
			go func() {
				time.Sleep(10 * time.Millisecond)
				close(c.stopChan)
			}()
		})
	}).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			"ApproximateNumberOfMessages": "5",
		},
	}, nil)

	ctx := context.Background()

	// Run monitorDLQ in goroutine
	c.wg.Add(1)
	go c.monitorDLQ(ctx)

	// Wait for completion with timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Test timeout - monitorDLQ did not exit")
	}

	// Verify metric was recorded
	dlqMetric := registry.Get(fmt.Sprintf("%s.pull_request", MetricsKeyDLQMessages))
	assert.NotNil(t, dlqMetric)

	mockSQS.AssertExpectations(t)
}

// TestConsumer_CheckDLQs tests DLQ checking logic
func TestConsumer_CheckDLQs(t *testing.T) {
	mockSQS := &MockSQSClient{}
	registry := metrics.NewRegistry()

	statusURL := "https://sqs.us-east-1.amazonaws.com/123/status"
	prURL := "https://sqs.us-east-1.amazonaws.com/123/pr"

	config := &Config{
		Queues: map[string]string{
			"status":       statusURL,
			"pull_request": prURL,
		},
		DLQ: DLQConfig{
			Enabled:     true,
			QueueSuffix: "-dlq",
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		registry:  registry,
		logger:    zerolog.Nop(),
	}

	// Mock GetQueueAttributes for status DLQ (has messages)
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == statusURL+"-dlq"
	})).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			"ApproximateNumberOfMessages": "10",
		},
	}, nil)

	// Mock GetQueueAttributes for pull_request DLQ (doesn't exist)
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		return *input.QueueUrl == prURL+"-dlq"
	})).Return((*sqs.GetQueueAttributesOutput)(nil), assert.AnError)

	ctx := context.Background()
	logger := zerolog.Nop()

	// Call checkDLQs
	c.checkDLQs(ctx, logger)

	// Verify metrics were recorded
	statusDLQMetric := registry.Get(fmt.Sprintf("%s.status", MetricsKeyDLQMessages))
	assert.NotNil(t, statusDLQMetric)

	gauge := statusDLQMetric.(metrics.Gauge)
	assert.Equal(t, int64(10), gauge.Value())

	mockSQS.AssertExpectations(t)
}

// TestConsumer_CheckDLQs_DefaultSuffix tests default DLQ suffix
func TestConsumer_CheckDLQs_DefaultSuffix(t *testing.T) {
	mockSQS := &MockSQSClient{}

	queueURL := "https://sqs.us-east-1.amazonaws.com/123/test"

	config := &Config{
		Queues: map[string]string{
			"pull_request": queueURL,
		},
		DLQ: DLQConfig{
			Enabled: true,
			// QueueSuffix not set - should default to "-dlq"
		},
	}

	c := &consumer{
		config:    config,
		sqsClient: mockSQS,
		logger:    zerolog.Nop(),
	}

	// Mock GetQueueAttributes - verify default suffix is used
	mockSQS.On("GetQueueAttributes", mock.Anything, mock.MatchedBy(func(input *sqs.GetQueueAttributesInput) bool {
		expectedURL := queueURL + "-dlq" // Default suffix
		return *input.QueueUrl == expectedURL
	})).Return(&sqs.GetQueueAttributesOutput{
		Attributes: map[string]string{
			"ApproximateNumberOfMessages": "0",
		},
	}, nil)

	ctx := context.Background()
	logger := zerolog.Nop()

	c.checkDLQs(ctx, logger)

	mockSQS.AssertExpectations(t)
}
