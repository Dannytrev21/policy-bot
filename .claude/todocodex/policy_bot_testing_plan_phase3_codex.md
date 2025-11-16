# Policy-Bot Testing Plan — Phase 3: Consumer Concurrency & Independence

## Goal
Prove the robustness of the SQS consumer lifecycle: safe concurrency, visibility-timeout semantics, graceful shutdown, and independence from HTTP routing. These tests answer Q3 and reinforce Q4 prior to full integration work.

## Context
- Build on Phase 2 fakes to simulate SQS polling without AWS dependencies.
- Focus on goroutine orchestration (`Start`, `consumeQueue`, `Stop`, `monitorDLQ`) and architectural boundaries (no goji/http coupling).
- Include race-detector-friendly suites so future automation can run `go test -race`.

## Deliverables
- New `server/sqsconsumer/consumer_*_test.go` cases covering worker fan-out, stop semantics, visibility timeout behavior, DLQ monitoring, and error surface area.
- Static/import-level assertions ensuring no HTTP router dependencies creep into the SQS packages.
- Guidance/scripts for running race detector once suites are in place.

## To-Do Checklist
- [ ] Extend `mockSQSClient` to support scripted responses for `ReceiveMessage`, `GetQueueAttributes`, and DLQ probing.
- [ ] Implement concurrency tests:
  - [ ] `Consumer_ConcurrentWorkers_NoRaceConditions`: spin up multiple workers, feed deterministic message slices, assert each message processed exactly once (run with `-race` locally).
  - [ ] `Consumer_VisibilityTimeout_PreventsDuplication`: simulate long-running handler and ensure second worker does not observe message before timeout.
  - [ ] `Consumer_GracefulShutdown_WaitsForInFlight`: trigger `Stop` mid-processing, verify WaitGroup drains and no new receives occur.
- [ ] Add lifecycle tests:
  - [ ] `Consumer_StartsWithoutHTTPServer`: instantiate consumer directly and confirm workers poll using fake client (addresses Q4).
  - [ ] `Consumer_RetryOnFailure` integration-lite test: combine processor retry logic with consumer loop to validate message survives through retries.
  - [ ] `Consumer_DLQMonitor_RecordsGauge`: simulate DLQ attribute responses and assert metrics update.
  - [ ] `Consumer_Health_PropagatesSQSErrors`: fake client returns error; ensure `Health()` surfaces it.
- [ ] Introduce static dependency check (optionally using `go list`/`build.Import`):
  - [ ] Assert `server/sqsconsumer` does not import `goji.io`, `net/http`, or `server/middleware` (Test 5.1 analogue).
- [ ] Document instructions for running race detector (`go test ./server/sqsconsumer -race`) once suites are ready.
