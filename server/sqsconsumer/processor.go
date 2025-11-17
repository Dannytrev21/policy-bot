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
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/cenkalti/backoff/v4"
	"github.com/palantir/go-githubapp/githubapp"
	policyhandler "github.com/palantir/policy-bot/server/handler"
	"github.com/pkg/errors"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
	"github.com/sony/gobreaker"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	// Context keys for SQS processing
	SQSEventSourceKey   = "sqs_event_source"
	SQSEventEnvironment = "sqs_event_environment"
	SQSQueueName        = "sqs_queue_name"
	SQSMessageID        = "sqs_message_id"
	SQSReceiptHandle    = "sqs_receipt_handle"

	// Metrics keys
	MetricsKeyMessagesProcessed  = "sqs.messages.processed"
	MetricsKeyMessagesFailed     = "sqs.messages.failed"
	MetricsKeyProcessingTime     = "sqs.processing.time"
	MetricsKeyQueueDepth         = "sqs.queue.depth"
	MetricsKeyDLQMessages        = "sqs.dlq.messages"
	MetricsKeyPoolHits           = "sqs.pool.hits"
	MetricsKeyPoolMisses         = "sqs.pool.misses"
	MetricsKeyRetryBackoffTime   = "sqs.retry.backoff_duration"
	MetricsKeyRetryAttemptsTotal = "sqs.retry.attempts_total"
)

// messagePool is a sync.Pool for SQSMessage structs to reduce allocations
// At 200 events/sec, this saves ~200 allocations/sec = 12,000/min
var messagePool = sync.Pool{
	New: func() interface{} {
		return &SQSMessage{}
	},
}

// SQSMessage represents a GitHub webhook message in SQS
type SQSMessage struct {
	EventType  string                 `json:"event_type"`
	DeliveryID string                 `json:"delivery_id"`
	Headers    map[string]interface{} `json:"headers,omitempty"` // GitHub webhook headers containing Host field
	Payload    json.RawMessage        `json:"payload"`
	RetryCount int                    `json:"retry_count,omitempty"`
	Source     string                 `json:"source,omitempty"` // Deprecated - kept for backward compatibility
}

// ProcessorConfig contains configuration for the SQS message processor
type ProcessorConfig struct {
	EnableRetry        bool
	MaxRetries         int
	VisibilityTimeout  int
	ProcessingMode     string // "scheduler" or "direct"
	EnableCircuitBreaker bool // Enable circuit breaker for resilience
	CircuitBreakerConfig *CircuitBreakerConfig // Optional custom circuit breaker config
}

// Processor handles processing of individual SQS messages
type Processor struct {
	config              *ProcessorConfig
	sqsClient           SQSClient
	enterpriseHandlers  map[string]githubapp.EventHandler
	cloudHandlers       map[string]githubapp.EventHandler
	enterpriseScheduler githubapp.Scheduler
	cloudScheduler      githubapp.Scheduler
	workerPoolMgr       *WorkerPoolManager
	idempotency         *IdempotencyManager
	circuitBreaker      *CircuitBreakerManager
	logger              zerolog.Logger
	registry            metrics.Registry
	queueWorkers        map[string]int // Worker count per event type
}

