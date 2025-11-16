# Policy-Bot Testing Plan — Phase 4: Integration, Load & Observability

## Goal
Validate the complete system flows (HTTP + SQS) against real-like infrastructure, confirm scheduler/value-add decisions under load, and capture observability baselines. This phase delivers high-confidence answers to Q1–Q4 and sets the stage for ongoing regression testing.

## Context
- Phases 1–3 established unit-level confidence; now we exercise the stitched components using LocalStack for SQS and mock GitHub servers.
- Integration suites should be runnable locally (document prerequisites) and in CI (skip or tag long-running tests).
- Performance/soak testing focuses on evidence gathering rather than exhaustive benchmarking.

## Deliverables
- Integration tests under `test/` covering HTTP webhook flow, SQS message flow, multi-queue processing, retry scenarios, and scheduler metrics validation.
- Optional performance scripts (Go tests or Go benchmarks) providing throughput/latency measurements and guidance for repeatable runs.
- Updated documentation (`TESTING.md` or similar) summarizing how to execute the new suites and interpret results.

## To-Do Checklist
- [ ] Prepare infra helpers:
  - [ ] Ensure LocalStack setup scripts/configs exist (`test/localstack_helpers.go`); update if necessary.
  - [ ] Spin up mock GitHub API server (reuse existing helpers or add lightweight HTTP server capturing requests).
- [ ] Implement integration tests:
  - [ ] `HTTPWebhook_ToGitHubAPI_AuthenticatedCall` (Test 6.1) verifying installation token usage end-to-end.
  - [ ] `SQSMessage_ToGitHubAPI_AuthenticatedCall` (Test 6.2) using LocalStack queue to prove SQS path mirrors HTTP auth.
  - [ ] `CloudAndEnterprise_UseDifferentCredentials` (Test 6.3) asserting separation of cloud vs enterprise credentials/hosts.
  - [ ] `Consumer_LifecycleManagement` (Test 7.1) covering start → process → stop flow with real queue.
  - [ ] `Consumer_HandlesMultipleQueues` (Test 7.2) ensuring per-queue workers and handler mapping.
  - [ ] `Consumer_RetryOnFailure` (Test 7.3) validating exponential delays with actual queue visibility changes.
- [ ] Add scheduler/performance checks:
  - [ ] Benchmark or soak test comparing throughput with/without scheduler backpressure (Test 8.x).
  - [ ] Measure latency/CPU/memory at representative load; capture results in `TESTING.md`.
- [ ] Run suite locally:
  - [ ] `go test ./test -tags=integration` (or chosen tag) with LocalStack running.
  - [ ] `go test ./server/... -race` to confirm no regressions.
- [ ] Update documentation:
  - [ ] Append new instructions + acceptance criteria to `TESTING.md`.
  - [ ] Note any environment variables or Docker commands needed to run integration tests.
