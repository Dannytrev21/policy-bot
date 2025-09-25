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

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server"
	"github.com/palantir/policy-bot/server/sqsconsumer"
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
	fmt.Printf("✅ Received %s event from %s: %s (delivery: %s)\n",
		eventType, source, eventType, deliveryID)

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

func main() {
	fmt.Println("🚀 Starting Policy Bot Event Processing Test")
	fmt.Println("=============================================")

	// Create test handler
	testHandler := NewTestEventHandler([]string{
		"pull_request", "pull_request_review", "issue_comment", "status", "check_run", "installation",
	})

	// Create server configuration
	serverConfig := &server.Config{
		Server: baseapp.HTTPConfig{
			Address:   "127.0.0.1",
			Port:      8080,
			PublicURL: "http://localhost:8080",
		},
		Logging: server.LoggingConfig{
			Level: "info",
			Text:  true,
		},
		Github: githubapp.Config{
			WebURL:   "https://github.com",
			V3APIURL: "https://api.github.com",
			V4APIURL: "https://api.github.com/graphql",
			App: struct {
				IntegrationID int64  `yaml:"integration_id" json:"integrationId"`
				WebhookSecret string `yaml:"webhook_secret" json:"webhookSecret"`
				PrivateKey    string `yaml:"private_key" json:"privateKey"`
			}{
				IntegrationID: 123456,
				WebhookSecret: "test-webhook-secret-123",
				PrivateKey:    testPrivateKey,
			},
		},
		Sessions: server.SessionsConfig{
			Key: "test-session-key",
		},
		Workers: server.WorkerConfig{
			Workers:   2,
			QueueSize: 10,
		},
		SQS: server.SQSConfig{
			Enabled:     true,
			Region:      "us-east-1",
			EndpointURL: "http://localhost:4566", // LocalStack
			Queues: map[string]string{
				"pull_request":        "http://localhost:4566/000000000000/github-pull-request",
				"pull_request_review": "http://localhost:4566/000000000000/github-pull-request-review",
				"issue_comment":       "http://localhost:4566/000000000000/github-issue-comment",
				"status":              "http://localhost:4566/000000000000/github-status",
				"check_run":           "http://localhost:4566/000000000000/github-check-run",
				"installation":        "http://localhost:4566/000000000000/github-installation",
			},
			WorkersPerQueue:   2,
			MaxMessages:       5,
			VisibilityTimeout: 30,
			WaitTimeSeconds:   5,
			ShutdownTimeout:   5 * time.Second,
		},
	}

	// Create server with test handler
	srv, err := server.NewWithTestHandlers(serverConfig, []githubapp.EventHandler{testHandler})
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Start the server
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	// Wait for server to start
	time.Sleep(2 * time.Second)

	fmt.Printf("🌐 Server started at http://localhost:8080\n")
	fmt.Printf("📊 SQS Consumer enabled with LocalStack at http://localhost:4566\n")
	fmt.Println()

	// Test HTTP webhook events
	fmt.Println("📡 Testing HTTP Webhook Events...")
	testHTTPEvents(testHandler)

	// Test SQS events
	fmt.Println("\n📨 Testing SQS Events...")
	testSQSEvents(testHandler)

	// Test parallel events
	fmt.Println("\n🔄 Testing Parallel HTTP and SQS Events...")
	testParallelEvents(testHandler, srv.Address())

	// Wait a bit for all events to be processed
	time.Sleep(3 * time.Second)

	// Show results
	fmt.Println("\n📈 Test Results:")
	fmt.Println("================")
	events := testHandler.GetEvents()

	httpCount := 0
	sqsCount := 0
	for _, event := range events {
		if event.Source == "http" {
			httpCount++
		} else if event.Source == "sqs" {
			sqsCount++
		}
	}

	fmt.Printf("✅ Total events received: %d\n", len(events))
	fmt.Printf("🌐 HTTP events: %d\n", httpCount)
	fmt.Printf("📨 SQS events: %d\n", sqsCount)
	fmt.Println()

	// Show detailed events
	for i, event := range events {
		fmt.Printf("%d. %s event from %s (delivery: %s)\n",
			i+1, event.EventType, event.Source, event.DeliveryID)
	}

	// Shutdown server
	fmt.Println("\n🛑 Shutting down server...")
	cancel()
	select {
	case err := <-serverErrChan:
		if err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
		}
	case <-time.After(5 * time.Second):
		fmt.Println("Server shutdown timeout")
	}

	fmt.Println("✅ Test completed!")
}

