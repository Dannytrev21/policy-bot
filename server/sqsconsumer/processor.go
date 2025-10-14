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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

const (
	// Context keys for SQS processing
	SQSEventSourceKey   = "sqs_event_source"
	SQSEventEnvironment = "sqs_event_environment"
	SQSQueueName        = "sqs_queue_name"
	SQSMessageID        = "sqs_message_id"
	SQSReceiptHandle    = "sqs_receipt_handle"

	// Metrics keys
	MetricsKeyMessagesProcessed = "sqs.messages.processed"
	MetricsKeyMessagesFailed    = "sqs.messages.failed"
	MetricsKeyProcessingTime    = "sqs.processing.time"
	MetricsKeyQueueDepth        = "sqs.queue.depth"
	MetricsKeyDLQMessages       = "sqs.dlq.messages"
)

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
	EnableRetry       bool
	MaxRetries        int
	VisibilityTimeout int
	ProcessingMode    string // "scheduler" or "direct"
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
	// Build handler map
	enterpriseHandlerMap := make(map[string]githubapp.EventHandler)
	for _, handler := range enterpriseHandlers {
		for _, eventType := range handler.Handles() {
			enterpriseHandlerMap[eventType] = handler
		}
	}

	cloudHandlerMap := make(map[string]githubapp.EventHandler)
	for _, handler := range cloudHandlers {
		for _, eventType := range handler.Handles() {
			cloudHandlerMap[eventType] = handler
		}
	}

	return &Processor{
		config:              config,
		sqsClient:           sqsClient,
		enterpriseHandlers:  enterpriseHandlerMap,
		cloudHandlers:       cloudHandlerMap,
		enterpriseScheduler: enterpriseScheduler,
		cloudScheduler:      cloudScheduler,
		workerPoolMgr:       workerPoolMgr,
		logger:              logger.With().Str("component", "sqs_processor").Logger(),
		registry:            registry,
		queueWorkers:        make(map[string]int),
	}
}

// ProcessMessage handles a single SQS message
func (p *Processor) ProcessMessage(ctx context.Context, eventType, queueURL string, message types.Message) error {
	if message.Body == nil {
		return errors.New("message body is nil")
	}

	// Parse the SQS message
	sqsMsg, err := p.parseMessage(eventType, message)
	if err != nil {
		return errors.Wrap(err, "failed to parse SQS message")
	}

	// Detect source from headers
	detectedSource := p.detectSourceFromHeaders(sqsMsg)

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

	// Record processing start time for metrics
	start := time.Now()

	handler, scheduler := p.selectHandler(sqsMsg)

	// Find the appropriate handler

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
		msgLogger.Error().Err(err).Msg("Failed to process GitHub event from SQS")

		// Handle retries if enabled
		if p.config.EnableRetry && sqsMsg.RetryCount < p.config.MaxRetries {
			return p.handleRetry(ctx, queueURL, message, sqsMsg, msgLogger)
		}

		// Don't delete the message so it will be retried by SQS
		return err
	}

	// Delete the message from the queue on successful processing
	return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
}

// parseMessage parses an SQS message into our internal format
func (p *Processor) parseMessage(eventType string, message types.Message) (SQSMessage, error) {
	var sqsMsg SQSMessage

	// Try to unmarshal as our structured SQS message format
	if err := json.Unmarshal([]byte(*message.Body), &sqsMsg); err != nil {
		// If it's not our expected format, check if it's a GitHub webhook with headers
		var webhookData map[string]interface{}
		if err2 := json.Unmarshal([]byte(*message.Body), &webhookData); err2 == nil {
			// Check if this looks like a GitHub webhook with headers at the top level
			if headers, hasHeaders := webhookData["headers"].(map[string]interface{}); hasHeaders {
				// Extract payload if it exists separately
				var payload json.RawMessage
				if payloadData, hasPayload := webhookData["payload"]; hasPayload {
					payload, _ = json.Marshal(payloadData)
				} else {
					// If no separate payload field, the whole message might be the payload
					payload = json.RawMessage(*message.Body)
				}

				sqsMsg = SQSMessage{
					EventType:  eventType,
					DeliveryID: aws.ToString(message.MessageId),
					Headers:    headers,
					Payload:    payload,
				}

				p.logger.Debug().
					Interface("headers", headers).
					Msg("Parsed GitHub webhook with headers")
			} else {
				// No headers found, treat entire body as payload
				sqsMsg = SQSMessage{
					EventType:  eventType,
					DeliveryID: aws.ToString(message.MessageId),
					Payload:    json.RawMessage(*message.Body),
				}
			}
		} else {
			// Complete fallback - couldn't parse at all, treat as raw payload
			sqsMsg = SQSMessage{
				EventType:  eventType,
				DeliveryID: aws.ToString(message.MessageId),
				Payload:    json.RawMessage(*message.Body),
			}
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

	return sqsMsg, nil
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

// handleRetry implements custom retry logic if enabled
func (p *Processor) handleRetry(ctx context.Context, queueURL string, message types.Message, sqsMsg SQSMessage, logger zerolog.Logger) error {
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
		Msg("Retrying message with exponential backoff")

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
func (p *Processor) detectSourceFromHeaders(sqsMsg SQSMessage) string {
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

func (p *Processor) selectHandler(sqsMsg SQSMessage) (githubapp.EventHandler, githubapp.Scheduler) {
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
func (p *Processor) processViaDirect(ctx context.Context, sqsMsg SQSMessage, handler githubapp.EventHandler, payload []byte, logger zerolog.Logger) error {
	if p.workerPoolMgr == nil {
		return errors.New("worker pool manager not initialized for direct processing")
	}

	// Get worker count for this event type (default to 5)
	workerCount := 5
	if count, exists := p.queueWorkers[sqsMsg.EventType]; exists && count > 0 {
		workerCount = count
	}

	// Get or create worker pool for this event type
	pool := p.workerPoolMgr.GetOrCreatePool(sqsMsg.EventType, workerCount, handler)

	logger.Debug().
		Str("processing_mode", "direct").
		Int("worker_capacity", workerCount).
		Msg("Processing event via worker pool")

	// Process the event through the worker pool
	err := pool.Process(ctx, sqsMsg.EventType, sqsMsg.DeliveryID, payload)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("Worker pool processing failed")
		return err
	}

	logger.Debug().Msg("Successfully processed event via worker pool")
	return nil
}

// processViaScheduler processes an event using the legacy scheduler
func (p *Processor) processViaScheduler(ctx context.Context, sqsMsg SQSMessage, handler githubapp.EventHandler, scheduler githubapp.Scheduler, payload []byte, logger zerolog.Logger) error {
	logger.Debug().
		Str("processing_mode", "scheduler").
		Msg("Processing event via scheduler")

	// Create a dispatch for the scheduler
	dispatch := githubapp.Dispatch{
		Handler:    handler,
		EventType:  sqsMsg.EventType,
		DeliveryID: sqsMsg.DeliveryID,
		Payload:    payload,
	}

	// Use the scheduler to process the event (maintains consistency with HTTP path)
	err := scheduler.Schedule(ctx, dispatch)
	if err != nil {
		logger.Error().
			Err(err).
			Msg("Scheduler processing failed")
		return err
	}

	logger.Debug().Msg("Successfully scheduled event")
	return nil
}

// SetQueueWorkers sets the worker count per event type for direct processing
func (p *Processor) SetQueueWorkers(queueWorkers map[string]int) {
	p.queueWorkers = queueWorkers
}
