# Policy-Bot Testing Plan — Phase 2: Processor & Scheduler Behavior

## Goal
Lock in the core SQS processing pipeline by exercising handler selection, scheduler usage, retry semantics, and per-environment metrics at the unit level. Provide concrete evidence for Q1, Q2, and Q4 without relying on external services.

## Context
- With routing unified (Phase 1), we now validate the processor’s end-to-end responsibilities: parsing messages, choosing the right handler/scheduler pair, dispatching work, and cleaning up SQS messages.
- Tests should use lightweight fakes for `githubapp.Scheduler`, `githubapp.EventHandler`, and the `SQSClient` so they remain fast and deterministic.
- Scheduler tests reinforce why sharing cloud/enterprise schedulers between HTTP and SQS is desirable (Q2).

## Deliverables
- Expanded `server/sqsconsumer/processor_*_test.go` suites covering scheduling, retries, metrics, context injection, and legacy compatibility (PR-01…PR-08, LG-01, LG-02).
- Unit tests demonstrating scheduler backpressure, error callbacks, and metrics registration.
- Clear fixtures/fakes ready for reuse in later integration tests.

## To-Do Checklist
- [ ] Build reusable fakes/mocks:
  - [ ] `fakeScheduler` that records `Dispatch` calls and can inject errors.
  - [ ] `fakeHandler` capturing invocation counts + payloads.
  - [ ] `mockSQSClient` supporting `DeleteMessage`/`SendMessage` assertions.
- [ ] Write processor tests:
  - [ ] Cloud message -> cloud handler + scheduler, delete on success (PR-01, PR-03).
  - [ ] Enterprise message path (PR-02).
  - [ ] Scheduler failure triggers retry flow with exponential delay and updated `retry_count` (PR-04).
  - [ ] Exhausted retries leaves message visible (PR-05).
  - [ ] Context carries queue name, message ID, environment (PR-06).
  - [ ] Metrics counters/timers increment per environment (PR-07).
  - [ ] GitHub login seam: integrate fake handler asserting correct `ClientCreator` usage post-dispatch (PR-08).
  - [ ] Legacy `Source` routing retained when headers absent (LG-01).
  - [ ] Config-driven HTTP-only routing short-circuits processing (LG-02).
- [ ] Add scheduler-focused tests (new `server/handler/scheduler_test.go` or similar):
  - [ ] Queue backpressure with limited `queueSize` (Scheduler_ProvidesBackpressure).
  - [ ] Metrics emission on success/failure (Scheduler_RecordsMetrics).
  - [ ] Error callback invoked when handler returns error (Scheduler_ErrorCallback).
- [ ] Ensure processor tests cover message parsing variants (structured SQS vs raw webhook JSON) for regression confidence.
- [ ] Run `go test ./server/sqsconsumer -count=1` and ensure coverage up-tick captured (optional: `go test -cover`).
