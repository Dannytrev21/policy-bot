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
	"strconv"
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
)

// Config contains SQS consumer configuration
type Config struct {
	// Enable SQS event consumption
	Enabled bool

	// AWS region for SQS queues
	Region string

	// AWS endpoint URL for LocalStack/testing (optional)
	EndpointURL string

	// Map of GitHub event type to SQS queue URL
	Queues map[string]string

	// Event routing: specify which events to process via SQS vs HTTP
	EventRouting map[string]string // event_type -> "sqs" | "http" | "both"

	// Default number of workers per queue
	WorkersPerQueue int

	// Per-queue worker allocation (overrides WorkersPerQueue for specific event types)
	QueueWorkers map[string]int

	// Maximum number of messages to receive in a single request (1-10)
	MaxMessages int

	// Message visibility timeout in seconds
	VisibilityTimeout int

	// Wait time for long polling (0-20 seconds)
	WaitTimeSeconds int

	// Maximum time to wait for graceful shutdown
	ShutdownTimeout time.Duration

	// Enable retry on message processing failure
	EnableRetry bool

	// Maximum number of retries before sending to DLQ
	MaxRetries int

	// Dead Letter Queue configuration
	DLQ DLQConfig
}

// DLQConfig configures Dead Letter Queue behavior
type DLQConfig struct {
	// Enable DLQ monitoring
	Enabled bool

	// Maximum times a message can be received before being sent to DLQ
	MaxReceiveCount int

	// Suffix to append to queue URLs for DLQ (e.g., "-dlq")
	QueueSuffix string
}

// Consumer handles consuming messages from SQS queues
type Consumer interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health() error
	DetailedHealth(ctx context.Context) (map[string]QueueHealth, error)
}

// QueueHealth represents health information for a single queue
type QueueHealth struct {
	QueueName         string `json:"queue_name"`
	QueueURL          string `json:"queue_url"`
	Status            string `json:"status"`
	ApproxMessages    int64  `json:"approximate_messages"`
	ApproxDelayed     int64  `json:"approximate_delayed_messages"`
	ApproxNotVisible  int64  `json:"approximate_not_visible_messages"`
	LastError         string `json:"last_error,omitempty"`
	CheckedAt         string `json:"checked_at"`
}

// consumer implements Consumer
type consumer struct {
	config    *Config
	sqsClient *sqs.Client
	processor *Processor
	logger    zerolog.Logger
	registry  metrics.Registry

	// channels for coordinating shutdown
	stopChan   chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	started    bool
	startMutex sync.Mutex
}

// New creates a new SQS consumer
func New(
	cfg *Config,
	cloudHandlers []githubapp.EventHandler,
	enterpriseHandlers []githubapp.EventHandler,
	cloudScheduler githubapp.Scheduler,
	enterpriseScheduler githubapp.Scheduler,
	logger zerolog.Logger,
	registry metrics.Registry,
) (Consumer, error) {
	if !cfg.Enabled {
		return &noOpConsumer{}, nil
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

	// Create processor
	processorConfig := &ProcessorConfig{
		EnableRetry:       cfg.EnableRetry,
		MaxRetries:        cfg.MaxRetries,
		VisibilityTimeout: cfg.VisibilityTimeout,
	}

	processor := NewProcessor(
		processorConfig,
		sqsClient,
		enterpriseHandlers,
		cloudHandlers,
		enterpriseScheduler,
		cloudScheduler,
		cloudScheduler, // Default shared scheduler
		logger,
		registry,
	)

	c := &consumer{
		config:    cfg,
		sqsClient: sqsClient,
		processor: processor,
		logger:    logger.With().Str("component", "sqs_consumer").Logger(),
		registry:  registry,
		stopChan:  make(chan struct{}),
	}

	// Initialize metrics
	c.initMetrics(registry)

	return c, nil
}

// initMetrics sets up SQS-specific metrics
func (c *consumer) initMetrics(registry metrics.Registry) {
	if registry == nil {
		return
	}

	// Create metrics for each queue
	for eventType := range c.config.Queues {
		metrics.NewRegisteredCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesProcessed, eventType), registry)
		metrics.NewRegisteredCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesFailed, eventType), registry)
		metrics.NewRegisteredTimer(fmt.Sprintf("%s.%s", MetricsKeyProcessingTime, eventType), registry)
	}
}

