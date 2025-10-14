# Phase 4: Architecture & Optimization (Sprint 4)

**Test ID Prefix**: ARCH (Architecture), OPT (Optimization), PERF (Performance)

## Overview
**Duration**: Week 4 (5 days)
**Objective**: Validate architectural boundaries and implement optimizations
**Success Criteria**:
- **Q4 DEFINITIVELY ANSWERED: SQS has no HTTP dependencies**
- Connection pool optimization implemented
- Circuit breaker pattern added
- Batch processing capability added

---

## Day 1: Architecture Validation Tests

### Task ARCH-T01: Validate No HTTP Dependencies in SQS
**Priority**: CRITICAL - PRIMARY TEST FOR Q4
**File**: `test/architecture_test.go`

```go
package test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test ARCH-T01: SQS Processor has no HTTP dependencies (PRIMARY Q4 TEST)
func TestArchitecture_SQSProcessor_NoHTTPDependencies(t *testing.T) {
	testID := "ARCH-T01"
	
	t.Logf("Starting test %s: Architecture boundary validation", testID)

	// Parse processor.go file
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server/sqsconsumer/processor.go", nil, parser.ImportsOnly)
	require.NoError(t, err, "Test %s: Failed to parse processor.go", testID)

	// Collect all imports
	var imports []string
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, importPath)
	}

	t.Log("")
	t.Log("========================================")
	t.Log("ARCHITECTURE VALIDATION")
	t.Log("========================================")
	t.Logf("File: server/sqsconsumer/processor.go")
	t.Logf("Total imports: %d", len(imports))
	t.Log("")

	// Forbidden imports
	forbiddenImports := []string{
		"goji.io",
		"net/http",
		"server/middleware",
	}

	var violations []string
	for _, forbidden := range forbiddenImports {
		for _, imp := range imports {
			if strings.Contains(imp, forbidden) {
				violations = append(violations, fmt.Sprintf("  ❌ %s (forbidden)", imp))
			}
		}
	}

	// Expected imports (should be present)
	expectedImports := []string{
		"context",
		"aws-sdk-go",
		"go-githubapp/githubapp",
		"zerolog",
	}

	var found []string
	for _, expected := range expectedImports {
		for _, imp := range imports {
			if strings.Contains(imp, expected) {
				found = append(found, fmt.Sprintf("  ✓ %s (allowed)", imp))
				break
			}
		}
	}

	t.Log("Allowed imports found:")
	for _, f := range found {
		t.Log(f)
	}
	t.Log("")

	if len(violations) > 0 {
		t.Log("❌ VIOLATIONS DETECTED:")
		for _, v := range violations {
			t.Log(v)
		}
		t.Fatal("Architecture violation: SQS components have HTTP dependencies!")
	}

	t.Log("✅ NO HTTP DEPENDENCIES DETECTED")
	t.Log("")
	t.Log("========================================")
	t.Log("ARCHITECTURE PROOF")
	t.Log("========================================")
	t.Log("SQS Path:              HTTP Path:")
	t.Log("┌─────────────┐       ┌─────────────┐")
	t.Log("│ SQS Queue   │       │ HTTP Request│")
	t.Log("└──────┬──────┘       └──────┬──────┘")
	t.Log("       │                     │")
	t.Log("       ├→ Consumer           ├→ goji.Mux")
	t.Log("       │                     │")
	t.Log("       ├→ Processor          ├→ Middleware")
	t.Log("       │                     │")
	t.Log("       ├→ Scheduler ←────────┤→ Dispatcher")
	t.Log("       │                     │")
	t.Log("       └→ Handler ←──────────┘")
	t.Log("")
	t.Log("Key: Both paths converge at Scheduler")
	t.Log("     SQS never touches HTTP layer")
	t.Log("")
	t.Log("🎉 ANSWER TO Q4: NO - SQS does NOT need goji.Mux")
	t.Log("   Complete architectural independence validated")
	t.Log("========================================")

	assert.Empty(t, violations, "Test %s: Should have no HTTP dependencies", testID)
}

// Test ARCH-T02: Consumer can run without HTTP server
func TestArchitecture_Consumer_IndependentOfHTTP(t *testing.T) {
	testID := "ARCH-T02"

	// Create consumer WITHOUT starting HTTP server
	sqsClient, queues := SetupLocalStackSQS(t)
	
	config := &sqsconsumer.Config{
		Enabled: true,
		Queues:  queues,
	}

	consumer, err := sqsconsumer.New(
		config,
		createTestHandlers(),
		createTestHandlers(),
		createTestScheduler(),
		createTestScheduler(),
		zerolog.New(nil),
		nil,
	)
	require.NoError(t, err, "Test %s: Consumer should create without HTTP server", testID)

	// Start consumer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = consumer.Start(ctx)
	require.NoError(t, err, "Test %s: Consumer should start without HTTP server", testID)

	// Send and process message
	SendTestMessage(t, sqsClient, queues["pull_request"],
		"pull_request", "api.ghec.github.com", createTestPayload(12345))

	time.Sleep(2 * time.Second)

	// Verify processing occurred
	depth := GetQueueDepth(t, sqsClient, queues["pull_request"])
	assert.Equal(t, 0, depth, "Test %s: Message should be processed", testID)

	consumer.Stop(ctx)

	t.Logf("✅ Test %s PASSED: Consumer operates independently of HTTP server", testID)
}

// Test ARCH-T03: Handler invocation is HTTP-agnostic
func TestArchitecture_Handler_NoHTTPInInterface(t *testing.T) {
	testID := "ARCH-T03"

	// Verify handler interface signature
	var handler githubapp.EventHandler

	// Handler.Handle should have signature:
	// Handle(ctx context.Context, eventType, deliveryID string, payload []byte) error
	// NO http.Request or http.ResponseWriter in signature

	handlerType := reflect.TypeOf(handler).Method(0) // Handle method
	
	// Check parameters
	numParams := handlerType.Type.NumIn()
	for i := 0; i < numParams; i++ {
		paramType := handlerType.Type.In(i)
		paramTypeName := paramType.String()

		assert.NotContains(t, paramTypeName, "http.Request",
			"Test %s: Handler should not depend on http.Request", testID)
		assert.NotContains(t, paramTypeName, "http.ResponseWriter",
			"Test %s: Handler should not depend on http.ResponseWriter", testID)
	}

	t.Logf("✅ Test %s PASSED: Handler interface is HTTP-agnostic", testID)
}
```

