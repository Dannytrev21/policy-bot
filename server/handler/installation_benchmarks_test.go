// Copyright 2025 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
	"github.com/rs/zerolog"
)

// BenchmarkPhase8Step7_RegistryCheck benchmarks the most common operation: checking installation status
func BenchmarkPhase8Step7_RegistryCheck(b *testing.B) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Pre-populate with some installations
	for i := int64(1); i <= 100; i++ {
		registry.MarkInstalled(i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Check a mix of existing and non-existing installations
		installationID := int64(i%150 + 1)
		registry.Check(installationID)
	}
}

// BenchmarkPhase8Step7_RegistryCheckParallel benchmarks concurrent registry checks
func BenchmarkPhase8Step7_RegistryCheckParallel(b *testing.B) {
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Pre-populate
	for i := int64(1); i <= 100; i++ {
		registry.MarkInstalled(i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			installationID := (i % 150) + 1
			registry.Check(installationID)
			i++
		}
	})
}

// BenchmarkPhase8Step7_ClientCacheLookup benchmarks client cache operations
func BenchmarkPhase8Step7_ClientCacheLookup(b *testing.B) {
	cache := NewClientCache(10*time.Minute, 1000)
	defer cache.Stop()

	// Pre-populate cache
	for i := int64(1); i <= 100; i++ {
		clients := &InstallationClients{}
		cache.Put(i, clients)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		installationID := int64(i%100 + 1)
		cache.Get(installationID)
	}
}

// BenchmarkPhase8Step7_ClientCacheLookupParallel benchmarks concurrent client cache lookups
func BenchmarkPhase8Step7_ClientCacheLookupParallel(b *testing.B) {
	cache := NewClientCache(10*time.Minute, 1000)
	defer cache.Stop()

	// Pre-populate
	for i := int64(1); i <= 100; i++ {
		clients := &InstallationClients{}
		cache.Put(i, clients)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := int64(0)
		for pb.Next() {
			installationID := (i % 100) + 1
			cache.Get(installationID)
			i++
		}
	})
}

// BenchmarkPhase8Step7_MappingCacheLookup benchmarks repository mapping cache
func BenchmarkPhase8Step7_MappingCacheLookup(b *testing.B) {
	cache := NewMappingCache(1*time.Hour, 5*time.Minute)

	// Pre-populate with repo mappings
	for i := 1; i <= 100; i++ {
		key := fmt.Sprintf("owner-%d/repo-%d", i, i)
		cache.Set(key, int64(i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		idx := (i % 100) + 1
		key := fmt.Sprintf("owner-%d/repo-%d", idx, idx)
		cache.Get(key)
	}
}

// BenchmarkPhase8Step7_FilterHandle benchmarks the complete filter handler pipeline
func BenchmarkPhase8Step7_FilterHandle(b *testing.B) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Pre-populate registry
	for i := int64(1); i <= 100; i++ {
		registry.MarkInstalled(i)
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filterConfig := &FilterConfig{
		WebhookFilteringEnabled: true,
		SQSFilteringEnabled:     true,
	}

	filter := NewInstallationFilterHandler(
		mockHandler,
		registry,
		nil,
		nil,
		nil,
		nil,
		nil,
		filterConfig,
	)

	// Create test payloads
	payloads := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		payloads[i] = createTestPayload(int64(i + 1))
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		payload := payloads[i%100]
		deliveryID := fmt.Sprintf("delivery-%d", i)
		filter.Handle(ctx, "pull_request", deliveryID, payload)
	}
}

// BenchmarkPhase8Step7_FilterHandleParallel benchmarks concurrent filter handling
func BenchmarkPhase8Step7_FilterHandleParallel(b *testing.B) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

	// Pre-populate
	for i := int64(1); i <= 100; i++ {
		registry.MarkInstalled(i)
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			return nil
		},
	}

	filterConfig := &FilterConfig{
		WebhookFilteringEnabled: true,
		SQSFilteringEnabled:     true,
	}

	filter := NewInstallationFilterHandler(
		mockHandler,
		registry,
		nil,
		nil,
		nil,
		nil,
		nil,
		filterConfig,
	)

	// Pre-create payloads to avoid JSON marshaling in benchmark
	payloads := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		payloads[i] = createTestPayload(int64(i + 1))
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			payload := payloads[i%100]
			deliveryID := fmt.Sprintf("delivery-%d", i)
			filter.Handle(ctx, "pull_request", deliveryID, payload)
			i++
		}
	})
}

