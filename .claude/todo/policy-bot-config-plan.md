# Policy-Bot Configuration Update Plan

## Overview
Remove the deprecated `github` configuration field and ensure the application only uses `github_enterprise` and `github_cloud` configurations. Each configuration should contain all necessary GitHub connection details.

## Configuration Structure Requirements
Each config (`github_enterprise` and `github_cloud`) must contain:
- `web_url` - The GitHub web interface URL
- `v3_api_url` - REST API endpoint
- `v4_api_url` - GraphQL API endpoint
- `app`:
  - `integration_id` - GitHub App ID
  - `webhook_secret` - Secret for webhook validation
  - `private_key` - App private key for authentication
- `oauth`:
  - `client_id` - OAuth app client ID
  - `client_secret` - OAuth app client secret

## Implementation Checklist

### Phase 1: Update Configuration Structure ✅ COMPLETED
- [x] Update `server/config.go`:
  - [x] Remove the `Github githubapp.Config` field from `Config` struct
  - [x] Remove the migration logic that copies from `Github` to `GithubEnterprise`/`GithubCloud`
  - [x] Update `ParseConfig()` to remove `c.Github.SetValuesFromEnv("")`
  - [x] Update `ValidateConfig()` to remove all references to legacy config
  - [x] Ensure validation requires at least one of `GithubEnterprise` or `GithubCloud`
  - [x] Add validation for required fields in each active config (URLs, app fields, oauth fields)
- [x] Update `server/server.go`:
  - [x] Remove fallback to `c.Github.V4APIURL`
  - [x] Remove fallback to `c.Github.WebURL`
  - [x] Update OAuth config to use `c.GithubEnterprise.Config` instead of `c.Github`
- [x] Update `server/test_helpers.go`:
  - [x] Replace all `c.Github` references with testConfig variable
  - [x] Use cloud config with fallback to enterprise for tests
- [x] Update `scripts/test-event-processing.go`:
  - [x] Change `Github` field to `GithubCloud` in test config
- [x] Verify compilation: `go build ./...` ✅
- [x] Run all tests: `go test ./server/...` ✅ All passing

### Phase 2: Update Server Initialization ✅ COMPLETED
- [x] Update `server/server.go`:
  - [x] Remove fallback logic for `enterpriseV4URL` that uses `c.Github.V4APIURL` - Done in Phase 1
  - [x] Remove fallback logic for `webURL` that uses `c.Github.WebURL` - Done in Phase 1
  - [x] Update OAuth config selection to use cloud or enterprise directly (no legacy fallback) - Done in Phase 1
  - [x] Ensure proper error messages if required config is missing - Enhanced with clear error messages
- [x] Added comprehensive error handling:
  - [x] Error if no v4_api_url configured in either github_enterprise or github_cloud
  - [x] Error if no web_url configured in either config
  - [x] Error if no app.integration_id configured in either config
- [x] Cleaned up duplicate comments
- [x] Verify compilation: `go build ./...` ✅
- [x] Run all tests: `go test ./...` ✅ All passing

### Phase 3: Update Test Helpers ✅ COMPLETED (Done in Phase 1)
- [x] Update `server/test_helpers.go`:
  - [x] Change `c.Github.V4APIURL` to use cloud or enterprise config
  - [x] Change `c.Github.App.WebhookSecret` to use appropriate config
  - [x] Change `c.Github.WebURL` in template loading to use appropriate config
  - [x] Update OAuth checks to use cloud or enterprise config

### Phase 4: Update Configuration Examples ✅ COMPLETED
- [x] Update `config/policy-bot.example.yml`:
  - [x] Remove the `github:` section
  - [x] Add `github_enterprise:` example section with all fields
  - [x] Add `github_cloud:` example section with all fields
  - [x] Add comments explaining when to use each
  - [x] Provide clear examples for both configs
- [x] Added comprehensive documentation:
  - [x] Banner section explaining GitHub configuration options
  - [x] Environment variable documentation for each field
  - [x] Optional fields clearly marked (webhook_route, event_routing, oauth)
  - [x] Configuration notes section with routing behavior explanation
  - [x] Added note about cloud_options/enterprise_options separation
