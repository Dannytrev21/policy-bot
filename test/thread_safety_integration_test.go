// Copyright 2025 Palantir Technologies, Inc.
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

package test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_ThreadSafety validates that the SQS consumer and worker pools
// are thread-safe under high concurrent load
func TestIntegration_ThreadSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping thread safety test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 10
	config.WorkerPoolSize = 20
	config.SQSProcessingMode = "direct"
	config.AdaptivePolling = true

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueConfig := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueConfig.EastRegionURL))
	}

	// Thread safety tracking handler
	handler := &ThreadSafetyHandler{
		eventTypes:        []string{"pull_request", "status", "issue_comment"},
		processingTime:    50 * time.Millisecond,
		concurrencyTracker: make(map[string]*int32),
	}

	// Initialize concurrency trackers for each event type
	for _, eventType := range handler.eventTypes {
		var counter int32
		handler.concurrencyTracker[eventType] = &counter
	}

	srv, _, cleanup := setupTestServerWithHandlers(t, config, handler, handler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("concurrent multi-event processing", func(t *testing.T) {
		handler.Reset()

		// Send messages from multiple goroutines to test thread safety
		var wg sync.WaitGroup
		messageCount := 100
		goroutineCount := 10

		for g := 0; g < goroutineCount; g++ {
			wg.Add(1)
			go func(goroutineID int) {
				defer wg.Done()

				for i := 0; i < messageCount/goroutineCount; i++ {
					eventType := handler.eventTypes[i%len(handler.eventTypes)]
					queueURL := config.SQSQueueURLs[eventType].EastRegionURL

					event := GitHubEvent{
						Type:   eventType,
						Action: "opened",
						Number: goroutineID*1000 + i,
					}

					sendSQSMessageWithHost(t, sqsClient, queueURL, event, "api.github.com")
				}
			}(g)
		}

		wg.Wait()

		// Wait for processing with timeout
		timeout := time.After(30 * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatalf("Timeout waiting for messages. Processed: %d/%d",
					handler.GetProcessedCount(), messageCount)
			case <-ticker.C:
				if handler.GetProcessedCount() >= messageCount {
					goto verifyThreadSafety
				}
			}
		}

	verifyThreadSafety:
		// Verify thread safety
		assert.Equal(t, messageCount, handler.GetProcessedCount(),
			"All messages should be processed")

		// Check that max concurrency never exceeded worker limits
		for eventType, maxConcurrency := range handler.GetMaxConcurrency() {
			workerLimit := config.SQSWorkersPerQueue
			assert.LessOrEqual(t, maxConcurrency, int32(workerLimit),
				"Max concurrency for %s should not exceed worker limit", eventType)
		}

		// Verify no race conditions occurred
		assert.Equal(t, 0, handler.GetRaceConditionCount(),
			"No race conditions should occur")
	})

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(shutdownCtx))
}

// TestIntegration_WorkerPoolExhaustion tests recovery from worker pool exhaustion
func TestIntegration_WorkerPoolExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping worker pool exhaustion test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 3  // Very limited workers
	config.WorkerPoolSize = 3
	config.SQSProcessingMode = "direct"
	config.AdaptivePolling = true

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueConfig := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueConfig.EastRegionURL))
	}

	// Handler that blocks initially then recovers
	handler := &ExhaustionRecoveryHandler{
		eventTypes:         []string{"pull_request"},
		initialBlockTime:   3 * time.Second,
		normalProcessTime:  100 * time.Millisecond,
		blockMessageCount:  3, // Block first 3 messages
	}

	srv, _, cleanup := setupTestServerWithHandlers(t, config, handler, handler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("worker exhaustion and recovery", func(t *testing.T) {
		handler.Reset()

		queueURL := config.SQSQueueURLs["pull_request"].EastRegionURL

		// Send more messages than workers
		messageCount := 10

		startTime := time.Now()

		for i := 0; i < messageCount; i++ {
			event := GitHubEvent{
				Type:   "pull_request",
				Action: "opened",
				Number: 3000 + i,
			}
			sendSQSMessageWithHost(t, sqsClient, queueURL, event, "api.github.com")
		}

		// Wait for all messages to be processed
		timeout := time.After(30 * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatalf("Timeout waiting for recovery. Processed: %d/%d",
					handler.GetProcessedCount(), messageCount)
			case <-ticker.C:
				processed := handler.GetProcessedCount()
				t.Logf("Processed: %d/%d, Blocked: %d, Recovered: %d",
					processed, messageCount,
					handler.GetBlockedCount(),
					handler.GetRecoveredCount())

				if processed >= messageCount {
					goto verifyRecovery
				}
			}
		}

	verifyRecovery:
		duration := time.Since(startTime)

		// All messages should be processed
		assert.Equal(t, messageCount, handler.GetProcessedCount(),
			"All messages should be processed after recovery")

		// First 3 messages should have blocked
		assert.Equal(t, handler.blockMessageCount, handler.GetBlockedCount(),
			"Expected number of messages should block")

		// Remaining messages should process normally after recovery
		assert.Equal(t, messageCount-handler.blockMessageCount, handler.GetRecoveredCount(),
			"Remaining messages should process after recovery")

		// Total time should show recovery happened
		// (3 workers blocked for 3s, then normal processing)
		assert.Greater(t, duration, handler.initialBlockTime,
			"Total duration should include blocking time")

		t.Logf("Worker pool recovered successfully. Total time: %v", duration)
	})

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(shutdownCtx))
}

