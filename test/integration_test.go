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

package test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventHandler tracks events received from both HTTP and SQS
type TestEventHandler struct {
	mu         sync.Mutex
	events     []ReceivedEvent
	eventTypes []string
}

type ReceivedEvent struct {
	EventType  string    `json:"event_type"`
	DeliveryID string    `json:"delivery_id"`
	Source     string    `json:"source"` // "http" or "sqs"
	Timestamp  time.Time `json:"timestamp"`
	Payload    string    `json:"payload"`
}

func NewTestEventHandler(eventTypes []string) *TestEventHandler {
	return &TestEventHandler{
		eventTypes: eventTypes,
		events:     make([]ReceivedEvent, 0),
	}
}

func (h *TestEventHandler) Handles() []string {
	return h.eventTypes
}

func (h *TestEventHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Determine source from context
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

	// Log the event for debugging
	logger := zerolog.Ctx(ctx)
	logger.Info().
		Str("event_type", eventType).
		Str("delivery_id", deliveryID).
		Str("source", source).
		Msg("Test handler received event")

	return nil
}

func (h *TestEventHandler) GetEvents() []ReceivedEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	events := make([]ReceivedEvent, len(h.events))
	copy(events, h.events)
	return events
}

func (h *TestEventHandler) GetEventCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *TestEventHandler) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = h.events[:0]
}

// Integration test configuration
type IntegrationTestConfig struct {
	UseLocalStack      bool
	LocalStackURL      string
	WebhookSecret      string
	ServerPort         string
	SQSQueueURLs       map[string]string
	TestDuration       time.Duration
	SQSWorkersPerQueue int
	SQSMaxMessages     int
	WorkerPoolSize     int
	SQSWaitTimeSeconds int
}

func DefaultIntegrationTestConfig() *IntegrationTestConfig {
	return &IntegrationTestConfig{
		UseLocalStack: true,
		LocalStackURL: "http://localhost:4566",
		WebhookSecret: "test-webhook-secret-123",
		ServerPort:    "8080",
		SQSQueueURLs: map[string]string{
			"pull_request":        "http://localhost:4566/000000000000/github-pull-request",
			"pull_request_review": "http://localhost:4566/000000000000/github-pull-request-review",
			"issue_comment":       "http://localhost:4566/000000000000/github-issue-comment",
			"status":              "http://localhost:4566/000000000000/github-status",
		},
		TestDuration:       10 * time.Second,
		SQSWorkersPerQueue: 2,
		SQSMaxMessages:     10,
		WorkerPoolSize:     4,
		SQSWaitTimeSeconds: 5,
	}
}

func TestIntegration_HTTPAndSQSEventProcessing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	config := DefaultIntegrationTestConfig()

	// Configure LocalStack if enabled
	var (
		localStack *LocalStackManager
		sqsClient  *sqs.Client
	)

	if config.UseLocalStack {
		localStack = NewLocalStackManager(t, LocalStackOptions{
			URL:             config.LocalStackURL,
			Region:          "us-east-1",
			RequirePresence: true,
		})
		defer localStack.Cleanup()

		config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
		for _, queueURL := range config.SQSQueueURLs {
			localStack.PurgeQueue(QueueNameFromURL(queueURL))
		}

		sqsClient = localStack.Client()
	}

	// Create test handler
	testHandler := NewTestEventHandler([]string{"pull_request", "pull_request_review", "issue_comment", "status"})

	// Setup server with test configuration
	srv, serverURL, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	// Start the server
	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	// Wait for server to start
	time.Sleep(2 * time.Second)

	// Test HTTP webhook events
	t.Run("HTTP Webhook Events", func(t *testing.T) {
		testHandler.Reset()

		events := []GitHubEvent{
			{Type: "pull_request", Action: "opened", Number: 1},
			{Type: "issue_comment", Action: "created", Number: 1},
			{Type: "status", State: "success", Context: "test"},
		}

		for _, event := range events {
			sendHTTPWebhook(t, serverURL, config.WebhookSecret, event)
		}

		// Wait for events to be processed
		waitForEvents(t, testHandler, len(events), 5*time.Second)

		receivedEvents := testHandler.GetEvents()
		assert.Len(t, receivedEvents, len(events))

		// Verify all events came from HTTP
		for _, event := range receivedEvents {
			assert.Equal(t, "http", event.Source)
		}
	})

	// Test SQS events
	if config.UseLocalStack && sqsClient != nil {
		t.Run("SQS Events", func(t *testing.T) {
			testHandler.Reset()

			events := []GitHubEvent{
				{Type: "pull_request_review", Action: "submitted", Number: 2},
				{Type: "status", State: "failure", Context: "test-sqs"},
			}

			for _, event := range events {
				sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
			}

			// Wait for events to be processed
			waitForEvents(t, testHandler, len(events), 10*time.Second)

			receivedEvents := testHandler.GetEvents()
			assert.Len(t, receivedEvents, len(events))

			// Verify all events came from SQS
			for _, event := range receivedEvents {
				assert.Equal(t, "sqs", event.Source)
			}
		})
	}

	// Test both HTTP and SQS simultaneously
	if config.UseLocalStack && sqsClient != nil {
		t.Run("Parallel HTTP and SQS Events", func(t *testing.T) {
			testHandler.Reset()

			// Send events to both paths simultaneously
			go func() {
				events := []GitHubEvent{
					{Type: "pull_request", Action: "synchronize", Number: 3},
					{Type: "issue_comment", Action: "edited", Number: 3},
				}
				for _, event := range events {
					sendHTTPWebhook(t, serverURL, config.WebhookSecret, event)
					time.Sleep(100 * time.Millisecond)
				}
			}()

			go func() {
				events := []GitHubEvent{
					{Type: "pull_request_review", Action: "dismissed", Number: 4},
					{Type: "status", State: "pending", Context: "test-parallel"},
				}
				for _, event := range events {
					sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
					time.Sleep(150 * time.Millisecond)
				}
			}()

			// Wait for all events to be processed
			waitForEvents(t, testHandler, 4, 10*time.Second)

			receivedEvents := testHandler.GetEvents()
			assert.Len(t, receivedEvents, 4)

			// Verify we got events from both sources
			httpEvents := 0
			sqsEvents := 0
			for _, event := range receivedEvents {
				if event.Source == "http" {
					httpEvents++
				} else if event.Source == "sqs" {
					sqsEvents++
				}
			}

			assert.Equal(t, 2, httpEvents, "Expected 2 HTTP events")
			assert.Equal(t, 2, sqsEvents, "Expected 2 SQS events")
		})
	}

	// Shutdown server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("Server shutdown error: %v", err)
	}
	shutdownCancel()

	select {
	case err := <-serverErrChan:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("Server error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("Server shutdown timeout")
	}
}

