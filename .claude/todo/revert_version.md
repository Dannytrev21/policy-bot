# Go-GitHub v74 to v47 Reversion Plan for Policy Bot

## Executive Summary
This document outlines a comprehensive plan to revert the Policy Bot application from go-github v74 to v47. This is a significant downgrade that spans 27 major versions and involves substantial breaking changes that must be carefully addressed.

## Current State Analysis

### Dependencies
- **Current Version**: `github.com/google/go-github/v74 v74.0.0`
- **Target Version**: `github.com/google/go-github/v47 v47.1.0`
- **Related Dependency**: `github.com/palantir/go-githubapp v0.38.1` (may need compatibility check)
- **Indirect Dependency**: `github.com/google/go-github/v72 v72.0.0` (needs investigation)

### Affected Files
The following project files directly import go-github v74:

#### Core Application Files
1. `pull/github.go` - Pull request data structures and methods
2. `pull/github_membership.go` - GitHub membership checks
3. `pull/github_test.go` - Tests for GitHub functionality
4. `pull/list_teams.go` - Team listing functionality

#### Handler Files
1. `server/handler/base.go` - Base handler implementation
2. `server/handler/check_run.go` - Check run event handling
3. `server/handler/cross_org.go` - Cross-organization functionality
4. `server/handler/details.go` - PR details handling
5. `server/handler/eval_context.go` - Evaluation context
6. `server/handler/eval_context_reviewers.go` - Reviewer evaluation
7. `server/handler/fetcher.go` - Data fetching logic
8. `server/handler/installation.go` - GitHub App installation handling
9. `server/handler/issue_comment.go` - Issue comment events
10. `server/handler/login.go` - Authentication logic
11. `server/handler/merge_group.go` - Merge group handling
12. `server/handler/pull_request.go` - Pull request events
13. `server/handler/pull_request_review.go` - Review events
14. `server/handler/status.go` - Status check handling
15. `server/handler/workflow_run.go` - Workflow run events

#### Test Files
1. `server/test_helpers.go` - Test utilities
2. `policy/predicate/status_test.go` - Status predicate tests

## Major Breaking Changes Between v47 and v74

### 1. Context Parameter Addition (v48-v50)
**Impact**: High
**Description**: Most API methods now require `context.Context` as the first parameter.

**Example Change Required**:
```go
// v47
pr, _, err := client.PullRequests.Get(owner, repo, number)

// v74
pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
```

### 2. Pointer Types for Optional Fields (v52-v60)
**Impact**: High
**Description**: Many optional fields are now pointer types to distinguish between zero values and unset fields.

**Example Change Required**:
```go
// v47
event.Action == "opened"

// v74
event.GetAction() == "opened"
```

### 3. Error Handling Changes (v55)
**Impact**: Medium
**Description**: Error types and handling mechanisms have been updated.

### 4. Pagination API Changes (v62)
**Impact**: Medium
**Description**: ListOptions and pagination handling have been restructured.

### 5. Event Type Restructuring (v65-v70)
**Impact**: High
**Description**: Several event types have been renamed or restructured:
- `PullRequestEvent` fields changed
- `IssueCommentEvent` modifications
- `CheckRunEvent` updates
- `WorkflowRunEvent` changes

### 6. GitHub App Authentication (v71)
**Impact**: Critical
**Description**: GitHub App authentication methods have changed significantly.

### 7. API Method Signature Changes
**Impact**: High
**Description**: Many API methods have changed signatures:
- Team API methods
- Organization membership checks
- Repository API calls

## Implementation Plan

### Phase 1: Dependency Analysis (Day 1-2)

#### Step 1.1: Verify go-githubapp Compatibility
```bash
# Check if go-githubapp v0.38.1 is compatible with go-github v47
go mod graph | grep go-githubapp | grep go-github
```

**Action Items**:
- [x] Determine if go-githubapp needs downgrading  
  - Result: `github.com/palantir/go-githubapp` v0.38.1 hard-pins go-github/v74; the last release that targets go-github/v47 is v0.14.0. A downgrade is required and will also roll back bundled middleware to 2022-era implementations.
- [x] Check for transitive dependency conflicts  
  - Finding: Downgrading to v0.14.0 pulls `github.com/bradleyfalzon/ghinstallation/v2@v2.1.0`, `github.com/hashicorp/golang-lru@v0.5.4`, and older oauth2/graphql clients. All APIs currently consumed by policy-bot (client creator options, middleware hooks, OAuth helper) exist in the older release, but we must validate newer functionality (metrics/logging wrappers, caching flags) still behave as expected.
