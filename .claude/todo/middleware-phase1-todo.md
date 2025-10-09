# Phase 1: Fix Critical Bugs and Compilation Issues

## Phase Overview
**Priority**: URGENT - Blocks all other work
**Estimated Time**: 1-2 hours
**Purpose**: Fix critical bugs that prevent compilation and correct dispatcher configuration errors

## Prerequisites
- [ ] Confirm access to `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Confirm access to `/Users/dannytrevino/development/policy-bot/server/middleware/header_check.go`
- [ ] Ensure git is initialized and on feature branch
- [ ] Review lines 289-301 and 339-341 and 373 in `server/server.go`

## Context
The current codebase has several critical issues:
1. Enterprise dispatcher uses wrong handlers (`cloudHandlers` instead of `enterpriseHandlers`)
2. Middleware file exists but is empty, breaking compilation
3. Missing import statement for middleware package
4. Reference to non-existent `middleware.SelectIndexHandler` function

## Tasks

### Task 1: Fix Dispatcher Handler Assignment Bug
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Locate line 290 (approximately): `enterpriseDispatcher := githubapp.NewEventDispatcher(cloudHandlers,`
- [ ] Change `cloudHandlers` to `enterpriseHandlers`
- [ ] Verify line should read: `enterpriseDispatcher := githubapp.NewEventDispatcher(enterpriseHandlers,`
- [ ] Save the file

### Task 2: Add Middleware Package Import
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Locate the import block at the top of the file
- [ ] Add the following import: `"github.com/palantir/policy-bot/server/middleware"`
- [ ] Ensure import is properly formatted and grouped with other project imports
- [ ] Save the file

### Task 3: Create Minimal Stub Middleware Implementation
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/middleware/header_check.go`
- [ ] Add package declaration: `package middleware`
- [ ] Add necessary imports:
  ```go
  import (
      "net/http"
      "github.com/palantir/policy-bot/server/handler"
      "github.com/palantir/go-githubapp/githubapp"
      "github.com/bluekeyes/templatetree"
  )
  ```
- [ ] Create stub function for `SelectIndexHandler`:
  ```go
  // SelectIndexHandler is a temporary stub - will be implemented in Phase 3
  func SelectIndexHandler(
      enterpriseBase handler.Base,
      cloudBase handler.Base,
      enterpriseConfig *githubapp.Config,
      cloudConfig *githubapp.Config,
      templates templatetree.HTMLTree,
  ) http.HandlerFunc {
      // Temporary implementation - always use cloud for now
      return func(w http.ResponseWriter, r *http.Request) {
          indexHandler := &handler.Index{
              Base:         cloudBase,
              GithubConfig: cloudConfig,
              Templates:    templates,
          }
          indexHandler.ServeHTTP(w, r)
      }
  }
  ```
- [ ] Save the file

### Task 4: Fix Variable Name Typo
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Search for `cloudeDispatcher` (note the extra 'e')
- [ ] Replace all instances of `cloudeDispatcher` with `cloudDispatcher`
- [ ] This should be around lines 296-301 and 340-341
- [ ] Save the file

### Task 5: Verify Compilation
- [ ] Run `go build ./...` from project root
- [ ] Check for compilation errors
- [ ] If errors exist related to middleware:
  - [ ] Check import paths are correct
  - [ ] Verify function signatures match
  - [ ] Add any missing type imports to middleware file
- [ ] Run `go mod tidy` to clean up dependencies

### Task 6: Run Basic Tests
- [ ] Run `go test ./server/...` to ensure no tests are broken
- [ ] Document any test failures (expected since middleware not fully implemented)
- [ ] Ensure critical server startup tests pass

## Acceptance Criteria
- [ ] Code compiles without errors
- [ ] `enterpriseDispatcher` uses `enterpriseHandlers` (not `cloudHandlers`)
- [ ] `cloudDispatcher` variable name is consistent (no typo)
- [ ] Middleware package is imported in `server/server.go`
- [ ] Stub `SelectIndexHandler` function exists and returns valid handler
- [ ] Server can start (may not be fully functional)
- [ ] Git diff shows only the intended changes

## Rollback Plan
If compilation still fails after these fixes:
1. Comment out line 373 (the `middleware.SelectIndexHandler` call)
2. Replace with direct handler instantiation (temporary)
3. Document the temporary workaround for removal in Phase 3

## Notes for Next Phase
- The stub implementation is temporary and will be replaced in Phase 3
- The middleware currently defaults to cloud behavior
- Full header-based routing will be implemented in Phase 3
- Configuration changes needed in Phase 2 before full implementation