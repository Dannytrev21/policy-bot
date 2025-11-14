# Organization-Level Installation Strategy for Multi-App Events

## Executive Summary

When your Policy Bot is installed at the **organization level** and receives events from other GitHub Apps, you need to map the organization (not repository) to your installation ID. One organization = one installation ID for your app.

## Key Insight: Organization-Level Installations

With org-level installations:
- **One installation ID** covers ALL repositories in the organization
- Repository-specific lookups aren't needed (all repos use same installation)
- The organization identifier is the key to finding your installation

## Recommended Solution: Organization-Based Lookup

### Primary Approach: Extract Organization from Event

```go
func (h *Handler) handleMultiAppEvent(ctx context.Context, payload []byte) error {
    // Parse the event to extract organization info
    var event struct {
        Installation *struct {
            ID      int64 `json:"id"`      // Their installation ID
            AppID   int64 `json:"app_id"`   // Their app ID
            Account struct {
                Login string `json:"login"`  // Organization name
                ID    int64  `json:"id"`     // Organization ID
                Type  string `json:"type"`   // "Organization" or "User"
            } `json:"account"`
        } `json:"installation"`
        Organization *struct {
            Login string `json:"login"`      // Organization name
            ID    int64  `json:"id"`         // Organization ID
        } `json:"organization"`
        Repository *struct {
            Owner struct {
                Login string `json:"login"`  // Also the org name
                ID    int64  `json:"id"`     // Also the org ID
                Type  string `json:"type"`   // "Organization"
            } `json:"owner"`
            Name string `json:"name"`
        } `json:"repository"`
    }

    if err := json.Unmarshal(payload, &event); err != nil {
        return err
    }

    // Extract organization identifier (multiple sources possible)
    var orgLogin string
    var orgID int64

    // Priority 1: Direct organization field
    if event.Organization != nil {
        orgLogin = event.Organization.Login
        orgID = event.Organization.ID
    }
    // Priority 2: Repository owner (for org-owned repos)
    else if event.Repository != nil && event.Repository.Owner.Type == "Organization" {
        orgLogin = event.Repository.Owner.Login
        orgID = event.Repository.Owner.ID
    }
    // Priority 3: Installation account (for installation events)
    else if event.Installation != nil && event.Installation.Account.Type == "Organization" {
        orgLogin = event.Installation.Account.Login
        orgID = event.Installation.Account.ID
    }

    if orgLogin == "" {
        return fmt.Errorf("cannot determine organization from event")
    }

    // Look up YOUR installation for this organization
    ourInstallationID, err := h.lookupOrgInstallation(ctx, orgLogin, orgID)
    if err != nil {
        return fmt.Errorf("Policy Bot not installed on org %s: %w", orgLogin, err)
    }

    // Use YOUR org-level installation ID
    clients, err := h.manager.GetClients(ctx, ourInstallationID, orgLogin)
    if err != nil {
        return err
    }

    // Process the event with your installation's clients
    return h.processWithClients(ctx, clients, payload)
}
```

## Implementation Strategy

### Step 1: Create Organization → Installation Mapping

```go
// OrgInstallationCache maps organizations to your installation IDs
type OrgInstallationCache struct {
    mu    sync.RWMutex
    cache map[string]int64  // org_login -> installation_id
    ttl   time.Duration
}

func (c *OrgInstallationCache) GetInstallation(orgLogin string) (int64, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    installID, exists := c.cache[orgLogin]
    return installID, exists
}

func (c *OrgInstallationCache) SetInstallation(orgLogin string, installationID int64) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.cache[orgLogin] = installationID
}
```

### Step 2: Build Organization Mapping on Startup

```go
func (h *Handler) buildOrgInstallationMapping(ctx context.Context) error {
    // List all installations for your app
    installations, err := h.listAppInstallations(ctx)
    if err != nil {
        return err
    }

    for _, installation := range installations {
        // For org-level installations
        if installation.Account.Type == "Organization" {
            h.orgCache.SetInstallation(
                installation.Account.Login,
                installation.ID,
            )

            logger.Info().
                Str("org", installation.Account.Login).
                Int64("installation_id", installation.ID).
                Msg("Mapped organization to installation")
        }
    }

    return nil
}
```

