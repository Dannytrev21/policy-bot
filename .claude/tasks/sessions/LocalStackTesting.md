# LocalStackTesting Task Session

**Created**: 2025-09-27
**Status**: Ready for execution
**Priority**: High - Next milestone task from architecture plan

## Objective

Build and enhance integration testing framework with LocalStack for SQS event processing. This addresses **Phase 4, Task 2** from the policy-bot SQS architecture plan: "Build integration test framework with LocalStack".

## Context Analysis

Based on `.claude/architecture/policy-bot-sqs_plan.md`, the implementation is **85% complete** with the following status:
- ✅ Core Infrastructure: SQS consumer, processor, and integration complete
- ✅ Configuration System: Comprehensive YAML configuration with routing
-  Testing Framework: Basic unit tests and integration tests need to be completed
- 🔄 **Performance Testing**: Basic testing complete, load testing in progress
- 📋 Production Readiness: Security audit and deployment guides pending

**Current Gap**: The integration testing framework exists but needs enhancement for comprehensive LocalStack testing scenarios and performance validation.

## Current Testing Infrastructure

### Existing Components
1. **Integration Test**: `/test/integration_test.go` - Comprehensive test with HTTP/SQS parallel processing
2. **Manual Test Script**: `/scripts/test-event-processing.go` - Interactive testing tool
3. **Testing Documentation**: `/TESTING.md` - Complete setup and usage guide
4. **Test Configuration**: `/config/test-config.yml` - Test server configuration

### SQS Test Coverage
- ✅ Unit tests for consumer and processor (95% coverage achieved)
- **Need Enhancement**: Basic integration tests with LocalStack
- **Missing**: HTTP + SQS parallel processing validation
-  **Need Enhancement**:Error handling and connection failure scenarios
- 🔄 **Need Enhancement**: Performance testing and load scenarios
- 📋 **Missing**: Automated LocalStack setup and teardown

## Task Implementation Plan

### Step 1: Analyze Existing Integration Tests
**Files to examine**:
- `/test/integration_test.go` - Main integration test suite
- `/server/sqsconsumer/consumer_test.go` - Consumer unit tests
- `/server/sqsconsumer/processor_test.go` - Processor unit tests
- `/scripts/manual-test.sh` - Manual testing workflows

**Goal**: Understand current test coverage and identify enhancement opportunities

### Step 2: Create and Enhance LocalStack Test Infrastructure
**Create**:
- Automated LocalStack container management (start/stop/health checks)
- Enhanced queue setup and teardown automation
- Improved test isolation and cleanup
- Performance benchmarking test scenarios

**Files to modify/create**:
- `/test/localstack_test.go` - Enhanced LocalStack integration tests
- `/scripts/setup-localstack.sh` - Automated LocalStack management
- `/test/performance_test.go` - Load testing and benchmarks

### Step 3: Implement Load Testing Scenarios
**Test scenarios**:
- High-volume message processing (1000+ messages/minute)
- Concurrent HTTP and SQS event processing
- Queue depth and backpressure testing
- Memory and CPU utilization under load
- Error recovery and retry behavior

### Step 4: Automated Test Pipeline
**Create**:
- Make target for running LocalStack tests
- CI/CD integration scripts
- Test result reporting and metrics collection
- Performance regression detection

## Test Cases to Implement

### 1. Enhanced Integration Tests
```go
// Test scenarios to add/enhance:
- TestLocalStackConnectionFailure()
- TestSQSQueueCreationAndDeletion()
- TestHighVolumeMessageProcessing()
- TestConcurrentConsumerBehavior()
- TestMessageRetryAndDLQHandling()
- TestGracefulShutdownWithPendingMessages()
```

### 2. Performance Benchmarks
```go
// Benchmark tests to create:
- BenchmarkSQSMessageThroughput()
- BenchmarkParallelHTTPSQSProcessing()
- BenchmarkMemoryUsageUnderLoad()
- BenchmarkQueueDepthRecovery()
```

