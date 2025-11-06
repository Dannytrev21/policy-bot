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

package sqsconsumer

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// BenchmarkMessagePooling benchmarks the sync.Pool implementation
func BenchmarkMessagePooling(b *testing.B) {
	testBody := `{"event_type":"pull_request","delivery_id":"test-123","payload":{"action":"opened"}}`

	b.Run("WithPool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			msg := getSQSMessageFromPool()
			_ = json.Unmarshal([]byte(testBody), msg)
			returnSQSMessageToPool(msg)
		}
	})

	b.Run("WithoutPool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			msg := &SQSMessage{}
			_ = json.Unmarshal([]byte(testBody), msg)
		}
	})
}

// BenchmarkParseMessage benchmarks message parsing with the pool
func BenchmarkParseMessage(b *testing.B) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "StructuredMessage",
			body: `{"event_type":"pull_request","delivery_id":"test-123","payload":{"action":"opened"}}`,
		},
		{
			name: "WebhookWithHeaders",
			body: `{"headers":{"Host":"github.com"},"action":"opened","pull_request":{}}`,
		},
		{
			name: "RawPayload",
			body: `{"action":"opened","pull_request":{"id":123}}`,
		},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			processor := &Processor{}
			message := types.Message{
				Body:      aws.String(tc.body),
				MessageId: aws.String("test-id"),
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				msg := getSQSMessageFromPool()
				_ = processor.parseMessage("pull_request", message, msg)
				returnSQSMessageToPool(msg)
			}
		})
	}
}

// BenchmarkMapAllocation benchmarks pre-allocated vs non-pre-allocated maps
func BenchmarkMapAllocation(b *testing.B) {
	handlers := make([]string, 10)
	for i := range handlers {
		handlers[i] = "event_type_" + string(rune(i))
	}

	b.Run("PreAllocated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m := make(map[string]int, len(handlers))
			for idx, h := range handlers {
				m[h] = idx
			}
		}
	})

	b.Run("NotPreAllocated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m := make(map[string]int)
			for idx, h := range handlers {
				m[h] = idx
			}
		}
	})
}

// BenchmarkSQSMessageStructSize measures the size impact of the struct
func BenchmarkSQSMessageStructSize(b *testing.B) {
	b.Run("CreateAndDiscard", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = &SQSMessage{
				EventType:  "pull_request",
				DeliveryID: "test-123",
				Payload:    json.RawMessage(`{"action":"opened"}`),
			}
		}
	})

	b.Run("CreateFromPool", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			msg := getSQSMessageFromPool()
			msg.EventType = "pull_request"
			msg.DeliveryID = "test-123"
			msg.Payload = json.RawMessage(`{"action":"opened"}`)
			returnSQSMessageToPool(msg)
		}
	})
}

// BenchmarkConcurrentPoolAccess benchmarks concurrent pool access
func BenchmarkConcurrentPoolAccess(b *testing.B) {
	b.Run("Sequential", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			msg := getSQSMessageFromPool()
			msg.EventType = "test"
			returnSQSMessageToPool(msg)
		}
	})

	b.Run("Parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				msg := getSQSMessageFromPool()
				msg.EventType = "test"
				returnSQSMessageToPool(msg)
			}
		})
	})
}
