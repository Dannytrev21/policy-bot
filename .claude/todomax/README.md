# Policy-Bot Testing Plan: Complete Implementation Guide

## Overview

This directory contains a comprehensive, AI-agent-friendly testing plan for policy-bot, synthesized from three original testing approaches and optimized for Test-Driven Development (TDD).

## 📁 File Structure

```
.claude/todomax/
├── README.md (this file)
├── policy_bot_testing_synthesis.md         # Analysis & synthesis
├── policy_bot_testing_plan_phase1.md       # Week 1: Foundation
├── policy_bot_testing_plan_phase2.md       # Week 2: Integration
├── policy_bot_testing_plan_phase3.md       # Week 3: Concurrency
├── policy_bot_testing_plan_phase4.md       # Week 4: Architecture
└── policy_bot_testing_plan_phase5.md       # Week 5: E2E & Docs
```

## 🎯 Critical Questions to Answer

This testing plan definitively answers 4 architectural questions:

| ID | Question | Answer | Primary Test |
|----|----------|--------|--------------|
| **Q1** | Can sqsconsumer/processor authenticate with GitHub after receiving webhook? | **YES** | `AUTH-INT-T01` (Phase 2) |
| **Q2** | Is the current scheduler approach optimal for SQS? | **YES** | `SCHED-INT-T01` (Phase 2) |
| **Q3** | How does SQS ensure thread safety? | **Multi-layered** | `STRESS-T01` (Phase 3) |
| **Q4** | Do SQS components need goji.Mux? | **NO** | `ARCH-T01` (Phase 4) |

## 🏗️ Architecture Innovation: Shared Source Router

A key innovation from the synthesis is the **shared source router** pattern:

```go
// server/internal/sourcerouter/router.go
// Eliminates duplication between HTTP middleware and SQS processor
type Router struct{}

func (r *Router) DetectSource(headers, queryParams, legacySource) (source, method) {
    // Unified logic consumed by both HTTP and SQS paths
}
```

**Benefits**:
- Single source of truth for routing logic
- Consistent behavior across HTTP and SQS
- Easier to test and maintain
- Eliminates potential for drift

## 📊 Testing Pyramid

```
        ┌─────────────────┐
        │   E2E Tests     │  5% - 5 tests
        ├─────────────────┤
        │  Performance    │  10% - 15 tests
        ├─────────────────┤
        │  Integration    │  20% - 30 tests
        ├─────────────────┤
        │   Unit Tests    │  65% - 100+ tests
        └─────────────────┘
        
Total: 150+ tests
```

## 🚀 Quick Start for AI Agents

### Step 1: Read Synthesis
```bash
cat .claude/todomax/policy_bot_testing_synthesis.md
```

Understand:
- Comparison of 3 original approaches
- Best elements selected from each
- Final phase structure
- Testing principles

### Step 2: Execute Phases Sequentially

#### Phase 1 (Week 1): Foundation
```bash
# Read phase file
cat .claude/todomax/policy_bot_testing_plan_phase1.md

# Key deliverables:
# - Shared router implementation + tests
# - Authentication unit tests  
# - Processor routing tests
# - 35+ unit tests passing
```

**Focus**: Unit tests, shared utilities, mocks

#### Phase 2 (Week 2): Integration
```bash
# Read phase file
cat .claude/todomax/policy_bot_testing_plan_phase2.md

# Key deliverables:
# - LocalStack setup
# - SQS → GitHub authentication (Q1 ANSWER)
# - Scheduler performance comparison (Q2 ANSWER)
# - 10+ integration tests
```

**Focus**: End-to-end flows, LocalStack, performance

#### Phase 3 (Week 3): Concurrency
```bash
# Read phase file
cat .claude/todomax/policy_bot_testing_plan_phase3.md

# Key deliverables:
# - 1000 concurrent messages test (Q3 ANSWER)
# - Race detection
# - Memory leak detection
# - Profiling results
```

**Focus**: Stress testing, thread safety, memory

#### Phase 4 (Week 4): Architecture
```bash
# Read phase file
cat .claude/todomax/policy_bot_testing_plan_phase4.md

# Key deliverables:
# - Import analysis (Q4 ANSWER)
# - Connection pool optimization
# - Circuit breaker implementation
# - Batch processing
```

**Focus**: Boundaries, optimizations

#### Phase 5 (Week 5): E2E & Docs
```bash
# Read phase file
cat .claude/todomax/policy_bot_testing_plan_phase5.md

# Key deliverables:
# - Complete E2E system test
# - Performance regression suite
# - All questions documented
# - Operations runbook
```

**Focus**: System validation, documentation

## 📋 Phase Checklist

Use this to track progress:

- [ ] **Phase 1 Complete**
  - [ ] Shared router implemented
  - [ ] 35+ unit tests passing
  - [ ] >85% coverage on critical components
  - [ ] No race conditions

- [ ] **Phase 2 Complete**
  - [ ] LocalStack operational
  - [ ] Q1 ANSWERED (SQS authentication)
  - [ ] Q2 ANSWERED (Scheduler performance)
  - [ ] Integration tests stable

- [ ] **Phase 3 Complete**
  - [ ] 1000 concurrent messages test passed
  - [ ] Q3 ANSWERED (Thread safety)
  - [ ] Memory stable under load
  - [ ] Profiling completed

- [ ] **Phase 4 Complete**
  - [ ] Q4 ANSWERED (No HTTP dependencies)
  - [ ] Optimizations implemented
  - [ ] Architecture validated

- [ ] **Phase 5 Complete**
  - [ ] E2E test passing
  - [ ] Documentation complete
  - [ ] Runbook created
  - [ ] Production ready

