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
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server"
	"github.com/palantir/policy-bot/server/sqsconsumer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComprehensive_SQSToWorkerPoolToHandlers validates the complete SQS → Worker Pool → Event Handler flow
func TestComprehensive_SQSToWorkerPoolToHandlers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 3
	config.WorkerPoolSize = 5

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	// Create test handlers that track which handler was invoked
	cloudHandler := NewRoutingTestHandler([]string{"pull_request", "status"}, "cloud")
	enterpriseHandler := NewRoutingTestHandler([]string{"pull_request", "status"}, "enterprise")

	srv, _, cleanup := setupTestServerWithHandlers(t, config, cloudHandler, enterpriseHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("SQS to Cloud Handler", func(t *testing.T) {
		cloudHandler.Reset()
		enterpriseHandler.Reset()

		// Send message with GHEC host header
		event := GitHubEvent{Type: "pull_request", Action: "opened", Number: 100}
		sendSQSMessageWithHost(t, sqsClient, config.SQSQueueURLs[event.Type], event, "api.github.ghec.com")

		waitForEventsGeneric(t, cloudHandler, 1, 5*time.Second)

		// Verify cloud handler was invoked
		assert.Equal(t, 1, cloudHandler.GetEventCount(), "cloud handler should receive the event")
		assert.Equal(t, 0, enterpriseHandler.GetEventCount(), "enterprise handler should not receive the event")

		events := cloudHandler.GetEvents()
		require.Len(t, events, 1)
		assert.Equal(t, "sqs", events[0].Source)
	})

	t.Run("SQS to Enterprise Handler", func(t *testing.T) {
		cloudHandler.Reset()
		enterpriseHandler.Reset()

		// Send message with enterprise host header
		event := GitHubEvent{Type: "pull_request", Action: "opened", Number: 101}
		sendSQSMessageWithHost(t, sqsClient, config.SQSQueueURLs[event.Type], event, "github.enterprise.com")

		waitForEventsGeneric(t, enterpriseHandler, 1, 5*time.Second)

		// Verify enterprise handler was invoked
		assert.Equal(t, 0, cloudHandler.GetEventCount(), "cloud handler should not receive the event")
		assert.Equal(t, 1, enterpriseHandler.GetEventCount(), "enterprise handler should receive the event")

		events := enterpriseHandler.GetEvents()
		require.Len(t, events, 1)
		assert.Equal(t, "sqs", events[0].Source)
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
}

// TestComprehensive_WebhookPathUnchanged verifies webhook processing still works (regression test)
func TestComprehensive_WebhookPathUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 2

	testHandler := NewTestEventHandler([]string{"pull_request", "issue_comment"})

	srv, serverURL, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	t.Run("Webhook Events Still Process", func(t *testing.T) {
		testHandler.Reset()

		events := []GitHubEvent{
			{Type: "pull_request", Action: "opened", Number: 200},
			{Type: "issue_comment", Action: "created", Number: 201},
		}

		for _, event := range events {
			sendHTTPWebhook(t, serverURL, config.WebhookSecret, event)
		}

		waitForEvents(t, testHandler, len(events), 5*time.Second)

		receivedEvents := testHandler.GetEvents()
		assert.Len(t, receivedEvents, len(events))

		// Verify all events came from HTTP
		for _, event := range receivedEvents {
			assert.Equal(t, "http", event.Source, "webhook events should have source=http")
		}
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
}

// TestComprehensive_DualProcessing tests webhook and SQS running simultaneously
func TestComprehensive_DualProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 3
	config.WorkerPoolSize = 5

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	testHandler := NewTestEventHandler([]string{"pull_request", "pull_request_review", "issue_comment", "status"})

	srv, serverURL, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("Simultaneous HTTP and SQS Processing", func(t *testing.T) {
		testHandler.Reset()

		var wg sync.WaitGroup

		// Send HTTP events
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				event := GitHubEvent{Type: "pull_request", Action: "synchronize", Number: 300 + i}
				sendHTTPWebhook(t, serverURL, config.WebhookSecret, event)
				time.Sleep(50 * time.Millisecond)
			}
		}()

		// Send SQS events
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				event := GitHubEvent{Type: "status", State: "success", Context: fmt.Sprintf("test-%d", i)}
				sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
				time.Sleep(75 * time.Millisecond)
			}
		}()

		wg.Wait()

		// Wait for all events to be processed
		waitForEvents(t, testHandler, 10, 10*time.Second)

		receivedEvents := testHandler.GetEvents()
		assert.Len(t, receivedEvents, 10)

		// Count events by source
		httpCount := 0
		sqsCount := 0
		for _, event := range receivedEvents {
			if event.Source == "http" {
				httpCount++
			} else if event.Source == "sqs" {
				sqsCount++
			}
		}

		assert.Equal(t, 5, httpCount, "should receive 5 HTTP events")
		assert.Equal(t, 5, sqsCount, "should receive 5 SQS events")
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
}