// Helper functions

type GitHubEvent struct {
	Type    string `json:"type"`
	Action  string `json:"action,omitempty"`
	Number  int    `json:"number,omitempty"`
	State   string `json:"state,omitempty"`
	Context string `json:"context,omitempty"`
}

func setupTestServer(t *testing.T, config *IntegrationTestConfig, handler *TestEventHandler) (*server.Server, string, func()) {
	port := 8080
	if config.ServerPort != "" {
		parsedPort, err := strconv.Atoi(config.ServerPort)
		require.NoError(t, err, "invalid server port")
		port = parsedPort
	}

	publicURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Create server configuration
	workerCount := config.WorkerPoolSize
	if workerCount <= 0 {
		workerCount = 2
	}

	sqsWorkers := config.SQSWorkersPerQueue
	if sqsWorkers <= 0 {
		sqsWorkers = 2
	}

	sqsMaxMessages := config.SQSMaxMessages
	if sqsMaxMessages <= 0 {
		sqsMaxMessages = 10
	}

	sqsWaitTime := config.SQSWaitTimeSeconds
	if sqsWaitTime < 0 {
		sqsWaitTime = 5
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
			MaxMessages:       sqsMaxMessages,
			VisibilityTimeout: 30,
			WaitTimeSeconds:   sqsWaitTime,
			ShutdownTimeout:   5 * time.Second,
		},
	}

	// Create server with test handler
	srv, err := server.NewWithTestHandlers(serverConfig, []githubapp.EventHandler{handler})
	require.NoError(t, err)

	// Get server URL
	serverURL := publicURL

	cleanup := func() {
		// Cleanup is handled by the test
	}

	return srv, serverURL, cleanup
}

func sendHTTPWebhook(t *testing.T, serverURL, secret string, event GitHubEvent) {
	payload := createGitHubPayload(event)

	signature := createWebhookSignature(payload, secret)

	req, err := http.NewRequest("POST", serverURL+"/api/github/hook", bytes.NewReader(payload))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event.Type)
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("test-delivery-%d", time.Now().UnixNano()))
	req.Header.Set("X-Hub-Signature-256", signature)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func sendSQSMessage(t *testing.T, client *sqs.Client, queueURL string, event GitHubEvent) {
	payload := createGitHubPayload(event)

	sqsMessage := sqsconsumer.SQSMessage{
		EventType:  event.Type,
		DeliveryID: fmt.Sprintf("sqs-delivery-%d", time.Now().UnixNano()),
		Payload:    json.RawMessage(payload),
		Source:     "sqs",
	}

	messageBody, err := json.Marshal(sqsMessage)
	require.NoError(t, err)

	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(string(messageBody)),
	})
	require.NoError(t, err)
}

