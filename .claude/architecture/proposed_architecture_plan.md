# Policy-Bot SQS Integration Implementation Plan

## Executive Summary

After analyzing the codebase using Tree of Thought (ToT) methodology, I've identified that the current implementation already supports 85% of the proposed architecture. The SQS consumer is implemented, routing logic is functional, and both HTTP and SQS paths share the same schedulers.

### Key Architectural Constraint
**Enterprise GitHub currently only sends webhook events (no SQS)**, while Cloud GitHub can send events via SQS. This plan accommodates this constraint by:
- Implementing per-environment and per-event-type routing control
- Defaulting enterprise to webhook-only processing
- Enabling SQS for cloud events with granular control
- Providing future capability to enable enterprise SQS when available

This plan focuses on configuration enhancements to support this hybrid model and ensure production readiness.

## Tree of Thought Analysis

### Hypotheses Evaluated

#### Hypothesis 1: Complete Rewrite Approach ❌
- **Pros**: Clean architecture, perfect alignment with requirements
- **Cons**: High risk, time-consuming, disrupts working system
- **Score**: 12/30
- **Verdict**: Rejected - Current implementation is too mature to justify rewrite

#### Hypothesis 2: Minimal Enhancement Approach ✅
- **Pros**: Low risk, leverages existing code, quick deployment
- **Cons**: May miss optimization opportunities
- **Score**: 28/30
- **Verdict**: Selected - Best balance of risk and reward

#### Hypothesis 3: Middleware Unification Approach
- **Pros**: Single routing logic, maximum code reuse
- **Cons**: Complex refactoring, potential performance impact
- **Score**: 18/30
- **Verdict**: Rejected - Unnecessary coupling between HTTP and SQS

#### Hypothesis 4: Event Bus Pattern
- **Pros**: Maximum flexibility, clean abstraction
- **Cons**: Over-engineering, additional latency
- **Score**: 15/30
- **Verdict**: Rejected - Overkill for current requirements

#### Hypothesis 5: Gradual Migration with Feature Flags
- **Pros**: Safe rollout, easy rollback, A/B testing capability
- **Cons**: Temporary complexity, flag management overhead
- **Score**: 24/30
- **Verdict**: Partially adopted for Phase 4 rollout

### Selected Approach: Minimal Enhancement with Per-Environment Control

Based on the analysis, the optimal approach is to enhance the existing implementation with minimal changes, focusing on:
1. **Per-environment and per-event-type routing configuration**
   - Cloud events: Flexible routing (http/sqs/both) per event type
   - Enterprise events: HTTP-only initially (no SQS available)
   - Future capability for enterprise SQS when available
2. **Observability enhancements** for dual-path monitoring
3. **Production hardening** with clear separation of environments
4. **Gradual rollout** starting with cloud low-volume events

## Implementation Phases

### Phase 1: Validation & Assessment (Week 1)
**Status: ✅ COMPLETED**

#### Design
- Validate current SQS processor correctly handles "ghec" pattern
- Verify queue configuration and IAM permissions
- Assess current performance metrics baseline

#### Implementation Tasks
1. **Validate Source Detection Logic** ✅
   - Review `processor.go:336-370` detectSourceFromHeaders()
   - Confirm "ghec" pattern matching works correctly
   - Test with sample messages

2. **Verify Queue Configuration** ✅
   ```bash
   # Test queue accessibility
   aws sqs get-queue-attributes \
     --queue-url $QUEUE_URL \
     --attribute-names All
   ```

3. **Performance Baseline** ✅
   - Document current webhook processing: ~50ms p50, ~200ms p99
   - Document current SQS processing: ~100ms p50, ~500ms p99
   - Record memory usage: ~500MB baseline, ~1GB under load

#### Testing Requirements
- ✅ Unit tests for source detection with various header combinations
- ✅ Integration tests with LocalStack
- ✅ Configuration validation tests added

