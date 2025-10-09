# Policy Bot Testing Plan (Codex)

## Scope & Objectives
- Validate that SQS-driven event ingestion behaves identically to HTTP webhooks for both GitHub Enterprise (GHES) and GitHub Enterprise Cloud (GHEC).
- Answer outstanding architectural questions about authentication, scheduler usage, thread safety, and router coupling **using tests first**.
- Produce guardrails that allow refactoring (e.g., refactoring environment detection) without breaking behaviour.

## Tree of Thought (ToT) Hypotheses

| ID | Hypothesis | Pros | Cons | Score | Verdict |
|----|------------|------|------|-------|---------|
| H1 | Introduce a shared “source router” utility consumed by HTTP middleware and SQS processor. | Single authority for env detection, aligns observability, simplifies testing. | Requires new seam + refactor call sites. | 8.5/10 | **Selected** |
| H2 | Split processor into two concrete types (cloud/enterprise) and bind them per queue. | Clear ownership and easier auth assertions. | Duplicates logic, complicates mixed routing (queues carrying both sources). | 5/10 | Rejected |
| H3 | Rely exclusively on queue configuration to determine environment (no header parsing). | Deterministic routing. | Breaks hybrid queues and diverges from HTTP semantics. | 4/10 | Rejected |

Testing Approach ToT:
- **T1 (Unit-first with fakes)**: Fast feedback, deterministic, covers edge-cases → **Chosen**.
- T2 (Integration-first with LocalStack): Higher confidence but slow, hard to iterate.
- T3 (Observability-only): Insufficient for regressions.

## High-Level Test Strategy
1. **Unit tests** around the new/shared routing abstraction, processor scheduling decisions, consumer concurrency controls.
2. **Component tests** that execute through schedulers with fake handlers to assert GitHub client usage without touching real handlers.
3. **Integration smoke** (optional follow-up) against LocalStack queues once unit coverage lands.

## Test Backlog & Acceptance Criteria

### 1. Shared Source Router (new package, reused by HTTP + SQS)
- **Test SR-01**: Given headers with `X-GitHub-Enterprise-Host`, router returns `enterprise`.  
  _Acceptance_: Returns `enterprise`, exposes `detection_method=enterprise_header`.
- **Test SR-02**: Given headers containing `"ghec"` marker (legacy) or DCP header, returns `cloud`.  
  _Acceptance_: Returns `cloud`, detection tagged as `cloud_header|legacy_host`.
- **Test SR-03**: When only query param `source=enterprise` is present (HTTP parity), returns `enterprise`.  
  _Acceptance_: Matches HTTP middleware fallback.
- **Test SR-04**: Default path returns `cloud` and records "default" reason.  
  _Acceptance_: Zero inputs → default `cloud`.

_Context_: Tests answer Q4 indirectly by demonstrating the router is HTTP-agnostic; SQS code will consume same helper without depending on goji.

### 2. `processor.go` Unit Tests
- **Test PR-01**: `ProcessMessage` with GHEC headers uses **cloud scheduler** and cloud handler.  
  _Setup_: Fake scheduler recording invocations, fake handler claiming `"pull_request"`.  
  _Acceptance_: Cloud scheduler called exactly once with queue metadata; enterprise scheduler untouched.
- **Test PR-02**: Same for GHES headers routes to enterprise scheduler.  
  _Acceptance_: Enterprise scheduler invoked; cloud untouched.
- **Test PR-03**: When scheduler succeeds, message deleted via mock SQS client.  
  _Acceptance_: `DeleteMessage` invoked with original receipt handle.
- **Test PR-04**: When scheduler fails and retries enabled, message requeued with incremented retry count and exponential delay.  
  _Acceptance_: `SendMessage` called with `retry_count=n+1`, delay bounded (≤300s); original message deleted.
- **Test PR-05**: When retries exhausted, message left in queue (no delete/send).  
  _Acceptance_: `DeleteMessage` not called; processor returns error.
- **Test PR-06**: Context enrichment exposes queue name, message ID, environment.  
  _Acceptance_: Handler sees context values matching queue + environment.
- **Test PR-07**: Metrics counters increment per environment (assert via `metrics.Registry`).  
  _Acceptance_: `sqs.messages.processed.cloud.pull_request` updated on success.
