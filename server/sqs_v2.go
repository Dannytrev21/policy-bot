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

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

const (
	DefaultWorkersPerQueue   = 5
	DefaultMaxMessages       = 10
	DefaultVisibilityTimeout = 30
	DefaultWaitTimeSeconds   = 20
	DefaultShutdownTimeout   = 30 * time.Second
	DefaultMaxRetries        = 3

	// Metrics keys
	MetricsKeyQueueDepth        = "sqs.queue.depth"
	MetricsKeyMessagesProcessed = "sqs.messages.processed"
	MetricsKeyMessagesFailed    = "sqs.messages.failed"
	MetricsKeyProcessingTime    = "sqs.processing.time"

	// Context keys for SQS processing
	SQSEventSourceKey = "sqs_event_source"
)

// SQSMessage represents a GitHub webhook message in SQS
type SQSMessage struct {
	EventType  string          `json:"event_type"`
	DeliveryID string          `json:"delivery_id"`
	Payload    json.RawMessage `json:"payload"`
	RetryCount int             `json:"retry_count,omitempty"`
	Source     string          `json:"source,omitempty"` // "webhook" or "sqs"
}

// SQSConsumerV2 handles consuming messages from SQS queues using AWS SDK v2
type SQSConsumerV2 interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health() error
}

// sqsConsumerV2 implements SQSConsumerV2
type sqsConsumerV2 struct {
	config    *SQSConfig
	sqsClient *sqs.Client
	scheduler githubapp.Scheduler
	handlers  map[string]githubapp.EventHandler
	logger    zerolog.Logger
	registry  metrics.Registry

	// channels for coordinating shutdown
	stopChan   chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	started    bool
	startMutex sync.Mutex
}

// NewSQSConsumerV2 creates a new SQS consumer using AWS SDK v2
func NewSQSConsumerV2(cfg *SQSConfig, handlers []githubapp.EventHandler, scheduler githubapp.Scheduler, logger zerolog.Logger, registry metrics.Registry) (SQSConsumerV2, error) {
	if !cfg.Enabled {
		return &noOpSQSConsumerV2{}, nil
	}

	// Create AWS config
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load AWS config")
	}

	// Create SQS client with optional endpoint override for LocalStack
	sqsClient := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.EndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
		}
	})

	// Build handler map
	handlerMap := make(map[string]githubapp.EventHandler)
	for _, handler := range handlers {
		for _, eventType := range handler.Handles() {
			handlerMap[eventType] = handler
		}
	}

	consumer := &sqsConsumerV2{
		config:    cfg,
		sqsClient: sqsClient,
		scheduler: scheduler,
		handlers:  handlerMap,
		logger:    logger.With().Str("component", "sqs_consumer_v2").Logger(),
		registry:  registry,
		stopChan:  make(chan struct{}),
	}

	// Initialize metrics
	consumer.initMetrics()

	return consumer, nil
}

// initMetrics sets up SQS-specific metrics
func (c *sqsConsumerV2) initMetrics() {
	if c.registry == nil {
		return
	}

	// Create metrics for each queue
	for eventType := range c.config.Queues {
		metrics.NewRegisteredCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesProcessed, eventType), c.registry)
		metrics.NewRegisteredCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesFailed, eventType), c.registry)
		metrics.NewRegisteredTimer(fmt.Sprintf("%s.%s", MetricsKeyProcessingTime, eventType), c.registry)
	}
}

// Start begins consuming messages from all configured SQS queues
func (c *sqsConsumerV2) Start(ctx context.Context) error {
	c.startMutex.Lock()
	defer c.startMutex.Unlock()

	if c.started {
		return errors.New("SQS consumer already started")
	}

	c.logger.Info().
		Int("num_queues", len(c.config.Queues)).
		Int("workers_per_queue", c.getWorkersPerQueue()).
		Msg("Starting SQS consumer")

	for eventType, queueURL := range c.config.Queues {
		// Check if this event type should be processed via SQS
		if !c.shouldProcessViaSQS(eventType) {
			c.logger.Info().
				Str("event_type", eventType).
				Msg("Skipping SQS processing for event type (configured for HTTP only)")
			continue
		}

		workersPerQueue := c.getWorkersPerQueue()
		for i := 0; i < workersPerQueue; i++ {
			c.wg.Add(1)
			go c.consumeQueue(ctx, eventType, queueURL, i)
		}
	}

	c.started = true
	return nil
}

// shouldProcessViaSQS checks if an event type should be processed via SQS
func (c *sqsConsumerV2) shouldProcessViaSQS(eventType string) bool {
	if c.config.EventRouting == nil {
		// If no routing specified, process all configured queues via SQS
		return true
	}

	routing, exists := c.config.EventRouting[eventType]
	if !exists {
		// Default to SQS if queue is configured but routing not specified
		return true
	}

	return routing == "sqs" || routing == "both"
}