### Step 3: Update InstallationLocator for Org Lookups

```go
func (l *InstallationLocator) LookupByOrganization(ctx context.Context, orgLogin string) (*LookupResult, error) {
    // Check cache first
    if installID, exists := l.orgCache.GetInstallation(orgLogin); exists {
        return &LookupResult{
            InstallationID: installID,
            Exists:        true,
            Source:        "org_cache",
        }, nil
    }

    // Fall back to GitHub API
    installation, err := l.findOrgInstallation(ctx, orgLogin)
    if err != nil {
        return nil, err
    }

    // Cache for future use
    l.orgCache.SetInstallation(orgLogin, installation.ID)

    return &LookupResult{
        InstallationID: installation.ID,
        Exists:        true,
        Source:        "github_api",
    }, nil
}
```

## Simplified Multi-App Handler

```go
func (h *Handler) ProcessCrossAppEvent(ctx context.Context, payload []byte) error {
    // Extract org from event (simplified)
    org := extractOrganization(payload)
    if org == "" {
        return errors.New("no organization in event")
    }

    // Get YOUR org-level installation
    result, err := h.locator.LookupByOrganization(ctx, org)
    if err != nil {
        // Your app is not installed on this org
        logger.Warn().
            Str("org", org).
            Msg("Policy Bot not installed on organization")
        return nil // Skip processing
    }

    // Create clients with YOUR installation ID
    clients, err := h.manager.GetClients(ctx, result.InstallationID, org)
    if err != nil {
        return fmt.Errorf("failed to create clients: %w", err)
    }

    // Process the event
    return h.processEvent(ctx, clients, payload)
}
```

## Configuration for Org-Level Multi-App

```yaml
# policy-bot-config.yml
installation:
  type: "organization"  # Not "repository"

multi_app:
  enabled: true
  strategy: "org_lookup"  # Use org-based lookup

  # Cache settings for org mappings
  org_cache:
    ttl: 1h
    refresh_interval: 15m

  # How to identify organizations in events
  org_extraction:
    priority:
      - "organization.login"        # Direct org field
      - "repository.owner.login"    # Repo owner
      - "installation.account.login" # Installation account
```

## Handling Edge Cases

### 1. Mixed Installation Types

If you have both org-level and repo-level installations:

```go
func (h *Handler) determineInstallationType(ctx context.Context, payload []byte) (string, error) {
    org := extractOrganization(payload)
    repo := extractRepository(payload)

    // Try org-level first
    if orgInstall, err := h.lookupOrgInstallation(ctx, org); err == nil {
        return "organization", nil
    }

    // Fall back to repo-level
    if repoInstall, err := h.lookupRepoInstallation(ctx, org, repo); err == nil {
        return "repository", nil
    }

    return "", errors.New("no installation found")
}
```

### 2. User-Level Installations

For personal accounts (not organizations):

```go
if event.Installation.Account.Type == "User" {
    // Handle user-level installation
    userLogin := event.Installation.Account.Login
    ourInstallID, err := h.lookupUserInstallation(ctx, userLogin)
}
```

### 3. Cross-Organization Events

If an event involves multiple organizations:

```go
// Example: User from Org A opens PR in Org B's repo
sourceOrg := event.Sender.Organization
targetOrg := event.Repository.Owner.Login

// You need the installation for the target org (where the repo is)
ourInstallID, err := h.lookupOrgInstallation(ctx, targetOrg)
```

## Performance Optimizations

### 1. Pre-warm Organization Cache

```go
func (h *Handler) Start(ctx context.Context) error {
    // Build org mapping on startup
    if err := h.buildOrgInstallationMapping(ctx); err != nil {
        return err
    }

    // Refresh periodically
    go h.refreshOrgMappings(ctx, 15*time.Minute)

    return nil
}
```