func testHTTPEvents(handler *TestEventHandler) {
	handler.Reset()

	events := []map[string]interface{}{
		{"type": "pull_request", "action": "opened", "number": 1},
		{"type": "issue_comment", "action": "created", "number": 1},
		{"type": "status", "state": "success", "context": "test"},
	}

	for _, event := range events {
		sendHTTPWebhook(event)
		time.Sleep(500 * time.Millisecond)
	}
}

func testSQSEvents(handler *TestEventHandler) {
	handler.Reset()

	// Setup SQS client
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		fmt.Printf("❌ Failed to create AWS config: %v\n", err)
		return
	}

	sqsClient := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String("http://localhost:4566")
	})

	events := []map[string]interface{}{
		{"type": "pull_request_review", "action": "submitted", "number": 2},
		{"type": "status", "state": "failure", "context": "test-sqs"},
		{"type": "check_run", "action": "completed", "number": 3},
	}

	for _, event := range events {
		sendSQSMessage(sqsClient, event)
		time.Sleep(500 * time.Millisecond)
	}
}

func testParallelEvents(handler *TestEventHandler, serverAddr string) {
	handler.Reset()

	// Send HTTP events
	go func() {
		events := []map[string]interface{}{
			{"type": "pull_request", "action": "synchronize", "number": 3},
			{"type": "issue_comment", "action": "edited", "number": 3},
		}
		for _, event := range events {
			sendHTTPWebhook(event)
			time.Sleep(200 * time.Millisecond)
		}
	}()

	// Send SQS events
	go func() {
		cfg, err := config.LoadDefaultConfig(context.Background(),
			config.WithRegion("us-east-1"),
		)
		if err != nil {
			return
		}

		sqsClient := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
			o.BaseEndpoint = aws.String("http://localhost:4566")
		})

		events := []map[string]interface{}{
			{"type": "pull_request_review", "action": "dismissed", "number": 4},
			{"type": "status", "state": "pending", "context": "test-parallel"},
		}
		for _, event := range events {
			sendSQSMessage(sqsClient, event)
			time.Sleep(300 * time.Millisecond)
		}
	}()
}

func sendHTTPWebhook(event map[string]interface{}) {
	payload := createGitHubPayload(event)
	signature := createWebhookSignature(payload, "test-webhook-secret-123")

	req, err := http.NewRequest("POST", "http://localhost:8080/api/github/hook",
		bytes.NewReader(payload))
	if err != nil {
		fmt.Printf("❌ Failed to create HTTP request: %v\n", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event["type"].(string))
	req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("test-delivery-%d", time.Now().UnixNano()))
	req.Header.Set("X-Hub-Signature-256", signature)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ Failed to send HTTP webhook: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("❌ HTTP webhook failed with status: %d\n", resp.StatusCode)
	}
}

func sendSQSMessage(client *sqs.Client, event map[string]interface{}) {
	payload := createGitHubPayload(event)

	sqsMessage := sqsconsumer.SQSMessage{
		EventType:  event["type"].(string),
		DeliveryID: fmt.Sprintf("sqs-delivery-%d", time.Now().UnixNano()),
		Payload:    json.RawMessage(payload),
		Source:     "sqs",
	}

	messageBody, err := json.Marshal(sqsMessage)
	if err != nil {
		fmt.Printf("❌ Failed to marshal SQS message: %v\n", err)
		return
	}

	queueURL := fmt.Sprintf("http://localhost:4566/000000000000/github-%s", event["type"])
	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(string(messageBody)),
	})
	if err != nil {
		fmt.Printf("❌ Failed to send SQS message: %v\n", err)
	}
}

func createGitHubPayload(event map[string]interface{}) []byte {
	payload := map[string]interface{}{
		"action": event["action"],
		"number": event["number"],
	}

	if state, ok := event["state"]; ok {
		payload["state"] = state
	}
	if context, ok := event["context"]; ok {
		payload["context"] = context
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
