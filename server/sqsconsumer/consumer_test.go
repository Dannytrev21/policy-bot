// Copyright 2018 Palantir Technologies, Inc.
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
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestConsumer_Disabled(t *testing.T) {
	config := &Config{
		Enabled: false,
	}

	logger := zerolog.New(nil)
	consumer, err := New(config, nil, nil, logger, nil)
	assert.NoError(t, err)

	// Should be a no-op consumer
	ctx := context.Background()

	err = consumer.Start(ctx)
	assert.NoError(t, err)

	err = consumer.Stop(ctx)
	assert.NoError(t, err)

	err = consumer.Health()
	assert.NoError(t, err)
}

func TestConsumer_EventRouting(t *testing.T) {
	tests := []struct {
		name          string
		eventRouting  map[string]string
		eventType     string
		shouldProcess bool
	}{
		{
			name:          "no routing config - should process",
			eventRouting:  nil,
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "explicit sqs routing",
			eventRouting:  map[string]string{"pull_request": "sqs"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "explicit http routing",
			eventRouting:  map[string]string{"pull_request": "http"},
			eventType:     "pull_request",
			shouldProcess: false,
		},
		{
			name:          "both routing",
			eventRouting:  map[string]string{"pull_request": "both"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
		{
			name:          "no routing for event type - default to process",
			eventRouting:  map[string]string{"status": "http"},
			eventType:     "pull_request",
			shouldProcess: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				Enabled:      true,
				Region:       "us-east-1",
				Queues:       map[string]string{tt.eventType: "https://sqs.us-east-1.amazonaws.com/123456789012/test"},
				EventRouting: tt.eventRouting,
			}

			logger := zerolog.New(nil)

			// Create a minimal consumer just to test the routing logic
			c := &consumer{
				config: config,
				logger: logger,
			}

			result := c.shouldProcessViaSQS(tt.eventType)
			assert.Equal(t, tt.shouldProcess, result)
		})
	}
}

func TestConsumer_PerQueueWorkerAllocation(t *testing.T) {
	tests := []struct {
		name            string
		config          *Config
		eventType       string
		expectedWorkers int
	}{
		{
			name: "uses queue-specific worker count",
			config: &Config{
				WorkersPerQueue: 5,
				QueueWorkers: map[string]int{
					"status":       15,
					"pull_request": 8,
				},
			},
			eventType:       "status",
			expectedWorkers: 15,
		},
		{
			name: "falls back to default when queue not specified",
			config: &Config{
				WorkersPerQueue: 7,
				QueueWorkers: map[string]int{
					"status": 15,
				},
			},
			eventType:       "pull_request",
			expectedWorkers: 7,
		},
		{
			name: "ignores zero or negative queue-specific values",
			config: &Config{
				WorkersPerQueue: 5,
				QueueWorkers: map[string]int{
					"status":       0,
					"pull_request": -1,
				},
			},
			eventType:       "status",
			expectedWorkers: 5,
		},
		{
			name: "works when QueueWorkers is nil",
			config: &Config{
				WorkersPerQueue: 6,
				QueueWorkers:    nil,
			},
			eventType:       "status",
			expectedWorkers: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config: tt.config,
			}

			result := c.getWorkersForQueue(tt.eventType)
			assert.Equal(t, tt.expectedWorkers, result)
		})
	}
}

func TestConsumer_ConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		field  string
		expect int
	}{
		{
			name:   "default workers per queue",
			config: &Config{WorkersPerQueue: 0},
			field:  "workers",
			expect: DefaultWorkersPerQueue,
		},
		{
			name:   "custom workers per queue",
			config: &Config{WorkersPerQueue: 3},
			field:  "workers",
			expect: 3,
		},
		{
			name:   "default max messages",
			config: &Config{MaxMessages: 0},
			field:  "maxMessages",
			expect: DefaultMaxMessages,
		},
		{
			name:   "invalid max messages (too high)",
			config: &Config{MaxMessages: 15},
			field:  "maxMessages",
			expect: DefaultMaxMessages,
		},
		{
			name:   "valid max messages",
			config: &Config{MaxMessages: 5},
			field:  "maxMessages",
			expect: 5,
		},
		{
			name:   "default visibility timeout",
			config: &Config{VisibilityTimeout: 0},
			field:  "visibilityTimeout",
			expect: DefaultVisibilityTimeout,
		},
		{
			name:   "default wait time",
			config: &Config{WaitTimeSeconds: -1},
			field:  "waitTime",
			expect: DefaultWaitTimeSeconds,
		},
		{
			name:   "invalid wait time (too high)",
			config: &Config{WaitTimeSeconds: 25},
			field:  "waitTime",
			expect: DefaultWaitTimeSeconds,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config: tt.config,
			}

			var result int
			switch tt.field {
			case "workers":
				result = c.getWorkersPerQueue()
			case "maxMessages":
				result = c.getMaxMessages()
			case "visibilityTimeout":
				result = c.getVisibilityTimeout()
			case "waitTime":
				result = c.getWaitTimeSeconds()
			}

			assert.Equal(t, tt.expect, result)
		})
	}
}