// SQSClient interface for SQS operations (allows mocking)
type SQSClient interface {
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	GetQueueAttributes(ctx context.Context, params *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
}

// getSQSMessageFromPool retrieves a message from the pool
// Returns a zeroed message ready for use
func getSQSMessageFromPool() *SQSMessage {
	msg := messagePool.Get().(*SQSMessage)
	return msg
}

// returnSQSMessageToPool returns a message to the pool after clearing its contents
// This prevents memory leaks and ensures clean state for next use
func returnSQSMessageToPool(msg *SQSMessage) {
	if msg == nil {
		return
	}

	// Clear all fields to prevent memory leaks
	msg.EventType = ""
	msg.DeliveryID = ""
	msg.Headers = nil
	msg.Payload = nil
	msg.RetryCount = 0
	msg.Source = ""

	messagePool.Put(msg)
}

// NewProcessor creates a new SQS message processor
func NewProcessor(
	config *ProcessorConfig,
	sqsClient SQSClient,
	enterpriseHandlers []githubapp.EventHandler,
	cloudHandlers []githubapp.EventHandler,
	enterpriseScheduler githubapp.Scheduler,
	cloudScheduler githubapp.Scheduler,
	scheduler githubapp.Scheduler,
	workerPoolMgr *WorkerPoolManager,
	logger zerolog.Logger,
	registry metrics.Registry,
) *Processor {
	// Pre-calculate total event types for map pre-allocation
	// This prevents map growth and reallocation during initialization
	enterpriseEventCount := 0
	for _, handler := range enterpriseHandlers {
		enterpriseEventCount += len(handler.Handles())
	}

	cloudEventCount := 0
	for _, handler := range cloudHandlers {
		cloudEventCount += len(handler.Handles())
	}

	// Build handler maps with pre-allocated capacity
	enterpriseHandlerMap := make(map[string]githubapp.EventHandler, enterpriseEventCount)
	for _, handler := range enterpriseHandlers {
		for _, eventType := range handler.Handles() {
			enterpriseHandlerMap[eventType] = handler
		}
	}

	cloudHandlerMap := make(map[string]githubapp.EventHandler, cloudEventCount)
	for _, handler := range cloudHandlers {
		for _, eventType := range handler.Handles() {
			cloudHandlerMap[eventType] = handler
		}
	}

	// Create idempotency manager
	idempotencyMgr, err := NewIdempotencyManager(
		DefaultIdempotencyCacheSize,
		DefaultIdempotencyTTL,
		registry,
	)
	if err != nil {
		// Log error but don't fail - idempotency is an optimization
		logger.Warn().Err(err).Msg("Failed to create idempotency manager, duplicate detection disabled")
	}

	// Register pool metrics if registry provided
	if registry != nil {
		metrics.GetOrRegisterCounter(MetricsKeyPoolHits, registry)
		metrics.GetOrRegisterCounter(MetricsKeyPoolMisses, registry)
		metrics.GetOrRegisterTimer(MetricsKeyRetryBackoffTime, registry)
		metrics.GetOrRegisterCounter(MetricsKeyRetryAttemptsTotal, registry)
	}

	// Create circuit breaker manager if enabled (default: enabled)
	var cbm *CircuitBreakerManager
	if config.EnableCircuitBreaker {
		cbConfig := config.CircuitBreakerConfig
		if cbConfig == nil {
			cbConfig = DefaultCircuitBreakerConfig()
		}
		cbm = NewCircuitBreakerManager(cbConfig, logger, registry)
		logger.Info().Msg("Circuit breaker enabled for SQS processing")
	} else {
		logger.Warn().Msg("Circuit breaker DISABLED - not recommended for production")
	}

	return &Processor{
		config:              config,
		sqsClient:           sqsClient,
		enterpriseHandlers:  enterpriseHandlerMap,
		cloudHandlers:       cloudHandlerMap,
		enterpriseScheduler: enterpriseScheduler,
		cloudScheduler:      cloudScheduler,
		workerPoolMgr:       workerPoolMgr,
		idempotency:         idempotencyMgr,
		circuitBreaker:      cbm,
		logger:              logger.With().Str("component", "sqs_processor").Logger(),
		registry:            registry,
		queueWorkers:        make(map[string]int),
	}
}

// extractInstallationID attempts to extract the GitHub installation ID from the webhook payload
// Returns 0 if installation ID cannot be found
func extractInstallationID(payload json.RawMessage) int64 {
	var webhookData struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}

	if err := json.Unmarshal(payload, &webhookData); err == nil {
		return webhookData.Installation.ID
	}
	return 0
}