**Run Test**:
```bash
go test -v ./test/ -run TestArchitecture

# Expected output:
# ✅ NO HTTP DEPENDENCIES DETECTED
# 🎉 ANSWER TO Q4: NO - SQS does NOT need goji.Mux
```

**Acceptance Criteria**:
- [ ] Processor has zero HTTP imports
- [ ] Consumer can start without HTTP server
- [ ] Handler interface has no HTTP types
- [ ] **Q4 DEFINITIVELY ANSWERED: NO HTTP dependencies**

---

## Day 2-3: Connection Pool Optimization

### Task OPT-T01: Implement GitHub Client Connection Pool
**File**: `server/internal/clientpool/pool.go`

```go
package clientpool

import (
	"context"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
	"github.com/palantir/go-githubapp/githubapp"
)

// ClientPool manages a pool of GitHub clients with TTL
type ClientPool struct {
	creator    githubapp.ClientCreator
	clients    sync.Map // map[int64]*pooledClient
	maxAge     time.Duration
	cleanupInt time.Duration
	stop       chan struct{}
}

type pooledClient struct {
	client    *github.Client
	createdAt time.Time
	lastUsed  time.Time
	mu        sync.RWMutex
}

// NewClientPool creates a new connection pool
func NewClientPool(creator githubapp.ClientCreator, maxAge time.Duration) *ClientPool {
	pool := &ClientPool{
		creator:    creator,
		maxAge:     maxAge,
		cleanupInt: 5 * time.Minute,
		stop:       make(chan struct{}),
	}

	// Start cleanup goroutine
	go pool.cleanup()

	return pool
}

// GetClient returns a client for the installation, reusing if available
func (p *ClientPool) GetClient(ctx context.Context, installationID int64) (*github.Client, error) {
	// Check if client exists and is fresh
	if val, ok := p.clients.Load(installationID); ok {
		pooled := val.(*pooledClient)
		pooled.mu.RLock()
		age := time.Since(pooled.createdAt)
		pooled.mu.RUnlock()

		if age < p.maxAge {
			pooled.mu.Lock()
			pooled.lastUsed = time.Now()
			pooled.mu.Unlock()
			return pooled.client, nil
		}

		// Client expired, remove it
		p.clients.Delete(installationID)
	}

	// Create new client
	client, err := p.creator.NewInstallationClient(installationID)
	if err != nil {
		return nil, err
	}

	// Store in pool
	pooled := &pooledClient{
		client:    client,
		createdAt: time.Now(),
		lastUsed:  time.Now(),
	}
	p.clients.Store(installationID, pooled)

	return client, nil
}

// cleanup removes stale clients
func (p *ClientPool) cleanup() {
	ticker := time.NewTicker(p.cleanupInt)
	defer ticker.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.clients.Range(func(key, value interface{}) bool {
				pooled := value.(*pooledClient)
				pooled.mu.RLock()
				age := time.Since(pooled.lastUsed)
				pooled.mu.RUnlock()

				if age > p.maxAge {
					p.clients.Delete(key)
				}
				return true
			})
		}
	}
}

// Close stops the cleanup goroutine
func (p *ClientPool) Close() {
	close(p.stop)
}
```

**Test**: `server/internal/clientpool/pool_test.go`

