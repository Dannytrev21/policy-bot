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

// TestConfig_BuildQueueMap tests building queue map for different environments
func TestConfig_BuildQueueMap(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		environment string
		region      string
		want        map[string]string
	}{
		{
			name: "cloud environment with east region",
			config: &Config{
				Region: "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
						WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/pr",
						EventRouting:  "sqs",
						GHECEnabled:   true,
						GHESEnabled:   false,
					},
					"status": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/status",
						WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/status",
						EventRouting:  "both",
						GHECEnabled:   true,
						GHESEnabled:   true,
					},
				},
			},
			environment: "cloud",
			want: map[string]string{
				"pull_request": "https://sqs.us-east-1.amazonaws.com/123/pr",
				"status": "https://sqs.us-east-1.amazonaws.com/123/status",
			},
		},
		{
			name: "enterprise environment filters out non-enterprise events",
			config: &Config{
				Region: "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
						EventRouting:  "sqs",
						GHECEnabled:   true,
						GHESEnabled:   false,
					},
					"status": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/status",
						EventRouting:  "both",
						GHECEnabled:   true,
						GHESEnabled:   true,
					},
				},
			},
			environment: "enterprise",
			want: map[string]string{
				"status": "https://sqs.us-east-1.amazonaws.com/123/status",
			},
		},
		{
			name: "west region selection",
			config: &Config{
				Region: "us-west-2",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
						WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/pr",
						GHECEnabled:   true,
					},
				},
			},
			environment: "cloud",
			want: map[string]string{
				"pull_request": "https://sqs.us-west-2.amazonaws.com/123/pr",
			},
		},
		{
			name: "fallback to east when west not available",
			config: &Config{
				Region: "us-west-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
						WestRegionURL: "", // Not configured
						GHECEnabled:   true,
					},
				},
			},
			environment: "cloud",
			want: map[string]string{
				"pull_request": "https://sqs.us-east-1.amazonaws.com/123/pr",
			},
		},
		{
			name: "both environments disabled returns empty",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
						GHECEnabled:   false,
						GHESEnabled:   false,
					},
				},
			},
			environment: "cloud",
			want:        map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.BuildQueueMap(tt.environment)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConfig_DetectRegion tests region detection from config and environment