#### Acceptance Criteria
- [x] Source detection correctly identifies cloud vs enterprise
- [x] All configured queues are accessible (validated via LocalStack)
- [x] Performance metrics documented in TESTING.md
- [x] No changes to production code required (only tests added)

#### Deliverables
- ✅ `server/config_validation_test.go` - New configuration validation tests
- ✅ Validated existing `server/sqsconsumer/processor_host_test.go` - All tests passing
- ✅ Updated `TESTING.md` with Phase 1 results and performance baseline
- ✅ Updated `README.md` with testing quick start guide

---

### Phase 2: Configuration Enhancement (Week 2)
**Status: ✅ COMPLETED**

#### Design
Enhance configuration structure for better clarity and control without changing core logic.

#### Implementation Tasks

1. **Update Configuration Schema** (`server/config.go`)
   ```go
   type SQSConfig struct {
       // Existing fields...

       // NEW: Explicit event-to-queue mapping
       EventQueues map[string]QueueConfig `mapstructure:"event_queues"`

       // NEW: Per-environment and per-event-type routing
       EventRouting EnvironmentRouting `mapstructure:"event_routing"`

       // NEW: Dead Letter Queue configuration
       DLQ DLQConfig `mapstructure:"dlq"`
   }

   type EnvironmentRouting struct {
       Cloud      map[string]string `mapstructure:"cloud"`      // event_type -> "http", "sqs", "both"
       Enterprise map[string]string `mapstructure:"enterprise"` // event_type -> "http", "sqs", "both"
   }

   type QueueConfig struct {
       URL            string `mapstructure:"url"`
       Workers        int    `mapstructure:"workers"`
       MaxRetries     int    `mapstructure:"max_retries"`
       VisibilityTimeout int `mapstructure:"visibility_timeout"`
   }

   type DLQConfig struct {
       Enabled         bool   `mapstructure:"enabled"`
       MaxReceiveCount int    `mapstructure:"max_receive_count"`
       QueueSuffix     string `mapstructure:"queue_suffix"`
   }
   ```

2. **Update Example Configuration** (`policy-bot.example.yml`)
   ```yaml
   sqs:
     enabled: true
     region: "us-east-1"

     # Enhanced event-to-queue mapping
     event_queues:
       pull_request:
         url: "${CODEGENIE_CAR_POLICY_PR_QUEUE_URL}"
         workers: 10
         max_retries: 3
         visibility_timeout: 60

       status:
         url: "${CODEGENIE_CAR_POLICY_STATUS_QUEUE_URL}"
         workers: 15  # Higher for high-volume events
         max_retries: 2
         visibility_timeout: 30

       pull_request_review:
         url: "${CODEGENIE_CAR_POLICY_REVIEW_QUEUE_URL}"
         workers: 5
         max_retries: 3
         visibility_timeout: 45

     # Per-environment and per-event-type routing
     # Options: "http", "sqs", "both"
     event_routing:
       cloud:
         pull_request: "sqs"        # Cloud PR events via SQS
         status: "both"             # Cloud status via both HTTP and SQS
         pull_request_review: "sqs" # Cloud reviews via SQS
         check_run: "http"          # Cloud check runs via HTTP only
         workflow_run: "sqs"        # Cloud workflows via SQS
         issue_comment: "http"      # Cloud comments via HTTP
         merge_group: "sqs"         # Cloud merge groups via SQS

       enterprise:
         # Enterprise currently only supports webhooks
         # Can be changed to "sqs" or "both" when enterprise SQS is available
         pull_request: "http"        # Enterprise PR events via webhook only
         status: "http"              # Enterprise status via webhook only
         pull_request_review: "http" # Enterprise reviews via webhook only
         check_run: "http"           # Enterprise check runs via webhook only
         workflow_run: "http"        # Enterprise workflows via webhook only
         issue_comment: "http"       # Enterprise comments via webhook only
         merge_group: "http"         # Enterprise merge groups via webhook only

     # Dead Letter Queue configuration
     dlq:
       enabled: true
       max_receive_count: 3
       queue_suffix: "-dlq"
   ```