// ProcessMessage handles a single SQS message
// Implements Phase 3.1: OpenTelemetry distributed tracing for message flow visibility
func (p *Processor) ProcessMessage(ctx context.Context, eventType, queueURL string, message types.Message) error {
	// Create top-level tracing span for the entire message processing lifecycle
	tracer := otel.Tracer("policy-bot.sqs-processor")
	ctx, span := tracer.Start(ctx, "sqs.process_message",
		trace.WithAttributes(
			attribute.String("queue.name", eventType),
			attribute.String("message.id", aws.ToString(message.MessageId)),
		),
	)
	defer span.End()

	if message.Body == nil {
		err := errors.New("message body is nil")
		span.RecordError(err)
		span.SetStatus(codes.Error, "message body is nil")
		return err
	}

	// Get a message from the pool to reduce allocations
	sqsMsg := getSQSMessageFromPool()
	defer returnSQSMessageToPool(sqsMsg)

	// Parse the SQS message into the pooled struct
	if err := p.parseMessage(eventType, message, sqsMsg); err != nil {
		wrappedErr := errors.Wrap(err, "failed to parse SQS message")
		span.RecordError(wrappedErr)
		span.SetStatus(codes.Error, "failed to parse message")
		return wrappedErr
	}

	// Add event type to span
	span.SetAttributes(attribute.String("event.type", sqsMsg.EventType))

	// Record pool usage metrics
	if p.registry != nil {
		if counter := p.registry.Get(MetricsKeyPoolHits); counter != nil {
			if c, ok := counter.(metrics.Counter); ok {
				c.Inc(1)
			}
		}
	}

	// Detect source from headers
	detectedSource := p.detectSourceFromHeaders(sqsMsg)

	// Add environment to span attributes
	span.SetAttributes(attribute.String("environment", detectedSource))

	// Extract and add installation ID if available
	installationID := extractInstallationID(sqsMsg.Payload)
	if installationID > 0 {
		span.SetAttributes(attribute.Int64("github.installation_id", installationID))
	}

	// Add enriched SQS context metadata for tracing
	ctx = context.WithValue(ctx, SQSEventSourceKey, "sqs")
	ctx = context.WithValue(ctx, SQSEventEnvironment, detectedSource)
	ctx = context.WithValue(ctx, SQSQueueName, eventType)
	ctx = context.WithValue(ctx, SQSMessageID, aws.ToString(message.MessageId))
	if message.ReceiptHandle != nil {
		ctx = context.WithValue(ctx, SQSReceiptHandle, aws.ToString(message.ReceiptHandle))
	}

	msgLogger := p.logger.With().
		Str("delivery_id", sqsMsg.DeliveryID).
		Str("message_id", aws.ToString(message.MessageId)).
		Str("environment", detectedSource).
		Str("event_type", sqsMsg.EventType).
		Str("queue_name", eventType).
		Str("source", "sqs").
		Logger()

	// Log Host header if present
	if sqsMsg.Headers != nil {
		if host, ok := sqsMsg.Headers["Host"].(string); ok {
			msgLogger = msgLogger.With().Str("host_header", host).Logger()
		}
	}

	ctx = msgLogger.WithContext(ctx)

	msgLogger.Debug().Msg("Processing SQS message")

	// Check idempotency to prevent duplicate processing
	if p.idempotency != nil && p.idempotency.CheckAndMark(sqsMsg.DeliveryID) {
		msgLogger.Info().
			Str("delivery_id", sqsMsg.DeliveryID).
			Msg("Duplicate message detected - skipping processing")

		// Delete the message since we've already processed it
		return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
	}

	// Record processing start time for metrics
	start := time.Now()

	handler, scheduler := p.selectHandler(sqsMsg)

	// Find the appropriate handler
	var err error
	if handler == nil {
		msgLogger.Debug().Msgf("No handler for event type: %s", sqsMsg.EventType)
		// Delete message since we can't process it anyway
		return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
	}

	// Convert payload to bytes for the handler
	payloadBytes := []byte(sqsMsg.Payload)

	// Process based on configured mode
	if p.config.ProcessingMode == "direct" {
		// Direct processing mode - bypass scheduler, use worker pool
		err = p.processViaDirect(ctx, sqsMsg, handler, payloadBytes, msgLogger)
	} else {
		// Legacy scheduler mode
		err = p.processViaScheduler(ctx, sqsMsg, handler, scheduler, payloadBytes, msgLogger)
	}

	// Record metrics with environment context
	p.recordMetrics(sqsMsg.EventType, detectedSource, start, err)

	if err != nil {
		// Record error in tracing span
		span.RecordError(err)

		// Use smart error classification from handler package
		// This reuses the same logic as InstallationManager for consistency
		isRetryable := policyhandler.IsRetryableError(err)
		isNotFound := policyhandler.IsInstallationNotFoundError(err)
		isAuth := policyhandler.IsAuthenticationError(err)

		// Add error classification to span
		span.SetAttributes(
			attribute.Bool("error.retryable", isRetryable),
			attribute.Bool("error.not_found", isNotFound),
			attribute.Bool("error.auth", isAuth),
		)

		// Log with appropriate level based on error type
		if isNotFound {
			msgLogger.Info().
				Err(err).
				Bool("retryable", false).
				Msg("GitHub App not installed on repository - will not retry")
		} else if isAuth {
			msgLogger.Warn().
				Err(err).
				Bool("retryable", false).
				Msg("Authentication/authorization error - will not retry")
		} else if isRetryable {
			msgLogger.Warn().
				Err(err).
				Bool("retryable", true).
				Int("retry_count", sqsMsg.RetryCount).
				Msg("Transient error processing GitHub event - will retry")
		} else {
			msgLogger.Error().
				Err(err).
				Bool("retryable", false).
				Msg("Permanent error processing GitHub event - will not retry")
		}

		// Set span status based on error type
		if isNotFound || isAuth {
			span.SetStatus(codes.Error, "non-retryable error")
		} else if isRetryable {
			span.SetStatus(codes.Error, "retryable error")
		} else {
			span.SetStatus(codes.Error, "permanent error")
		}

		// Only retry if the error is retryable and we haven't exceeded retry limit
		if isRetryable && p.config.EnableRetry && sqsMsg.RetryCount < p.config.MaxRetries {
			return p.handleRetry(ctx, queueURL, message, sqsMsg, msgLogger)
		}

		// For non-retryable errors (404s, auth errors), delete the message
		// so it doesn't keep getting retried unnecessarily
		if !isRetryable {
			msgLogger.Info().
				Msg("Deleting message with non-retryable error")
			return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
		}

		// For retryable errors that exceeded retry limit, return error
		// so message goes to DLQ if configured
		return err
	}

	// Mark span as successful
	span.SetStatus(codes.Ok, "message processed successfully")

	// Delete the message from the queue on successful processing
	return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
}

