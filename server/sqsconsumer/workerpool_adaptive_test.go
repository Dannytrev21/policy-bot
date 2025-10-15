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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkerPool_HasAvailableWorkers(t *testing.T) {
	tests := []struct {
		name          string
		capacity      int
		activeWorkers int64
		closed        bool
		expected      bool
	}{
		{
			name:          "pool with available workers",
			capacity:      5,
			activeWorkers: 2,
			closed:        false,
			expected:      true,
		},
		{
			name:          "saturated pool",
			capacity:      5,
			activeWorkers: 5,
			closed:        false,
			expected:      false,
		},
		{
			name:          "closed pool",
			capacity:      5,
			activeWorkers: 0,
			closed:        true,
			expected:      false,
		},
		{
			name:          "pool with one available worker",
			capacity:      10,
			activeWorkers: 9,
			closed:        false,
			expected:      true,
		},
		{
			name:          "empty pool",
			capacity:      5,
			activeWorkers: 0,
			closed:        false,
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &WorkerPool{
				eventType:     "test",
				capacity:      tt.capacity,
				activeWorkers: tt.activeWorkers,
				closed:        tt.closed,
			}

			result := pool.HasAvailableWorkers()
			assert.Equal(t, tt.expected, result, "HasAvailableWorkers should return %v", tt.expected)
		})
	}
}

func TestWorkerPool_GetAvailableCapacity(t *testing.T) {
	tests := []struct {
		name          string
		capacity      int
		activeWorkers int64
		closed        bool
		expected      int
	}{
		{
			name:          "pool with available capacity",
			capacity:      10,
			activeWorkers: 3,
			closed:        false,
			expected:      7,
		},
		{
			name:          "saturated pool",
			capacity:      5,
			activeWorkers: 5,
			closed:        false,
			expected:      0,
		},
		{
			name:          "closed pool",
			capacity:      5,
			activeWorkers: 0,
			closed:        true,
			expected:      0,
		},
		{
			name:          "empty pool",
			capacity:      10,
			activeWorkers: 0,
			closed:        false,
			expected:      10,
		},
		{
			name:          "almost saturated pool",
			capacity:      10,
			activeWorkers: 9,
			closed:        false,
			expected:      1,
		},
		{
			name:          "over-saturated pool (should not happen but handle gracefully)",
			capacity:      5,
			activeWorkers: 6,
			closed:        false,
			expected:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &WorkerPool{
				eventType:     "test",
				capacity:      tt.capacity,
				activeWorkers: tt.activeWorkers,
				closed:        tt.closed,
			}

			result := pool.GetAvailableCapacity()
			assert.Equal(t, tt.expected, result, "GetAvailableCapacity should return %d", tt.expected)
		})
	}
}

func TestWorkerPoolManager_HasAvailableWorkersForEventType(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		setup     func(*WorkerPoolManager)
		expected  bool
	}{
		{
			name:      "pool does not exist",
			eventType: "nonexistent",
			setup:     func(m *WorkerPoolManager) {},
			expected:  true, // Pool will be created with full capacity
		},
		{
			name:      "pool has available workers",
			eventType: "pull_request",
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "pull_request",
					capacity:      5,
					activeWorkers: 2,
					closed:        false,
				}
				m.pools["pull_request"] = pool
			},
			expected: true,
		},
		{
			name:      "pool is saturated",
			eventType: "status",
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "status",
					capacity:      10,
					activeWorkers: 10,
					closed:        false,
				}
				m.pools["status"] = pool
			},
			expected: false,
		},
		{
			name:      "pool is closed",
			eventType: "issue_comment",
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "issue_comment",
					capacity:      5,
					activeWorkers: 0,
					closed:        true,
				}
				m.pools["issue_comment"] = pool
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &WorkerPoolManager{
				pools: make(map[string]*WorkerPool),
			}
			tt.setup(mgr)

			result := mgr.HasAvailableWorkersForEventType(tt.eventType)
			assert.Equal(t, tt.expected, result, "HasAvailableWorkersForEventType should return %v", tt.expected)
		})
	}
}

func TestWorkerPoolManager_GetAvailableCapacityForEventType(t *testing.T) {
	tests := []struct {
		name            string
		eventType       string
		defaultCapacity int
		setup           func(*WorkerPoolManager)
		expected        int
	}{
		{
			name:            "pool does not exist",
			eventType:       "nonexistent",
			defaultCapacity: 10,
			setup:           func(m *WorkerPoolManager) {},
			expected:        10, // Returns default capacity
		},
		{
			name:            "pool has available capacity",
			eventType:       "pull_request",
			defaultCapacity: 5,
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "pull_request",
					capacity:      10,
					activeWorkers: 3,
					closed:        false,
				}
				m.pools["pull_request"] = pool
			},
			expected: 7,
		},
		{
			name:            "pool is saturated",
			eventType:       "status",
			defaultCapacity: 5,
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "status",
					capacity:      10,
					activeWorkers: 10,
					closed:        false,
				}
				m.pools["status"] = pool
			},
			expected: 0,
		},
		{
			name:            "pool is closed",
			eventType:       "issue_comment",
			defaultCapacity: 5,
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "issue_comment",
					capacity:      5,
					activeWorkers: 0,
					closed:        true,
				}
				m.pools["issue_comment"] = pool
			},
			expected: 0,
		},
		{
			name:            "pool with one available worker",
			eventType:       "check_run",
			defaultCapacity: 5,
			setup: func(m *WorkerPoolManager) {
				pool := &WorkerPool{
					eventType:     "check_run",
					capacity:      10,
					activeWorkers: 9,
					closed:        false,
				}
				m.pools["check_run"] = pool
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &WorkerPoolManager{
				pools: make(map[string]*WorkerPool),
			}
			tt.setup(mgr)

			result := mgr.GetAvailableCapacityForEventType(tt.eventType, tt.defaultCapacity)
			assert.Equal(t, tt.expected, result, "GetAvailableCapacityForEventType should return %d", tt.expected)
		})
	}
}