// TestComprehensive_GracefulShutdown validates graceful shutdown with in-flight messages
func TestComprehensive_GracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 2
	config.WorkerPoolSize = 3

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	slowHandler := NewSlowTestHandler([]string{"pull_request"}, 500*time.Millisecond)

	srv, _, cleanup := setupTestServerWithHandlers(t, config, slowHandler, slowHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	// Send events that will take time to process
	for i := 0; i < 5; i++ {
		event := GitHubEvent{Type: "pull_request", Action: "opened", Number: 400 + i}
		sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
	}

	// Let a few start processing
	time.Sleep(1 * time.Second)

	// Initiate shutdown while events are in flight
	shutdownStart := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	err := srv.Shutdown(shutdownCtx)
	shutdownDuration := time.Since(shutdownStart)

	assert.NoError(t, err, "shutdown should complete without error")
	assert.LessOrEqual(t, shutdownDuration, 10*time.Second, "shutdown should complete within timeout")

	// Verify that in-flight messages were processed
	processedCount := slowHandler.GetEventCount()
	t.Logf("Processed %d events during graceful shutdown", processedCount)
	assert.GreaterOrEqual(t, processedCount, 1, "at least some in-flight events should have been processed")
}

// TestComprehensive_HighVolumeBurst tests system under load
func TestComprehensive_HighVolumeBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 5
	config.WorkerPoolSize = 10
	config.SQSMaxMessages = 10

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	testHandler := NewTestEventHandler([]string{"pull_request", "status"})

	srv, _, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	t.Run("Burst of 50 Events", func(t *testing.T) {
		testHandler.Reset()

		// Send burst of events
		eventCount := 50
		for i := 0; i < eventCount; i++ {
			event := GitHubEvent{Type: "status", State: "success", Context: fmt.Sprintf("burst-%d", i)}
			sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
		}

		// Wait for all events to be processed
		waitForEvents(t, testHandler, eventCount, 15*time.Second)

		receivedCount := testHandler.GetEventCount()
		assert.Equal(t, eventCount, receivedCount, "all burst events should be processed")
	})

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
}