// parseMessage parses an SQS message into our internal format
// The sqsMsg parameter should be obtained from getSQSMessageFromPool()
// This function fills the provided message structure to avoid allocations
func (p *Processor) parseMessage(eventType string, message types.Message, sqsMsg *SQSMessage) error {
	// Try to unmarshal as our structured SQS message format
	if err := json.Unmarshal([]byte(*message.Body), sqsMsg); err != nil {
		// If it's not our expected format, check if it's a GitHub webhook with headers
		var webhookData map[string]interface{}
		if err2 := json.Unmarshal([]byte(*message.Body), &webhookData); err2 == nil {
			// Check if this looks like a GitHub webhook with headers at the top level
			if headers, hasHeaders := webhookData["headers"].(map[string]interface{}); hasHeaders {
				// Extract payload if it exists separately
				var payload json.RawMessage
				if payloadData, hasPayload := webhookData["payload"]; hasPayload {
					// Structured format with explicit payload field
					payload, _ = json.Marshal(payloadData)
				} else {
					// Webhook format: headers at root level, GitHub event data is everything else
					// Extract the GitHub event by excluding the headers field
					payloadData := make(map[string]interface{})
					for k, v := range webhookData {
						if k != "headers" {
							payloadData[k] = v
						}
					}
					payload, _ = json.Marshal(payloadData)
				}

				sqsMsg.EventType = eventType
				sqsMsg.DeliveryID = aws.ToString(message.MessageId)
				sqsMsg.Headers = headers
				sqsMsg.Payload = payload

				p.logger.Debug().
					Interface("headers", headers).
					Int("payload_fields", len(webhookData)-1).
					Msg("Parsed GitHub webhook with headers")
			} else {
				// No headers found, treat entire body as payload
				sqsMsg.EventType = eventType
				sqsMsg.DeliveryID = aws.ToString(message.MessageId)
				sqsMsg.Payload = json.RawMessage(*message.Body)
			}
		} else {
			// Complete fallback - couldn't parse at all, treat as raw payload
			sqsMsg.EventType = eventType
			sqsMsg.DeliveryID = aws.ToString(message.MessageId)
			sqsMsg.Payload = json.RawMessage(*message.Body)
		}
	}

	// Set default event type if not provided
	if sqsMsg.EventType == "" {
		sqsMsg.EventType = eventType
	}

	// Ensure payload is set if it's still nil (shouldn't happen, but be safe)
	if sqsMsg.Payload == nil || len(sqsMsg.Payload) == 0 {
		sqsMsg.Payload = json.RawMessage(*message.Body)
	}

	return nil
}

