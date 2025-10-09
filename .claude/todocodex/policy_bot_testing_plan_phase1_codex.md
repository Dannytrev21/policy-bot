# Policy-Bot Testing Plan — Phase 1: Foundational Unit Coverage

## Goal
Establish a shared, thoroughly tested routing and authentication foundation so that both HTTP and SQS ingestion paths choose the correct GitHub environment and can obtain authenticated clients on demand.

## Context
- Current routing logic is split between HTTP middleware (`server/middleware/header_check.go`) and SQS processor (`server/sqsconsumer/processor.go`). Aligning them behind a common helper reduces drift and simplifies future changes.
- Authentication flows live in `handler.Base`; confirming installation client creation for both cloud and enterprise instances proves that SQS handlers can “log in” after message receipt (answers Q1).
- Focus only on unit-level seams; defer integration/localstack work to later phases.

## Deliverables
- Shared routing helper package with exhaustive unit tests covering headers, legacy fallbacks, and defaults.
- Middleware and SQS processor updated to consume the helper without behavior regressions.
- New unit tests for `handler.Base` validating installation client creation, GraphQL client wiring, and error handling.

## To-Do Checklist
- [ ] Draft routing helper API (e.g. `package routing`) that accepts HTTP headers + query params and returns `{environment, detectionReason}`.
- [ ] Port HTTP middleware to use helper; ensure Prometheus labels keep parity.
- [ ] Update SQS processor `detectSourceFromHeaders` call sites to use helper result; remove duplicated heuristics.
- [ ] Implement unit tests:
  - [ ] `routing` package: cover enterprise header, DCP header, `Host` containing `ghec`, legacy `source` field, and default path (SR-01…SR-04).
  - [ ] Middleware regression tests to ensure helper decisions propagate to dispatcher selection.
  - [ ] Processor routing tests to confirm cloud/enterprise handler + scheduler pairing still works with helper output.
- [ ] Add `handler/base_auth_test.go` (or similar):
  - [ ] Verify `Base.NewInstallationClient` returns authenticated clients for both cloud and enterprise bases (Test 1.1).
  - [ ] Assert invalid installation IDs surface clean errors (Test 1.2).
  - [ ] Confirm `Base.NewEvalContext` builds REST/GraphQL clients and pulls config (Test 1.3).
- [ ] Document any new helper usage in `.claude/context` or inline comments for future contributors.
- [ ] Run `go test ./server/... -run Routing -count=1` (or equivalent) to validate new suites locally.
