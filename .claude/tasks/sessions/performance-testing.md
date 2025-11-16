# Performance Testing Task Session

**Created**: 2025-09-27  
**Status**: Ready for execution  
**Priority**: High – Critical Phase 4 milestone  
**Architecture Plan Reference**: Phase 4, Task 4 – Performance testing and benchmarking

## Objective

Deliver **Milestone A: Load-Test Harness Foundations**. Build the reusable measurement helpers and first wave of automated performance tests so we can repeatedly validate high-volume SQS processing and worker scaling within minutes. This milestone establishes the baseline needed before tackling deeper memory profiling, long-haul simulations, or CI regression automation.

## Context Snapshot

- ✅ Core SQS infrastructure, configuration, and LocalStack test harnesses are in place.  
- ✅ Unit and integration coverage is strong; a simple burst test (20 events) exists.  
- 🔄 Architecture plan now calls for 1000+ events/minute throughput validation, latency checks, and observability.  
- ⚠️ No shared performance metrics helpers or worker-scaling tests yet; parallel HTTP/SQS load is unverified.

## Current Sprint Focus

1. **Metrics Toolkit** – Lightweight helpers to measure throughput, latency, and LocalStack queue depth so all subsequent tests share instrumentation.  
2. **High-Volume + Scaling Tests** – Expand `test/performance_test.go` with configurable workloads (event count, concurrency, worker overrides) that finish quickly but stress the pipeline.  
3. **Execution Hygiene** – Keep tests under ~90s on a laptop, add guardrails for `testing.Short()`, and document prerequisites in `TESTING.md`.  
4. **Future Readiness** – Leave stubs/notes for upcoming benchmarks, memory profiling, and regression tooling without implementing them yet.

## Plan – Milestone A

### Step 1: Metrics & Utilities
- Create `test/performance_metrics.go` containing timers, throughput calculators, and queue-depth polling.  
- Expose simple structs (`LoadScenario`, `RunStats`) so tests can declare workloads declaratively.  
- Reuse existing `LocalStackManager` and ensure helpers gracefully skip when LocalStack is unavailable.

### Step 2: High-Volume & Worker Scaling Tests
- Enhance `test/performance_test.go` with:
  - `TestIntegration_SQSHighVolume` – configurable total events and per-send delay, asserting throughput >= target.  
  - `TestIntegration_SQSWorkerScaling` – run the same scenario under different worker counts and compare completion times.  
  - Optional parallel HTTP/SQS mini-load using existing webhook helpers.  
- Keep runtime bounded (e.g., 1–2 minute upper limit) and surface metrics via `t.Logf` for manual review.

### Step 3: Documentation & Entrypoints
- Update `TESTING.md` with a “Performance Smoke Tests” section (how to start LocalStack, run new tests, read output).  
- Outline follow-on tasks (benchmarks, memory profiling) in a **Backlog** section of this document so future work stays visible.

### Step 4: Validation
- Run `go test ./test -run TestIntegration_SQSHighVolume -count=1`.  
- Capture sample throughput/latency logs for documentation.  
- Ensure `go test ./test` still passes locally.

## Milestone Completion Criteria

- [ ] Metrics helpers committed and reused by new tests.  
- [ ] High-volume SQS test hits configured throughput target (default: ≥1000 events/minute aggregate).  
- [ ] Worker-scaling test proves multiple worker counts execute without errors and records comparative metrics.  
- [ ] Updated documentation explains how to execute/interpret the new tests.  
- [ ] All tests in `./test` pass locally with LocalStack running; suite skips cleanly when unavailable.

## Backlog (Future Milestones)

- **Milestone B – Benchmarks & Profiling**: Go benchmarks (`benchmark_test.go`), memory/CPU profiling hooks, and baseline capture scripts.  
- **Milestone C – Production Simulations**: Long-running stability, mixed HTTP/SQS routing patterns, stress and failure scenarios.  
- **Milestone D – Regression Framework**: Baseline JSON storage, automated comparison tooling, CI integration.

Use this document to track progress; update Status and Completion Criteria as Milestone A advances.