// TestIntegration_MessageOrdering tests that messages maintain relative ordering
// within the constraints of concurrent processing
func TestIntegration_MessageOrdering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping message ordering test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 1  // Single worker to test ordering
	config.WorkerPoolSize = 5
	config.SQSProcessingMode = "direct"
	config.AdaptivePolling = false  // Disable to ensure consistent polling

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueConfig := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueConfig.EastRegionURL))
	}

	// Handler that tracks processing order
	handler := &OrderTrackingHandler{
		eventTypes:     []string{"pull_request", "status"},
		processingTime: 10 * time.Millisecond,
	}

	srv, _, cleanup := setupTestServerWithHandlers(t, config, handler, handler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("message ordering within event type", func(t *testing.T) {
		handler.Reset()

		// Send ordered messages for each event type
		messageCount := 20

		// Send pull_request messages
		prQueueURL := config.SQSQueueURLs["pull_request"].EastRegionURL
		for i := 0; i < messageCount/2; i++ {
			event := GitHubEvent{
				Type:   "pull_request",
				Action: "opened",
				Number: 4000 + i,
			}
			sendSQSMessageWithHost(t, sqsClient, prQueueURL, event, "api.github.com")
			time.Sleep(10 * time.Millisecond) // Ensure ordering in SQS
		}

		// Send status messages
		statusQueueURL := config.SQSQueueURLs["status"].EastRegionURL
		for i := 0; i < messageCount/2; i++ {
			event := GitHubEvent{
				Type:   "status",
				Action: "success",
				Number: 5000 + i,
			}
			sendSQSMessageWithHost(t, sqsClient, statusQueueURL, event, "api.github.com")
			time.Sleep(10 * time.Millisecond) // Ensure ordering in SQS
		}

		// Wait for processing
		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatalf("Timeout waiting for ordering test. Processed: %d/%d",
					handler.GetProcessedCount(), messageCount)
			case <-ticker.C:
				if handler.GetProcessedCount() >= messageCount {
					goto verifyOrdering
				}
			}
		}

	verifyOrdering:
		// Verify all messages processed
		assert.Equal(t, messageCount, handler.GetProcessedCount(),
			"All messages should be processed")

		// Check ordering within each event type (with single worker, should be FIFO)
		prOrder := handler.GetProcessingOrder("pull_request")
		statusOrder := handler.GetProcessingOrder("status")

		// With a single worker per queue, messages should be processed in order
		for i := 1; i < len(prOrder); i++ {
			assert.Greater(t, prOrder[i], prOrder[i-1],
				"Pull request messages should be processed in order")
		}

		for i := 1; i < len(statusOrder); i++ {
			assert.Greater(t, statusOrder[i], statusOrder[i-1],
				"Status messages should be processed in order")
		}

		t.Logf("Message ordering maintained: PR count=%d, Status count=%d",
			len(prOrder), len(statusOrder))
	})

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(shutdownCtx))
}

// ThreadSafetyHandler tracks concurrent processing for thread safety validation
type ThreadSafetyHandler struct {
	mu                 sync.RWMutex
	processedCount     int32
	raceConditions     int32
	processingTime     time.Duration
	eventTypes         []string
	concurrencyTracker map[string]*int32
	maxConcurrency     map[string]int32
}

func (h *ThreadSafetyHandler) Reset() {
	atomic.StoreInt32(&h.processedCount, 0)
	atomic.StoreInt32(&h.raceConditions, 0)
	h.mu.Lock()
	h.maxConcurrency = make(map[string]int32)
	h.mu.Unlock()
}