func TestConfig_DetectRegion(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		envVars   map[string]string
		want      string
	}{
		{
			name:   "region from config",
			config: &Config{Region: "eu-west-1"},
			want:   "eu-west-1",
		},
		{
			name:    "region from AWS_REGION env",
			config:  &Config{},
			envVars: map[string]string{"AWS_REGION": "ap-south-1"},
			want:    "ap-south-1",
		},
		{
			name:    "region from AWS_DEFAULT_REGION env",
			config:  &Config{},
			envVars: map[string]string{"AWS_DEFAULT_REGION": "eu-central-1"},
			want:    "eu-central-1",
		},
		{
			name:   "default to us-east-1",
			config: &Config{},
			want:   "us-east-1",
		},
		{
			name:   "config overrides environment",
			config: &Config{Region: "ap-northeast-1"},
			envVars: map[string]string{
				"AWS_REGION":         "us-west-1",
				"AWS_DEFAULT_REGION": "us-west-2",
			},
			want: "ap-northeast-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			got := tt.config.DetectRegion()
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConfig_SelectRegionURL tests URL selection based on region
func TestConfig_SelectRegionURL(t *testing.T) {
	tests := []struct {
		name        string
		queueConfig EventQueueConfig
		region      string
		want        string
	}{
		{
			name: "east region selects east URL",
			queueConfig: EventQueueConfig{
				EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/queue",
				WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/queue",
			},
			region: "us-east-1",
			want:   "https://sqs.us-east-1.amazonaws.com/123/queue",
		},
		{
			name: "west region selects west URL",
			queueConfig: EventQueueConfig{
				EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/queue",
				WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/queue",
			},
			region: "us-west-2",
			want:   "https://sqs.us-west-2.amazonaws.com/123/queue",
		},
		{
			name: "west region falls back to east if west not configured",
			queueConfig: EventQueueConfig{
				EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/queue",
				WestRegionURL: "",
			},
			region: "us-west-1",
			want:   "https://sqs.us-east-1.amazonaws.com/123/queue",
		},
		{
			name: "east region falls back to west if east not configured",
			queueConfig: EventQueueConfig{
				EastRegionURL: "",
				WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/queue",
			},
			region: "us-east-1",
			want:   "https://sqs.us-west-2.amazonaws.com/123/queue",
		},
		{
			name: "empty URLs return empty string",
			queueConfig: EventQueueConfig{
				EastRegionURL: "",
				WestRegionURL: "",
			},
			region: "us-east-1",
			want:   "",
		},
		{
			name: "europe region defaults to east URL",
			queueConfig: EventQueueConfig{
				EastRegionURL: "https://sqs.eu-central-1.amazonaws.com/123/queue",
				WestRegionURL: "https://sqs.us-west-2.amazonaws.com/123/queue",
			},
			region: "eu-central-1",
			want:   "https://sqs.eu-central-1.amazonaws.com/123/queue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{}
			got := c.SelectRegionURL(tt.queueConfig, tt.region)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConfig_IsEventEnabledForEnvironment tests environment filtering
func TestConfig_IsEventEnabledForEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		eventType   string
		environment string
		want        bool
	}{
		{
			name: "cloud enabled for ghec",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						GHECEnabled: true,
						GHESEnabled: false,
					},
				},
			},
			eventType:   "pull_request",
			environment: "cloud",
			want:        true,
		},
		{
			name: "enterprise disabled for ghec-only event",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						GHECEnabled: true,
						GHESEnabled: false,
					},
				},
			},
			eventType:   "pull_request",
			environment: "enterprise",
			want:        false,
		},
		{
			name: "both environments enabled",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"status": {
						GHECEnabled: true,
						GHESEnabled: true,
					},
				},
			},
			eventType:   "status",
			environment: "enterprise",
			want:        true,
		},
		{
			name:        "event not in config defaults to enabled",
			config:      &Config{Queues: map[string]EventQueueConfig{}},
			eventType:   "installation",
			environment: "cloud",
			want:        true,
		},
		{
			name: "ghec alias works",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"status": {
						GHECEnabled: true,
						GHESEnabled: false,
					},
				},
			},
			eventType:   "status",
			environment: "ghec",
			want:        true,
		},
		{
			name: "ghes alias works",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"status": {
						GHECEnabled: false,
						GHESEnabled: true,
					},
				},
			},
			eventType:   "status",
			environment: "ghes",
			want:        true,
		},
		{
			name: "unknown environment checks both",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"status": {
						GHECEnabled: false,
						GHESEnabled: true,
					},
				},
			},
			eventType:   "status",
			environment: "unknown",
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.IsEventEnabledForEnvironment(tt.eventType, tt.environment)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConsumer_getWorkersForQueue tests worker count resolution
func TestConsumer_getWorkersForQueue(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		eventType string
		want      int
	}{
		{
			name: "from EventQueueConfig",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						QueueWorkers: 10,
					},
				},
				WorkersPerQueue: 5,
			},
			eventType: "pull_request",
			want:      10,
		},
		{
			name: "from EventQueueConfig.QueueWorkers",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"status": {
						QueueWorkers: 15,
					},
				},
				WorkersPerQueue: 5,
			},
			eventType: "status",
			want:      15,
		},
		{
			name: "from WorkersPerQueue default",
			config: &Config{
				WorkersPerQueue: 8,
			},
			eventType: "issue_comment",
			want:      8,
		},
		{
			name:      "hardcoded default",
			config:    &Config{},
			eventType: "installation",
			want:      5,
		},
		{
			name: "zero workers in EventQueueConfig falls back to WorkersPerQueue",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						QueueWorkers: 0,
					},
				},
				WorkersPerQueue: 7,
			},
			eventType: "pull_request",
			want:      7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{config: tt.config}
			got := c.getWorkersForQueue(tt.eventType)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConsumer_shouldProcessViaSQS tests event routing logic
func TestConsumer_shouldProcessViaSQS(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		queueMap  map[string]string
		eventType string
		want      bool
	}{
		{
			name: "EventQueueConfig routing set to sqs",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EventRouting: "sqs",
					},
				},
			},
			queueMap:  map[string]string{"pull_request": "https://sqs/pr"},
			eventType: "pull_request",
			want:      true,
		},
		{
			name: "EventQueueConfig routing set to both",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EventRouting: "both",
					},
				},
			},
			queueMap:  map[string]string{"pull_request": "https://sqs/pr"},
			eventType: "pull_request",
			want:      true,
		},
		{
			name: "EventQueueConfig routing set to http",
			config: &Config{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EventRouting: "http",
					},
				},
			},
			queueMap:  map[string]string{"pull_request": "https://sqs/pr"},
			eventType: "pull_request",
			want:      false,
		},
		{
			name: "falls back to legacy EventRouting",
			config: &Config{
							},
			queueMap:  map[string]string{"status": "https://sqs/status"},
			eventType: "status",
			want:      true,
		},
		{
			name:      "defaults to true if queue configured",
			config:    &Config{},
			queueMap:  map[string]string{"installation": "https://sqs/install"},
			eventType: "installation",
			want:      true,
		},
		{
			name:      "returns false if no queue configured",
			config:    &Config{},
			queueMap:  map[string]string{},
			eventType: "installation",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &consumer{
				config:   tt.config,
				queueMap: tt.queueMap,
			}
			got := c.shouldProcessViaSQS(tt.eventType)
			assert.Equal(t, tt.want, got)
		})
	}
}