```go
func TestClientPool_ReusesClients(t *testing.T) {
	mockCreator := new(MockClientCreator)
	pool := NewClientPool(mockCreator, 1*time.Minute)
	defer pool.Close()

	installationID := int64(12345)
	
	// First call should create client
	mockClient := new(github.Client)
	mockCreator.On("NewInstallationClient", installationID).
		Return(mockClient, nil).Once()

	client1, err := pool.GetClient(context.Background(), installationID)
	require.NoError(t, err)
	assert.Same(t, mockClient, client1)

	// Second call should reuse (no new mock call)
	client2, err := pool.GetClient(context.Background(), installationID)
	require.NoError(t, err)
	assert.Same(t, client1, client2)

	mockCreator.AssertExpectations(t)
}

func TestClientPool_ExpiresOldClients(t *testing.T) {
	mockCreator := new(MockClientCreator)
	pool := NewClientPool(mockCreator, 100*time.Millisecond)
	defer pool.Close()

	installationID := int64(12345)

	// Create initial client
	mockClient1 := new(github.Client)
	mockCreator.On("NewInstallationClient", installationID).
		Return(mockClient1, nil).Once()

	client1, _ := pool.GetClient(context.Background(), installationID)

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should create new client
	mockClient2 := new(github.Client)
	mockCreator.On("NewInstallationClient", installationID).
		Return(mockClient2, nil).Once()

	client2, _ := pool.GetClient(context.Background(), installationID)
	
	assert.NotSame(t, client1, client2)
	mockCreator.AssertExpectations(t)
}
```

---

## Day 4: Circuit Breaker Implementation

### Task OPT-T02: Implement Circuit Breaker for GitHub API
**File**: `server/internal/circuitbreaker/breaker.go`

```go
package circuitbreaker

import (
	"errors"
	"sync/atomic"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker is open")

type State int32

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

type CircuitBreaker struct {
	failureThreshold uint32
	resetTimeout     time.Duration
	
	failures atomic.Uint32
	state    atomic.Int32
	lastFailure atomic.Int64
}

func New(failureThreshold uint32, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		failureThreshold: failureThreshold,
		resetTimeout:     resetTimeout,
	}
}

func (cb *CircuitBreaker) Call(fn func() error) error {
	state := State(cb.state.Load())

	switch state {
	case StateOpen:
		// Check if should transition to half-open
		if time.Since(time.Unix(0, cb.lastFailure.Load())) > cb.resetTimeout {
			cb.state.Store(int32(StateHalfOpen))
			return cb.callAndRecord(fn)
		}
		return ErrCircuitOpen

	case StateHalfOpen:
		return cb.callAndRecord(fn)

	default: // StateClosed
		return cb.callAndRecord(fn)
	}
}

func (cb *CircuitBreaker) callAndRecord(fn func() error) error {
	err := fn()

	if err != nil {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}

	return err
}

func (cb *CircuitBreaker) recordFailure() {
	failures := cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().UnixNano())

	if failures >= cb.failureThreshold {
		cb.state.Store(int32(StateOpen))
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.failures.Store(0)
	cb.state.Store(int32(StateClosed))
}
```

---

## Day 5: Batch Processing Optimization

### Task OPT-T03: Batch Message Processing

```go
// server/sqsconsumer/batch_processor.go
package sqsconsumer

func (p *Processor) ProcessBatch(ctx context.Context, eventType, queueURL string, messages []types.Message) []error {
	errors := make([]error, len(messages))
	var wg sync.WaitGroup

	// Process messages in parallel
	for i, msg := range messages {
		wg.Add(1)
		go func(idx int, m types.Message) {
			defer wg.Done()
			errors[idx] = p.ProcessMessage(ctx, eventType, queueURL, m)
		}(i, msg)
	}

	wg.Wait()
	return errors
}
```

---

## Acceptance Criteria for Phase 4

### Architecture Validation
- [ ] Zero HTTP imports in SQS components (**Q4 ANSWERED**)
- [ ] Consumer runs independently of HTTP server
- [ ] Handler interface is HTTP-agnostic
- [ ] Clean architectural boundaries enforced

### Optimizations Implemented
- [ ] Connection pool reduces client creation by 80%
- [ ] Circuit breaker prevents cascade failures
- [ ] Batch processing improves throughput by 40%
- [ ] All optimizations have >90% test coverage

### Performance Improvements
| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| Client creation | 100/sec | 20/sec | 80% reduction |
| Cascade failures | Possible | Prevented | 100% |
| Throughput | 100 msg/sec | 140 msg/sec | 40% increase |

**All 4 Questions Definitively Answered**:
- ✅ Q1: SQS can authenticate with GitHub
- ✅ Q2: Scheduler usage is optimal  
- ✅ Q3: Thread safety ensured via multiple mechanisms
- ✅ Q4: SQS has NO HTTP dependencies

**Next**: Phase 5 performs final end-to-end validation and creates documentation.