// Stop gracefully shuts down all SQS consumers
func (c *sqsConsumerV2) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		c.logger.Info().Msg("Stopping SQS consumer")
		close(c.stopChan)
	})

	shutdownTimeout := c.config.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info().Msg("All SQS consumer workers stopped gracefully")
		return nil
	case <-time.After(shutdownTimeout):
		c.logger.Warn().Msg("SQS consumer shutdown timeout exceeded")
		return errors.New("shutdown timeout exceeded")
	case <-ctx.Done():
		c.logger.Warn().Msg("SQS consumer shutdown context cancelled")
		return ctx.Err()
	}
}

// Health checks if the SQS consumer is healthy
func (c *sqsConsumerV2) Health() error {
	// Try to get queue attributes for one queue to verify connectivity
	for _, queueURL := range c.config.Queues {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := c.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(queueURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
		})
		cancel()
		return err // Return first result (success or failure)
	}
	return nil
}

// getWorkersPerQueue returns the configured number of workers per queue
func (c *sqsConsumerV2) getWorkersPerQueue() int {
	if c.config.WorkersPerQueue <= 0 {
		return DefaultWorkersPerQueue
	}
	return c.config.WorkersPerQueue
}

// consumeQueue handles consuming messages from a single queue
func (c *sqsConsumerV2) consumeQueue(ctx context.Context, eventType, queueURL string, workerID int) {
	defer c.wg.Done()

	logger := c.logger.With().
		Str("event_type", eventType).
		Str("queue_url", queueURL).
		Int("worker_id", workerID).
		Logger()

	logger.Info().Msg("Starting SQS queue consumer worker")

	maxMessages := c.getMaxMessages()
	visibilityTimeout := c.getVisibilityTimeout()
	waitTimeSeconds := c.getWaitTimeSeconds()

	// Set up metrics for this worker
	var (
		processedCounter metrics.Counter
		failedCounter    metrics.Counter
		processingTimer  metrics.Timer
	)

	if c.registry != nil {
		processedCounter = metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesProcessed, eventType), c.registry)
		failedCounter = metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesFailed, eventType), c.registry)
		processingTimer = metrics.GetOrRegisterTimer(fmt.Sprintf("%s.%s", MetricsKeyProcessingTime, eventType), c.registry)
	}

	for {
		select {
		case <-c.stopChan:
			logger.Info().Msg("SQS queue consumer worker stopping")
			return
		case <-ctx.Done():
			logger.Info().Msg("SQS queue consumer worker context cancelled")
			return
		default:
			// Continue processing
		}

		// Receive messages from SQS
		result, err := c.sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: int32(maxMessages),
			VisibilityTimeout:   int32(visibilityTimeout),
			WaitTimeSeconds:     int32(waitTimeSeconds),
		})

		if err != nil {
			logger.Error().Err(err).Msg("Failed to receive messages from SQS")
			// Exponential backoff on errors
			select {
			case <-time.After(5 * time.Second):
			case <-c.stopChan:
				return
			case <-ctx.Done():
				return
			}
			continue
		}

		// Process each message
		for _, message := range result.Messages {
			start := time.Now()
			err := c.processMessage(ctx, eventType, queueURL, message, logger)

			if processingTimer != nil {
				processingTimer.UpdateSince(start)
			}

			if err != nil {
				logger.Error().Err(err).Msg("Failed to process SQS message")
				if failedCounter != nil {
					failedCounter.Inc(1)
				}
			} else {
				if processedCounter != nil {
					processedCounter.Inc(1)
				}
			}
		}
	}
}