- [x] Document required version combinations  
  - Target matrix: go-github/v47.1.0 + go-githubapp/v0.14.0 + ghinstallation/v2.1.0. No later go-githubapp version supports v47 because v0.15.0 jumps to go-github/v50.

#### Step 1.2: Resolve Indirect Dependency
- [x] Investigate why go-github v72 is an indirect dependency  
  - Root cause: `github.com/bradleyfalzon/ghinstallation/v2@v2.16.0` (pulled in by go-githubapp v0.38.1) embeds go-github/v72 for REST data types used during installation token refresh.
- [x] Determine if it needs to be removed or can coexist  
  - Assessment: The duplicate major version is benign at build time, but keeping v72 alongside v47 complicates the downgrade and vendor tree. Switching go-githubapp to v0.14.0 automatically swaps ghinstallation to v2.1.0, which depends on go-github/v45 instead of v72, eliminating the stray v72 module.
- [x] Document resolution strategy  
  - Plan: Downgrade go-githubapp first; the indirect v72 requirement disappears with the older ghinstallation dependency. Confirm `go mod tidy` removes v72 from go.mod/go.sum during Phase 4 (go.mod update) to keep the module graph clean.

### Phase 2: Code Modification (Day 3-7)

#### Step 2.1: Remove Context Parameters
**Files to Modify**: All handler and pull package files

**Pattern to Replace**:
```go
// Find patterns like:
client.PullRequests.Get(ctx, owner, repo, number)

// Replace with:
client.PullRequests.Get(owner, repo, number)
```

**Automated Script**:
```bash
# Create backup
cp -r . ../policy-bot-backup

# Use sed or similar tool for bulk replacement
find . -name "*.go" -not -path "./vendor/*" | xargs sed -i 's/\.Get(ctx, /\.Get(/g'
find . -name "*.go" -not -path "./vendor/*" | xargs sed -i 's/\.List(ctx, /\.List(/g'
find . -name "*.go" -not -path "./vendor/*" | xargs sed -i 's/\.Create(ctx, /\.Create(/g'
# ... continue for other methods
```
- [ ] Action not required  
  - Finding: go-github introduced `context.Context` parameters well before v47; APIs still require `ctx`, so removing it would break all client calls. Left usage unchanged and documented mismatch with original downgrade plan.

#### Step 2.2: Revert Pointer Field Access
**Pattern to Replace**:
```go
// Find GetXXX() method calls:
event.GetAction()
pr.GetNumber()

// Replace with direct field access:
event.Action
pr.Number
```

**Manual Review Required** for nullable fields that legitimately need nil checks.
- [x] Applied targeted fixes  
  - `pull/github.go` now handles `time.Time` values returned by v47 (`GetCreatedAt()` no longer wraps `github.Timestamp`). Updated status helpers to avoid `.Time` accessors and ensured tests construct `*time.Time` values directly.

#### Step 2.3: Update Event Type Usage
**Critical Files**:
- `server/handler/pull_request.go`
- `server/handler/pull_request_review.go`
- `server/handler/issue_comment.go`
- `server/handler/check_run.go`
- `server/handler/workflow_run.go`

**Changes Required**:
- Review each event handler for field name changes
- Update event parsing logic
- Verify webhook payload compatibility
- [x] Adjusted for missing v74 types  
  - Added local `mergeGroupEvent`/`mergeGroupPayload` shims so merge-queue handling continues to work without `github.MergeGroupEvent`.  
  - Reworked `LatestWorkflowRuns` to drop v74-only list options, fetch workflow metadata via `GetWorkflowByID`, and preserve SQS/HTTP behavior expected by policy predicates and tests.  
  - Updated tests and fixtures to cover the new code path and avoid relying on removed fields.

#### Step 2.4: Fix API Method Signatures
**Focus Areas**:
1. Team API calls in `pull/list_teams.go`
2. Membership checks in `pull/github_membership.go`
3. Installation handling in `server/handler/installation.go`
- [x] Reconciled signature shifts  
  - `ListPullRequestsWithCommit` invocations now pass `PullRequestListOptions`.  
  - `GetBranch` uses the legacy `followRedirects` bool parameter.  
  - Queue schedulers explicitly preserve context (`context.WithoutCancel`) so SQS-derived metadata still reaches handlers under go-githubapp v0.14.0.

### Phase 3: Testing (Day 8-10)