// BenchmarkPhase8Step7_PayloadParsing benchmarks JSON parsing overhead
func BenchmarkPhase8Step7_PayloadParsing(b *testing.B) {
	payload := createTestPayload(12345)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		extractInstallationID(payload)
	}
}

// BenchmarkPhase8Step7_CircuitBreakerAllow benchmarks circuit breaker check operation
func BenchmarkPhase8Step7_CircuitBreakerAllow(b *testing.B) {
	cb := NewCircuitBreaker()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cb.Allow()
	}
}

// BenchmarkPhase8Step7_ThroughputSimulation simulates sustained event processing
func BenchmarkPhase8Step7_ThroughputSimulation(b *testing.B) {
	ctx := zerolog.New(nil).WithContext(context.Background())
	registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, metrics.NewRegistry())

	// Pre-populate with realistic data
	for i := int64(1); i <= 1000; i++ {
		if i%3 == 0 {
			registry.MarkNotInstalled(i)
		} else {
			registry.MarkInstalled(i)
		}
	}

	mockHandler := &MockEventHandler{
		eventTypes: []string{"pull_request", "status", "pull_request_review"},
		handlerFunc: func(ctx context.Context, eventType, deliveryID string, payload []byte) error {
			// Simulate minimal processing time
			time.Sleep(100 * time.Microsecond)
			return nil
		},
	}

	filterConfig := &FilterConfig{
		WebhookFilteringEnabled: false, // Webhook pass-through
		SQSFilteringEnabled:     true,  // SQS filtering enabled
	}

	filter := NewInstallationFilterHandler(
		mockHandler,
		registry,
		nil,
		nil,
		NewMappingCache(1*time.Hour, 5*time.Minute),
		NewMappingCache(1*time.Hour, 5*time.Minute),
		nil,
		filterConfig,
	)

	// Create mix of event types and payloads
	eventTypes := []string{"pull_request", "status", "pull_request_review"}
	payloads := make([][]byte, 1000)
	for i := 0; i < 1000; i++ {
		payloads[i] = createTestPayload(int64(i + 1))
	}

	b.ResetTimer()
	b.ReportAllocs()

	// Simulate 200 events/sec throughput target
	b.Run("Sequential", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			payload := payloads[i%1000]
			eventType := eventTypes[i%3]
			deliveryID := fmt.Sprintf("delivery-%d", i)

			filter.Handle(ctx, eventType, deliveryID, payload)
		}
	})

	b.Run("Concurrent", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				payload := payloads[i%1000]
				eventType := eventTypes[i%3]
				deliveryID := fmt.Sprintf("delivery-%d", i)

				filter.Handle(ctx, eventType, deliveryID, payload)
				i++
			}
		})
	})
}

// BenchmarkPhase8Step7_MemoryAllocation measures memory allocation patterns
func BenchmarkPhase8Step7_MemoryAllocation(b *testing.B) {
	b.Run("RegistryOperations", func(b *testing.B) {
		registry := NewInstallationRegistry(1*time.Hour, 5*time.Minute, nil)

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			id := int64(i%100 + 1)
			if i%2 == 0 {
				registry.MarkInstalled(id)
			} else {
				registry.Check(id)
			}
		}
	})

	b.Run("PayloadCreation", func(b *testing.B) {
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			data := map[string]interface{}{
				"installation": map[string]interface{}{
					"id": int64(i + 1),
				},
			}
			json.Marshal(data)
		}
	})

	b.Run("ConcurrentCacheAccess", func(b *testing.B) {
		cache := NewClientCache(10*time.Minute, 1000)
		defer cache.Stop()

		// Pre-populate
		for i := int64(1); i <= 100; i++ {
			cache.Put(i, &InstallationClients{})
		}

		b.ResetTimer()
		b.ReportAllocs()

		var wg sync.WaitGroup
		workers := 10

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for i := 0; i < b.N/workers; i++ {
					id := int64((workerID*b.N/workers+i)%100 + 1)
					cache.Get(id)
				}
			}(w)
		}

		wg.Wait()
	})
}