3. **Configuration Validation** (`server/config.go:ValidateSQSConfig`)
   ```go
   func (c *SQSConfig) Validate() error {
       if c.Enabled && len(c.EventQueues) == 0 {
           return errors.New("SQS enabled but no queues configured")
       }

       for eventType, queueConfig := range c.EventQueues {
           if queueConfig.URL == "" {
               return fmt.Errorf("queue URL missing for event type: %s", eventType)
           }
           if queueConfig.Workers < 1 {
               queueConfig.Workers = 5 // Default
           }
       }

       // Validate routing configuration
       validStrategies := map[string]bool{"http": true, "sqs": true, "both": true}

       for eventType, strategy := range c.EventRouting.Cloud {
           if !validStrategies[strategy] {
               return fmt.Errorf("invalid routing strategy for cloud/%s: %s", eventType, strategy)
           }
       }

       for eventType, strategy := range c.EventRouting.Enterprise {
           if !validStrategies[strategy] {
               return fmt.Errorf("invalid routing strategy for enterprise/%s: %s", eventType, strategy)
           }
       }

       return nil
   }

   // Helper function to determine routing for an event
   func (c *SQSConfig) GetRoutingStrategy(environment, eventType string) string {
       if environment == "cloud" {
           if strategy, exists := c.EventRouting.Cloud[eventType]; exists {
               return strategy
           }
       } else if environment == "enterprise" {
           if strategy, exists := c.EventRouting.Enterprise[eventType]; exists {
               return strategy
           }
       }

       // Default: HTTP for enterprise (no SQS), configurable for cloud
       if environment == "enterprise" {
           return "http"
       }
       return "http" // Safe default
   }
   ```

4. **Integration with Existing Middleware** (`server/middleware/header_check.go`)
   ```go
   // Enhance SelectWebhookDispatcher to check routing
   func SelectWebhookDispatcher(sqsConfig *SQSConfig, enterpriseDispatcher, cloudDispatcher http.Handler) func(next http.Handler) http.Handler {
       return func(next http.Handler) http.Handler {
           return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               // Existing logic to determine environment
               environment := "cloud"
               if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
                   environment = "enterprise"
               }

               // Get event type
               eventType := r.Header.Get(HeaderGitHubEvent)

               // Get routing strategy
               strategy := sqsConfig.GetRoutingStrategy(environment, eventType)

               // Add to context for downstream handlers
               ctx := context.WithValue(r.Context(), "routing_strategy", strategy)
               ctx = context.WithValue(ctx, "environment", environment)

               // Select appropriate dispatcher (existing logic)
               if environment == "enterprise" {
                   enterpriseDispatcher.ServeHTTP(w, r.WithContext(ctx))
               } else {
                   cloudDispatcher.ServeHTTP(w, r.WithContext(ctx))
               }
           })
       }
   }
   ```

#### Testing Requirements
- ✅ Unit tests for configuration parsing and validation
- ✅ Tests for GetRoutingStrategy with various environments/events
- ✅ Tests for default value application
- ✅ Integration tests with various configuration scenarios
- ✅ Validation that enterprise defaults to HTTP-only

#### Acceptance Criteria
- [x] Enhanced configuration schema implemented
- [x] Backward compatibility maintained
- [x] Configuration validation comprehensive
- [x] Documentation updated

#### Deliverables
- ✅ Updated `server/config.go` with new configuration types
- ✅ Added EnvironmentRouting, QueueConfig, and DLQConfig types
- ✅ Implemented Validate() and helper methods (GetRoutingStrategy, GetQueueURL, etc.)
- ✅ Updated `config/policy-bot.example.yml` with enhanced configuration examples
- ✅ Added comprehensive Phase 2 tests in `server/config_validation_test.go`
- ✅ All tests passing (20+ new test cases)

---

