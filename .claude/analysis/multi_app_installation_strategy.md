# Multi-App Installation ID Strategy Analysis

## Executive Summary

When receiving events from **other GitHub Apps**, you should **NOT** use the installation ID from the payload directly. Instead, you need a mapping or discovery mechanism to find YOUR app's installation ID for that repository.

## The Problem

When your Policy Bot receives events from other GitHub Apps:
- The `installation.id` in the payload belongs to the OTHER app, not yours
- Using that ID to create API clients will fail (wrong app credentials)
- You need YOUR app's installation ID to create valid API clients

## Current Architecture Analysis

### Current Flow (Single App)
```
GitHub Event → installation.id (yours) → Create API Client → Process Event
```

### Multi-App Flow (PROBLEM)
```
Other App Event → installation.id (theirs) → ❌ Can't create YOUR client
```

## Recommended Solutions

### Solution 1: Repository-Based Lookup (RECOMMENDED)

Use the repository information to find YOUR installation ID:

```go
// Extract repo info from payload
owner := payload.Repository.Owner.Login
repo := payload.Repository.Name

// Look up YOUR installation ID for this repo
myInstallationID, err := locator.LookupByRepository(ctx, owner, repo)
if err != nil {
    // Your app might not be installed on this repo
    return fmt.Errorf("Policy Bot not installed on %s/%s", owner, repo)
}

// Use YOUR installation ID to create clients
clients, err := manager.GetClients(ctx, myInstallationID, fmt.Sprintf("%s/%s", owner, repo))
```

### Solution 2: Installation Mapping Table

Maintain a mapping between other apps' installation IDs and yours:

```go
type InstallationMapping struct {
    TheirAppID          int64  `json:"their_app_id"`
    TheirInstallationID int64  `json:"their_installation_id"`
    OurInstallationID   int64  `json:"our_installation_id"`
    Repository          string `json:"repository"`
    LastUpdated         time.Time
}

// When receiving event from another app
theirInstallationID := extractInstallationID(payload)
ourInstallationID, err := mappingService.GetOurInstallation(theirAppID, theirInstallationID)
```

### Solution 3: App-Specific Event Source Identification

Identify the source app and handle accordingly:

```go
type EventSource struct {
    AppID          int64  `json:"app_id"`
    AppSlug        string `json:"app_slug"`
    InstallationID int64  `json:"installation_id"`
}

func (h *Handler) ProcessMultiAppEvent(payload []byte) error {
    // Identify source app
    source := identifyEventSource(payload)

    switch source.AppSlug {
    case "dependabot":
        return h.processDependabotEvent(payload)
    case "renovate":
        return h.processRenovateEvent(payload)
    case "github-actions":
        return h.processActionsEvent(payload)
    default:
        // For unknown apps, use repo lookup
        return h.processGenericAppEvent(payload)
    }
}
```

## Implementation Approach

### Step 1: Modify Installation Filter

```go
// installation_filter.go modification
func (f *InstallationFilterHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
    // Check if this is from another app
    sourceApp := extractSourceApp(payload)

    if sourceApp != nil && sourceApp.ID != f.ourAppID {
        // This is from another app - need special handling
        return f.handleCrossAppEvent(ctx, eventType, deliveryID, payload, sourceApp)
    }

    // Normal flow for our own app events
    installationID := extractInstallationID(payload)
    // ... continue as normal
}

func (f *InstallationFilterHandler) handleCrossAppEvent(
    ctx context.Context,
    eventType, deliveryID string,
    payload []byte,
    sourceApp *AppInfo,
) error {
    // Extract repository information
    repo := extractRepository(payload)
    if repo == nil {
        return fmt.Errorf("cannot process cross-app event without repository info")
    }

    // Look up OUR installation for this repository
    ourInstallation, err := f.locator.LookupByRepository(ctx, repo.Owner, repo.Name)
    if err != nil {
        return fmt.Errorf("Policy Bot not installed on %s/%s: %w",
            repo.Owner, repo.Name, err)
    }

    // Inject our installation ID into the event context
    modifiedPayload := injectOurInstallationID(payload, ourInstallation.ID)

    // Continue processing with our installation ID
    return f.Next.Handle(ctx, eventType, deliveryID, modifiedPayload)
}
```