// processMessage handles a single SQS message
func (c *sqsConsumerV2) processMessage(ctx context.Context, eventType, queueURL string, message types.Message, logger zerolog.Logger) error {
	if message.Body == nil {
		return errors.New("message body is nil")
	}

	// Parse the SQS message
	var sqsMsg SQSMessage
	if err := json.Unmarshal([]byte(*message.Body), &sqsMsg); err != nil {
		// If it's not our expected format, treat the body as raw payload
		sqsMsg = SQSMessage{
			EventType:  eventType,
			DeliveryID: aws.ToString(message.MessageId),
			Payload:    json.RawMessage(*message.Body),
			Source:     "sqs",
		}
	} else {
		// Ensure source is set
		if sqsMsg.Source == "" {
			sqsMsg.Source = "sqs"
		}
	}

	// Add SQS context metadata
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")

	msgLogger := logger.With().
		Str("delivery_id", sqsMsg.DeliveryID).
		Str("message_id", aws.ToString(message.MessageId)).
		Str("source", sqsMsg.Source).
		Logger()

	ctx = msgLogger.WithContext(ctx)

	msgLogger.Debug().Msg("Processing SQS message")

	// Convert payload to bytes for the handler
	payloadBytes := []byte(sqsMsg.Payload)

	// Find the appropriate handler
	handler, exists := c.handlers[sqsMsg.EventType]
	if !exists {
		msgLogger.Debug().Msgf("No handler for event type: %s", sqsMsg.EventType)
		// Delete message since we can't process it anyway
		return c.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
	}

	// Create a dispatch for the scheduler
	dispatch := githubapp.Dispatch{
		Handler:    handler,
		EventType:  sqsMsg.EventType,
		DeliveryID: sqsMsg.DeliveryID,
		Payload:    payloadBytes,
	}

	// Use the scheduler to process the event (maintains consistency with HTTP path)
	err := c.scheduler.Schedule(ctx, dispatch)
	if err != nil {
		msgLogger.Error().Err(err).Msg("Failed to schedule GitHub event from SQS")

		// Handle retries if enabled
		if c.config.EnableRetry && sqsMsg.RetryCount < c.getMaxRetries() {
			return c.handleRetry(ctx, queueURL, message, sqsMsg, msgLogger)
		}

		// Don't delete the message so it will be retried by SQS
		return err
	}

	// Delete the message from the queue on successful processing
	return c.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
}

// deleteMessage removes a successfully processed message from the queue
func (c *sqsConsumerV2) deleteMessage(ctx context.Context, queueURL string, receiptHandle *string, logger zerolog.Logger) error {
	_, err := c.sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: receiptHandle,
	})

	if err != nil {
		logger.Error().Err(err).Msg("Failed to delete processed message from SQS")
		return err
	}

	logger.Debug().Msg("Successfully processed and deleted SQS message")
	return nil
}

// handleRetry implements custom retry logic if enabled
func (c *sqsConsumerV2) handleRetry(ctx context.Context, queueURL string, message types.Message, sqsMsg SQSMessage, logger zerolog.Logger) error {
	sqsMsg.RetryCount++

	// Re-queue the message with updated retry count
	retryBody, err := json.Marshal(sqsMsg)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal retry message")
		return err
	}

	// Calculate delay for exponential backoff
	delay := time.Duration(sqsMsg.RetryCount*sqsMsg.RetryCount) * time.Second
	if delay > 300*time.Second {
		delay = 300 * time.Second // Cap at 5 minutes
	}

	_, err = c.sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:     aws.String(queueURL),
		MessageBody:  aws.String(string(retryBody)),
		DelaySeconds: int32(delay.Seconds()),
	})

	if err != nil {
		logger.Error().Err(err).Msg("Failed to send retry message")
		return err
	}

	// Delete the original message
	return c.deleteMessage(ctx, queueURL, message.ReceiptHandle, logger)
}

// getMaxMessages returns the configured max messages with bounds checking
func (c *sqsConsumerV2) getMaxMessages() int {
	maxMessages := c.config.MaxMessages
	if maxMessages <= 0 || maxMessages > 10 {
		maxMessages = DefaultMaxMessages
	}
	return maxMessages
}

// getVisibilityTimeout returns the configured visibility timeout with bounds checking
func (c *sqsConsumerV2) getVisibilityTimeout() int {
	visibilityTimeout := c.config.VisibilityTimeout
	if visibilityTimeout <= 0 {
		visibilityTimeout = DefaultVisibilityTimeout
	}
	return visibilityTimeout
}

// getWaitTimeSeconds returns the configured wait time with bounds checking
func (c *sqsConsumerV2) getWaitTimeSeconds() int {
	waitTimeSeconds := c.config.WaitTimeSeconds
	if waitTimeSeconds < 0 || waitTimeSeconds > 20 {
		waitTimeSeconds = DefaultWaitTimeSeconds
	}
	return waitTimeSeconds
}

// getMaxRetries returns the configured max retries
func (c *sqsConsumerV2) getMaxRetries() int {
	if c.config.MaxRetries <= 0 {
		return DefaultMaxRetries
	}
	return c.config.MaxRetries
}

// noOpSQSConsumerV2 is used when SQS is disabled
type noOpSQSConsumerV2 struct{}

func (c *noOpSQSConsumerV2) Start(ctx context.Context) error {
	return nil
}

func (c *noOpSQSConsumerV2) Stop(ctx context.Context) error {
	return nil
}

func (c *noOpSQSConsumerV2) Health() error {
	return nil
}