// Start begins consuming messages from all configured SQS queues
func (c *consumer) Start(ctx context.Context) error {
	c.startMutex.Lock()
	defer c.startMutex.Unlock()

	if c.started {
		return errors.New("SQS consumer already started")
	}

	// Calculate total workers for logging
	totalWorkers := 0
	for eventType := range c.config.Queues {
		if c.shouldProcessViaSQS(eventType) {
			totalWorkers += c.getWorkersForQueue(eventType)
		}
	}

	c.logger.Info().
		Int("num_queues", len(c.config.Queues)).
		Int("total_workers", totalWorkers).
		Msg("Starting SQS consumer")

	for eventType, queueURL := range c.config.Queues {
		// Check if this event type should be processed via SQS
		if !c.shouldProcessViaSQS(eventType) {
			c.logger.Info().
				Str("event_type", eventType).
				Msg("Skipping SQS processing for event type (configured for HTTP only)")
			continue
		}

		workersPerQueue := c.getWorkersForQueue(eventType)
		c.logger.Info().
			Str("event_type", eventType).
			Int("workers", workersPerQueue).
			Msg("Starting SQS workers for queue")

		for i := 0; i < workersPerQueue; i++ {
			c.wg.Add(1)
			go c.consumeQueue(ctx, eventType, queueURL, i)
		}
	}

	// Start DLQ monitoring if enabled
	if c.config.DLQ.Enabled {
		c.logger.Info().Msg("Starting DLQ monitoring")
		c.wg.Add(1)
		go c.monitorDLQ(ctx)
	}

	c.started = true
	return nil
}

// shouldProcessViaSQS checks if an event type should be processed via SQS
func (c *consumer) shouldProcessViaSQS(eventType string) bool {
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
func (c *consumer) Stop(ctx context.Context) error {
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
func (c *consumer) Health() error {
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

// DetailedHealth returns detailed health information for all configured queues
func (c *consumer) DetailedHealth(ctx context.Context) (map[string]QueueHealth, error) {
	health := make(map[string]QueueHealth)
	checkedAt := time.Now().UTC().Format(time.RFC3339)

	for eventType, queueURL := range c.config.Queues {
		queueHealth := QueueHealth{
			QueueName: eventType,
			QueueURL:  queueURL,
			CheckedAt: checkedAt,
			Status:    "unknown",
		}

		// Create context with timeout for this check
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := c.sqsClient.GetQueueAttributes(checkCtx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL),
			AttributeNames: []types.QueueAttributeName{
				types.QueueAttributeNameApproximateNumberOfMessages,
				types.QueueAttributeNameApproximateNumberOfMessagesDelayed,
				types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		cancel()

		if err != nil {
			queueHealth.Status = "unhealthy"
			queueHealth.LastError = err.Error()
			c.logger.Warn().
				Err(err).
				Str("queue_name", eventType).
				Str("queue_url", queueURL).
				Msg("Failed to get queue attributes for health check")
		} else {
			queueHealth.Status = "healthy"

			// Parse queue attributes
			if result.Attributes != nil {
				if val, ok := result.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]; ok {
					if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
						queueHealth.ApproxMessages = parsed
					}
				}
				if val, ok := result.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesDelayed)]; ok {
					if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
						queueHealth.ApproxDelayed = parsed
					}
				}
				if val, ok := result.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)]; ok {
					if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
						queueHealth.ApproxNotVisible = parsed
					}
				}
			}

			c.logger.Debug().
				Str("queue_name", eventType).
				Int64("messages", queueHealth.ApproxMessages).
				Int64("delayed", queueHealth.ApproxDelayed).
				Int64("not_visible", queueHealth.ApproxNotVisible).
				Msg("Queue health check completed")
		}

		health[eventType] = queueHealth
	}

	return health, nil
}

// getWorkersForQueue returns the number of workers for a specific queue
func (c *consumer) getWorkersForQueue(eventType string) int {
	if c.config.QueueWorkers != nil {
		if workers, exists := c.config.QueueWorkers[eventType]; exists && workers > 0 {
			return workers
		}
	}
	return c.getWorkersPerQueue()
}

// getWorkersPerQueue returns the configured number of workers per queue
func (c *consumer) getWorkersPerQueue() int {
	if c.config.WorkersPerQueue <= 0 {
		return DefaultWorkersPerQueue
	}
	return c.config.WorkersPerQueue
}