#### Step 3.1: Unit Test Updates
- [x] Update test files to match v47 API *(no additional edits required; downgrade patches compile cleanly)*
- [x] Fix mock objects and test helpers *(existing fixtures compatible with v47)*
- [x] Ensure all unit tests pass (`go test ./...`)

#### Step 3.2: Integration Testing
- [ ] Set up test GitHub App with v47 *(blocked: requires external GitHub App credentials)*
- [x] Test all webhook event types:
  - [x] Pull request events (`go test ./test`)
  - [x] Issue comment events (`go test ./test`)
  - [x] Check run events (`go test ./test`)
  - [x] Status events (`go test ./test`)
  - [x] Workflow run events (`go test ./test`)
  - [x] Installation events (`go test ./test`)
  - [x] Merge group events (`go test ./test`)

#### Step 3.3: Regression Testing
- [ ] Test approval policies *(pending manual validation in staging)*
- [ ] Test disapproval mechanisms *(pending manual validation in staging)*
- [ ] Test cross-organization features *(pending live org data)*
- [ ] Test GitHub App authentication *(blocked until staged GitHub App available)*
- [ ] Test pagination for large result sets *(manual load scenario not executed)*

**Phase 3 Findings**
- Added local `server/metricsbridge` to restore Prometheus export support missing in go-baseapp v0.4.1.
- Dropped usage of `baseapp.IgnoreAll`; health and metrics handlers now operate without that helper.
- `go test ./...` passes in ~30s covering unit suites plus simulated webhook flows (HTTP + SQS) in `test/`.

### Phase 4: go.mod Update (Day 11)

#### Step 4.1: Update Dependencies
```bash
# Update go.mod
go mod edit -require=github.com/google/go-github/v47@v47.1.0

# If go-githubapp needs updating:
go mod edit -require=github.com/palantir/go-githubapp@[compatible-version]

# Tidy and download
go mod tidy
go mod download
```
- [x] go.mod downgraded to go-github v47.1.0, go-githubapp v0.14.0, go-baseapp v0.4.1.
- [x] go mod tidy
- [x] go mod download

#### Step 4.2: Vendor Update
```bash
# Update vendor directory
go mod vendor
```
- [x] go mod vendor *(pulled v47/v45 REST models; removed v74/v72 assets)*  
- [x] Added `server/metricsbridge` to restore Prometheus export support missing from go-baseapp v0.4.1 (keeps existing `prometheus` config block working without code changes).

### Phase 5: Validation (Day 12-14)

#### Step 5.1: Build Verification
```bash
# Clean build
go clean -cache
go build ./...

# Run linters
./godelw verify
```
- [x] `go clean -cache` / `go build ./...`
- [x] `./godelw verify` *(initial run surfaced errcheck/ineffassign hits from older go-baseapp; resolved by wrapping `Close()` calls, logging index render errors, and fixing env helper to mutate pointers.)*

#### Step 5.2: Comprehensive Test Suite
```bash
# Run all tests with coverage
go test -v -cover ./...

# Run race detection
go test -race ./...
```
- [x] `go test -v -cover ./...` *(passes; integration + performance harness exercising HTTP+SQS flows on downgraded clients.)*
- [x] `go test -race ./...` *(passes with macOS ld warnings about LC_DYSYMTAB ordering—non-fatal and consistent across packages.)*

#### Step 5.3: Docker Image Build
```bash
# Build Docker image
./godelw docker build --verbose

# Test Docker container
docker run --rm -v "$(pwd)/config:/secrets/" -p 8080:8080 palantirtechnologies/policy-bot:latest
```

### Phase 6: Staging Deployment (Day 15-17)

#### Step 6.1: Staging Environment Setup
- [ ] Deploy to staging environment
- [ ] Configure test repositories
- [ ] Set up monitoring and logging

#### Step 6.2: End-to-End Testing
- [ ] Create test pull requests
- [ ] Verify policy evaluation
- [ ] Test all approval rules
- [ ] Verify status checks
- [ ] Test UI functionality

#### Step 6.3: Performance Testing
- [ ] Monitor API rate limits
- [ ] Check response times
- [ ] Verify webhook processing speed
- [ ] Test under load

## Risk Assessment

### High Risk Areas

1. **GitHub App Authentication**
   - Risk: Authentication methods may be incompatible
   - Mitigation: Thorough testing of all auth flows
   - Fallback: May need to update go-githubapp or implement custom auth

2. **Event Processing**
   - Risk: Webhook payloads may not parse correctly
   - Mitigation: Comprehensive webhook testing
   - Fallback: Implement adapters for event translation

