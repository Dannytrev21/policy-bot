# SQS Configuration Update Implementation Plan

## Overview
Update the Policy Bot SQS configuration to support event-type organization with environment-specific controls (ghec/ghes) and automatic region URL selection (east/west) based on deployment region.

## Current Configuration Analysis

### Test Config Structure (test-config.yml)
The user has already updated the test configuration with the new structure:
```yaml
queues:
  pull_request:
    east_region_url: "http://sqs.us-east-1..."
    west_region_url: "http://sqs.us-west-2..."
    event_routing: "both"
    ghec_enabled: true
    ghes_enabled: true
    queue_workers: 3
```

## Key Requirements
1. **Unit test coverage > 80%** for server/sqsconsumer package
2. **Event-type based organization** - Each queue config organized by GitHub event type
3. **Environment controls** - Ability to turn on/off SQS events by environment (ghec/ghes)
4. **Region URL selection** - Choose correct region URL (east/west) based on app region

## Implementation Approach

### Phase 1: Update Configuration Structures

#### 1.1 Update server/config.go
- Replace the current `Queues map[string]string` with a new structure
- Add support for event configurations with multiple URLs and environment flags
- Implement region detection and URL selection logic

New config structure:
```go
type EventQueueConfig struct {
    EastRegionURL  string `yaml:"east_region_url"`
    WestRegionURL  string `yaml:"west_region_url"`
    EventRouting   string `yaml:"event_routing"`   // "http", "sqs", or "both"
    GHECEnabled    bool   `yaml:"ghec_enabled"`
    GHESEnabled    bool   `yaml:"ghes_enabled"`
    QueueWorkers   int    `yaml:"queue_workers"`
}

type SQSConfig struct {
    Enabled         bool                        `yaml:"enabled"`
    Region          string                      `yaml:"region"`
    EndpointURL     string                      `yaml:"endpoint_url"`
    ProcessingMode  string                      `yaml:"processing_mode"`
    Queues          map[string]EventQueueConfig `yaml:"queues"`  // NEW
    // ... keep other fields
}
```

#### 1.2 Add Region Detection Logic
- Detect AWS region from environment or config
- Select appropriate queue URL based on region
- Support fallback logic if region not detected

### Phase 2: Update SQS Consumer

#### 2.1 Update consumer.go
- Modify `New()` to handle new config structure
- Update queue URL selection based on region
- Filter queues based on environment enablement

#### 2.2 Update processor.go
- Ensure processor works with new config
- Add environment-aware routing logic

### Phase 3: Testing

#### 3.1 Unit Tests
- Test region URL selection
- Test environment filtering
- Test configuration parsing and validation
- Achieve > 80% coverage

#### 3.2 Integration Tests
- Test with LocalStack
- Verify event routing works correctly
- Test environment-specific filtering

## Detailed Implementation Steps

### Step 1: Update Config Structure (server/config.go)
1. Define `EventQueueConfig` struct
2. Update `SQSConfig` to use new structure
3. Add `GetQueueURL(eventType, region)` method
4. Add `IsEventEnabled(eventType, environment)` method
5. Add validation for new config format
6. Maintain backward compatibility with old format

### Step 2: Implement Region Detection (server/config.go)
1. Add `DetectRegion()` method that checks:
   - AWS_REGION environment variable
   - Config.Region field
   - Default to us-east-1
2. Add `SelectQueueURL(config, region)` logic:
   - If region contains "west", use west_region_url
   - Otherwise use east_region_url
   - Fallback to any available URL if specific region URL missing

### Step 3: Update Consumer Logic (sqsconsumer/consumer.go)
1. Modify `New()` to:
   - Detect current region
   - Build queue map with correct URLs
   - Filter based on environment (ghec/ghes)
2. Update `Start()` to use new queue configuration
3. Ensure health checks work with new structure

### Step 4: Update Processor (sqsconsumer/processor.go)
1. Ensure compatibility with new config
2. Add environment-aware metrics
3. Update routing logic if needed

### Step 5: Create Comprehensive Tests
1. Unit tests for:
   - Config parsing and validation
   - Region detection and URL selection
   - Environment filtering
   - Queue worker configuration
2. Integration tests for:
   - End-to-end event processing
   - Environment-specific routing
   - Region failover scenarios

## Code Changes Summary

### Files to Modify:
1. `server/config.go` - New config structures and methods
2. `server/sqsconsumer/consumer.go` - Handle new config format
3. `server/sqsconsumer/processor.go` - Minor updates for compatibility
4. `server/server.go` - Pass environment context to consumer

### Files to Create:
1. `server/config_test.go` - Unit tests for new config logic
2. `server/sqsconsumer/consumer_test.go` - Enhanced tests
3. `server/sqsconsumer/processor_test.go` - Enhanced tests

## Testing Strategy

### Unit Test Coverage Goals:
- `server/config.go`: 95%+ coverage
- `server/sqsconsumer/consumer.go`: 85%+ coverage
- `server/sqsconsumer/processor.go`: 85%+ coverage
- Overall sqsconsumer package: 80%+ coverage

### Test Scenarios:
1. **Region Selection**:
   - East region selection
   - West region selection
   - Fallback when region URL missing
   - Invalid region handling

