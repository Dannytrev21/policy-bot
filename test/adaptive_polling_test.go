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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdaptivePolling_WorkerSaturation verifies that adaptive polling
// prevents message timeouts when all workers are busy
func TestAdaptivePolling_WorkerSaturation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping adaptive polling test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	// Use minimal workers to make saturation easy to achieve
	config.SQSWorkersPerQueue = 2
	config.WorkerPoolSize = 5
	config.SQSProcessingMode = "direct" // Required for adaptive polling
	config.AdaptivePolling = true       // Enable adaptive polling

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

	// Create a slow handler that will saturate workers
	slowHandler := &AdaptivePollingSlowHandler{
		processingTime: 2 * time.Second, // Slow enough to saturate workers
		eventTypes:     []string{"pull_request"},
	}

	srv, _, cleanup := setupTestServerWithHandlers(t, config, slowHandler, slowHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("saturate workers and verify no timeouts", func(t *testing.T) {
		slowHandler.Reset()

		// Send 10 messages but only 2 workers available
		// This should trigger adaptive polling backoff
		messageCount := 10
		queueURL := config.SQSQueueURLs["pull_request"].EastRegionURL

		t.Logf("Sending %d messages to saturate %d workers", messageCount, config.SQSWorkersPerQueue)

		for i := 0; i < messageCount; i++ {
			event := GitHubEvent{
				Type:   "pull_request",
				Action: "opened",
				Number: 2000 + i,
			}
			sendSQSMessageWithHost(t, sqsClient, queueURL, event, "api.github.com")
		}

		// Wait for all messages to be processed
		// With 2 workers and 2s processing time, this should take ~10 seconds
		// (5 batches of 2 messages each)
		timeout := time.After(30 * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				t.Fatalf("Timeout waiting for messages to be processed. Processed: %d/%d",
					slowHandler.GetProcessedCount(), messageCount)
			case <-ticker.C:
				processed := slowHandler.GetProcessedCount()
				t.Logf("Processed: %d/%d", processed, messageCount)
				if processed >= messageCount {
					goto done
				}
			}
		}

	done:
		// Verify all messages were processed
		assert.Equal(t, messageCount, slowHandler.GetProcessedCount(),
			"All messages should be processed without timeouts")

		// Verify no failures (which would indicate worker pool timeouts)
		assert.Equal(t, 0, slowHandler.GetFailureCount(),
			"There should be no failures from worker pool timeouts")

		t.Logf("Successfully processed all %d messages with only %d workers",
			messageCount, config.SQSWorkersPerQueue)
	})

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(shutdownCtx))
}

// AdaptivePollingSlowHandler processes events slowly to saturate workers
type AdaptivePollingSlowHandler struct {
	mu             sync.RWMutex
	processedCount int32
	failureCount   int32
	processingTime time.Duration
	eventTypes     []string
}

func (h *AdaptivePollingSlowHandler) Reset() {
	atomic.StoreInt32(&h.processedCount, 0)
	atomic.StoreInt32(&h.failureCount, 0)
}

func (h *AdaptivePollingSlowHandler) GetProcessedCount() int {
	return int(atomic.LoadInt32(&h.processedCount))
}

func (h *AdaptivePollingSlowHandler) GetFailureCount() int {
	return int(atomic.LoadInt32(&h.failureCount))
}

func (h *AdaptivePollingSlowHandler) Handles() []string {
	return h.eventTypes
}

func (h *AdaptivePollingSlowHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	// Simulate slow processing
	time.Sleep(h.processingTime)

	atomic.AddInt32(&h.processedCount, 1)
	return nil
}