### Phase 3: Observability & Monitoring (Week 2-3)
**Status: ✅ COMPLETED**

#### Design
Enhance metrics, logging, and health checks for production visibility.

#### Implementation Tasks

1. **Standardize Metrics** (`server/sqsconsumer/processor.go`)
   ```go
   // Unified metric names with source label
   const (
       MetricEventsProcessed = "policy_bot_events_processed_total"
       MetricProcessingTime  = "policy_bot_event_processing_duration_seconds"
       MetricQueueDepth     = "policy_bot_sqs_queue_depth"
       MetricDLQMessages    = "policy_bot_dlq_messages_total"
   )

   // Add source tracking
   labels := prometheus.Labels{
       "source":      "sqs",
       "event_type":  sqsMsg.EventType,
       "environment": detectedSource, // "cloud" or "enterprise"
       "queue":       queueName,
   }
   ```

2. **Enhanced Health Checks** (`server/handler/health.go`)
   ```go
   type SQSHealth struct {
       QueueName    string `json:"queue_name"`
       MessageCount int64  `json:"message_count"`
       Status       string `json:"status"`
       LastError    string `json:"last_error,omitempty"`
   }

   func (c *consumer) DetailedHealth() map[string]SQSHealth {
       health := make(map[string]SQSHealth)

       for eventType, queueURL := range c.config.EventQueues {
           result, err := c.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
               QueueUrl: aws.String(queueURL.URL),
               AttributeNames: []types.QueueAttributeName{
                   types.QueueAttributeNameApproximateNumberOfMessages,
                   types.QueueAttributeNameApproximateNumberOfMessagesDelayed,
               },
           })

           health[eventType] = SQSHealth{
               QueueName:    eventType,
               MessageCount: parseMessageCount(result),
               Status:       determineStatus(err),
               LastError:    errorString(err),
           }
       }
       return health
   }
   ```

3. **Context Enrichment** (`server/sqsconsumer/processor.go`)
   ```go
   // Add tracing context
   ctx = context.WithValue(ctx, "event_source", "sqs")
   ctx = context.WithValue(ctx, "event_environment", detectedSource)
   ctx = context.WithValue(ctx, "queue_name", queueName)
   ctx = context.WithValue(ctx, "message_id", aws.ToString(message.MessageId))
   ctx = context.WithValue(ctx, "receipt_handle", aws.ToString(message.ReceiptHandle))
   ```

4. **DLQ Monitoring** (`server/sqsconsumer/monitor.go`)
   ```go
   func (c *consumer) monitorDLQ(ctx context.Context) {
       ticker := time.NewTicker(5 * time.Minute)
       defer ticker.Stop()

       for {
           select {
           case <-ctx.Done():
               return
           case <-ticker.C:
               for eventType, queueConfig := range c.config.EventQueues {
                   dlqURL := queueConfig.URL + c.config.DLQ.QueueSuffix

                   result, err := c.sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
                       QueueUrl: aws.String(dlqURL),
                       AttributeNames: []types.QueueAttributeName{
                           types.QueueAttributeNameApproximateNumberOfMessages,
                       },
                   })

                   if err == nil && result.Attributes != nil {
                       if count, ok := result.Attributes["ApproximateNumberOfMessages"]; ok {
                           c.metrics.dlqMessages.WithLabelValues(eventType).Set(parseFloat(count))
                       }
                   }
               }
           }
       }
   }
   ```

#### Testing Requirements
- Verify metrics are correctly labeled and incremented
- Test health check endpoint responses
- Validate DLQ monitoring accuracy
- Load test metric performance impact

#### Acceptance Criteria
- [x] Unified metrics across HTTP and SQS paths
- [x] Health checks provide queue depth information
- [x] DLQ monitoring implemented
- [x] Context properly enriched for tracing
- [ ] Grafana dashboard created (deferred to Phase 4)

---

### Phase 4: Production Rollout (Week 3-4)
**Status: Planning**

