# LocalStackTesting Task Session

**Created**: 2025-09-27
**Status**: Ready for execution
**Priority**: High - Next milestone task from architecture plan

## Objective

Build and enhance integration testing framework with LocalStack for SQS event processing. This addresses **Phase 4, Task 2** from the policy-bot SQS architecture plan: "Build integration test framework with LocalStack".

## Context Analysis

Based on `.claude/architecture/policy-bot-sqs_plan.md`, the implementation is **80 complete** with the following status:
- ✅ Core Infrastructure: SQS consumer, processor, and integration complete
- ✅ Configuration System: Comprehensive YAML configuration with routing
-  Testing Framework: Basic unit tests and integration tests need to be completed
- 🔄 **Performance Testing**: Basic unit testing complete, load testing in progress

**Current Gap**: The integration testing framework exists but needs enhancement for comprehensive LocalStack http and sqs event testing scenarios and performance validation.

## Current Testing Infrastructure

### Existing Components
1. **Integration Test**: `/test/integration_test.go` - Comprehensive test with HTTP/SQS parallel processing
2. **Manual Test Script**: `/scripts/test-event-processing.go` - Interactive testing tool
3. **Testing Documentation**: `/TESTING.md` - Complete setup and usage guide
4. **Test Configuration**: `/config/test-config.yml` - Test server configuration

### SQS Test Coverage
- ✅ Unit tests for consumer and processor (95% coverage achieved)
- ✅ : Basic integration tests with LocalStack
- **Missing**: HTTP + SQS parallel processing validation
- 📋 **Missing**: Automated LocalStack setup and teardown

## Task Implementation Plan

### Step 1: Analyze Existing Integration Tests and code 
**Files to examine**:
- `/test/integration_test.go` - Main integration test suite
- `/server/sqsconsumer/consumer_test.go` - Consumer unit tests
- `/server/sqsconsumer/processor_test.go` - Processor unit tests
- `/server/server.go`                  - server code 
- `/scripts/manual-test.sh` - Manual testing workflows


**Goal**: Understand current test coverage and identify enhancement opportunities

### Step 2: Create and Enhance Integration / LocalStack Test Infrastructure
**Create**:
- Basic integration tests with LocalStack for http/api events
- Automated LocalStack container management (start/stop/health checks)
- Enhanced queue setup and teardown automation
- Concurrent HTTP and SQS event processing
- Improved test isolation and cleanup
- Performance benchmarking test scenarios

**Files to modify/create**:
- `/test/localstack_test.go` - Enhanced LocalStack integration tests
- `/scripts/setup-localstack.sh` - Automated LocalStack management
- `/test/performance_test.go` - Load testing and benchmarks


## Files Requiring Updates

### Primary Files
1. **`/test/integration_test.go`**
   - Add integration testing methods
   - Enhance LocalStack lifecycle management
   - Ensure it runs correctly with both SQS and HTTP events

2. **`/test/localstack_helpers.go`** (new)
   - LocalStack container management utilities
   - Queue setup/teardown automation
   - Health check and connectivity testing

3. **`/test/performance_test.go`** (new)
   - Benchmark test suite
   - Memory and CPU profiling
   - Throughput measurement utilities

### Supporting Files
4. **`/scripts/setup-localstack.sh`** (new)
   - Automated LocalStack container management
   - Queue initialization scripts
   - Health check automation


## Success Criteria

### Functional Requirements
- [ ] All existing integration tests pass with enhanced LocalStack framework
- [ ] Automated LocalStack setup/teardown works reliably
- [ ] Test isolation ensures no cross-test contamination

### Quality Assurance
- [ ] Test documentation is comprehensive and up-to-date
- [ ] Error scenarios are thoroughly tested and documented

## Verification Steps

### Phase 1: Integration Testing
1. Run existing integration tests to establish baseline
2. Implement enhanced LocalStack management utilities
3. Add new integration test scenarios for edge cases
4. Verify all tests pass consistently in clean environment


## Dependencies and Prerequisites

### External Dependencies
- **Docker**: For LocalStack container management
- **LocalStack**: AWS service emulation (SQS, etc.)
- **AWS CLI**: For queue management and testing utilities

### Go Dependencies (already present)
- `github.com/aws/aws-sdk-go-v2` - AWS SDK
- `github.com/stretchr/testify` - Testing framework
- `github.com/palantir/go-githubapp` - GitHub App framework

### Environment Setup
- LocalStack container with SQS service enabled
- Test AWS credentials (localstack compatible)
- Network connectivity to localhost:4566

## Risk Mitigation

### Technical Risks
1. **LocalStack Instability**: Use container health checks and retry logic
2. **Test Flakiness**: Implement proper test isolation and cleanup


### Resource Constraints
1. **Memory Usage**: Monitor test resource consumption and set limits
2. **Test Duration**: Implement configurable test timeouts and quick smoke tests
3. **LocalStack Startup Time**: Cache containers and use persistent volumes

## Completion Checklist

### Development
- [ ] Ensure integration tests are passing with HTTP/API and SQS events
- [ ] Fix any tests that are not passing
- [ ] Enhanced integration test framework implemented
- [ ] Automated LocalStack management working

### Documentation
- [ ] Updated TESTING.md with new procedures
- [ ] Test troubleshooting guide updated

### Validation
- [ ] All tests pass in clean environment