### 3. Load Testing Scenarios
- **Scenario A**: 1000 messages/minute sustained load
- **Scenario B**: Burst processing (5000 messages in 5 minutes)
- **Scenario C**: Mixed event types with different processing times
- **Scenario D**: Network interruption recovery testing

## Files Requiring Updates

### Primary Files
1. **`/test/integration_test.go`**
   - Add performance testing methods
   - Enhance LocalStack lifecycle management
   - Add load testing scenarios

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

5. **`/Makefile`** (enhance existing)
   - Add `make test-localstack` target
   - Add `make test-performance` target
   - Add `make test-load` target

6. **`/.github/workflows/test.yml`** (if exists, enhance)
   - Add LocalStack testing to CI pipeline
   - Performance regression detection
   - Test result reporting

## Success Criteria

### Functional Requirements
- [ ] All existing integration tests pass with enhanced LocalStack framework
- [ ] New performance tests demonstrate 1000+ events/minute throughput
- [ ] Load testing scenarios validate system behavior under stress
- [ ] Automated LocalStack setup/teardown works reliably
- [ ] Test isolation ensures no cross-test contamination

### Performance Targets
- [ ] **Throughput**: >1000 events/minute processing capacity
- [ ] **Latency**: <500ms average processing time per event
- [ ] **Memory**: <100MB memory usage with 10 workers under load
- [ ] **Recovery**: <5 seconds to recover from queue backlog of 100 messages
- [ ] **Reliability**: >99% message processing success rate

### Quality Assurance
- [ ] All tests run reliably in CI/CD environment
- [ ] Test documentation is comprehensive and up-to-date
- [ ] Performance baselines are established for regression detection
- [ ] Error scenarios are thoroughly tested and documented

## Verification Steps

### Phase 1: Enhanced Integration Testing
1. Run existing integration tests to establish baseline
2. Implement enhanced LocalStack management utilities
3. Add new integration test scenarios for edge cases
4. Verify all tests pass consistently in clean environment

### Phase 2: Performance Validation
1. Implement load testing framework
2. Run performance benchmarks and collect baseline metrics
3. Validate throughput and latency targets are met
4. Document performance characteristics and limitations

### Phase 3: Automation and CI
1. Create automated test scripts and Make targets
2. Integrate with CI/CD pipeline (if applicable)
3. Verify tests run reliably in containerized environment
4. Establish performance regression detection

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
3. **Performance Variability**: Use statistical analysis and multiple test runs
4. **CI/CD Integration**: Provide fallback for environments without Docker

### Resource Constraints
1. **Memory Usage**: Monitor test resource consumption and set limits
2. **Test Duration**: Implement configurable test timeouts and quick smoke tests
3. **LocalStack Startup Time**: Cache containers and use persistent volumes

## Completion Checklist

### Development
- [ ] Enhanced integration test framework implemented
- [ ] Performance testing suite created
- [ ] Load testing scenarios validated
- [ ] Automated LocalStack management working

### Documentation
- [ ] Updated TESTING.md with new procedures
- [ ] Performance benchmarks documented
- [ ] Test troubleshooting guide updated
- [ ] CI/CD integration instructions added

### Validation
- [ ] All tests pass in clean environment
- [ ] Performance targets achieved and documented
- [ ] Load testing scenarios demonstrate scalability
- [ ] Integration with existing CI/CD pipeline (if applicable)

## Next Steps After Completion

1. **Phase 5 Preparation**: Use performance data to optimize for production readiness
2. **Security Testing**: Integrate with security audit phase
3. **Documentation Updates**: Update architecture plan with performance characteristics
4. **Team Enablement**: Provide training on new testing capabilities

---

## Implementation Notes

**Estimated Effort**: 2-3 development sessions
**Skills Required**: Go testing, Docker/LocalStack, performance testing
**Success Measurement**: All test scenarios pass + performance targets achieved

This task directly advances the SQS architecture plan from 85% to 95% completion by addressing the critical performance testing and LocalStack integration requirements.