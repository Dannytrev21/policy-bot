# SQS Host-Based Routing Update Plan

## Current State Analysis

### Problem Statement
The SQS consumer currently routes messages based on a simple `source` field, but the actual GitHub webhook messages coming through SQS contain headers within the JSON payload. Specifically, there's a "Host" field that indicates the source:
- If Host contains "ghec" → Use GitHub Cloud resources
- Otherwise → Use GitHub Enterprise resources

### Current Implementation
```go
// Current routing logic in processor.go
func (p *Processor) selectHandler(sqsMsg SQSMessage) {
    if sqsMsg.Source == "enterprise" {
        // Use enterprise handlers
    } else {
        // Use cloud handlers
    }
}
```

### Required Changes
The SQS processor needs to inspect the actual GitHub webhook payload structure to determine routing, similar to how the HTTP middleware inspects headers.

## Implementation Plan (KISS Approach)

### Step 1: Update SQSMessage Structure
Update the message structure to properly represent GitHub webhook payloads coming from SQS.

```go
// Updated SQSMessage structure
type SQSMessage struct {
    EventType  string                 `json:"event_type"`
    DeliveryID string                 `json:"delivery_id"`
    Headers    map[string]interface{} `json:"headers,omitempty"`  // GitHub webhook headers
    Payload    json.RawMessage        `json:"payload"`
    RetryCount int                    `json:"retry_count,omitempty"`
    Source     string                 `json:"source,omitempty"` // Deprecated - kept for backward compat
}
```

### Step 2: Add Host Detection Logic
Create a helper function to detect the source from the message headers.

```go
// detectSourceFromHeaders examines the headers in the SQS message to determine source
func (p *Processor) detectSourceFromHeaders(sqsMsg SQSMessage) string {
    // Check headers for Host field
    if sqsMsg.Headers != nil {
        if host, ok := sqsMsg.Headers["Host"].(string); ok {
            // If Host contains "ghec", it's cloud
            if strings.Contains(strings.ToLower(host), "ghec") {
                return "cloud"
            }
            // Otherwise it's enterprise
            return "enterprise"
        }
    }

    // Fallback: check legacy source field
    if sqsMsg.Source == "enterprise" {
        return "enterprise"
    }

    // Default to cloud (consistent with HTTP routing)
    return "cloud"
}
```

### Step 3: Update selectHandler Method
Modify the handler selection to use the new detection logic.

```go
func (p *Processor) selectHandler(sqsMsg SQSMessage) (githubapp.EventHandler, githubapp.Scheduler) {
    source := p.detectSourceFromHeaders(sqsMsg)

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
```

### Step 4: Update Message Parsing
Ensure the parseMessage function properly handles the headers field.

```go
func (p *Processor) parseMessage(eventType string, message types.Message) (SQSMessage, error) {
    var sqsMsg SQSMessage

    // Try to unmarshal as structured message with headers
    if err := json.Unmarshal([]byte(*message.Body), &sqsMsg); err != nil {
        // If it's not our expected format, try to parse as GitHub webhook
        var webhookData map[string]interface{}
        if err2 := json.Unmarshal([]byte(*message.Body), &webhookData); err2 == nil {
            // Check if this looks like a GitHub webhook with headers
            if headers, hasHeaders := webhookData["headers"].(map[string]interface{}); hasHeaders {
                sqsMsg = SQSMessage{
                    EventType:  eventType,
                    DeliveryID: aws.ToString(message.MessageId),
                    Headers:    headers,
                    Payload:    json.RawMessage(*message.Body),
                }
            } else {
                // Fallback to raw payload
                sqsMsg = SQSMessage{
                    EventType:  eventType,
                    DeliveryID: aws.ToString(message.MessageId),
                    Payload:    json.RawMessage(*message.Body),
                }
            }
        } else {
            // Complete fallback
            sqsMsg = SQSMessage{
                EventType:  eventType,
                DeliveryID: aws.ToString(message.MessageId),
                Payload:    json.RawMessage(*message.Body),
            }
        }
    }

    return sqsMsg, nil
}
```

### Step 5: Add Logging for Debugging
Enhance logging to track routing decisions.

```go
func (p *Processor) ProcessMessage(ctx context.Context, eventType, queueURL string, message types.Message) error {
    // ... existing code ...

    source := p.detectSourceFromHeaders(sqsMsg)

    msgLogger := p.logger.With().
        Str("delivery_id", sqsMsg.DeliveryID).
        Str("message_id", aws.ToString(message.MessageId)).
        Str("detected_source", source).  // Add detected source
        Str("event_type", sqsMsg.EventType).
        Logger()

    if sqsMsg.Headers != nil {
        if host, ok := sqsMsg.Headers["Host"].(string); ok {
            msgLogger = msgLogger.With().Str("host_header", host).Logger()
        }
    }

    // ... rest of processing ...
}
```

## Testing Strategy

### Unit Tests
Create tests to verify Host-based routing:

```go
func TestProcessor_DetectSourceFromHeaders(t *testing.T) {
    tests := []struct {
        name           string
        message        SQSMessage
        expectedSource string
    }{
        {
            name: "ghec_in_host",
            message: SQSMessage{
                Headers: map[string]interface{}{
                    "Host": "github.ghec.example.com",
                },
            },
            expectedSource: "cloud",
        },
        {
            name: "ghes_host",
            message: SQSMessage{
                Headers: map[string]interface{}{
                    "Host": "github.enterprise.example.com",
                },
            },
            expectedSource: "enterprise",
        },
        {
            name: "no_headers_default_cloud",
            message: SQSMessage{},
            expectedSource: "cloud",
        },
        {
            name: "legacy_source_field",
            message: SQSMessage{
                Source: "enterprise",
            },
            expectedSource: "enterprise",
        },
    }

    // Run tests...
}
```

### Integration Test
Test with actual SQS message formats:

```go
func TestProcessor_RealGitHubWebhookFormat(t *testing.T) {
    // Test with actual GitHub webhook JSON structure
    githubWebhook := `{
        "headers": {
            "Host": "github.ghec.mycompany.com",
            "X-GitHub-Event": "pull_request",
            "X-GitHub-Delivery": "12345-67890"
        },
        "payload": {
            "action": "opened",
            "pull_request": {...}
        }
    }`

    // Process and verify correct routing
}
```

## Queue Configuration

The application will receive events from different queues based on event type:
- Pull request events: `codegenie-car-policy-pr` queue
- Status events: `codegenie-car-policy-status` queue
- Other events: Similar naming pattern

Both GHES and GHEC send to the same queues, differentiated by the Host header in the message.

## Rollout Strategy

1. **Backward Compatibility**: Keep support for legacy `source` field
2. **Logging**: Add comprehensive logging for routing decisions
3. **Monitoring**: Track metrics for enterprise vs cloud routing
4. **Gradual Migration**: Can run with both old and new message formats

## Summary

This plan updates the SQS processor to:
1. Inspect the Host header within GitHub webhook messages
2. Route to enterprise resources if Host doesn't contain "ghec"
3. Route to cloud resources if Host contains "ghec"
4. Maintain backward compatibility with existing `source` field
5. Follow the same routing logic as HTTP middleware for consistency

The implementation is simple (KISS principle) and maintains compatibility while adding the required Host-based routing capability.