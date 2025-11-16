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
	"os"
	"strconv"
	"strings"
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

// EventQueueConfig provides event-specific queue configuration with regional URLs and environment controls
type EventQueueConfig struct {
	// Regional queue URLs
	EastRegionURL string
	WestRegionURL string

	// Event routing strategy: "http", "sqs", or "both"
	EventRouting string

	// Environment-specific enablement
	GHECEnabled bool
	GHESEnabled bool

	// Number of workers for this event type
	QueueWorkers int

	// Optional overrides
	VisibilityTimeout int
	MaxRetries        int
}

// Config contains SQS consumer configuration
type Config struct {
	// Enable SQS event consumption
	Enabled bool

	// AWS region for SQS queues
	Region string

	// AWS endpoint URL for LocalStack/testing (optional)
	EndpointURL string

	// Processing mode: "scheduler" (legacy) or "direct" (worker pools)
	ProcessingMode string

	// Event-based queue configuration with region URLs and environment controls
	Queues map[string]EventQueueConfig

	// Default number of workers per queue
	WorkersPerQueue int

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

	// Adaptive polling configuration
	AdaptivePolling AdaptivePollingConfig
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

// AdaptivePollingConfig configures adaptive SQS polling based on worker availability
type AdaptivePollingConfig struct {
	// Enable adaptive polling based on worker availability
	Enabled bool

	// Base backoff duration when workers are saturated
	BaseBackoff time.Duration

	// Maximum backoff duration
	MaxBackoff time.Duration

	// Enable per-event-type configuration
	EventTypeOverrides map[string]AdaptivePollingEventConfig
}

// AdaptivePollingEventConfig configures adaptive polling for a specific event type
type AdaptivePollingEventConfig struct {
	Enabled     bool
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// BuildQueueMap builds a map of event types to queue URLs for a given environment
func (c *Config) BuildQueueMap(environment string) map[string]string {
	result := make(map[string]string)

	// Process EventQueueConfig format
	for eventType, queueConfig := range c.Queues {
		// Check if event is enabled for this environment
		if !c.IsEventEnabledForEnvironment(eventType, environment) {
			continue
		}

		// Get the appropriate URL based on region
		region := c.DetectRegion()
		url := c.SelectRegionURL(queueConfig, region)
		if url != "" {
			result[eventType] = url
		}
	}

	return result
}

// DetectRegion determines the current AWS region from configuration or environment
func (c *Config) DetectRegion() string {
	// First check if Region is explicitly set in config
	if c.Region != "" {
		return c.Region
	}

	// Check AWS_REGION environment variable
	if region, ok := os.LookupEnv("AWS_REGION"); ok && region != "" {
		return region
	}

	// Check AWS_DEFAULT_REGION environment variable
	if region, ok := os.LookupEnv("AWS_DEFAULT_REGION"); ok && region != "" {
		return region
	}

	// Default to us-east-1
	return "us-east-1"
}

// SelectRegionURL selects the appropriate queue URL based on the region
func (c *Config) SelectRegionURL(queueConfig EventQueueConfig, region string) string {
	// If region contains "west", use west URL if available
	if strings.Contains(strings.ToLower(region), "west") {
		if queueConfig.WestRegionURL != "" {
			return queueConfig.WestRegionURL
		}
		// Fall back to east URL if west not available
		return queueConfig.EastRegionURL
	}

	// For east or any other region, prefer east URL
	if queueConfig.EastRegionURL != "" {
		return queueConfig.EastRegionURL
	}

	// Fall back to west URL if east not available
	return queueConfig.WestRegionURL
}

// IsEventEnabledForEnvironment checks if an event is enabled for a specific environment
func (c *Config) IsEventEnabledForEnvironment(eventType, environment string) bool {
	queueConfig, exists := c.Queues[eventType]
	if !exists {
		// If not in new config, assume enabled for backward compatibility
		return true
	}

	switch environment {
	case "cloud", "ghec":
		return queueConfig.GHECEnabled
	case "enterprise", "ghes":
		return queueConfig.GHESEnabled
	default:
		// Unknown environment, check both
		return queueConfig.GHECEnabled || queueConfig.GHESEnabled
	}
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
	QueueName        string `json:"queue_name"`
	QueueURL         string `json:"queue_url"`
	Status           string `json:"status"`
	ApproxMessages   int64  `json:"approximate_messages"`
	ApproxDelayed    int64  `json:"approximate_delayed_messages"`
	ApproxNotVisible int64  `json:"approximate_not_visible_messages"`
	LastError        string `json:"last_error,omitempty"`
	CheckedAt        string `json:"checked_at"`
}

// consumer implements Consumer
type consumer struct {
	config             *Config
	sqsClient          SQSClient
	processor          *Processor
	workerPoolMgr      *WorkerPoolManager
	logger             zerolog.Logger
	registry           metrics.Registry
	cloudHandlers      []githubapp.EventHandler
	enterpriseHandlers []githubapp.EventHandler
	environment        string // "cloud" or "enterprise"
	queueMap           map[string]string // Resolved event type to queue URL mapping

	// channels for coordinating shutdown
	stopChan   chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	started    bool
	startMutex sync.Mutex
}

// NewWithEnvironment creates a new SQS consumer for a specific environment
func NewWithEnvironment(
	cfg *Config,
	environment string,
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

	// Set default processing mode if not specified
	if cfg.ProcessingMode == "" {
		cfg.ProcessingMode = "scheduler" // Default to legacy mode
	}

	// Create worker pool manager for direct processing mode
	workerPoolMgr := NewWorkerPoolManager(logger, registry)

	// Create processor
	processorConfig := &ProcessorConfig{
		EnableRetry:          cfg.EnableRetry,
		MaxRetries:           cfg.MaxRetries,
		VisibilityTimeout:    cfg.VisibilityTimeout,
		ProcessingMode:       cfg.ProcessingMode,
		EnableCircuitBreaker: true, // Enable circuit breaker for production resilience
		CircuitBreakerConfig: nil,  // Use default config
	}

	processor := NewProcessor(
		processorConfig,
		sqsClient,
		enterpriseHandlers,
		cloudHandlers,
		enterpriseScheduler,
		cloudScheduler,
		cloudScheduler, // Default shared scheduler
		workerPoolMgr,
		logger,
		registry,
	)

	// Build queue worker map for direct processing mode from EventQueueConfig
	queueWorkers := make(map[string]int)
	for eventType, queueConfig := range cfg.Queues {
		if queueConfig.QueueWorkers > 0 {
			queueWorkers[eventType] = queueConfig.QueueWorkers
		}
	}
	if len(queueWorkers) > 0 {
		processor.SetQueueWorkers(queueWorkers)
	}

	// Build queue map from config
	queueMap := cfg.BuildQueueMap(environment)

	c := &consumer{
		config:             cfg,
		sqsClient:          sqsClient,
		processor:          processor,
		workerPoolMgr:      workerPoolMgr,
		cloudHandlers:      cloudHandlers,
		enterpriseHandlers: enterpriseHandlers,
		environment:        environment,
		queueMap:           queueMap,
		logger:             logger.With().Str("component", "sqs_consumer").Str("environment", environment).Logger(),
		registry:           registry,
		stopChan:           make(chan struct{}),
	}

	// Initialize metrics
	c.initMetrics(registry)

	return c, nil
}

// New creates a new SQS consumer (defaults to cloud environment for backward compatibility)
func New(
	cfg *Config,
	cloudHandlers []githubapp.EventHandler,
	enterpriseHandlers []githubapp.EventHandler,
	cloudScheduler githubapp.Scheduler,
	enterpriseScheduler githubapp.Scheduler,
	logger zerolog.Logger,
	registry metrics.Registry,
) (Consumer, error) {
	// Default to cloud environment for backward compatibility
	return NewWithEnvironment(cfg, "cloud", cloudHandlers, enterpriseHandlers, cloudScheduler, enterpriseScheduler, logger, registry)
}

// initMetrics sets up SQS-specific metrics
func (c *consumer) initMetrics(registry metrics.Registry) {
	if registry == nil {
		return
	}

	// Create metrics for each queue
	for eventType := range c.queueMap {
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
	for eventType := range c.queueMap {
		if c.shouldProcessViaSQS(eventType) {
			totalWorkers += c.getWorkersForQueue(eventType)
		}
	}

	c.logger.Info().
		Int("num_queues", len(c.queueMap)).
		Int("total_workers", totalWorkers).
		Str("environment", c.environment).
		Msg("Starting SQS consumer")

	for eventType, queueURL := range c.queueMap {
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
			Str("queue_url", queueURL).
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
	// Check EventQueueConfig routing
	if queueConfig, exists := c.config.Queues[eventType]; exists && queueConfig.EventRouting != "" {
		routing := queueConfig.EventRouting
		return routing == "sqs" || routing == "both"
	}

	// Default to SQS if queue is configured
	_, hasQueue := c.queueMap[eventType]
	return hasQueue
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

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	// Wait for all consumer workers to stop
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info().Msg("All SQS consumer workers stopped")
	case <-time.After(shutdownTimeout):
		c.logger.Warn().Msg("SQS consumer shutdown timeout exceeded")
		return errors.New("shutdown timeout exceeded")
	case <-ctx.Done():
		c.logger.Warn().Msg("SQS consumer shutdown context cancelled")
		return ctx.Err()
	}

	// Shutdown worker pool manager if using direct mode
	if c.config.ProcessingMode == "direct" && c.workerPoolMgr != nil {
		c.logger.Info().Msg("Shutting down worker pool manager")
		if err := c.workerPoolMgr.Shutdown(shutdownCtx); err != nil {
			c.logger.Error().Err(err).Msg("Error shutting down worker pool manager")
			return err
		}
	}

	c.logger.Info().Msg("SQS consumer stopped gracefully")
	return nil
}

// Health checks if the SQS consumer is healthy
func (c *consumer) Health() error {
	// Try to get queue attributes for one queue to verify connectivity
	for _, queueURL := range c.queueMap {
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

	for eventType, queueURL := range c.queueMap {
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
	// Check EventQueueConfig.QueueWorkers
	if queueConfig, exists := c.config.Queues[eventType]; exists && queueConfig.QueueWorkers > 0 {
		return queueConfig.QueueWorkers
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

	// Track consecutive saturation events for backoff
	consecutiveSaturations := 0

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

		// Check if adaptive polling is enabled
		adaptiveEnabled := c.config.AdaptivePolling.Enabled
		if override, exists := c.config.AdaptivePolling.EventTypeOverrides[eventType]; exists {
			adaptiveEnabled = override.Enabled
		}

		// OPTIMIZATION: Check worker availability before polling
		if adaptiveEnabled && c.config.ProcessingMode == "direct" && c.workerPoolMgr != nil {
			workerCount := c.getWorkersForQueue(eventType)
			availableCapacity := c.workerPoolMgr.GetAvailableCapacityForEventType(eventType, workerCount)

			if availableCapacity == 0 {
				// All workers busy, implement backoff
				consecutiveSaturations++
				backoffDuration := c.calculateBackoff(eventType, consecutiveSaturations)

				logger.Debug().
					Int("consecutive_saturations", consecutiveSaturations).
					Dur("backoff_duration", backoffDuration).
					Msg("Worker pool saturated, backing off SQS polling")

				// Record saturation metric
				if c.registry != nil {
					saturationCounter := metrics.GetOrRegisterCounter(
						fmt.Sprintf("sqs.worker_pool.saturation_events.%s", eventType),
						c.registry,
					)
					saturationCounter.Inc(1)
				}

				select {
				case <-time.After(backoffDuration):
				case <-c.stopChan:
					return
				case <-ctx.Done():
					return
				}
				continue
			}

			// Reset saturation counter when workers become available
			if consecutiveSaturations > 0 {
				logger.Debug().
					Int("available_capacity", availableCapacity).
					Msg("Workers available, resuming normal polling")
				consecutiveSaturations = 0
			}

			// Adjust max messages based on available capacity
			originalMaxMessages := maxMessages
			if availableCapacity < maxMessages {
				maxMessages = availableCapacity
				logger.Debug().
					Int("original_max_messages", originalMaxMessages).
					Int("adjusted_max_messages", maxMessages).
					Int("available_capacity", availableCapacity).
					Msg("Adjusted max messages to match available capacity")

				if c.registry != nil {
					adjustmentCounter := metrics.GetOrRegisterCounter(
						fmt.Sprintf("sqs.consumer.adaptive_adjustments.%s", eventType),
						c.registry,
					)
					adjustmentCounter.Inc(1)
				}
			}
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

		// Reset maxMessages for next iteration if it was adjusted
		maxMessages = c.getMaxMessages()
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

// calculateBackoff calculates exponential backoff duration for worker saturation
func (c *consumer) calculateBackoff(eventType string, consecutiveSaturations int) time.Duration {
	// Default values if not configured
	baseBackoff := time.Second
	maxBackoff := 30 * time.Second

	// Check for event-specific overrides first
	if override, exists := c.config.AdaptivePolling.EventTypeOverrides[eventType]; exists {
		if override.BaseBackoff > 0 {
			baseBackoff = override.BaseBackoff
		}
		if override.MaxBackoff > 0 {
			maxBackoff = override.MaxBackoff
		}
	} else {
		// Use global configuration if available
		if c.config.AdaptivePolling.BaseBackoff > 0 {
			baseBackoff = c.config.AdaptivePolling.BaseBackoff
		}
		if c.config.AdaptivePolling.MaxBackoff > 0 {
			maxBackoff = c.config.AdaptivePolling.MaxBackoff
		}
	}

	// Calculate exponential backoff
	backoff := baseBackoff * time.Duration(1<<uint(consecutiveSaturations-1))
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	// Add jitter (±10%) to prevent thundering herd
	jitterPercent := 0.1 * (2*float64(consecutiveSaturations%100)/100.0 - 1)
	jitter := time.Duration(float64(backoff) * jitterPercent)
	backoff += jitter

	return backoff
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

	for eventType, queueURL := range c.queueMap {
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