// consumeQueue handles consuming messages from a single queue
func (c *consumer) consumeQueue(ctx context.Context, eventType, queueURL string, workerID int) {
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

		// Process each message using the processor
		for _, message := range result.Messages {
			if err := c.processor.ProcessMessage(ctx, eventType, queueURL, message); err != nil {
				logger.Error().Err(err).Msg("Failed to process SQS message")
			}
		}
	}
}

// getMaxMessages returns the configured max messages with bounds checking
func (c *consumer) getMaxMessages() int {
	maxMessages := c.config.MaxMessages
	if maxMessages <= 0 || maxMessages > 10 {
		maxMessages = DefaultMaxMessages
	}
	return maxMessages
}

// getVisibilityTimeout returns the configured visibility timeout with bounds checking
func (c *consumer) getVisibilityTimeout() int {
	visibilityTimeout := c.config.VisibilityTimeout
	if visibilityTimeout <= 0 {
		visibilityTimeout = DefaultVisibilityTimeout
	}
	return visibilityTimeout
}

// getWaitTimeSeconds returns the configured wait time with bounds checking
func (c *consumer) getWaitTimeSeconds() int {
	waitTimeSeconds := c.config.WaitTimeSeconds
	if waitTimeSeconds < 0 || waitTimeSeconds > 20 {
		waitTimeSeconds = DefaultWaitTimeSeconds
	}
	return waitTimeSeconds
}

// monitorDLQ periodically checks Dead Letter Queue depths and records metrics
func (c *consumer) monitorDLQ(ctx context.Context) {
	defer c.wg.Done()

	logger := c.logger.With().Str("component", "dlq_monitor").Logger()
	logger.Info().Msg("DLQ monitoring started")

	// Monitor every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Do an immediate check on start
	c.checkDLQs(ctx, logger)

	for {
		select {
		case <-c.stopChan:
			logger.Info().Msg("DLQ monitoring stopping")
			return
		case <-ctx.Done():
			logger.Info().Msg("DLQ monitoring context cancelled")
			return
		case <-ticker.C:
			c.checkDLQs(ctx, logger)
		}
	}
}

// checkDLQs checks all DLQ queues and records metrics
func (c *consumer) checkDLQs(ctx context.Context, logger zerolog.Logger) {
	if c.config.DLQ.QueueSuffix == "" {
		c.config.DLQ.QueueSuffix = "-dlq"
	}

	for eventType, queueURL := range c.config.Queues {
		// Construct DLQ URL by appending suffix
		dlqURL := queueURL + c.config.DLQ.QueueSuffix

		// Create context with timeout for this check
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, err := c.sqsClient.GetQueueAttributes(checkCtx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(dlqURL),
			AttributeNames: []types.QueueAttributeName{
				types.QueueAttributeNameApproximateNumberOfMessages,
			},
		})
		cancel()

		if err != nil {
			logger.Debug().
				Err(err).
				Str("event_type", eventType).
				Str("dlq_url", dlqURL).
				Msg("Could not check DLQ (may not exist)")
			continue
		}

		// Parse message count
		var messageCount int64
		if result.Attributes != nil {
			if val, ok := result.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]; ok {
				if parsed, err := strconv.ParseInt(val, 10, 64); err == nil {
					messageCount = parsed
				}
			}
		}

		// Record metric if registry is available
		if c.registry != nil {
			metricKey := fmt.Sprintf("%s.%s", MetricsKeyDLQMessages, eventType)
			if gauge := metrics.GetOrRegisterGauge(metricKey, c.registry); gauge != nil {
				gauge.Update(messageCount)
			}
		}

		// Log warning if messages are in DLQ
		if messageCount > 0 {
			logger.Warn().
				Str("event_type", eventType).
				Int64("message_count", messageCount).
				Str("dlq_url", dlqURL).
				Msg("Messages found in Dead Letter Queue - manual intervention may be required")
		} else {
			logger.Debug().
				Str("event_type", eventType).
				Int64("message_count", messageCount).
				Msg("DLQ check completed - no messages")
		}
	}
}

// noOpConsumer is used when SQS is disabled
type noOpConsumer struct{}

func (c *noOpConsumer) Start(ctx context.Context) error {
	return nil
}

func (c *noOpConsumer) Stop(ctx context.Context) error {
	return nil
}

func (c *noOpConsumer) Health() error {
	return nil
}

func (c *noOpConsumer) DetailedHealth(ctx context.Context) (map[string]QueueHealth, error) {
	return make(map[string]QueueHealth), nil
}