// TestComprehensive_CloudVsEnterpriseRouting validates handler selection based on Host header
func TestComprehensive_CloudVsEnterpriseRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 2

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	cloudHandler := NewRoutingTestHandler([]string{"pull_request", "status"}, "cloud")
	enterpriseHandler := NewRoutingTestHandler([]string{"pull_request", "status"}, "enterprise")

	srv, _, cleanup := setupTestServerWithHandlers(t, config, cloudHandler, enterpriseHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	testCases := []struct {
		name              string
		hostHeader        string
		expectedHandler   *RoutingTestHandler
		unexpectedHandler *RoutingTestHandler
	}{
		{
			name:              "GHEC routes to cloud handler",
			hostHeader:        "api.github.ghec.com",
			expectedHandler:   cloudHandler,
			unexpectedHandler: enterpriseHandler,
		},
		{
			name:              "ghec (lowercase) routes to cloud handler",
			hostHeader:        "api.github.ghec.internal",
			expectedHandler:   cloudHandler,
			unexpectedHandler: enterpriseHandler,
		},
		{
			name:              "Enterprise host routes to enterprise handler",
			hostHeader:        "github.enterprise.corp",
			expectedHandler:   enterpriseHandler,
			unexpectedHandler: cloudHandler,
		},
		{
			name:              "Custom enterprise host routes to enterprise handler",
			hostHeader:        "github.company.com",
			expectedHandler:   enterpriseHandler,
			unexpectedHandler: cloudHandler,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cloudHandler.Reset()
			enterpriseHandler.Reset()

			event := GitHubEvent{Type: "pull_request", Action: "opened", Number: 500}
			sendSQSMessageWithHost(t, sqsClient, config.SQSQueueURLs[event.Type], event, tc.hostHeader)

			waitForEventsGeneric(t, tc.expectedHandler, 1, 5*time.Second)

			assert.Equal(t, 1, tc.expectedHandler.GetEventCount(), "expected handler should receive the event")
			assert.Equal(t, 0, tc.unexpectedHandler.GetEventCount(), "unexpected handler should not receive the event")
		})
	}

	// Cleanup
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
}

// Helper types and functions

// EventCounter is an interface for handlers that can count events
type EventCounter interface {
	GetEventCount() int
}

// waitForEventsGeneric waits for a handler to receive the expected number of events
func waitForEventsGeneric(t *testing.T, handler EventCounter, expectedCount int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if handler.GetEventCount() >= expectedCount {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("Timeout waiting for %d events, got %d", expectedCount, handler.GetEventCount())
}

// RoutingTestHandler tracks which environment handler was invoked
type RoutingTestHandler struct {
	mu          sync.Mutex
	events      []ReceivedEvent
	eventTypes  []string
	environment string // "cloud" or "enterprise"
}

func NewRoutingTestHandler(eventTypes []string, environment string) *RoutingTestHandler {
	return &RoutingTestHandler{
		eventTypes:  eventTypes,
		environment: environment,
		events:      make([]ReceivedEvent, 0),
	}
}

func (h *RoutingTestHandler) Handles() []string {
	return h.eventTypes
}

func (h *RoutingTestHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	source := "http"
	if ctx.Value(sqsconsumer.SQSEventSourceKey) == "sqs" {
		source = "sqs"
	}

	event := ReceivedEvent{
		EventType:  eventType,
		DeliveryID: deliveryID,
		Source:     source,
		Timestamp:  time.Now(),
		Payload:    string(payload),
	}

	h.events = append(h.events, event)

	return nil
}

func (h *RoutingTestHandler) GetEvents() []ReceivedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	events := make([]ReceivedEvent, len(h.events))
	copy(events, h.events)
	return events
}

func (h *RoutingTestHandler) GetEventCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *RoutingTestHandler) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = h.events[:0]
}

// SlowTestHandler simulates slow event processing
type SlowTestHandler struct {
	*TestEventHandler
	processingDelay time.Duration
}

func NewSlowTestHandler(eventTypes []string, delay time.Duration) *SlowTestHandler {
	return &SlowTestHandler{
		TestEventHandler: NewTestEventHandler(eventTypes),
		processingDelay:  delay,
	}
}

func (h *SlowTestHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	// Simulate slow processing
	time.Sleep(h.processingDelay)
	return h.TestEventHandler.Handle(ctx, eventType, deliveryID, payload)
}