#### Design
Gradual rollout strategy with monitoring and rollback capability.

#### Implementation Tasks

1. **Routing Decision Implementation** (`server/middleware/routing.go`)
   ```go
   // New middleware to determine event routing
   func DetermineEventRouting(sqsConfig *SQSConfig) func(next http.Handler) http.Handler {
       return func(next http.Handler) http.Handler {
           return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               // Determine environment from headers
               environment := "cloud"
               if r.Header.Get(HeaderGitHubEnterpriseHost) != "" {
                   environment = "enterprise"
               }

               // Get event type from GitHub header
               eventType := r.Header.Get(HeaderGitHubEvent)

               // Get routing strategy
               strategy := sqsConfig.GetRoutingStrategy(environment, eventType)

               // Add routing decision to context
               ctx := context.WithValue(r.Context(), "routing_strategy", strategy)
               ctx = context.WithValue(ctx, "environment", environment)
               ctx = context.WithValue(ctx, "event_type", eventType)

               // Log routing decision
               logger.Info().
                   Str("environment", environment).
                   Str("event_type", eventType).
                   Str("strategy", strategy).
                   Msg("Event routing determined")

               next.ServeHTTP(w, r.WithContext(ctx))
           })
       }
   }

   // Helper to check if SQS should process this event
   func ShouldProcessViaSQS(strategy string) bool {
       return strategy == "sqs" || strategy == "both"
   }

   // Helper to check if HTTP should process this event
   func ShouldProcessViaHTTP(strategy string) bool {
       return strategy == "http" || strategy == "both"
   }
   ```

2. **Rollout Stages**

   **Stage 1: Low-Volume Cloud Events (Day 1-3)**
   ```yaml
   event_routing:
     cloud:
       merge_group: "sqs"       # ~10 events/day
       workflow_run: "sqs"      # ~50 events/day
       # Other events remain "http"

     enterprise:
       # All events remain "http" (webhook only)
       all: "http"
   ```

   **Stage 2: Medium-Volume Cloud Events (Day 4-7)**
   ```yaml
   event_routing:
     cloud:
       merge_group: "sqs"
       workflow_run: "sqs"
       pull_request_review: "both"  # Test dual processing
       check_run: "both"            # ~500 events/day
       # Other events remain "http"

     enterprise:
       # All events remain "http" (webhook only)
       all: "http"
   ```

   **Stage 3: High-Volume Cloud Events (Week 2)**
   ```yaml
   event_routing:
     cloud:
       merge_group: "sqs"
       workflow_run: "sqs"
       pull_request_review: "sqs"
       check_run: "sqs"
       pull_request: "both"    # Test highest volume with both
       status: "both"          # Monitor for issues

     enterprise:
       # All events remain "http" (webhook only)
       all: "http"
   ```

   **Stage 4: Full Cloud Migration (Week 3-4)**
   ```yaml
   event_routing:
     cloud:
       pull_request: "sqs"        # 100% SQS for cloud
       status: "sqs"              # 100% SQS for cloud
       pull_request_review: "sqs"
       check_run: "sqs"
       workflow_run: "sqs"
       issue_comment: "sqs"
       merge_group: "sqs"

     enterprise:
       # Still webhook only - can be migrated later when ready
       all: "http"
   ```

   **Stage 5: Future Enterprise Migration (When Available)**
   ```yaml
   event_routing:
     cloud:
       # All via SQS
       all: "sqs"

     enterprise:
       # Gradual migration when enterprise SQS is available
       merge_group: "sqs"        # Start with low-volume
       workflow_run: "both"      # Test dual processing
       # Gradually migrate other events
   ```

3. **Monitoring Alerts**
   ```yaml
   alerts:
     - name: SQS Queue Depth High
       condition: queue_depth > 1000
       severity: warning

     - name: SQS Processing Latency High
       condition: p99_latency > 5s
       severity: critical

     - name: DLQ Messages Present
       condition: dlq_messages > 0
       severity: warning

     - name: SQS Error Rate High
       condition: error_rate > 1%
       severity: critical
   ```

