# Plan: Fix Authentication Failure Handling in Policy Bot

## Plan Status Checklist

- [ ] **Step 1**: Implement reactive auth handling in PostStatus function
- [ ] **Step 2**: Add auth failure recovery to all GitHub API calls
- [ ] **Step 3**: Ensure SQS retry logic correctly handles auth failures
- [ ] **Step 4**: Add comprehensive test coverage
- [ ] **Step 5**: Document the authentication recovery flow

## Context

The application has a `HandleAuthFailure` function that was designed to reactively handle authentication failures, but it is **NOT being called** in production code where GitHub API calls are made. This means:

1. When tokens expire, API calls fail with 401/403 errors
2. These errors are NOT triggering token refresh/client recreation
3. The SQS consumer correctly classifies auth errors as non-retryable, but doesn't attempt recovery
4. Messages with auth failures are deleted from the queue without attempting to fix the auth issue

## Constraints

- Must maintain backward compatibility
- Cannot modify vendor dependencies
- Must follow reactive token management (no proactive token creation)
- Must handle both GHEC (per-org) and GHES (per-installation) environments
- Must preserve idempotency for message processing

## References

- **HandleAuthFailure**: `server/handler/base.go:811-853`
- **retrieveClientAndInstallationId**: `server/handler/base.go:694-794`
- **PostStatus**: `server/handler/base.go:245-249`
- **SQS Processor**: `server/sqsconsumer/processor.go:367-441`
- **Error Classification**: `server/handler/errors.go:32-155`
- **Documentation**: `.claude/documentation/02-technical-architecture.md`

## Where to Look for Information

- **Event Handlers**: `server/handler/pull_request.go`, `server/handler/issue_comment.go`, etc.
- **GitHub API Calls**: Search for `client.Repositories`, `client.PullRequests`, `client.Issues`
- **SQS Processing**: `server/sqsconsumer/processor.go`
- **Test Files**: `server/handler/base_getclientsbyowner_test.go` (has examples of HandleAuthFailure usage)

## Things to Keep in Mind

1. **Token Lifecycle**: `ghinstallation.Transport` automatically refreshes tokens 1 minute before expiry
2. **Cache Invalidation**: HandleAuthFailure clears cache entries when auth fails
3. **Negative Caching**: 404/410 errors result in negative caching (2-minute TTL)
4. **Rate Limits**: 403 rate limit errors should NOT trigger auth refresh
5. **Idempotency**: Successfully processed messages are tracked to prevent duplicates

---

## Detailed Steps

### Step 1: Implement Reactive Auth Handling in PostStatus Function

**Information**:
- PostStatus is called from eval_context.go to update commit status
- Currently just returns error without attempting recovery
- Location: `server/handler/base.go:245-249`

**Implementation**:
1. Modify PostStatus to detect auth failures
2. Call HandleAuthFailure when 401/403/404/422 detected
3. Retry API call with refreshed clients
4. Only fail after retry attempt

```go
func PostStatus(ctx context.Context, client *github.Client, owner, repo, ref string, status *github.RepoStatus) error {
    logger := zerolog.Ctx(ctx).Info()
    logger.Msgf("Setting %q status on %s to %s: %s", status.GetContext(), ref, status.GetState(), status.GetDescription())

    _, _, err := client.Repositories.CreateStatus(ctx, owner, repo, ref, status)
    if err != nil {
        // Check if auth-related error
        status, isRateLimit, isAuth := classifyGitHubError(err)
        if isAuth && !isRateLimit {
            // Need access to Base to call HandleAuthFailure
            // This requires refactoring to pass Base or handler context
            // Alternative: Return a special error type for caller to handle
            return &AuthFailureError{
                OriginalError: err,
                Owner: owner,
                Repo: repo,
            }
        }
    }
    return errors.WithStack(err)
}
```

**Testing Plan**:
1. Unit test PostStatus with mock client returning 401/403
2. Verify auth recovery is attempted
3. Test successful retry after token refresh
4. Test permanent failure after retry

**Acceptance Criteria**:
- [ ] PostStatus detects auth failures
- [ ] Auth failures trigger HandleAuthFailure
- [ ] API call is retried with new clients
- [ ] Rate limit errors don't trigger refresh

### Step 2: Add Auth Failure Recovery Wrapper

**Information**:
- Multiple places call GitHub API directly through clients
- Need consistent auth failure handling across all API calls
- Should be implemented as a wrapper or helper function

**Implementation**:
1. Create a generic wrapper for GitHub API calls:

```go
// ExecuteWithAuthRecovery wraps a GitHub API call with auth failure recovery
func (b *Base) ExecuteWithAuthRecovery(
    ctx context.Context,
    owner string,
    ownerID int64,
    repo string,
    installationID int64,
    apiCall func(*github.Client) error,
) error {
    // Get current client
    client := // ... get from context or cache

    // Try API call
    err := apiCall(client)
    if err == nil {
        return nil
    }

    // Check if auth failure
    _, isRateLimit, isAuth := classifyGitHubError(err)
    if !isAuth || isRateLimit {
        return err // Not an auth issue, return as-is
    }

    // Attempt auth recovery
    refreshedClients, _, refreshErr := b.HandleAuthFailure(ctx, owner, ownerID, repo, installationID, err)
    if refreshErr != nil {
        return refreshErr // Recovery failed
    }

    // Retry with refreshed client
    return apiCall(refreshedClients.V3Client)
}
```

2. Update critical API calls to use the wrapper:
   - PostStatus
   - RequestReviewers
   - DismissReview
   - CreateComment

