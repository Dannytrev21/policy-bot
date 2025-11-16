# Policy-Bot Middleware & SQS Integration Plan

## Current State Assessment ✅

### Integration Status: **WORKING**

All tests are passing and the integration between the middleware and SQS consumer is functioning correctly. The systems work together seamlessly with proper separation of concerns.

## How the Integration Works

### 1. HTTP Path (via Middleware)
- **Entry Point**: HTTP webhooks arrive at `/api/github/hook`
- **Routing**: `middleware.SelectWebhookDispatcher` routes based on headers:
  - `X-GitHub-Enterprise-Host` → Enterprise dispatcher
  - `x-dcp-destination-host` → Cloud dispatcher
  - Default → Cloud dispatcher
- **Handlers**: Separate enterprise/cloud handlers process events

### 2. SQS Path (via Processor)
- **Entry Point**: Messages consumed from SQS queues
- **Routing**: `processor.selectHandler` routes based on message source field:
  - `source: "enterprise"` → Enterprise handlers & scheduler
  - `source: "cloud"` or default → Cloud handlers & scheduler
- **Handlers**: Same enterprise/cloud handlers as HTTP path

### 3. Shared Components
- Both paths use the same event handlers (enterprise/cloud)
- Both paths use the same schedulers (enterprise/cloud)
- Consistent routing logic (headers for HTTP, source field for SQS)

## Testing Verification ✅

### Completed Tests
- [x] SQS Consumer unit tests - **PASSING**
- [x] SQS Processor unit tests - **PASSING**
- [x] Middleware unit tests - **PASSING**
- [x] Middleware routing integration tests - **PASSING**
- [x] SQS and HTTP consistency tests - **PASSING**

## KISS Implementation Checklist

### Current Working Features (No Changes Needed)
- [x] Middleware routes HTTP webhooks based on headers
- [x] SQS processor routes messages based on source field
- [x] Both systems use the same handlers and schedulers
- [x] Error handling and retry logic in place
- [x] Metrics and logging implemented

### Optional Minor Enhancements (Low Priority)

#### 1. Add Source Enrichment for SQS Messages
**Why**: Ensure SQS messages have proper source field
**Effort**: 15 minutes
**Priority**: Low (defaults work fine)

```go
// In SQS message producer (if you control it)
message := SQSMessage{
    EventType:  "pull_request",
    DeliveryID: deliveryID,
    Payload:    payload,
    Source:     detectSourceFromPayload(payload), // "enterprise" or "cloud"
}
```

#### 2. Document Source Field Convention
**Why**: Clear documentation for SQS message producers
**Effort**: 10 minutes
**Priority**: Low

Add to SQS configuration docs:
```yaml
# SQS Message Format
{
  "event_type": "pull_request",
  "delivery_id": "uuid",
  "payload": {...},
  "source": "enterprise|cloud"  # Required for proper routing
}
```

#### 3. Add Integration Test for SQS Source Routing
**Why**: Explicitly test SQS source-based routing
**Effort**: 20 minutes
**Priority**: Low (unit tests cover this)

```go
func TestSQSProcessorSourceRouting(t *testing.T) {
    // Test that processor correctly routes based on source field
}
```

## Best Practices Already Implemented ✅

1. **Separation of Concerns**
   - Middleware handles HTTP routing
   - Processor handles SQS routing
   - No cross-dependencies

2. **Consistent Routing Logic**
   - Both use same concept (enterprise vs cloud)
   - Both use same handlers and schedulers
   - Clear defaults (cloud)

3. **Proper Error Handling**
   - Retry mechanisms in place
   - Graceful degradation
   - Comprehensive logging

4. **Testing Coverage**
   - Unit tests for each component
   - Integration tests for routing
   - Consistency tests between paths

## Summary

**Status: Integration is fully functional and requires no immediate changes.**

The middleware and SQS consumer work together correctly:
- HTTP requests are routed via headers
- SQS messages are routed via source field
- Both use the same underlying handlers
- All tests are passing

The implementation follows the KISS principle - each system handles its own routing independently with no complex interdependencies.

## No Action Required

The integration is working as designed. The optional enhancements listed above are nice-to-haves but not necessary for proper functioning.