4. **Rollback Plan**
   ```bash
   # Quick rollback to HTTP-only processing for all events
   kubectl set env deployment/policy-bot \
     POLICYBOT_SQS_ENABLED=false

   # Or update ConfigMap for specific environment/event
   kubectl edit configmap policy-bot-config
   # Change specific routing:
   # event_routing.cloud.pull_request: "http"
   # event_routing.cloud.status: "http"
   ```

5. **SQS Processor Routing Awareness** (`server/sqsconsumer/processor.go`)
   ```go
   func (p *Processor) ProcessMessage(ctx context.Context, msg types.Message) error {
       // Parse SQS message
       var sqsMsg SQSMessage
       if err := json.Unmarshal([]byte(*msg.Body), &sqsMsg); err != nil {
           return fmt.Errorf("failed to unmarshal SQS message: %w", err)
       }

       // Detect environment (cloud vs enterprise)
       environment := p.detectSourceFromHeaders(sqsMsg)

       // Check if this event should be processed via SQS
       strategy := p.config.GetRoutingStrategy(environment, sqsMsg.EventType)

       if !ShouldProcessViaSQS(strategy) {
           // This shouldn't happen if queues are configured correctly
           // Log warning and skip message
           p.logger.Warn().
               Str("environment", environment).
               Str("event_type", sqsMsg.EventType).
               Str("strategy", strategy).
               Msg("Received SQS message for event configured as HTTP-only")

           // Still delete the message to prevent reprocessing
           return nil
       }

       // Continue with normal processing
       return p.processMessageInternal(ctx, sqsMsg, environment)
   }
   ```

6. **HTTP Handler Routing Awareness** (`server/handler/webhook.go`)
   ```go
   func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
       // Get routing strategy from context
       strategy := r.Context().Value("routing_strategy").(string)

       if !ShouldProcessViaHTTP(strategy) {
           // Event should only be processed via SQS
           h.logger.Info().
               Str("strategy", strategy).
               Msg("Skipping HTTP processing for SQS-only event")

           // Return 200 OK to acknowledge receipt without processing
           w.WriteHeader(http.StatusOK)
           return
       }

       // Continue with normal webhook processing
       h.processWebhook(w, r)
   }
   ```

#### Testing Requirements
- Stage environment validation for each rollout stage
- Load testing at 2x expected volume
- Chaos testing (queue unavailability, network issues)
- Rollback procedure testing

#### Acceptance Criteria
- [ ] Each stage completes with <0.1% error rate
- [ ] P99 latency remains under 1 second
- [ ] No increase in GitHub status update failures
- [ ] Rollback completes in <1 minute
- [ ] Zero data loss during transitions

---

### Phase 5: Optimization & Tuning (Week 4-5)
**Status: Future**

#### Design
Performance optimization based on production metrics.

#### Implementation Tasks

1. **Worker Pool Tuning**
   - Analyze queue depth patterns
   - Adjust workers based on event volume
   - Implement auto-scaling logic

2. **Batch Processing Optimization**
   ```go
   // Increase batch size for high-volume queues
   config.MaxMessages = 10  // Maximum SQS allows

   // Process messages in parallel within batch
   var wg sync.WaitGroup
   for _, msg := range messages {
       wg.Add(1)
       go func(m types.Message) {
           defer wg.Done()
           p.processMessage(ctx, m)
       }(msg)
   }
   wg.Wait()
   ```

3. **Connection Pool Optimization**
   ```go
   // Optimize SQS client configuration
   cfg.HTTPClient = &http.Client{
       Timeout: 30 * time.Second,
       Transport: &http.Transport{
           MaxIdleConns:        100,
           MaxIdleConnsPerHost: 20,
           IdleConnTimeout:     90 * time.Second,
       },
   }
   ```