// deleteMessage removes a successfully processed message from the queue
func (p *Processor) deleteMessage(ctx context.Context, queueURL string, receiptHandle *string, logger zerolog.Logger) error {
	_, err := p.sqsClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
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

// handleRetry implements custom retry logic with exponential backoff and jitter
// Uses cenkalti/backoff library for production-grade retry behavior
func (p *Processor) handleRetry(ctx context.Context, queueURL string, message types.Message, sqsMsg *SQSMessage, logger zerolog.Logger) error {
	sqsMsg.RetryCount++

	// Re-queue the message with updated retry count
	retryBody, err := json.Marshal(sqsMsg)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal retry message")
		return err
	}

	// Calculate delay using exponential backoff with jitter
	// This prevents thundering herd when multiple messages retry simultaneously
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Second
	b.MaxInterval = 300 * time.Second // 5 minutes max
	b.Multiplier = 2.0
	b.RandomizationFactor = 0.5 // ±50% jitter
	b.MaxElapsedTime = 0        // No max elapsed time (we control via retry count)

	// Calculate the delay based on retry count
	var delay time.Duration
	for i := 0; i < sqsMsg.RetryCount; i++ {
		delay = b.NextBackOff()
	}

	// Add additional randomization to prevent synchronized retries
	// This further reduces thundering herd probability
	if delay > 0 {
		maxJitter := delay / 4 // Up to 25% additional jitter
		if maxJitter > 0 {
			jitter := time.Duration(rand.Int63n(int64(maxJitter)))
			delay = delay + jitter
		}
	}

	// Cap at max to ensure we don't exceed SQS limits (15 minutes max delay)
	// Minimum delay of 1 second to ensure proper retry spacing
	if delay > 300*time.Second {
		delay = 300 * time.Second
	} else if delay < 1*time.Second {
		delay = 1 * time.Second
	}

	// Record backoff duration in metrics
	if p.registry != nil {
		if timer := p.registry.Get(MetricsKeyRetryBackoffTime); timer != nil {
			if t, ok := timer.(metrics.Timer); ok {
				t.Update(delay)
			}
		}
		if counter := p.registry.Get(MetricsKeyRetryAttemptsTotal); counter != nil {
			if c, ok := counter.(metrics.Counter); ok {
				c.Inc(1)
			}
		}
	}

	_, err = p.sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:     aws.String(queueURL),
		MessageBody:  aws.String(string(retryBody)),
		DelaySeconds: int32(delay.Seconds()),
	})

	if err != nil {
		logger.Error().Err(err).Msg("Failed to send retry message")
		return err
	}

	logger.Info().
		Int("retry_count", sqsMsg.RetryCount).
		Dur("delay", delay).
		Msg("Retrying message with exponential backoff and jitter")

	// Delete the original message
	return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, logger)
}

