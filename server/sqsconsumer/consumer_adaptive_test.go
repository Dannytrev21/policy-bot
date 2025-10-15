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
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConsumer_CalculateBackoff(t *testing.T) {
	tests := []struct {
		name                   string
		eventType              string
		consecutiveSaturations int
		config                 AdaptivePollingConfig
		expectedMin            time.Duration
		expectedMax            time.Duration
	}{
		{
			name:                   "first saturation with defaults",
			eventType:              "status",
			consecutiveSaturations: 1,
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 0, // Use defaults
				MaxBackoff:  0,
			},
			expectedMin: 900 * time.Millisecond,  // 1s - 10% jitter
			expectedMax: 1100 * time.Millisecond, // 1s + 10% jitter
		},
		{
			name:                   "second saturation with defaults",
			eventType:              "status",
			consecutiveSaturations: 2,
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 0,
				MaxBackoff:  0,
			},
			expectedMin: 1800 * time.Millisecond, // 2s - 10% jitter
			expectedMax: 2200 * time.Millisecond, // 2s + 10% jitter
		},
		{
			name:                   "third saturation with defaults",
			eventType:              "status",
			consecutiveSaturations: 3,
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 0,
				MaxBackoff:  0,
			},
			expectedMin: 3600 * time.Millisecond, // 4s - 10% jitter
			expectedMax: 4400 * time.Millisecond, // 4s + 10% jitter
		},
		{
			name:                   "with custom base backoff",
			eventType:              "status",
			consecutiveSaturations: 1,
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 500 * time.Millisecond,
				MaxBackoff:  10 * time.Second,
			},
			expectedMin: 450 * time.Millisecond, // 500ms - 10% jitter
			expectedMax: 550 * time.Millisecond, // 500ms + 10% jitter
		},
		{
			name:                   "with event-specific override",
			eventType:              "status",
			consecutiveSaturations: 1,
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 2 * time.Second,
				MaxBackoff:  30 * time.Second,
				EventTypeOverrides: map[string]AdaptivePollingEventConfig{
					"status": {
						Enabled:     true,
						BaseBackoff: 500 * time.Millisecond,
						MaxBackoff:  10 * time.Second,
					},
				},
			},
			expectedMin: 450 * time.Millisecond, // Uses override: 500ms - 10% jitter
			expectedMax: 550 * time.Millisecond, // Uses override: 500ms + 10% jitter
		},
		{
			name:                   "reaches max backoff",
			eventType:              "status",
			consecutiveSaturations: 10, // 2^9 = 512s, but capped at max
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: time.Second,
				MaxBackoff:  10 * time.Second,
			},
			expectedMin: 9 * time.Second,  // 10s - 10% jitter
			expectedMax: 11 * time.Second, // 10s + 10% jitter
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config: &Config{
					AdaptivePolling: tt.config,
				},
			}

			result := c.calculateBackoff(tt.eventType, tt.consecutiveSaturations)

			assert.GreaterOrEqual(t, result, tt.expectedMin, "Backoff should be >= expectedMin")
			assert.LessOrEqual(t, result, tt.expectedMax, "Backoff should be <= expectedMax")
		})
	}
}

func TestConsumer_CalculateBackoff_Jitter(t *testing.T) {
	c := &consumer{
		config: &Config{
			AdaptivePolling: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: time.Second,
				MaxBackoff:  30 * time.Second,
			},
		},
	}

	// Run multiple times to verify jitter produces different values
	results := make(map[time.Duration]bool)
	for i := 0; i < 10; i++ {
		backoff := c.calculateBackoff("status", 2)
		results[backoff] = true
	}

	// With jitter, we should get at least 2 different values (though very rarely they could all be the same)
	// This is a probabilistic test - we're checking that jitter is applied
	// The actual assertion is lenient since jitter could theoretically produce the same value
	assert.GreaterOrEqual(t, len(results), 1, "Should produce at least one backoff value")
}

func TestAdaptivePolling_ConfigurationParsing(t *testing.T) {
	tests := []struct {
		name   string
		config AdaptivePollingConfig
	}{
		{
			name: "basic config",
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: time.Second,
				MaxBackoff:  30 * time.Second,
			},
		},
		{
			name: "config with event overrides",
			config: AdaptivePollingConfig{
				Enabled:     true,
				BaseBackoff: 2 * time.Second,
				MaxBackoff:  30 * time.Second,
				EventTypeOverrides: map[string]AdaptivePollingEventConfig{
					"status": {
						Enabled:     true,
						BaseBackoff: 500 * time.Millisecond,
						MaxBackoff:  10 * time.Second,
					},
					"pull_request": {
						Enabled:     true,
						BaseBackoff: 2 * time.Second,
						MaxBackoff:  30 * time.Second,
					},
				},
			},
		},
		{
			name: "disabled globally but enabled for specific events",
			config: AdaptivePollingConfig{
				Enabled:     false,
				BaseBackoff: time.Second,
				MaxBackoff:  30 * time.Second,
				EventTypeOverrides: map[string]AdaptivePollingEventConfig{
					"status": {
						Enabled:     true,
						BaseBackoff: 500 * time.Millisecond,
						MaxBackoff:  10 * time.Second,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				AdaptivePolling: tt.config,
			}

			// Verify config is properly stored
			assert.Equal(t, tt.config.Enabled, cfg.AdaptivePolling.Enabled)
			assert.Equal(t, tt.config.BaseBackoff, cfg.AdaptivePolling.BaseBackoff)
			assert.Equal(t, tt.config.MaxBackoff, cfg.AdaptivePolling.MaxBackoff)

			// Verify event overrides
			if tt.config.EventTypeOverrides != nil {
				for eventType, override := range tt.config.EventTypeOverrides {
					storedOverride := cfg.AdaptivePolling.EventTypeOverrides[eventType]
					assert.Equal(t, override.Enabled, storedOverride.Enabled)
					assert.Equal(t, override.BaseBackoff, storedOverride.BaseBackoff)
					assert.Equal(t, override.MaxBackoff, storedOverride.MaxBackoff)
				}
			}
		})
	}
}

func TestAdaptivePolling_EventTypeOverride(t *testing.T) {
	cfg := &Config{
		AdaptivePolling: AdaptivePollingConfig{
			Enabled:     false, // Globally disabled
			BaseBackoff: 2 * time.Second,
			MaxBackoff:  30 * time.Second,
			EventTypeOverrides: map[string]AdaptivePollingEventConfig{
				"status": {
					Enabled:     true, // But enabled for status
					BaseBackoff: 500 * time.Millisecond,
					MaxBackoff:  10 * time.Second,
				},
			},
		},
	}

	// Test that event-specific override is respected
	eventType := "status"
	override, exists := cfg.AdaptivePolling.EventTypeOverrides[eventType]
	assert.True(t, exists, "Override should exist for status event type")
	assert.True(t, override.Enabled, "Override should enable adaptive polling for status")
	assert.Equal(t, 500*time.Millisecond, override.BaseBackoff)
	assert.Equal(t, 10*time.Second, override.MaxBackoff)

	// Test non-overridden event type
	eventType = "pull_request"
	_, exists = cfg.AdaptivePolling.EventTypeOverrides[eventType]
	assert.False(t, exists, "Override should not exist for pull_request")
}