func createGitHubPayload(event GitHubEvent) []byte {
	payload := map[string]interface{}{
		"action": event.Action,
		"number": event.Number,
	}

	if event.State != "" {
		payload["state"] = event.State
	}
	if event.Context != "" {
		payload["context"] = event.Context
	}

	// Add mock repository and other required fields
	payload["repository"] = map[string]interface{}{
		"name":      "test-repo",
		"full_name": "test-org/test-repo",
		"owner": map[string]interface{}{
			"login": "test-org",
		},
	}

	data, _ := json.Marshal(payload)
	return data
}

func createWebhookSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

func waitForEvents(t *testing.T, handler *TestEventHandler, expectedCount int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if handler.GetEventCount() >= expectedCount {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("Timeout waiting for %d events, got %d", expectedCount, handler.GetEventCount())
}

// Test private key for GitHub App (not a real key)
const testPrivateKey = `-----BEGIN PRIVATE KEY-----
MIIEvgIBADANBgkqhkiG9w0BAQEFAASCBKgwggSkAgEAAoIBAQCpBPCbbPHrtTCk
qwjuLrsXyj7WYDhBdtM9v5wXeJp92hqCj6gJBrJlY9s6IaRFN7IRUNqh+yk9BBpG
W5fpS2aI3aWCWHebQobRgWt5KSy2vIMSRK0lYj5r5FQ33npMPz9zvtOIS4f/OuC7
M/2JPJN0LXug4M5CMeqPxAX+cy6Uk9Fm97Bkt9qIpPgvgKyIanE3f0zlzkCV7Okx
KvLa9sQh1OnXDRV6LSWrH00h84HxS6Zv9ndoaGgxLWvLTQFIBrGCyGZvO6KbM3dt
/GkbGElGu3Yo41VgLlNhU8uloN/HFiK7lAUPPrLMB9BC/YPAqoHDKJ+wACn+0pxZ
0BFqKcDnAgMBAAECggEAKNABjXZMIF97JHgMSv9LvB3g+ID5dI1Nyt5GwcAkhfkx
Z49qwus0DpmDKVFQSkp9nALLGEv+lDY2ZgDd+L51Pt1Oht/32azBwzseCX6wxltU
xweAS8OiUQkscOUu4NRw7PEKQSID79R2yZ1vPkE7VdVZweomxAMroZVy4RNNDyER
mtcwuNHbAbyKYrq7iWDwpyJhSxLlH9+dXWmbIHp70J1uiWnm/Brs8HopibKS/Gya
OhKrTwpqR6WpXODIWQo4ghZIMc3SEzEiGygomQo1s/JnC+ZffNTBaF8Bt9z2O3S2
OIK3RBvafmfZjG5gWvHMu35dk6ap0tihr5jsmO3xiQKBgQDjB+Txisvn5nB3bViX
qeyQOSb1YJJ8brVHLR/IaN3F24zFcbK3I9hU102BDN4JhHW4OW4r58w6LCqrVtYd
NAzj03rqchHRHTuomEFFcHJw561dP1P7XtD4jMRy5oxp6wa0O4y33TgQr6XEklmx
Afb6WvjDOFsHFvQydjm7ShEm6wKBgQC+lg/bXvvaPtJCKl3RJpGEZUMggvMQWa1p
zO3km8aVTQHmQ0b7akdnglGHsER0DrFLn7dtqBYVfoyL994ICs95yUUh6hMG6Hk8
Gvrtz3QWz859XcCKFHMTmelvQCkudssnbmWNTrSnq+pWSyGBWoPYNL4i3n5/htDl
tB0/uQIG9QKBgE/2mNnGhDlCvfwihGCu1gaaSrGEeTPgnnLaXuZsoSguQy/L8yF0
O57uUnsQuCfsAraHa9mFBDa9Fa5RoIqaqauY8iMfWE0qGbgxIFQ/3d8MitBcHM1d
wQa6NfsuXuhzgmH6035zKWsfIqjQz0x8H6xgXFwOPmmJ2Sro3z6rQM1PAoGBAKoq
bCAPb8mOe8ct8rQyvoy6qTPXF9UKbOZhXirW13ko18BTY4ZJf1WxKsB/Jq+FCtId
2fYjtQweALlcZ7dAh70ScxJz3+c0HEMJR/CbYOiZRKH02luvJIxkyONXIy3kTUF4
tV1036IxwjqoPFM1kTCy7u1NQR72LYBa0B68Pk4dAoGBANAP3pSx3X9IacLPdCFZ
RbTpnzUlPGUfhDIztYpt4o4XJIUWTqkua17n0UFrAEsz9hWJApIF88V2a6HO2m8q
4ZQUcYsJURnVXDI1cP2XIOUbWh28l8YyEPNvdZ+gFmEPbuPPBLIPpQi+oiIFUICD
VO9x3spm/KLa3Ce3+GWdSQ24
-----END PRIVATE KEY-----`