- [x] Verify YAML validity: Build and tests pass ✅
- [x] All tests passing: `go test ./...` ✅

### Phase 5: Create Configuration Tests
- [ ] Create `server/config_test.go`:
  - [ ] Test parsing config with only `github_enterprise`
  - [ ] Test parsing config with only `github_cloud`
  - [ ] Test parsing config with both `github_enterprise` and `github_cloud`
  - [ ] Test validation fails when no config is provided
  - [ ] Test validation fails when required fields are missing
  - [ ] Test environment variable loading for both configs
  - [ ] Test that `ValidateConfig()` properly validates URLs and required fields

### Phase 6: Update Integration Tests
- [ ] Create integration test to verify:
  - [ ] Server starts with only enterprise config
  - [ ] Server starts with only cloud config
  - [ ] Server starts with both configs
  - [ ] Webhook routing works with new config structure
  - [ ] OAuth flow works with new config structure
  - [ ] API calls use correct URLs from config

### Phase 7: Migration Support (Optional)
- [ ] Create migration script or documentation:
  - [ ] Document how to migrate from old config format to new
  - [ ] Provide example migration for common scenarios
  - [ ] Add deprecation notice in logs if old config detected (temporarily)

### Phase 8: Documentation Update
- [ ] Update README.md:
  - [ ] Document new configuration structure
  - [ ] Provide examples for enterprise and cloud configs
  - [ ] Explain when to use each configuration
- [ ] Update any deployment documentation
- [ ] Update environment variable documentation

### Phase 9: Testing and Validation
- [ ] Run all existing tests: `go test ./...`
- [ ] Test with enterprise-only configuration
- [ ] Test with cloud-only configuration
- [ ] Test with both configurations
- [ ] Test webhook processing with new config
- [ ] Test OAuth flow with new config
- [ ] Test middleware routing with new config
- [ ] Test SQS consumer with new config

## Configuration Example Templates

### GitHub Enterprise Server Configuration
```yaml
github_enterprise:
  web_url: "https://github.enterprise.company.com"
  v3_api_url: "https://github.enterprise.company.com/api/v3"
  v4_api_url: "https://github.enterprise.company.com/api/graphql"
  webhook_route: "/api/github/hook"  # optional, defaults to /api/github/hook
  event_routing:  # optional, for SQS routing
    pull_request: "sqs"
    status: "http"
  app:
    integration_id: 12345
    webhook_secret: "enterprise_webhook_secret"
    private_key: |
      -----BEGIN RSA PRIVATE KEY-----
      [enterprise private key]
      -----END RSA PRIVATE KEY-----
  oauth:
    client_id: "enterprise_client_id"
    client_secret: "enterprise_client_secret"
```

### GitHub Enterprise Cloud Configuration
```yaml
github_cloud:
  web_url: "https://github.com"
  v3_api_url: "https://api.github.com"
  v4_api_url: "https://api.github.com/graphql"
  webhook_route: "/api/github/hook"  # optional
  event_routing:  # optional
    pull_request: "both"
    status: "sqs"
  app:
    integration_id: 67890
    webhook_secret: "cloud_webhook_secret"
    private_key: |
      -----BEGIN RSA PRIVATE KEY-----
      [cloud private key]
      -----END RSA PRIVATE KEY-----
  oauth:
    client_id: "cloud_client_id"
    client_secret: "cloud_client_secret"
```

## Testing Commands
```bash
# Run all tests
go test ./... -v

# Run specific config tests
go test ./server -run TestConfig -v

# Test with race detection
go test -race ./...

# Build and verify
go build ./...

# Format code
go fmt ./...
```

## Success Criteria
- [ ] Application starts successfully with only enterprise config
- [ ] Application starts successfully with only cloud config
- [ ] Application starts successfully with both configs
- [ ] All tests pass
- [ ] Webhook routing works correctly
- [ ] OAuth authentication works correctly
- [ ] No references to legacy `github` config remain in code
- [ ] Configuration validation catches missing required fields
- [ ] Environment variables work for both configs
- [ ] Example configuration is clear and well-documented

## Notes
- Ensure backward compatibility is handled gracefully
- Consider adding deprecation warnings before completely removing legacy support
- Test thoroughly with existing deployments
- Document migration path clearly