**Testing Plan**:
1. Test wrapper with successful API call
2. Test wrapper with auth failure and successful recovery
3. Test wrapper with auth failure and failed recovery
4. Test wrapper with non-auth errors (should pass through)

**Acceptance Criteria**:
- [ ] Wrapper correctly identifies auth failures
- [ ] Wrapper calls HandleAuthFailure for auth errors
- [ ] Wrapper retries with refreshed clients
- [ ] Non-auth errors pass through unchanged

### Step 3: Fix SQS Auth Handling Flow

**Information**:
- SQS processor correctly identifies auth errors as non-retryable
- But it deletes messages without attempting token refresh
- Need to attempt recovery before marking as non-retryable

**Implementation**:
1. Modify processor.go to attempt auth recovery:

```go
// In ProcessMessage, after error occurs (line 367):
if err != nil {
    // Try auth recovery if it's an auth error
    if policyhandler.IsAuthenticationError(err) && !isRetryable {
        // Extract owner/repo from message
        owner, repo := extractOwnerRepo(sqsMsg.Payload)

        // Get Base handler to attempt recovery
        if baseHandler, ok := handler.(*policyhandler.Base); ok {
            // Attempt auth recovery
            _, _, recoveryErr := baseHandler.HandleAuthFailure(
                ctx, owner, 0, repo, installationID, err,
            )

            if recoveryErr == nil {
                // Retry processing with recovered auth
                err = p.processViaDirect(ctx, sqsMsg, handler, payloadBytes, msgLogger)
                if err == nil {
                    // Success after recovery!
                    p.idempotency.MarkProcessed(sqsMsg.DeliveryID)
                    return p.deleteMessage(ctx, queueURL, message.ReceiptHandle, msgLogger)
                }
            }
        }
    }

    // Continue with existing error handling...
}
```

**Testing Plan**:
1. Test SQS processing with auth failure
2. Verify HandleAuthFailure is called
3. Test successful recovery and retry
4. Test failed recovery (message deleted)
5. Test idempotency after recovery

**Acceptance Criteria**:
- [ ] Auth failures trigger recovery attempt
- [ ] Successful recovery allows message processing
- [ ] Failed recovery marks message as non-retryable
- [ ] Idempotency maintained throughout

### Step 4: Add Comprehensive Test Coverage

**Information**:
- Need tests for all auth recovery scenarios
- Must test both GHEC and GHES environments
- Should verify cache invalidation and metrics

**Implementation**:
1. Add integration tests:
   - `TestAuthFailureRecoveryInEvaluation`
   - `TestAuthFailureRecoveryInPostStatus`
   - `TestAuthFailureRecoveryInSQS`

2. Add unit tests:
   - `TestHandleAuthFailureWithAllErrorCodes`
   - `TestExecuteWithAuthRecovery`
   - `TestSQSAuthRecoveryFlow`

3. Add benchmark tests:
   - `BenchmarkAuthRecovery`
   - `BenchmarkCacheInvalidation`

**Testing Plan**:
1. Mock GitHub API to return auth errors
2. Verify recovery attempts
3. Test successful and failed recovery paths
4. Verify metrics and logging
5. Test cache behavior

**Acceptance Criteria**:
- [ ] 100% code coverage for auth handling
- [ ] Tests pass for both GHEC and GHES
- [ ] Performance benchmarks meet targets
- [ ] No regressions in existing tests

### Step 5: Document Authentication Recovery Flow

**Information**:
- Need clear documentation for operators
- Should include troubleshooting guide
- Must explain retry behavior

**Implementation**:
1. Update technical architecture doc with auth recovery flow
2. Add auth failure handling to operations playbook
3. Create troubleshooting guide for common auth issues
4. Add metrics dashboard for auth failures/recoveries

**Documentation Sections**:
- Authentication Token Lifecycle
- Reactive Recovery Flow
- Cache Invalidation Strategy
- SQS Retry Behavior
- Monitoring and Alerts
- Common Issues and Solutions

**Testing Plan**:
1. Review documentation with SRE team
2. Validate runbook procedures
3. Test monitoring alerts
4. Verify dashboard accuracy

**Acceptance Criteria**:
- [ ] Complete auth recovery documentation
- [ ] Runbook tested and validated
- [ ] Monitoring dashboard deployed
- [ ] Team trained on new behavior

---

## Implementation Priority

1. **Critical** (Do First):
   - Step 3: Fix SQS auth handling (prevents message loss)
   - Step 1: Fix PostStatus (most common API call)

2. **Important** (Do Second):
   - Step 2: Add auth recovery wrapper (comprehensive fix)
   - Step 4: Add test coverage (ensure reliability)

3. **Nice to Have** (Do Last):
   - Step 5: Documentation (important but not blocking)

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Token refresh loop | High | Add retry limit (max 1 retry) |
| Cache corruption | Medium | Add cache validation before use |
| Performance impact | Low | Use singleflight for concurrent requests |
| Breaking changes | High | Feature flag for gradual rollout |

## Success Metrics

- Zero auth-related message failures in SQS
- 90% reduction in 401/403 errors
- Token refresh success rate > 99%
- No increase in API rate limit errors
- P95 latency unchanged

## Notes

- The `ghinstallation.Transport` already handles token refresh automatically, but only for tokens it created
- When we get cached clients with expired tokens, the transport doesn't know to refresh
- HandleAuthFailure forces recreation of clients with fresh tokens
- This is a reactive approach - we only refresh when we detect failures