## 🧪 Test Naming Convention

All tests follow this pattern:

```
Test{Component}_{Scenario}_{Expected}

Examples:
- TestProcessor_CloudMessage_UsesCloudScheduler
- TestRouter_EnterpriseHeader_ReturnsEnterprise
- TestConsumer_1000Messages_NoRaceConditions
```

## 🔍 Test IDs

Each test has a unique ID for tracking:

```
{PREFIX}-T{NUMBER}{VARIANT}

Examples:
- SR-T01: Shared Router Test 01
- AUTH-T02a: Authentication Test 02, variant a
- PROC-T03: Processor Test 03
- STRESS-T01: Stress Test 01
```

## 🛠️ Tools Required

### Prerequisites
```bash
# Go 1.21+
go version

# Testing tools
go get github.com/stretchr/testify
go get go.uber.org/goleak

# LocalStack
pip install localstack
docker --version

# AWS CLI v2
aws --version

# Profiling tools (optional)
brew install graphviz
```

### Environment Setup
```bash
# Set up test directories
mkdir -p server/internal/sourcerouter
mkdir -p test/fixtures
mkdir -p test/localstack

# Start LocalStack (Phase 2+)
cd test/localstack
docker-compose up -d

# Verify
aws --endpoint-url=http://localhost:4566 sqs list-queues
```

## 📈 Success Metrics

### Code Quality
- ✅ >90% coverage on critical components
- ✅ 0 race conditions
- ✅ All tests pass in CI
- ✅ <10 second unit test execution

### Performance
- ✅ >100 msg/sec throughput
- ✅ <1GB memory under load
- ✅ <20% scheduler overhead
- ✅ <1 second P99 latency

### Architecture
- ✅ 0 HTTP imports in SQS
- ✅ Clean separation of concerns
- ✅ Shared router eliminates duplication
- ✅ All 4 questions answered

## 🔬 Running Tests

### Unit Tests (Phase 1)
```bash
# Run all unit tests
go test -v ./server/internal/sourcerouter/...
go test -v ./server/handler/ -run "TestBase_"
go test -v ./server/sqsconsumer/ -run "TestProcessor_"

# With coverage
go test -cover ./...

# With race detector
go test -race ./...
```

### Integration Tests (Phase 2)
```bash
# Ensure LocalStack running
docker-compose -f test/localstack/docker-compose.yml up -d

# Run integration tests
go test -v -tags=integration ./test/...

# Specific tests
go test -v -tags=integration ./test/ -run TestIntegration_SQS_AuthenticatesWithGitHub
```

### Stress Tests (Phase 3)
```bash
# With race detector (critical!)
go test -v -race -tags=integration ./test/ -run TestStress_1000ConcurrentMessages

# Memory profiling
go test -memprofile=mem.prof -bench=. ./server/sqsconsumer/...
go tool pprof -http=:8080 mem.prof
```

### E2E Tests (Phase 5)
```bash
# Full system test
go test -v -tags=e2e ./test/ -run TestE2E_CompleteSystem
```

## 📚 Key Concepts

### Test-Driven Development (TDD)
1. Write failing test first
2. Implement minimal code to pass
3. Refactor with confidence
4. Repeat

### Table-Driven Tests
```go
tests := []struct {
    name     string
    input    InputType
    expected ExpectedType
}{
    // Test cases
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // Test implementation
    })
}
```

### Mock External Only
- Mock: GitHub API, AWS SDK
- Don't Mock: Internal components, schedulers
- Use Real: Handlers, processors in integration tests

## 🚨 Common Issues

### LocalStack not starting
```bash
# Check Docker
docker ps

# Restart LocalStack
docker-compose -f test/localstack/docker-compose.yml down
docker-compose -f test/localstack/docker-compose.yml up -d

# Check logs
docker-compose -f test/localstack/docker-compose.yml logs
```

### Race conditions detected
```bash
# Always run with -race during development
go test -race ./...

# Fix by using proper synchronization:
# - atomic.Value for shared values
# - sync.Mutex for critical sections
# - channels for goroutine coordination
```

### Tests timeout
```bash
# Increase context timeout in tests
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

# Or run with longer timeout
go test -timeout 5m ./...
```

## 📖 Additional Resources

- [Go Testing Guide](https://golang.org/doc/tutorial/add-a-test)
- [Testify Documentation](https://github.com/stretchr/testify)
- [LocalStack Docs](https://docs.localstack.cloud/)
- [Go Race Detector](https://golang.org/doc/articles/race_detector)
- [Table-Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)

## 🤖 For AI Agents

When implementing this plan:

1. **Read completely** before starting any phase
2. **Follow order**: Phases 1→2→3→4→5 (dependencies exist)
3. **Test first**: Write failing test before implementation
4. **Run frequently**: Execute tests after each change
5. **Document deviations**: Note any changes from plan
6. **Ask if stuck**: Don't guess, clarify requirements

## 📝 Final Notes

This testing plan is:
- ✅ **Comprehensive**: 150+ tests covering all aspects
- ✅ **Practical**: Real code examples and templates
- ✅ **AI-friendly**: Clear instructions and acceptance criteria
- ✅ **Evidence-based**: Answers all 4 questions definitively
- ✅ **Production-ready**: Includes runbook and metrics

**Estimated Effort**: 5 weeks, 1-2 developers
**Risk Level**: Low (incremental TDD approach)
**Expected Outcome**: >90% test coverage, all questions answered, production-ready system

---

**Good luck! 🚀**

For questions or issues, refer to the individual phase files for detailed guidance.