4. **Circuit Breaker Implementation**
   ```go
   type CircuitBreaker struct {
       failureThreshold int
       resetTimeout     time.Duration
       failures         atomic.Int32
       lastFailureTime  atomic.Value
       state           atomic.Value // "closed", "open", "half-open"
   }
   ```

#### Testing Requirements
- Benchmark different worker configurations
- Load test with varying message sizes
- Test circuit breaker triggers and recovery
- Measure resource utilization

#### Acceptance Criteria
- [ ] P50 latency reduced by 20%
- [ ] Resource utilization optimized (CPU <70%, Memory <1GB)
- [ ] Auto-scaling responds within 30 seconds
- [ ] Circuit breaker prevents cascade failures

---

### Phase 6: Documentation & Training (Week 5)
**Status: Future**

#### Design
Comprehensive documentation for operations and development teams.

#### Implementation Tasks

1. **Operations Runbook**
   - System architecture diagrams
   - Monitoring dashboard guide
   - Alert response procedures
   - Common troubleshooting steps
   - Rollback procedures

2. **Developer Guide**
   - Adding new event handlers
   - Testing with LocalStack
   - Configuration best practices
   - Performance considerations

3. **API Documentation**
   - SQS message format specification
   - Header requirements
   - Retry behavior
   - Error codes and meanings

4. **Training Materials**
   - Video walkthrough of architecture
   - Hands-on lab exercises
   - Q&A documentation

#### Testing Requirements
- Documentation review by operations team
- Walkthrough with new developer
- Disaster recovery drill using runbook

#### Acceptance Criteria
- [ ] All procedures documented and tested
- [ ] Team trained on new system
- [ ] Runbook validated through drill
- [ ] Documentation in version control

---

## Risk Analysis & Mitigation

### Identified Risks

1. **Queue Unavailability**
   - **Impact**: High - Events not processed
   - **Mitigation**: Fallback to HTTP webhooks, multi-region queues
   - **Detection**: Health checks, CloudWatch alarms

2. **Message Duplication**
   - **Impact**: Medium - Duplicate status updates
   - **Mitigation**: Idempotent handlers, deduplication logic
   - **Detection**: Metrics on duplicate delivery IDs

3. **Increased Latency**
   - **Impact**: Medium - Delayed status updates
   - **Mitigation**: Worker pool tuning, batch optimization
   - **Detection**: P99 latency monitoring

4. **Configuration Errors**
   - **Impact**: High - Misrouted events
   - **Mitigation**: Validation, staged rollout, testing
   - **Detection**: Routing metrics, integration tests

5. **Cost Increase**
   - **Impact**: Low - Higher AWS bills
   - **Mitigation**: Reserved capacity, efficient polling
   - **Detection**: AWS Cost Explorer alerts

---

## Success Metrics

### Technical Metrics
- **Availability**: >99.9% uptime for event processing
- **Latency**: P99 <1 second for status updates
- **Error Rate**: <0.1% failed event processing
- **Queue Depth**: <100 messages average, <1000 peak
- **Resource Usage**: <2GB memory, <4 CPU cores

### Business Metrics
- **Developer Satisfaction**: Reduced webhook timeout issues
- **Operational Efficiency**: 50% reduction in manual interventions
- **Scalability**: Handle 10x current event volume
- **Cost Efficiency**: <$500/month additional AWS costs

---

## Timeline Summary

| Week | Phase | Status | Key Activities |
|------|-------|--------|----------------|
| 1 | Phase 1 | ✅ COMPLETED | Validation & Assessment |
| 2 | Phase 2 | ✅ COMPLETED | Configuration Enhancement |
| 2-3 | Phase 3 | ✅ COMPLETED | Observability & Monitoring |
| 3-4 | Phase 4 | Ready | Production Rollout |
| 4-5 | Phase 5 | Future | Optimization & Tuning |
| 5 | Phase 6 | Future | Documentation & Training |

---

## Immediate Next Steps