// recordMetrics records processing metrics if registry is available
// Includes environment context for better observability
func (p *Processor) recordMetrics(eventType, environment string, start time.Time, err error) {
	if p.registry == nil {
		return
	}

	duration := time.Since(start)

	// Log structured metrics for observability
	logEvent := p.logger.With().
		Str("event_type", eventType).
		Str("environment", environment).
		Str("source", "sqs").
		Dur("processing_duration", duration).
		Bool("success", err == nil).
		Logger()

	if err != nil {
		logEvent.Debug().Msg("SQS message processing completed with error")
	} else {
		logEvent.Debug().Msg("SQS message processing completed successfully")
	}

	// Record processing time per event type
	if processingTimer := metrics.GetOrRegisterTimer(fmt.Sprintf("%s.%s", MetricsKeyProcessingTime, eventType), p.registry); processingTimer != nil {
		processingTimer.UpdateSince(start)
	}

	// Record environment-specific processing time
	if envTimer := metrics.GetOrRegisterTimer(fmt.Sprintf("%s.%s.%s", MetricsKeyProcessingTime, environment, eventType), p.registry); envTimer != nil {
		envTimer.UpdateSince(start)
	}

	// Record success/failure counters
	if err != nil {
		if failedCounter := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesFailed, eventType), p.registry); failedCounter != nil {
			failedCounter.Inc(1)
		}
		// Environment-specific failure counter
		if envFailedCounter := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s.%s", MetricsKeyMessagesFailed, environment, eventType), p.registry); envFailedCounter != nil {
			envFailedCounter.Inc(1)
		}
	} else {
		if processedCounter := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s", MetricsKeyMessagesProcessed, eventType), p.registry); processedCounter != nil {
			processedCounter.Inc(1)
		}
		// Environment-specific success counter
		if envProcessedCounter := metrics.GetOrRegisterCounter(fmt.Sprintf("%s.%s.%s", MetricsKeyMessagesProcessed, environment, eventType), p.registry); envProcessedCounter != nil {
			envProcessedCounter.Inc(1)
		}
	}
}

// detectSourceFromHeaders examines the headers in the SQS message to determine source
func (p *Processor) detectSourceFromHeaders(sqsMsg *SQSMessage) string {
	// Check headers for Host field
	if sqsMsg.Headers != nil {
		if host, ok := sqsMsg.Headers["Host"].(string); ok {
			// If Host contains "ghec", it's cloud
			if strings.Contains(strings.ToLower(host), "ghec") {
				p.logger.Debug().
					Str("host", host).
					Str("detected_source", "cloud").
					Msg("Detected cloud source from Host header containing 'ghec'")
				return "cloud"
			}
			// Otherwise it's enterprise
			p.logger.Debug().
				Str("host", host).
				Str("detected_source", "enterprise").
				Msg("Detected enterprise source from Host header (no 'ghec')")
			return "enterprise"
		}
	}

	// Fallback: check legacy source field for backward compatibility
	if sqsMsg.Source == "enterprise" {
		p.logger.Debug().
			Str("legacy_source", sqsMsg.Source).
			Msg("Using legacy source field for routing")
		return "enterprise"
	}

	// Default to cloud (consistent with HTTP routing)
	p.logger.Debug().
		Msg("No Host header or source field found, defaulting to cloud")
	return "cloud"
}

func (p *Processor) selectHandler(sqsMsg *SQSMessage) (githubapp.EventHandler, githubapp.Scheduler) {
	source := p.detectSourceFromHeaders(sqsMsg)

	// Record routing decision metrics
	if p.registry != nil {
		routingMetric := metrics.GetOrRegisterCounter(
			fmt.Sprintf("sqs.routing.%s.%s", source, sqsMsg.EventType),
			p.registry,
		)
		routingMetric.Inc(1)
	}

	if source == "enterprise" {
		enterpriseHandler, exists := p.enterpriseHandlers[sqsMsg.EventType]
		if !exists {
			return nil, nil
		}
		return enterpriseHandler, p.enterpriseScheduler
	} else {
		cloudHandler, exists := p.cloudHandlers[sqsMsg.EventType]
		if !exists {
			return nil, nil
		}
		return cloudHandler, p.cloudScheduler
	}
}