3. **API Rate Limiting**
   - Risk: v47 may have different rate limit handling
   - Mitigation: Review rate limit code carefully
   - Fallback: Implement custom rate limiting logic

4. **Transitive Dependencies**
   - Risk: Other packages may depend on go-github features
   - Mitigation: Full dependency analysis
   - Fallback: Fork and patch dependent packages

### Medium Risk Areas

1. **Pagination Changes**
   - Risk: Large result sets may not paginate correctly
   - Mitigation: Test with repositories having many PRs/issues

2. **Error Handling**
   - Risk: Error types may have changed
   - Mitigation: Review all error handling code

### Low Risk Areas

1. **UI Components**
   - Risk: Minimal as UI is separate from API
   - Mitigation: Basic UI testing

## Rollback Plan

If the reversion fails or causes critical issues:

1. **Immediate Rollback**
   ```bash
   git checkout main
   git revert HEAD
   ```

2. **Restore from Backup**
   ```bash
   cp -r ../policy-bot-backup/* .
   go mod download
   go build ./...
   ```

3. **Emergency Hotfix**
   - Keep v74 but implement compatibility layer
   - Deploy previous Docker image
   - Restore previous deployment

## Success Criteria

The reversion is considered successful when:

1. [ ] All unit tests pass
2. [ ] All integration tests pass
3. [ ] Docker image builds successfully
4. [ ] Staging deployment works correctly
5. [ ] All webhook events process correctly
6. [ ] Policy evaluation works as expected
7. [ ] No performance degradation observed
8. [ ] Authentication and authorization work correctly
9. [ ] Cross-organization features function properly
10. [ ] UI displays information correctly

## Timeline Summary

- **Days 1-2**: Dependency Analysis
- **Days 3-7**: Code Modification (5 days)
- **Days 8-10**: Testing (3 days)
- **Day 11**: go.mod Update
- **Days 12-14**: Validation (3 days)
- **Days 15-17**: Staging Deployment (3 days)

**Total Estimated Time**: 17 days

## Commands Reference

### Quick Commands for Reversion

```bash
# 1. Create backup
cp -r . ../policy-bot-backup

# 2. Update go.mod
go mod edit -require=github.com/google/go-github/v47@v47.1.0

# 3. Remove context parameters (example)
find . -name "*.go" -not -path "./vendor/*" -exec sed -i 's/\.Get(ctx, /\.Get(/g' {} \;

# 4. Build and test
go mod tidy
go build ./...
go test ./...

# 5. Run linters
./godelw verify

# 6. Build Docker image
./godelw docker build --verbose
```

## Conclusion

This reversion from go-github v74 to v47 is a complex undertaking that requires careful attention to:
- API method signature changes
- Event type modifications
- Authentication mechanisms
- Error handling patterns

The plan prioritizes safety through comprehensive testing and provides clear rollback procedures. Success depends on thorough testing at each phase and careful validation of all GitHub API interactions.

## Appendix A: File-by-File Change Summary

### pull/github.go
- Remove context from API calls
- Change `GetXXX()` methods to direct field access
- Update PullRequest struct usage

### server/handler/pull_request.go
- Update PullRequestEvent structure
- Remove context from event processing
- Fix action string comparisons

### server/handler/installation.go
- Update installation event handling
- Fix GitHub App authentication calls
- Remove context parameters

[Continue for each affected file...]

## Appendix B: Testing Checklist

### Unit Tests
- [ ] pull/github_test.go
- [ ] policy/predicate/status_test.go
- [ ] server/test_helpers.go modifications

### Integration Tests
- [ ] Pull request creation
- [ ] Pull request updates
- [ ] Comment handling
- [ ] Review handling
- [ ] Status checks
- [ ] Workflow runs
- [ ] Label operations
- [ ] Cross-org operations

### End-to-End Scenarios
- [ ] Complete approval flow
- [ ] Disapproval flow
- [ ] Policy evaluation with complex rules
- [ ] Multi-organization scenarios
- [ ] Large repository handling
- [ ] Rate limit behavior

## Appendix C: Monitoring and Alerts

Post-deployment monitoring should focus on:
1. API error rates
2. Webhook processing times
3. Authentication failures
4. Rate limit exhaustion
5. Memory usage patterns
6. Response time metrics

Set up alerts for:
- Authentication failures > 1%
- Webhook processing time > 5s
- API errors > 5%
- Rate limit usage > 80%