// setupTestServerWithHandlers creates a test server with separate cloud and enterprise handlers
func setupTestServerWithHandlers(t *testing.T, config *IntegrationTestConfig, cloudHandler, enterpriseHandler githubapp.EventHandler) (*server.Server, string, func()) {
	port := 8080
	if config.ServerPort != "" {
		parsedPort, err := strconv.Atoi(config.ServerPort)
		require.NoError(t, err, "invalid server port")
		port = parsedPort
	}

	publicURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	workerCount := config.WorkerPoolSize
	if workerCount <= 0 {
		workerCount = 2
	}

	sqsWorkers := config.SQSWorkersPerQueue
	if sqsWorkers <= 0 {
		sqsWorkers = 2
	}

	serverConfig := &server.Config{
		Server: baseapp.HTTPConfig{
			Address:   "127.0.0.1",
			Port:      port,
			PublicURL: publicURL,
		},
		Logging: server.LoggingConfig{
			Level: "debug",
			Text:  true,
		},
		GithubCloud: server.GithubAppConfig{
			Config: githubapp.Config{
				WebURL:   "https://github.com",
				V3APIURL: "https://api.github.com",
				V4APIURL: "https://api.github.com/graphql",
				App: struct {
					IntegrationID int64  `yaml:"integration_id" json:"integrationId"`
					WebhookSecret string `yaml:"webhook_secret" json:"webhookSecret"`
					PrivateKey    string `yaml:"private_key" json:"privateKey"`
				}{
					IntegrationID: 123456,
					WebhookSecret: config.WebhookSecret,
					PrivateKey:    testPrivateKey,
				},
			},
		},
		GithubEnterprise: server.GithubAppConfig{
			Config: githubapp.Config{
				WebURL:   "https://github.enterprise.com",
				V3APIURL: "https://github.enterprise.com/api/v3",
				V4APIURL: "https://github.enterprise.com/api/graphql",
				App: struct {
					IntegrationID int64  `yaml:"integration_id" json:"integrationId"`
					WebhookSecret string `yaml:"webhook_secret" json:"webhookSecret"`
					PrivateKey    string `yaml:"private_key" json:"privateKey"`
				}{
					IntegrationID: 789012,
					WebhookSecret: config.WebhookSecret,
					PrivateKey:    testPrivateKey,
				},
			},
		},
		Sessions: server.SessionsConfig{
			Key: "test-session-key",
		},
		Workers: server.WorkerConfig{
			Workers:   workerCount,
			QueueSize: 10,
		},
		SQS: server.SQSConfig{
			Enabled:           config.UseLocalStack,
			Region:            "us-east-1",
			EndpointURL:       config.LocalStackURL,
			Queues:            config.SQSQueueURLs,
			WorkersPerQueue:   sqsWorkers,
			MaxMessages:       config.SQSMaxMessages,
			VisibilityTimeout: 30,
			WaitTimeSeconds:   config.SQSWaitTimeSeconds,
			ShutdownTimeout:   5 * time.Second,
		},
	}

	// Create server with separate cloud and enterprise handlers
	srv, err := server.NewWithSeparateHandlers(serverConfig, []githubapp.EventHandler{cloudHandler}, []githubapp.EventHandler{enterpriseHandler})
	require.NoError(t, err)

	cleanup := func() {
		// Cleanup is handled by the test
	}

	return srv, publicURL, cleanup
}

// sendSQSMessageWithHost sends an SQS message with a specific Host header
func sendSQSMessageWithHost(t *testing.T, client *sqs.Client, queueURL string, event GitHubEvent, host string) {
	payload := createGitHubPayload(event)

	headers := map[string]interface{}{
		"Host":            host,
		"X-GitHub-Event":  event.Type,
		"Content-Type":    "application/json",
		"X-Hub-Signature": "sha256=test",
	}

	sqsMessage := sqsconsumer.SQSMessage{
		EventType:  event.Type,
		DeliveryID: fmt.Sprintf("sqs-delivery-%d", time.Now().UnixNano()),
		Headers:    headers,
		Payload:    json.RawMessage(payload),
	}

	messageBody, err := json.Marshal(sqsMessage)
	require.NoError(t, err)

	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(string(messageBody)),
	})
	require.NoError(t, err)
}