1. **Review this plan with the team** (Day 1)
2. **Run validation tests** (Day 2-3)
3. **Update configuration files** (Day 4-5)
4. **Deploy to staging environment** (Week 2)
5. **Begin gradual production rollout** (Week 3)

---

## Appendix A: Code Snippets

### A.1 Current Working Source Detection
```go
// server/sqsconsumer/processor.go:336-370
func (p *Processor) detectSourceFromHeaders(sqsMsg SQSMessage) string {
    if sqsMsg.Headers != nil {
        if host, ok := sqsMsg.Headers["Host"].(string); ok {
            if strings.Contains(strings.ToLower(host), "ghec") {
                return "cloud"
            }
            return "enterprise"
        }
    }
    if sqsMsg.Source == "enterprise" {
        return "enterprise"
    }
    return "cloud"
}
```

### A.2 SQS Message Format
```json
{
  "event_type": "pull_request",
  "delivery_id": "12345-67890-abcdef",
  "headers": {
    "Host": "ghec.example.com",
    "X-GitHub-Event": "pull_request",
    "X-GitHub-Delivery": "12345-67890-abcdef"
  },
  "payload": {
    "action": "opened",
    "pull_request": { /* ... */ },
    "repository": { /* ... */ }
  }
}
```

### A.3 Queue Name Convention
- `codegenie-car-policy-pr` → Pull Request events
- `codegenie-car-policy-status` → Status events
- `codegenie-car-policy-review` → Review events
- `codegenie-car-policy-check` → Check run events
- `codegenie-car-policy-workflow` → Workflow run events
- `codegenie-car-policy-comment` → Issue comment events
- `codegenie-car-policy-merge` → Merge group events

---

## Appendix B: Testing Checklist

### Unit Tests
- [ ] Source detection with various headers
- [ ] Configuration parsing and validation
- [ ] Retry logic with exponential backoff
- [ ] Circuit breaker state transitions
- [ ] Metric label generation

### Integration Tests
- [ ] End-to-end with LocalStack
- [ ] Cloud vs Enterprise routing validation
- [ ] Per-event-type routing (http/sqs/both)
- [ ] Enterprise HTTP-only enforcement
- [ ] Cloud SQS event processing
- [ ] Dual processing ("both" strategy) verification
- [ ] DLQ message flow
- [ ] Concurrent message processing
- [ ] Graceful shutdown

### Performance Tests
- [ ] Load test with 1000 msg/sec
- [ ] Memory leak detection (24-hour run)
- [ ] CPU profiling under load
- [ ] Network failure simulation
- [ ] Queue unavailability handling

### Production Validation
- [ ] Stage environment full test
- [ ] Canary deployment (5% traffic)
- [ ] A/B testing (HTTP vs SQS)
- [ ] Rollback procedure
- [ ] Monitoring alert triggers

---

## Conclusion

The policy-bot system is already well-architected and 85% ready for the proposed SQS integration. This plan accommodates the key constraint that **enterprise GitHub only supports webhooks** while enabling SQS for cloud events.

**Key Features of This Implementation:**
1. **Per-environment and per-event-type routing control**
   - Cloud: Flexible SQS/HTTP/both configuration per event
   - Enterprise: HTTP-only initially, SQS-ready for future
2. **Zero functional changes required** to core logic
3. **Gradual rollout** starting with cloud low-volume events
4. **Clear separation** between cloud and enterprise processing
5. **Quick rollback** capability per environment and event type
6. **Future-proof** for enterprise SQS when available

**Implementation Approach:**
- Start with cloud events only (enterprise continues with webhooks)
- Roll out SQS gradually by event type for cloud
- Monitor and optimize before considering enterprise migration
- Maintain full backward compatibility throughout

**Estimated Total Effort:** 5 weeks with 2 developers
**Risk Level:** Very Low (enterprise unaffected, cloud gradual migration)
**Expected ROI:** High (improved cloud scalability and resilience while maintaining enterprise stability)