### Step 2: Add Event Source Metadata

```go
type EventMetadata struct {
    SourceApp      *AppInfo       `json:"source_app,omitempty"`
    OriginalInstID int64          `json:"original_installation_id"`
    OurInstID      int64          `json:"our_installation_id"`
    Repository     string         `json:"repository"`
    EventTime      time.Time      `json:"event_time"`
}

// Attach metadata to context for downstream handlers
ctx = context.WithValue(ctx, "event_metadata", metadata)
```

### Step 3: Update Installation Locator

The existing `InstallationLocator` already has the right method:

```go
// This already exists and is perfect for multi-app scenarios!
func (l *InstallationLocator) Lookup(ctx context.Context, req *LookupRequest) (*LookupResult, error) {
    // Strategy 1: Direct installation ID (only use if it's OUR app)
    if req.InstallationID > 0 && req.IsOurApp {
        // ... existing code
    }

    // Strategy 2: Repository lookup (BEST for multi-app)
    if req.Owner != "" && req.Repo != "" {
        result, err := l.lookupByRepository(ctx, req.Owner, req.Repo)
        // This finds OUR installation for the repo
    }
}
```

## Configuration for Multi-App Support

```yaml
# policy-bot-config.yml
multi_app:
  enabled: true

  # How to handle events from other apps
  strategy: "repository_lookup"  # or "mapping_table"

  # Our app's ID for comparison
  our_app_id: 12345

  # Trusted source apps
  trusted_apps:
    - id: 67890
      slug: "dependabot"
      trust_level: "full"
    - id: 11111
      slug: "renovate"
      trust_level: "limited"

  # What to do if we're not installed
  on_missing_installation: "skip"  # or "error" or "install"
```

## Security Considerations

### 1. Never Trust External Installation IDs

```go
// ❌ WRONG - Security vulnerability
func processExternalEvent(payload []byte) {
    installID := extractInstallationID(payload)
    client := createClient(installID)  // Using their ID with our credentials!
}

// ✅ CORRECT
func processExternalEvent(payload []byte) {
    repo := extractRepository(payload)
    ourInstallID := findOurInstallation(repo)
    client := createClient(ourInstallID)  // Using our own ID
}
```

### 2. Validate Event Sources

```go
func validateEventSource(req *http.Request) error {
    // Check webhook signature
    signature := req.Header.Get("X-Hub-Signature-256")
    if !verifySignature(req.Body, signature, webhookSecret) {
        return errors.New("invalid webhook signature")
    }

    // Check if from trusted app
    appID := extractAppID(req.Body)
    if !isTrustedApp(appID) {
        return fmt.Errorf("untrusted app: %d", appID)
    }

    return nil
}
```

### 3. Rate Limit by Source

```go
// Different rate limits for different source apps
func getRateLimitForSource(source *AppInfo) *RateLimitConfig {
    switch source.Slug {
    case "dependabot":
        // Higher rate limit for dependabot
        return &RateLimitConfig{Rate: 10.0, Burst: 50}
    case "unknown":
        // Lower rate limit for unknown apps
        return &RateLimitConfig{Rate: 1.0, Burst: 5}
    default:
        return DefaultRateLimitConfig()
    }
}
```

## Testing Multi-App Scenarios

