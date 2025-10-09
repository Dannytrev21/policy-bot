# Phase 2: Configuration Structure Enhancement

## Phase Overview
**Priority**: HIGH - Foundation for middleware
**Estimated Time**: 2-3 hours
**Purpose**: Add separate configuration sections for GitHub Enterprise and GitHub Cloud to support independent dispatcher configurations

## Prerequisites
- [ ] Phase 1 must be completed (code compiles)
- [ ] Access to `/Users/dannytrevino/development/policy-bot/server/config.go`
- [ ] Access to `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Understanding of existing `githubapp.Config` structure
- [ ] Sample configuration file for testing

## Context
Currently, the code references `c.GithubEnterprise` and `c.GithubCloud` (line 373 in server.go) but these fields don't exist in the Config struct. We need to:
1. Add these configuration fields
2. Support backward compatibility with existing `Github` field
3. Update server initialization to use the new configs
4. Ensure webhook secrets are properly separated

## Tasks

### Task 1: Analyze Current Config Structure
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/config.go`
- [ ] Review the existing `Config` struct (around line 47)
- [ ] Note the current `Github githubapp.Config` field
- [ ] Review `githubapp.Config` structure from imports
- [ ] Document any custom fields in current config

### Task 2: Create GithubAppConfig Type
- [ ] In `/Users/dannytrevino/development/policy-bot/server/config.go`, add before the `Config` struct:
  ```go
  // GithubAppConfig extends githubapp.Config with additional app-specific settings
  type GithubAppConfig struct {
      githubapp.Config `yaml:",inline" json:",inline"`

      // WebhookRoute allows custom webhook route per app (optional)
      WebhookRoute string `yaml:"webhook_route" json:"webhook_route"`

      // EventRouting controls which events go to SQS vs HTTP
      EventRouting map[string]string `yaml:"event_routing" json:"event_routing"`
  }
  ```
- [ ] Add appropriate comments explaining each field
- [ ] Save the file

### Task 3: Update Config Struct
- [ ] In the `Config` struct, after the existing `Github` field, add:
  ```go
  // Github is deprecated - use GithubEnterprise or GithubCloud instead
  Github githubapp.Config `yaml:"github" json:"github"`

  // GithubEnterprise configuration for GitHub Enterprise Server
  GithubEnterprise GithubAppConfig `yaml:"github_enterprise" json:"github_enterprise"`

  // GithubCloud configuration for GitHub Enterprise Cloud
  GithubCloud GithubAppConfig `yaml:"github_cloud" json:"github_cloud"`
  ```
- [ ] Keep the original `Github` field for backward compatibility
- [ ] Add deprecation comment
- [ ] Save the file

### Task 4: Implement Configuration Loading Logic
- [ ] In `/Users/dannytrevino/development/policy-bot/server/config.go`, find the `SetDefaults` method
- [ ] Add logic to handle migration:
  ```go
  // Inside SetDefaults method, after existing defaults

  // Migration: If only Github is set, copy to both new fields
  if c.Github.App.ID != 0 && c.GithubEnterprise.App.ID == 0 && c.GithubCloud.App.ID == 0 {
      log.Warn().Msg("Using deprecated 'github' config. Please migrate to 'github_enterprise' and 'github_cloud'")

      // Copy to enterprise config
      c.GithubEnterprise.Config = c.Github
      if c.GithubEnterprise.WebhookRoute == "" {
          c.GithubEnterprise.WebhookRoute = githubapp.DefaultWebhookRoute
      }

      // Copy to cloud config
      c.GithubCloud.Config = c.Github
      if c.GithubCloud.WebhookRoute == "" {
          c.GithubCloud.WebhookRoute = githubapp.DefaultWebhookRoute
      }
  }

  // Set defaults for webhook routes if not specified
  if c.GithubEnterprise.App.ID != 0 && c.GithubEnterprise.WebhookRoute == "" {
      c.GithubEnterprise.WebhookRoute = githubapp.DefaultWebhookRoute
  }
  if c.GithubCloud.App.ID != 0 && c.GithubCloud.WebhookRoute == "" {
      c.GithubCloud.WebhookRoute = githubapp.DefaultWebhookRoute
  }
  ```
- [ ] Save the file

### Task 5: Add Configuration Validation
- [ ] Create or update validation logic in config.go:
  ```go
  // ValidateConfig ensures at least one GitHub configuration is present
  func (c *Config) ValidateConfig() error {
      hasLegacy := c.Github.App.ID != 0
      hasEnterprise := c.GithubEnterprise.App.ID != 0
      hasCloud := c.GithubCloud.App.ID != 0

      if !hasLegacy && !hasEnterprise && !hasCloud {
          return errors.New("no GitHub configuration found: specify 'github_enterprise', 'github_cloud', or legacy 'github'")
      }

      // Validate enterprise config if present
      if hasEnterprise {
          if err := c.GithubEnterprise.Config.Validate(); err != nil {
              return errors.Wrap(err, "invalid github_enterprise configuration")
          }
      }

      // Validate cloud config if present
      if hasCloud {
          if err := c.GithubCloud.Config.Validate(); err != nil {
              return errors.Wrap(err, "invalid github_cloud configuration")
          }
      }

      return nil
  }
  ```
- [ ] Add call to `ValidateConfig` in the server startup if not already present
- [ ] Save the file

