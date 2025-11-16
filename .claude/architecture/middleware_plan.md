# Middleware Enhancement: Split Enterprise and Cloud Dispatchers

## Context
This task splits the single webhook dispatcher into separate enterprise (GHES) and cloud (GHEC) dispatchers with header-based routing middleware.

## Prerequisites
Before starting, analyze:
- `.claude/architecture/policy-bot-sqs_plan.md`
- `.claude/context.md`
- Current codebase structure, especially the existing dispatcher implementation

## Implementation Checklist

### Phase 1: Create Middleware Infrastructure

- [ ] Create new file `server/middleware/header_check.go`
- [ ] Implement `SelectWebhookHandler` function
  - Takes two parameters: `enterpriseHandler http.Handler`, `cloudHandler http.Handler`
  - Returns: `http.Handler`
  - Logic: Check for headers to route requests
    - If `x-dcp-destination-host` header present → route to `cloudHandler`
    - If `X-Github-Enterprise-Host` header present → route to `enterpriseHandler`
    - Handle cases where neither header is present (default behavior or error)
- [ ] Implement `SelectWebhookIndexHandler` function
  - Parameters:
    - `enterpriseBasePolicyHandler` (handler)
    - `cloudBasePolicyHandler` (handler)
    - `enterpriseGithubConfig` (config)
    - `cloudGithubConfig` (config)
    - `templates` (template data)
  - Returns: `http.HandlerFunc`
  - Logic: Check headers and serve appropriate handler's ServeHTTP
    - If `x-dcp-destination-host` → serve cloud handler
    - If `X-Github-Enterprise-Host` → serve enterprise handler
- [ ] Add appropriate error handling and logging to both middleware functions
- [ ] Add unit tests for `header_check.go`

### Phase 2: Split Dispatcher Configuration

- [ ] Locate the current dispatcher initialization:
  ```go
  dispatcher := githubapp.NewEventDispatcher(
      handlers,
      c.Github.App.WebhookSecret,
      githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
      githubapp.WithScheduler(scheduler),
  )
  ```
- [ ] Create separate scheduler for enterprise
  - Name: `enterpriseScheduler`
- [ ] Create separate scheduler for cloud
  - Name: `cloudScheduler`
- [ ] Create `enterpriseDispatcher`:
  - Use `githubapp.NewEventDispatcher` with GHES-specific configuration
  - Include all event handlers needed for enterprise
  - Use `enterpriseScheduler`
  - Use enterprise webhook secret from config
- [ ] Create `cloudDispatcher`:
  - Use `githubapp.NewEventDispatcher` with GHEC-specific configuration
  - Include all event handlers needed for cloud
  - Use `cloudScheduler`
  - Use cloud webhook secret from config
- [ ] Ensure both dispatchers use appropriate error callbacks

### Phase 3: Update Configuration

- [ ] Add `GithubEnterprise` configuration section to config structure
  - Should mirror existing Github config structure
  - Include enterprise-specific webhook secret
  - Include enterprise-specific app settings
- [ ] Add `GithubCloud` configuration section to config structure
  - Should mirror existing Github config structure
  - Include cloud-specific webhook secret
  - Include cloud-specific app settings
- [ ] Update configuration loading to populate both enterprise and cloud configs
- [ ] Verify configuration validation handles both new sections

### Phase 4: Update Routing

- [ ] Create `enterpriseBasePolicyHandler` instance
- [ ] Create `cloudBasePolicyHandler` instance
- [ ] Update index route from:
  ```go
  mux.Handle(pat.Get("/"), hatpear.Try(&handler.Index{
      Base:         basePolicyHandler,
      GithubConfig: &c.Github,
      Templates:    templates,
  }))
  ```
  To:
  ```go
  mux.Handle(pat.Get("/"), middleware.SelectWebhookIndexHandler(
      enterpriseBasePolicyHandler,
      cloudBasePolicyHandler,
      &c.GithubEnterprise,
      &c.GithubCloud,
      templates,
  ))
  ```
- [ ] Create `enterpriseDetailsHandler` instance
- [ ] Create `cloudDetailsHandler` instance
- [ ] Update details routes from:
  ```go
  details.Handle(pat.Get("/:owner/:repo/:number"), hatpear.Try(&detailsHandler))
  ```
  To:
  ```go
  details.Handle(pat.Get("/ghes/:owner/:repo/:number"), hatpear.Try(&enterpriseDetailsHandler))
  details.Handle(pat.Get("/ghec/:owner/:repo/:number"), hatpear.Try(&cloudDetailsHandler))
  ```
- [ ] Update any webhook endpoint registration to use `middleware.SelectWebhookHandler`
- [ ] Verify all route patterns are updated consistently

### Phase 5: Testing & Validation

- [ ] Write integration tests for header-based routing
  - Test `x-dcp-destination-host` routes to cloud dispatcher
  - Test `X-Github-Enterprise-Host` routes to enterprise dispatcher
  - Test behavior when headers are missing
  - Test behavior when both headers are present (edge case)
- [ ] Write unit tests for both dispatchers
- [ ] Test webhook delivery to both dispatchers
- [ ] Verify schedulers work independently for each dispatcher
- [ ] Test index handler routing with different headers
- [ ] Test details handler routing for both `/ghes/` and `/ghec/` paths
- [ ] Verify backward compatibility where applicable

### Phase 6: Documentation

- [ ] Update `.claude/architecture/policy-bot-sqs_plan.md` with new dispatcher architecture
- [ ] Document header-based routing behavior
- [ ] Update configuration documentation with new `GithubEnterprise` and `GithubCloud` sections
- [ ] Add comments in code explaining dispatcher split rationale
- [ ] Document the new URL path patterns (`/ghes/` and `/ghec/`)

## Notes

- Ensure thread-safety when both dispatchers are running concurrently
- Consider logging which dispatcher handles each request for debugging
- Verify that both dispatchers can coexist without resource conflicts
- Check if any existing handlers need updates to work with split dispatchers
- Review if any monitoring/metrics need updates