```go
func TestMultiAppEventHandling(t *testing.T) {
    tests := []struct {
        name           string
        sourceAppID    int64
        theirInstallID int64
        repository     string
        expectSuccess  bool
    }{
        {
            name:           "Event from Dependabot",
            sourceAppID:    67890,
            theirInstallID: 99999,
            repository:     "owner/repo",
            expectSuccess:  true,
        },
        {
            name:           "Event from unknown app",
            sourceAppID:    00000,
            theirInstallID: 88888,
            repository:     "owner/repo",
            expectSuccess:  false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Create payload with their installation ID
            payload := createTestPayload(tt.sourceAppID, tt.theirInstallID, tt.repository)

            // Process event
            err := handler.ProcessMultiAppEvent(payload)

            if tt.expectSuccess {
                assert.NoError(t, err)
                // Verify we used OUR installation ID, not theirs
                assert.NotEqual(t, tt.theirInstallID, handler.lastUsedInstallID)
            } else {
                assert.Error(t, err)
            }
        })
    }
}
```

## Monitoring & Observability

```go
// Track cross-app events
metrics.Counter("multi_app.events_received").
    WithLabels("source_app", sourceApp.Slug).
    Inc()

// Track installation lookup failures
metrics.Counter("multi_app.installation_not_found").
    WithLabels("source_app", sourceApp.Slug, "repository", repo).
    Inc()

// Log cross-app event processing
logger.Info().
    Int64("their_installation_id", theirInstallID).
    Int64("our_installation_id", ourInstallID).
    Str("source_app", sourceApp.Slug).
    Str("repository", repo).
    Msg("Processing cross-app event")
```

## Recommended Implementation Path

### Phase 1: Repository-Based Lookup (Quick Win)
1. Modify `InstallationFilterHandler` to detect external app events
2. Use repository info to lookup your installation
3. Log when processing cross-app events
4. Add metrics for monitoring

### Phase 2: Add Mapping Support (If Needed)
1. Create installation mapping table
2. Build sync mechanism to keep mappings current
3. Add caching for performance
4. Implement fallback to repository lookup

### Phase 3: Advanced Features
1. Per-app rate limiting
2. Event transformation/enrichment
3. Cross-app event correlation
4. Automated installation discovery

## Example Implementation

```go
// Updated InstallationFilterHandler
func (f *InstallationFilterHandler) Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error {
    // Parse event to determine source
    var event struct {
        Installation *struct {
            ID     int64 `json:"id"`
            AppID  int64 `json:"app_id"`
        } `json:"installation"`
        Repository *struct {
            Owner struct {
                Login string `json:"login"`
            } `json:"owner"`
            Name string `json:"name"`
        } `json:"repository"`
    }

    if err := json.Unmarshal(payload, &event); err != nil {
        return fmt.Errorf("failed to parse event: %w", err)
    }

    // Determine if this is our app or another
    var installationID int64

    if event.Installation != nil && event.Installation.AppID == f.ourAppID {
        // Our app's event - use installation ID directly
        installationID = event.Installation.ID
    } else if event.Repository != nil {
        // Another app's event - lookup our installation
        result, err := f.locator.LookupByRepository(
            ctx,
            event.Repository.Owner.Login,
            event.Repository.Name,
        )
        if err != nil {
            logger.Warn().
                Str("repository", fmt.Sprintf("%s/%s",
                    event.Repository.Owner.Login,
                    event.Repository.Name)).
                Msg("Policy Bot not installed on repository")
            return nil // Skip processing
        }
        installationID = result.InstallationID

        logger.Info().
            Int64("their_installation_id", event.Installation.ID).
            Int64("our_installation_id", installationID).
            Msg("Processing cross-app event")
    } else {
        return errors.New("unable to determine installation context")
    }

    // Continue with our installation ID
    if !f.shouldProcess(installationID) {
        return nil
    }

    return f.Next.Handle(ctx, eventType, deliveryID, payload)
}
```

## Summary

**Key Recommendation**: Use **repository-based lookup** to find YOUR app's installation ID when receiving events from other GitHub Apps. Never use the installation ID from another app's payload directly.

This approach:
- ✅ Works with your existing `InstallationLocator`
- ✅ Maintains security boundaries
- ✅ Supports multiple app scenarios
- ✅ Allows proper API client creation
- ✅ Enables cross-app event processing

The installation ID in the payload should only be used when you're certain it belongs to YOUR app.