- **Test PR-08**: GitHub login seam — fake handler that calls injected `ClientCreator` ensures correct creator (cloud vs enterprise) is used post-dispatch.  
  _Acceptance_: For both environments, fake creator records `NewInstallationClient` call with installation ID from payload, proving ability to "login" after webhook reception (answers Q1).

### 3. `consumer.go` Concurrency & Thread-Safety
- **Test CN-01**: `Start` spawns `WorkersPerQueue` goroutines per queue (using controllable fake SQS client that blocks).  
  _Acceptance_: WaitGroup counter hits expected worker count; no duplicate workers.  
  _Thread-safety insight_: demonstrates isolation per queue (answers Q3).
- **Test CN-02**: Concurrent `Stop` closes `stopChan` once and joins workers without panic.  
  _Acceptance_: Multiple `Stop` invocations safe, WaitGroup drained.
- **Test CN-03**: When context cancelled, workers exit promptly.  
  _Acceptance_: Worker logs indicate graceful exit, WaitGroup zero.
- **Test CN-04**: DLQ monitor respects `QueueSuffix` and updates metrics gauge (use fake client).  
  _Acceptance_: Gauge updated with returned message count.
- **Test CN-05**: Health check surfaces upstream error (simulate AWS failure).  
  _Acceptance_: `Health()` returns error when `GetQueueAttributes` fails, verifying observability.

_Thread safety conclusion_: CN-01..03 combined establish that workers operate in separate goroutines with guarded shutdown (answer Q3).

### 4. `server.go` Composition Tests
- **Test SV-01**: With SQS disabled, `New` returns server with `noOpConsumer`.  
  _Acceptance_: `Start` skips `Consumer.Start`.
- **Test SV-02**: With SQS enabled, `Start` invokes `Consumer.Start` exactly once and defers `Stop`.  
  _Acceptance_: Use stub consumer recording calls; verifies orchestration without hitting Goji.
- **Test SV-03**: Ensure `mux` selection (root vs submux) does not leak into SQS setup.  
  _Acceptance_: Build server with non-empty `PublicURL`; assert SQS consumer initialized regardless of `goji.SubMux`, answering Q4 (no dependency).
- **Test SV-04**: Verify `handler.Base` instances share schedulers with SQS.  
  _Acceptance_: Introspect server after `New` to confirm scheduler pointers wired identically for HTTP + SQS (answers Q2).

### 5. Legacy Compatibility
- **Test LG-01**: Processor honors legacy `Source` field when headers absent.  
  _Acceptance_: Routes to enterprise when `Source=enterprise`.
- **Test LG-02**: Environment routing config (HTTP/SQS/both) still respected after router refactor.  
  _Acceptance_: Stubbing config per event, confirm `ProcessMessage` short-circuits when routing indicates HTTP-only.

### 6. Optional Integration Follow-up (post unit coverage)
- **IT-01**: Replay sample webhook through LocalStack queue, assert handler triggered via fake GitHub clients.  
  _Acceptance_: Handler invoked once, `NewInstallationClient` recorded, message deleted.
- **IT-02**: Stress test concurrency (multiple messages) with `-race`.  
  _Acceptance_: No race detector findings; processed count matches enqueued count.

## Execution Order
1. Implement shared source router helper (unit tests SR-01..04 first, then refactor middleware + processor to consume it).
2. Expand processor unit suite (PR-01..08) using fakes/mocks.
3. Cover consumer concurrency tests (CN-01..05).
4. Add server composition tests (SV-01..04).
5. Address legacy compatibility (LG-01..02).
6. (Optional) Run integration scenarios IT-01..02 with LocalStack once unit suites pass.

## Tooling Notes
- Use `testify` + `gomock`/`stretchr/mock` (already in repo) for scheduler and SQS client fakes.
- Consider custom lightweight scheduler fake implementing `githubapp.Scheduler` to execute dispatch inline for login assertions.
- Run `go test ./... -race` after CN/IT suites to surface thread-safety regressions.
- Ensure new router helper lives in an internal package to avoid HTTP dependencies inside SQS code.

## Deliverable Checklist
- [ ] Router helper + SR test suite passing.
- [ ] Processor tests PR-01..08 implemented, covering Q1/Q2.
- [ ] Consumer tests CN-01..05 implemented, covering Q3.
- [ ] Server tests SV-01..04 implemented, covering Q2 & Q4.
- [ ] Legacy compatibility tests LG-01..02 green.
- [ ] Optional LocalStack integration docs updated after IT suite.