### 2. Lazy Loading with Negative Caching

```go
type OrgInstallationResult struct {
    InstallationID int64
    Exists        bool
    CachedAt      time.Time
}

// Cache both positive and negative results
func (c *OrgCache) Set(org string, result OrgInstallationResult) {
    // Cache "not installed" to avoid repeated API calls
    c.cache[org] = result
}
```

## Security Considerations

### 1. Validate Organization Access

```go
func validateOrgAccess(ourInstallID int64, theirInstallID int64, org string) error {
    if ourInstallID == 0 {
        return fmt.Errorf("we don't have access to org %s", org)
    }

    // Log cross-app access for audit
    logger.Info().
        Int64("our_installation", ourInstallID).
        Int64("their_installation", theirInstallID).
        Str("org", org).
        Msg("Processing cross-app event")

    return nil
}
```

### 2. Rate Limit by Organization

```go
func (r *RateLimiter) GetOrgLimiter(org string) *rate.Limiter {
    // Different rate limits per org
    if limiter, exists := r.orgLimiters[org]; exists {
        return limiter
    }

    // Create new limiter for this org
    limiter := rate.NewLimiter(
        rate.Limit(r.config.OrgRate),
        r.config.OrgBurst,
    )
    r.orgLimiters[org] = limiter
    return limiter
}
```

## Testing Strategy

```go
func TestOrgLevelMultiAppEvent(t *testing.T) {
    // Create event from another app for an org-level installation
    payload := createOrgEventFromApp(
        appID:     12345,        // Another app
        installID: 99999,        // Their installation
        org:       "my-org",     // Target org
        repo:      "my-repo",    // Repo in that org
    )

    // Process the event
    err := handler.ProcessCrossAppEvent(ctx, payload)
    require.NoError(t, err)

    // Verify we used OUR org installation ID
    assert.Equal(t, ourOrgInstallID, handler.lastUsedInstallID)
    assert.NotEqual(t, 99999, handler.lastUsedInstallID)
}
```

## Migration Path

### Phase 1: Add Organization Mapping
1. Create `OrgInstallationCache`
2. Build mapping on startup
3. Add org-based lookup methods

### Phase 2: Update Event Processing
1. Modify `InstallationFilterHandler` to detect cross-app events
2. Use org lookup for external app events
3. Keep existing logic for your own app events

### Phase 3: Monitor and Optimize
1. Track cache hit rates
2. Monitor cross-app event volumes
3. Tune cache TTLs based on usage patterns

## Key Advantages of Org-Level Approach

1. **Simpler**: One installation per org (not per repo)
2. **Faster**: Fewer cache entries to manage
3. **Consistent**: Same installation ID for all repos in org
4. **Efficient**: Single API call to get org installation

## Example: Complete Implementation

```go
// InstallationFilterHandler modification for org-level
func (f *InstallationFilterHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
    // Detect if this is from another app
    appID := extractAppID(payload)

    var installationID int64

    if appID == f.ourAppID {
        // Our app's event - use installation ID directly
        installationID = extractInstallationID(payload)
    } else {
        // Another app's event - use org lookup
        org := extractOrganization(payload)
        if org == "" {
            return errors.New("cannot determine organization")
        }

        result, err := f.locator.LookupByOrganization(ctx, org)
        if err != nil {
            // We're not installed on this org
            return nil // Skip
        }

        installationID = result.InstallationID

        logger.Info().
            Str("org", org).
            Int64("their_app_id", appID).
            Int64("our_installation_id", installationID).
            Msg("Processing cross-app event for org")
    }

    // Continue with our installation ID
    return f.processWithInstallation(ctx, installationID, eventType, payload)
}
```

## Summary

For **organization-level installations**:
1. Map organization name/ID to your installation ID
2. Extract organization from incoming events
3. Use org mapping to find your installation
4. Process event with your org-level installation ID

This is much simpler than repository-level lookups and works perfectly with GitHub's org-level app installation model.