2. **Environment Filtering**:
   - GHEC-only events
   - GHES-only events
   - Both environments enabled
   - Neither environment enabled

3. **Configuration Validation**:
   - Valid new format
   - Backward compatibility with old format
   - Missing required fields
   - Invalid routing strategies

4. **Integration**:
   - Events processed correctly with new config
   - Metrics recorded properly
   - Health checks function

## Migration Path

### Backward Compatibility:
1. Support both old and new config formats initially
2. Log deprecation warnings for old format
3. Provide migration guide

### Rollout:
1. Deploy with backward compatibility
2. Update configurations gradually
3. Remove old format support in future release

## Success Criteria
✅ Unit test coverage > 80% for sqsconsumer package (**89.6% achieved**)
✅ Configs organized by event type
✅ Environment-specific enable/disable working
✅ Correct region URL selection based on deployment
✅ All existing functionality preserved
✅ All unit tests passing
✅ All integration tests passing
✅ Legacy config code removed

## Completion Summary (2025-10-15)

### Work Completed
All tasks have been successfully completed! The SQS configuration has been fully updated with:

#### 1. Legacy Code Removal
- Removed all legacy fields from `server/config.go`: `LegacyQueues`, `EventQueues`, `EventRouting`, `EnvironmentEventRouting`, `QueueWorkers`
- Removed deprecated structs: `QueueConfig`, `EnvironmentRouting`
- Kept only: `Queues map[string]EventQueueConfig`

#### 2. Configuration Simplification
- All config methods updated to use only new `Queues` format
- Methods simplified: `GetQueueURL()`, `GetQueueWorkers()`, `GetEventRouting()`, `GetEnabledQueuesForEnvironment()`

#### 3. SQS Consumer Updates
- Updated `server/sqsconsumer/consumer.go` to remove legacy field support
- Simplified `BuildQueueMap()` to only process new format
- Updated `NewWithEnvironment()` to build queue workers from EventQueueConfig

#### 4. Integration Test Updates
- Changed `IntegrationTestConfig.SQSQueueURLs` from `map[string]string` to `map[string]server.EventQueueConfig`
- Updated all 6 integration test files
- Fixed all queue URL access patterns throughout tests

#### 5. Unit Test Updates
- Fixed all unit test expectations in `config_test.go` and `consumer_test.go`
- Updated map[string]string expectations to use plain URL strings
- Updated worker allocation tests to use EventQueueConfig.QueueWorkers

### Test Results
- **Unit Tests**: ALL PASSING with **89.6% coverage** (exceeds 80% target)
- **Integration Tests**: ALL PASSING with 73.5% coverage
- **No tests skipped**

### Key Features Working
- ✅ Event-type based queue organization
- ✅ GHEC/GHES environment filtering
- ✅ East/West region URL selection
- ✅ Per-event worker configuration
- ✅ Event routing control (http/sqs/both)

### Files Modified
- `server/config.go` - Removed legacy fields, simplified methods
- `server/sqsconsumer/consumer.go` - Removed legacy support
- `server/sqsconsumer/config_test.go` - Fixed unit tests
- `server/sqsconsumer/consumer_test.go` - Fixed unit tests
- `server/server.go` - Updated config conversion
- `test/*.go` (6 files) - Updated integration tests

### Status: COMPLETE ✅

---

## October 15, 2025 - SQS Event Parsing Enhancement

### Work Completed
Fixed SQS message parsing to properly handle GitHub webhook format where headers are at the root level alongside the event data.

#### Problem Fixed
The sample SQS event in `test/test_events/sqs_status_event.json` has a structure where:
- Headers are at the root level in a `headers` field
- GitHub event data (status event fields) are also at root level alongside headers
- The previous parser was including the headers field in the payload sent to handlers

#### Solution Implemented
Updated `parseMessage` function in `server/sqsconsumer/processor.go` (lines 235-244):
- When headers are detected at root level without a separate `payload` field
- Creates a new map excluding the `headers` key
- Marshals only the GitHub event data (everything except headers) as the payload
- Headers are properly separated into `sqsMsg.Headers`

#### Code Changes
- Modified `server/sqsconsumer/processor.go:parseMessage()` to exclude headers from payload
- Added logic to extract GitHub event data by iterating through all fields except "headers"
- Maintained backward compatibility with existing structured message formats

### Test Results
- **Unit Tests**: ALL PASSING with **88.9% coverage** (exceeds 80% target)
  - 77 test cases passed
  - Coverage: `coverage: 88.9% of statements`
- **Integration Tests**: ALL PASSING
  - All comprehensive tests passed
  - Event routing working correctly for cloud and enterprise
  - SQS message processing successful with proper header detection

### Key Features Validated
- ✅ Webhook format with headers at root level properly parsed
- ✅ Headers extracted and separated from event payload
- ✅ GitHub event data (excluding headers) sent to handlers
- ✅ Cloud/Enterprise routing based on Host header working
- ✅ All existing message formats still supported

### Files Modified
- `server/sqsconsumer/processor.go` - Updated parseMessage() function

### Status: COMPLETE ✅