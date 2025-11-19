# Plan: Wire HandleAuthFailure into handlers

## Context
- `HandleAuthFailure` now provides reactive cache invalidation/recreation for auth-ish errors (401/403/404/410/422) and ignores rate limits. It’s defined/tested in `server/handler/base.go` but not invoked in runtime code.
- Goal: ensure auth failures observed during webhook/SQS handling trigger the refresh path without proactive token minting.

## Constraints / best practices
- Do not mint tokens proactively; rely on ghinstallation auto-refresh.
- Only trigger on auth-ish responses; rate limits (403 RateLimitError) should pass through.
- Keep GHES behavior intact (repo-based lookups) and GHEC owner cache semantics.

## Steps
1) **Identify auth-sensitive call sites**
   - PR evaluation (`NewEvalContext` consumers), status/checkrun posting, installation events, and any SQS processors where GitHub REST/GraphQL calls are made.
   - Map which already catch errors vs bubble up.

2) **Add a small helper wrapper**
   - Create a shared utility per handler (e.g., `withAuthRefresh(ctx, owner, ownerID, installationID, repo, fn)`) that:
     - Executes `fn()`.
     - On error, runs `HandleAuthFailure`; if it returns new clients, retry `fn()` once with refreshed clients or bubble the refreshed clients upward.
   - Ensure repo/owner/ownerID are passed when available for correct cache keys.

3) **Integrate into handlers**
   - `IssueComment`, `PullRequest`, `PullRequestReview`, `Status`, `CheckRun`, `WorkflowRun` handlers: wrap the initial client retrieval + first API call that can 401/404.
   - SQS consumer paths (if applicable) should use the same wrapper.
   - Keep logging clear when refresh is attempted vs skipped.

4) **Surface refreshed clients**
   - Where handler functions accept `*InstallationClients`, allow the retry path to swap in refreshed clients (v3/v4) returned by `HandleAuthFailure`.

5) **Testing**
   - Unit tests per handler to simulate 401/404/422 and assert:
     - Cache invalidation/negative caching occurs.
     - A retry happens once with refreshed clients.
     - Rate-limit 403 does NOT trigger refresh.
   - Ensure existing handler tests still pass.

6) **Docs**
   - Briefly document the reactive auth-refresh flow in `TESTING.md` or `.claude/documentation/03-operations-playbook.md` for oncall awareness.

## Acceptance
- Auth failures in handlers (webhook/SQS) trigger `HandleAuthFailure` and either refresh clients or negative-cache appropriately.
- Rate-limit errors bypass refresh.
- No proactive token minting introduced; handler test suites still green.
