# Phase 4: Routing Updates

## Phase Overview
**Priority**: HIGH - Integrate middleware
**Estimated Time**: 2-3 hours
**Purpose**: Update all routes in the server to use the new middleware functions, fixing route collisions and ensuring proper separation

## Prerequisites
- [x] Phase 1-3 completed successfully
- [x] Middleware functions implemented and tested
- [x] Configuration supports separate enterprise/cloud configs
- [x] Access to `/Users/dannytrevino/development/policy-bot/server/server.go`
- [x] Understanding of current routing structure (lines 330-401)

## Context
Current issues to fix:
1. Webhook routes collision (lines 339-341) - both dispatchers on same path
2. API routes collision - both handlers registered on same paths
3. Index route already references middleware but needs verification
4. Details routes already separated (`/ghes/` and `/ghec/`) - keep as is

## Tasks

### Task 1: Fix Webhook Route Collision
- [x] Open `/Users/dannytrevino/development/policy-bot/server/server.go`
- [x] Locate lines 339-341 (webhook route registration)
- [x] Remove both existing webhook route registrations:
  ```go
  // DELETE these lines:
  // mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), cloudDispatcher)
  // mux.Handle(pat.Post(githubapp.DefaultWebhookRoute), enterpriseDispatcher)
  ```
- [x] Replace with single middleware-based route:
  ```go
  // Single webhook endpoint with header-based routing
  mux.Handle(pat.Post(githubapp.DefaultWebhookRoute),
      middleware.SelectWebhookDispatcher(enterpriseDispatcher, cloudDispatcher))
  ```
- [x] Add a comment explaining the routing logic:
  ```go
  // Webhook endpoint uses header-based routing:
  // - X-GitHub-Enterprise-Host header -> enterprise dispatcher
  // - x-dcp-destination-host header -> cloud dispatcher
  // - No header -> defaults to cloud dispatcher
  ```
- [x] Save the file

### Task 2: Verify Index Route
- [x] Locate line 373 (index route with SelectIndexHandler)
- [x] Verify the middleware call is correct:
  ```go
  mux.Handle(pat.Get("/"), middleware.SelectIndexHandler(
      enterpriseBasePolicyHandler,
      cloudBasePolicyHandler,
      &c.GithubEnterprise,
      &c.GithubCloud,
      templates,
  ))
  ```
- [x] If the function signature doesn't match, update accordingly
- [x] Add explanatory comment:
  ```go
  // Index page uses header-based routing to display appropriate GitHub App info
  ```
- [x] Save the file

### Task 3: Fix API Validation Route
- [x] Locate the `/api/validate` route registration (around line 354)
- [x] Create separate validation handlers if not already done:
  ```go
  enterpriseValidateHandler := &handler.Validate{
      Base: enterpriseBasePolicyHandler,
  }
  cloudValidateHandler := &handler.Validate{
      Base: cloudBasePolicyHandler,
  }
  ```
- [x] Replace the route registration with middleware:
  ```go
  mux.Handle(pat.Post("/api/validate"),
      middleware.SelectAPIHandler(enterpriseValidateHandler, cloudValidateHandler))
  ```
- [x] Add comment:
  ```go
  // Policy validation endpoint - routes based on headers or source param
  ```
- [x] Save the file

### Task 4: Fix API Simulation Route
- [x] Locate the `/api/simulate/:owner/:repo/:number` routes (around line 355-356)
- [x] Remove duplicate registrations
- [x] Create separate simulation handlers:
  ```go
  enterpriseSimulateHandler := &handler.Simulate{
      Base: enterpriseBasePolicyHandler,
  }
  cloudSimulateHandler := &handler.Simulate{
      Base: cloudBasePolicyHandler,
  }
  ```
- [x] Replace with single middleware-based route:
  ```go
  mux.Handle(pat.Post("/api/simulate/:owner/:repo/:number"),
      middleware.SelectAPIHandler(enterpriseSimulateHandler, cloudSimulateHandler))
  ```
- [x] Add comment:
  ```go
  // Policy simulation endpoint - routes based on headers or source param
  ```
- [x] Save the file

### Task 5: Review Details Routes
- [x] Locate the details routes (lines 378-401)
- [x] Verify the existing separation is maintained:
  ```go
  // Enterprise details route
  details.Handle(pat.Get("/ghes/:owner/:repo/:number"),
      hatpear.Try(&enterpriseDetailsHandler))

  // Cloud details route
  details.Handle(pat.Get("/ghec/:owner/:repo/:number"),
      hatpear.Try(&cloudDetailsHandler))
  ```
- [x] Do NOT change these - they are already properly separated by path
- [x] Add comment if not present:
  ```go
  // Details pages use explicit path separation:
  // - /details/ghes/* for GitHub Enterprise Server
  // - /details/ghec/* for GitHub Enterprise Cloud
  ```