// processViaDirect processes an event directly using worker pools
func (p *Processor) processViaDirect(ctx context.Context, sqsMsg *SQSMessage, handler githubapp.EventHandler, payload []byte, logger zerolog.Logger) error {
	// Create child span for handler execution to track processing time
	_, span := otel.Tracer("policy-bot.sqs-processor").Start(ctx, "handler.execute_direct",
		trace.WithAttributes(
			attribute.String("processing_mode", "direct"),
			attribute.String("event.type", sqsMsg.EventType),
		),
	)
	defer span.End()

	if p.workerPoolMgr == nil {
		err := errors.New("worker pool manager not initialized for direct processing")
		span.RecordError(err)
		span.SetStatus(codes.Error, "worker pool manager not initialized")
		return err
	}

	// Get worker count for this event type (default to 5)
	workerCount := 5
	if count, exists := p.queueWorkers[sqsMsg.EventType]; exists && count > 0 {
		workerCount = count
	}

	// Get or create worker pool for this event type
	pool := p.workerPoolMgr.GetOrCreatePool(sqsMsg.EventType, workerCount, handler)

	// Add worker capacity to span
	span.SetAttributes(attribute.Int("worker.capacity", workerCount))

	logger.Debug().
		Str("processing_mode", "direct").
		Int("worker_capacity", workerCount).
		Msg("Processing event via worker pool")

	// Execute through circuit breaker if enabled
	// This provides fail-fast behavior when GitHub API or downstream services are unhealthy
	executeHandler := func() error {
		return pool.Process(ctx, sqsMsg.EventType, sqsMsg.DeliveryID, payload)
	}

	var err error
	if p.circuitBreaker != nil {
		// Detect environment to use appropriate circuit breaker
		environment := p.detectSourceFromHeaders(sqsMsg)

		logger.Debug().
			Str("environment", environment).
			Msg("Executing handler via circuit breaker")

		err = p.circuitBreaker.Execute(environment, executeHandler)

		// Log circuit breaker rejections (use gobreaker's exported error variables)
		if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
			logger.Warn().
				Str("environment", environment).
				Err(err).
				Msg("Circuit breaker rejected request - system is unhealthy")
		}
	} else {
		// Fallback to direct execution if circuit breaker disabled
		err = executeHandler()
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "worker pool processing failed")
		logger.Error().
			Err(err).
			Msg("Worker pool processing failed")
		return err
	}

	span.SetStatus(codes.Ok, "handler executed successfully")
	logger.Debug().Msg("Successfully processed event via worker pool")
	return nil
}

// processViaScheduler processes an event using the legacy scheduler
// Note: Circuit breaker wraps scheduler execution for consistent fail-fast behavior
func (p *Processor) processViaScheduler(ctx context.Context, sqsMsg *SQSMessage, handler githubapp.EventHandler, scheduler githubapp.Scheduler, payload []byte, logger zerolog.Logger) error {
	// Create child span for handler execution to track processing time
	_, span := otel.Tracer("policy-bot.sqs-processor").Start(ctx, "handler.execute_scheduler",
		trace.WithAttributes(
			attribute.String("processing_mode", "scheduler"),
			attribute.String("event.type", sqsMsg.EventType),
		),
	)
	defer span.End()

	logger.Debug().
		Str("processing_mode", "scheduler").
		Msg("Processing event via scheduler")

	// Execute through circuit breaker if enabled
	executeHandler := func() error {
		// Create a dispatch for the scheduler
		dispatch := githubapp.Dispatch{
			Handler:    handler,
			EventType:  sqsMsg.EventType,
			DeliveryID: sqsMsg.DeliveryID,
			Payload:    payload,
		}

		// Use the scheduler to process the event (maintains consistency with HTTP path)
		return scheduler.Schedule(ctx, dispatch)
	}

	var err error
	if p.circuitBreaker != nil {
		// Detect environment to use appropriate circuit breaker
		environment := p.detectSourceFromHeaders(sqsMsg)

		logger.Debug().
			Str("environment", environment).
			Msg("Executing handler via circuit breaker (scheduler mode)")

		err = p.circuitBreaker.Execute(environment, executeHandler)

		// Log circuit breaker rejections (use gobreaker's exported error variables)
		if err == gobreaker.ErrOpenState || err == gobreaker.ErrTooManyRequests {
			logger.Warn().
				Str("environment", environment).
				Err(err).
				Msg("Circuit breaker rejected scheduler request - system is unhealthy")
		}
	} else {
		// Fallback to direct execution if circuit breaker disabled
		err = executeHandler()
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "scheduler processing failed")
		logger.Error().
			Err(err).
			Msg("Scheduler processing failed")
		return err
	}

	span.SetStatus(codes.Ok, "handler executed successfully")
	logger.Debug().Msg("Successfully scheduled event")
	return nil
}

// SetQueueWorkers sets the worker count per event type for direct processing
func (p *Processor) SetQueueWorkers(queueWorkers map[string]int) {
	p.queueWorkers = queueWorkers
}