### Task 6: Update Server Initialization - Enterprise Components
- [ ] Open `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Find enterprise client creator initialization (around line 99)
- [ ] Update to use `c.GithubEnterprise`:
  ```go
  enterpriseClientCreator, err := githubapp.NewDefaultCachingClientCreator(
      &c.GithubEnterprise.Config,  // Changed from c.Github
      githubapp.WithClientUserAgent(fmt.Sprintf("policy-bot/%s", version.GetVersion())),
      githubapp.WithClientTimeout(30*time.Second),
      githubapp.WithClientCaching(false, func() httpcache.Cache { return redisCache }),
      githubapp.WithClientMiddleware(githubapp.ClientMetrics(registry)),
      githubapp.WithTransport(transport),
  )
  ```
- [ ] Update enterprise dispatcher initialization (around line 289) to use enterprise config:
  ```go
  enterpriseDispatcher := githubapp.NewEventDispatcher(
      enterpriseHandlers,
      c.GithubEnterprise.App.WebhookSecret,  // Changed from c.Github.App.WebhookSecret
      githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
      githubapp.WithScheduler(enterpriseScheduler),
  )
  ```
- [ ] Save the file

### Task 7: Update Server Initialization - Cloud Components
- [ ] In `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Find cloud client creator initialization (around line 119)
- [ ] Update to use `c.GithubCloud`:
  ```go
  cloudClientCreator, err := githubapp.NewDefaultCachingClientCreator(
      &c.GithubCloud.Config,  // Changed from c.Github
      githubapp.WithClientUserAgent(fmt.Sprintf("policy-bot/%s", version.GetVersion())),
      githubapp.WithClientTimeout(30*time.Second),
      githubapp.WithClientCaching(false, func() httpcache.Cache { return redisCache }),
      githubapp.WithClientMiddleware(githubapp.ClientMetrics(registry)),
      githubapp.WithTransport(transport),
  )
  ```
- [ ] Update cloud dispatcher initialization (around line 296) to use cloud config:
  ```go
  cloudDispatcher := githubapp.NewEventDispatcher(
      cloudHandlers,
      c.GithubCloud.App.WebhookSecret,  // Changed from c.Github.App.WebhookSecret
      githubapp.WithErrorCallback(githubapp.MetricsErrorCallback(base.Registry())),
      githubapp.WithScheduler(cloudScheduler),
  )
  ```
- [ ] Save the file

### Task 8: Update Middleware Call on Line 373
- [ ] In `/Users/dannytrevino/development/policy-bot/server/server.go`
- [ ] Find line 373 (the SelectIndexHandler call)
- [ ] Verify it now uses the correct config references:
  ```go
  mux.Handle(pat.Get("/"), middleware.SelectIndexHandler(
      enterpriseBasePolicyHandler,
      cloudBasePolicyHandler,
      &c.GithubEnterprise.Config,  // Now this field exists
      &c.GithubCloud.Config,       // Now this field exists
      templates,
  ))
  ```
- [ ] Save the file

### Task 9: Create Sample Configuration File
- [ ] Create a test configuration file `test-config.yml`:
  ```yaml
  # Legacy format (deprecated but still supported)
  # github:
  #   app:
  #     id: 12345
  #     webhook_secret: "old-secret"

  # New format - GitHub Enterprise Server
  github_enterprise:
    app:
      id: 12345
      webhook_secret: "enterprise-webhook-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ... key content ...
        -----END RSA PRIVATE KEY-----
    base_url: "https://github.enterprise.example.com"
    webhook_route: "/api/github/hook"
    event_routing:
      pull_request: "sqs"
      issue_comment: "http"

  # New format - GitHub Cloud
  github_cloud:
    app:
      id: 67890
      webhook_secret: "cloud-webhook-secret"
      private_key: |
        -----BEGIN RSA PRIVATE KEY-----
        ... key content ...
        -----END RSA PRIVATE KEY-----
    webhook_route: "/api/github/hook"
    event_routing:
      pull_request: "both"
      issue_comment: "http"

  # Rest of configuration remains the same
  sessions:
    key: "session-key"

  # ... other config sections
  ```
- [ ] Save the test configuration

### Task 10: Test Configuration Loading
- [ ] Run `go build ./...` to ensure compilation
- [ ] Create a test program or use existing tests to verify config loading
- [ ] Test with legacy format (only `github` field)
- [ ] Test with new format (separate enterprise/cloud)
- [ ] Test with both formats (should log deprecation warning)
- [ ] Verify validation catches missing configs

## Acceptance Criteria
- [ ] `GithubAppConfig` type is defined with inline githubapp.Config
- [ ] Config struct has both `GithubEnterprise` and `GithubCloud` fields
- [ ] Legacy `Github` field is preserved with deprecation comment
- [ ] Migration logic copies legacy config to both new fields
- [ ] Validation ensures at least one config is present
- [ ] Server uses `c.GithubEnterprise` for enterprise components
- [ ] Server uses `c.GithubCloud` for cloud components
- [ ] Line 373 references now resolve correctly
- [ ] Configuration loading handles all three scenarios:
  - Legacy only
  - New format only
  - Mixed (logs warning)
- [ ] Code compiles without errors
- [ ] Deprecation warnings are logged appropriately

## Testing Checklist
- [ ] Test with only legacy `github` config - should work with warning
- [ ] Test with only `github_enterprise` - should work
- [ ] Test with only `github_cloud` - should work
- [ ] Test with both enterprise and cloud - should work
- [ ] Test with no config - should fail validation
- [ ] Test webhook secret separation works correctly

## Notes for Next Phase
- Phase 3 will implement the actual middleware logic
- The stub SelectIndexHandler will be replaced with real implementation
- Header detection logic will use these separate configs
- Event routing maps will be utilized in Phase 3