### Task 6: Update Reviewers Expansion Routes
- [x] Locate the reviewers route (around line 390-400)
- [x] Check if it needs separation or if it's shared
- [x] If it needs separation, create two routes:
  ```go
  // Enterprise reviewers endpoint
  details.Handle(pat.Post("/ghes/:owner/:repo/:number/reviewers"),
      hatpear.Try(&handler.PullRequestReviewersExpansion{
          Base: enterpriseBasePolicyHandler,
      }))

  // Cloud reviewers endpoint
  details.Handle(pat.Post("/ghec/:owner/:repo/:number/reviewers"),
      hatpear.Try(&handler.PullRequestReviewersExpansion{
          Base: cloudBasePolicyHandler,
      }))
  ```
- [x] Save the file

### Task 7: Add Health Check Route Updates
- [x] Review the `/api/health` endpoint
- [x] Determine if it needs source-specific health checks
- [x] If yes, implement health check separation:
  ```go
  type CombinedHealthHandler struct {
      EnterpriseBase handler.Base
      CloudBase      handler.Base
  }

  func (h *CombinedHealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
      // Check both enterprise and cloud health
      // Return combined status
  }

  healthHandler := &CombinedHealthHandler{
      EnterpriseBase: enterpriseBasePolicyHandler,
      CloudBase:      cloudBasePolicyHandler,
  }
  mux.Handle(pat.Get("/api/health"), healthHandler)
  ```
- [x] If not needed, leave as is with comment
- [x] Save the file

### Task 8: Update OAuth Callback Route
- [x] Review the OAuth callback route (lines 358-368)
- [x] Determine if OAuth needs source separation
- [x] If OAuth is shared, add comment explaining why:
  ```go
  // OAuth callback is shared between enterprise and cloud
  // Session state determines which GitHub instance to authenticate with
  ```
- [x] If separation is needed, implement with middleware or path separation
- [x] Save the file

### Task 9: Create Route Documentation
- [x] Add a comprehensive comment block at the start of the routing section:
  ```go
  // Route Configuration:
  //
  // Header-based routing (via middleware):
  //   - /api/github/hook -> webhooks (enterprise/cloud based on headers)
  //   - / -> index page (enterprise/cloud based on headers)
  //   - /api/validate -> validation (enterprise/cloud based on headers)
  //   - /api/simulate/* -> simulation (enterprise/cloud based on headers)
  //
  // Path-based routing (explicit):
  //   - /details/ghes/* -> GitHub Enterprise Server details
  //   - /details/ghec/* -> GitHub Enterprise Cloud details
  //
  // Shared routes:
  //   - /api/health -> combined health check
  //   - /api/metrics -> Prometheus metrics
  //   - /static/* -> static assets
  //   - /oauth/callback -> OAuth callback (session-based)
  //
  // Routing priority:
  //   1. X-GitHub-Enterprise-Host header -> enterprise
  //   2. x-dcp-destination-host header -> cloud
  //   3. source query parameter -> enterprise/cloud
  //   4. Default -> cloud
  ```
- [x] Save the file

### Task 10: Test Route Registration
- [x] Run `go build ./...` to ensure compilation
- [x] Check for any duplicate route warnings
- [x] Verify all handlers are properly initialized before use
- [x] Run the server locally with test config:
  ```bash
  go run main.go server --config test-config.yml
  ```
- [x] Check server starts without route conflicts
- [x] Review startup logs for any routing errors

## Acceptance Criteria
- [x] No duplicate webhook routes (single route with middleware)
- [x] No duplicate API routes (middleware handles routing)
- [x] Index route uses SelectIndexHandler middleware
- [x] Details routes maintain `/ghes/` and `/ghec/` separation
- [x] All route registrations compile successfully
- [x] Server starts without route conflict warnings
- [x] Route documentation clearly explains routing strategy
- [x] No breaking changes to existing API paths
- [x] Middleware is properly integrated for header-based routing

## Testing Checklist
- [x] Test webhook endpoint with enterprise header
- [x] Test webhook endpoint with cloud header
- [x] Test webhook endpoint with no header (default)
- [x] Test `/api/validate` with different headers
- [x] Test `/api/simulate` with source query param
- [x] Test index page routing
- [x] Verify `/details/ghes/` routes work
- [x] Verify `/details/ghec/` routes work
- [x] Check health endpoint responds correctly

## Common Issues and Solutions

### Issue: Type mismatch in middleware parameters
**Solution**: Ensure handler types match what middleware expects. May need to wrap handlers in interfaces.

### Issue: Route patterns conflict
**Solution**: Use more specific patterns or ensure middleware handles all routing for shared paths.

### Issue: Handlers not initialized
**Solution**: Ensure all handlers are created before route registration.

### Issue: Middleware import not found
**Solution**: Check import path matches package location, run `go mod tidy`.

## Notes for Next Phase
- Phase 5 will enhance detection logic with payload inspection
- Phase 6 will add comprehensive testing for all routes
- Monitor logs to identify any routing edge cases
- Consider adding route-specific metrics in Phase 8