func (h *ThreadSafetyHandler) GetProcessedCount() int {
	return int(atomic.LoadInt32(&h.processedCount))
}

func (h *ThreadSafetyHandler) GetRaceConditionCount() int {
	return int(atomic.LoadInt32(&h.raceConditions))
}

func (h *ThreadSafetyHandler) GetMaxConcurrency() map[string]int32 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make(map[string]int32)
	for k, v := range h.maxConcurrency {
		result[k] = v
	}
	return result
}

func (h *ThreadSafetyHandler) Handles() []string {
	return h.eventTypes
}

func (h *ThreadSafetyHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	// Track concurrent executions
	counter := h.concurrencyTracker[eventType]
	current := atomic.AddInt32(counter, 1)
	defer atomic.AddInt32(counter, -1)

	// Update max concurrency
	h.mu.Lock()
	if current > h.maxConcurrency[eventType] {
		h.maxConcurrency[eventType] = current
	}
	h.mu.Unlock()

	// Simulate processing
	time.Sleep(h.processingTime)

	// Check for race conditions (simplified check)
	before := atomic.LoadInt32(&h.processedCount)
	atomic.AddInt32(&h.processedCount, 1)
	after := atomic.LoadInt32(&h.processedCount)

	if after != before+1 {
		atomic.AddInt32(&h.raceConditions, 1)
	}

	return nil
}

// ExhaustionRecoveryHandler simulates worker pool exhaustion and recovery
type ExhaustionRecoveryHandler struct {
	mu                sync.RWMutex
	processedCount    int32
	blockedCount      int
	recoveredCount    int
	eventTypes        []string
	initialBlockTime  time.Duration
	normalProcessTime time.Duration
	blockMessageCount int
}

func (h *ExhaustionRecoveryHandler) Reset() {
	atomic.StoreInt32(&h.processedCount, 0)
	h.mu.Lock()
	h.blockedCount = 0
	h.recoveredCount = 0
	h.mu.Unlock()
}

func (h *ExhaustionRecoveryHandler) GetProcessedCount() int {
	return int(atomic.LoadInt32(&h.processedCount))
}

func (h *ExhaustionRecoveryHandler) GetBlockedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.blockedCount
}

func (h *ExhaustionRecoveryHandler) GetRecoveredCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.recoveredCount
}

func (h *ExhaustionRecoveryHandler) Handles() []string {
	return h.eventTypes
}

func (h *ExhaustionRecoveryHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	processed := int(atomic.LoadInt32(&h.processedCount))

	if processed < h.blockMessageCount {
		// Block initial messages to simulate exhaustion
		h.mu.Lock()
		h.blockedCount++
		h.mu.Unlock()

		time.Sleep(h.initialBlockTime)
	} else {
		// Normal processing after recovery
		h.mu.Lock()
		h.recoveredCount++
		h.mu.Unlock()

		time.Sleep(h.normalProcessTime)
	}

	atomic.AddInt32(&h.processedCount, 1)
	return nil
}

// OrderTrackingHandler tracks the order of message processing
type OrderTrackingHandler struct {
	mu              sync.RWMutex
	processedCount  int32
	processingOrder map[string][]int  // event type -> list of message numbers
	processingTime  time.Duration
	eventTypes      []string
}

func (h *OrderTrackingHandler) Reset() {
	atomic.StoreInt32(&h.processedCount, 0)
	h.mu.Lock()
	h.processingOrder = make(map[string][]int)
	h.mu.Unlock()
}

func (h *OrderTrackingHandler) GetProcessedCount() int {
	return int(atomic.LoadInt32(&h.processedCount))
}

func (h *OrderTrackingHandler) GetProcessingOrder(eventType string) []int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if order, exists := h.processingOrder[eventType]; exists {
		result := make([]int, len(order))
		copy(result, order)
		return result
	}
	return []int{}
}

func (h *OrderTrackingHandler) Handles() []string {
	return h.eventTypes
}

func (h *OrderTrackingHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	// Extract message number from payload
	var event GitHubEvent
	if err := parseGitHubEvent(payload, &event); err != nil {
		return fmt.Errorf("failed to parse event: %w", err)
	}

	// Track processing order
	h.mu.Lock()
	if h.processingOrder == nil {
		h.processingOrder = make(map[string][]int)
	}
	h.processingOrder[eventType] = append(h.processingOrder[eventType], event.Number)
	h.mu.Unlock()

	// Simulate processing
	time.Sleep(h.processingTime)

	atomic.AddInt32(&h.processedCount, 1)
	return nil
}