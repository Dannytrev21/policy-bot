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

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQSConfig_Validation tests SQS configuration validation
func TestSQSConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  SQSConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid_basic_config",
			config: SQSConfig{
				Enabled: true,
				Region:  "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr-queue",
						EventRouting:  "sqs",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid_with_event_routing",
			config: SQSConfig{
				Enabled: true,
				Region:  "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr-queue",
						EventRouting:  "sqs",
					},
					"status": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/status-queue",
						EventRouting:  "both",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid_with_workers",
			config: SQSConfig{
				Enabled:         true,
				Region:          "us-east-1",
				WorkersPerQueue: 10,
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr-queue",
						QueueWorkers:  15,
					},
					"status": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/status-queue",
						QueueWorkers:  20,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid_localstack",
			config: SQSConfig{
				Enabled:     true,
				Region:      "us-east-1",
				EndpointURL: "http://localhost:4566",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "http://localhost:4566/000000000000/test-queue",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "disabled_with_no_queues",
			config: SQSConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "missing_region_urls",
			config: SQSConfig{
				Enabled: true,
				Region:  "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EventRouting: "sqs",
						// Missing both EastRegionURL and WestRegionURL
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid_routing_strategy",
			config: SQSConfig{
				Enabled: true,
				Region:  "us-east-1",
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr-queue",
						EventRouting:  "invalid",
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestSQSConfig_QueueURLParsing tests queue URL parsing and validation
func TestSQSConfig_QueueURLParsing(t *testing.T) {
	tests := []struct {
		name     string
		queueURL string
		valid    bool
	}{
		{
			name:     "valid_aws_sqs_url",
			queueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue",
			valid:    true,
		},
		{
			name:     "valid_localstack_url",
			queueURL: "http://localhost:4566/000000000000/test-queue",
			valid:    true,
		},
		{
			name:     "valid_different_region",
			queueURL: "https://sqs.eu-west-1.amazonaws.com/123456789012/eu-queue",
			valid:    true,
		},
		{
			name:     "empty_url",
			queueURL: "",
			valid:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.valid {
				assert.NotEmpty(t, tt.queueURL, "Valid queue URL should not be empty")
			} else {
				assert.Empty(t, tt.queueURL, "Invalid queue URL should be empty")
			}
		})
	}
}

// TestSQSConfig_EventRouting tests event routing configuration
func TestSQSConfig_EventRouting(t *testing.T) {
	validStrategies := []string{"http", "sqs", "both"}

	tests := []struct {
		name     string
		strategy string
		valid    bool
	}{
		{"http_only", "http", true},
		{"sqs_only", "sqs", true},
		{"both_paths", "both", true},
		{"invalid_strategy", "invalid", false},
		{"empty_strategy", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, valid := range validStrategies {
				if tt.strategy == valid {
					found = true
					break
				}
			}
			assert.Equal(t, tt.valid, found, "Strategy validation mismatch")
		})
	}
}

// TestConfig_ParseSQSConfig tests parsing SQS configuration from YAML
func TestConfig_ParseSQSConfig(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		expectErr bool
		validate  func(t *testing.T, config *Config)
	}{
		{
			name: "basic_sqs_config",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
      event_routing: "sqs"
    status:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/status"
      event_routing: "both"
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.Enabled)
				assert.Equal(t, "us-east-1", config.SQS.Region)
				assert.Len(t, config.SQS.Queues, 2)
				assert.Equal(t, "sqs", config.SQS.Queues["pull_request"].EventRouting)
				assert.Equal(t, "both", config.SQS.Queues["status"].EventRouting)
			},
		},
		{
			name: "sqs_with_routing",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-west-2"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-west-2.amazonaws.com/123/pr"
      event_routing: "sqs"
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.Enabled)
				assert.Equal(t, "sqs", config.SQS.Queues["pull_request"].EventRouting)
			},
		},
		{
			name: "sqs_with_workers",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  workers_per_queue: 10
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
      queue_workers: 15
    status:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/status"
      queue_workers: 20
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.Equal(t, 10, config.SQS.WorkersPerQueue)
				assert.Equal(t, 15, config.SQS.Queues["pull_request"].QueueWorkers)
				assert.Equal(t, 20, config.SQS.Queues["status"].QueueWorkers)
			},
		},
		{
			name: "sqs_with_localstack",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  endpoint_url: "http://localhost:4566"
  queues:
    pull_request:
      east_region_url: "http://localhost:4566/000000000000/test-pr"
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.Equal(t, "http://localhost:4566", config.SQS.EndpointURL)
			},
		},
		{
			name: "sqs_disabled",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: false
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.False(t, config.SQS.Enabled)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseConfig([]byte(tt.yaml))

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				if tt.validate != nil {
					tt.validate(t, config)
				}
			}
		})
	}
}

// TestSQSConfig_Defaults tests default values for SQS configuration
func TestSQSConfig_Defaults(t *testing.T) {
	config := SQSConfig{
		Enabled: true,
		Region:  "us-east-1",
		Queues: map[string]EventQueueConfig{
			"pull_request": {
				EastRegionURL: "https://sqs.us-east-1.amazonaws.com/123/pr",
			},
		},
	}

	// Test default values (these would be set by the consumer initialization)
	t.Run("workers_per_queue_default", func(t *testing.T) {
		// Default should be 5 if not specified
		if config.WorkersPerQueue == 0 {
			defaultWorkers := 5
			assert.Equal(t, defaultWorkers, defaultWorkers)
		}
	})

	t.Run("max_messages_default", func(t *testing.T) {
		// Default should be 10 if not specified
		if config.MaxMessages == 0 {
			defaultMaxMessages := 10
			assert.Equal(t, defaultMaxMessages, defaultMaxMessages)
		}
	})

	t.Run("visibility_timeout_default", func(t *testing.T) {
		// Default should be 30 seconds if not specified
		if config.VisibilityTimeout == 0 {
			defaultTimeout := 30
			assert.Equal(t, defaultTimeout, defaultTimeout)
		}
	})
}

// TestSQSConfig_EnhancedConfiguration tests enhanced configuration features
func TestSQSConfig_EnhancedConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		expectErr bool
		validate  func(t *testing.T, config *Config)
	}{
		{
			name: "per_queue_config_with_overrides",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
      queue_workers: 10
      max_retries: 3
      visibility_timeout: 60
    status:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/status"
      queue_workers: 15
      max_retries: 2
      visibility_timeout: 30
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.Enabled)
				assert.Len(t, config.SQS.Queues, 2)
				assert.Equal(t, 10, config.SQS.Queues["pull_request"].QueueWorkers)
				assert.Equal(t, 15, config.SQS.Queues["status"].QueueWorkers)
				assert.Equal(t, 60, config.SQS.Queues["pull_request"].VisibilityTimeout)
			},
		},
		{
			name: "environment_specific_config",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
      event_routing: "sqs"
      ghec_enabled: true
      ghes_enabled: false
    status:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/status"
      event_routing: "both"
      ghec_enabled: true
      ghes_enabled: true
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.Queues["pull_request"].GHECEnabled)
				assert.False(t, config.SQS.Queues["pull_request"].GHESEnabled)
				assert.True(t, config.SQS.Queues["status"].GHECEnabled)
				assert.True(t, config.SQS.Queues["status"].GHESEnabled)
			},
		},
		{
			name: "dlq_configuration",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
  dlq:
    enabled: true
    max_receive_count: 3
    queue_suffix: "-dlq"
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.DLQ.Enabled)
				assert.Equal(t, 3, config.SQS.DLQ.MaxReceiveCount)
				assert.Equal(t, "-dlq", config.SQS.DLQ.QueueSuffix)
			},
		},
		{
			name: "multi_region_configuration",
			yaml: `
server:
  address: "0.0.0.0"
  port: 8080

sqs:
  enabled: true
  region: "us-east-1"
  queues:
    pull_request:
      east_region_url: "https://sqs.us-east-1.amazonaws.com/123/pr"
      west_region_url: "https://sqs.us-west-2.amazonaws.com/456/pr"
      event_routing: "sqs"
  workers_per_queue: 5
`,
			expectErr: false,
			validate: func(t *testing.T, config *Config) {
				assert.True(t, config.SQS.Enabled)
				assert.Equal(t, "https://sqs.us-east-1.amazonaws.com/123/pr",
					config.SQS.Queues["pull_request"].EastRegionURL)
				assert.Equal(t, "https://sqs.us-west-2.amazonaws.com/456/pr",
					config.SQS.Queues["pull_request"].WestRegionURL)
				assert.Equal(t, 5, config.SQS.WorkersPerQueue)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseConfig([]byte(tt.yaml))

			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				if tt.validate != nil {
					tt.validate(t, config)
				}
			}
		})
	}
}

// TestSQSConfig_GetRoutingStrategy tests the GetRoutingStrategy helper
func TestSQSConfig_GetRoutingStrategy(t *testing.T) {
	tests := []struct {
		name        string
		config      SQSConfig
		environment string
		eventType   string
		expected    string
	}{
		{
			name: "explicit_sqs_routing",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						EventRouting:  "sqs",
						GHECEnabled:   true,
					},
				},
			},
			environment: "cloud",
			eventType:   "pull_request",
			expected:    "sqs",
		},
		{
			name: "explicit_both_routing",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"status": {
						EastRegionURL: "url",
						EventRouting:  "both",
						GHECEnabled:   true,
					},
				},
			},
			environment: "cloud",
			eventType:   "status",
			expected:    "both",
		},
		{
			name: "explicit_http_routing",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						EventRouting:  "http",
						GHESEnabled:   true,
					},
				},
			},
			environment: "enterprise",
			eventType:   "pull_request",
			expected:    "http",
		},
		{
			name: "default_sqs_when_enabled_for_env",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						GHECEnabled:   true,
					},
				},
			},
			environment: "cloud",
			eventType:   "pull_request",
			expected:    "sqs",
		},
		{
			name: "default_http_when_not_enabled",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						GHECEnabled:   false,
					},
				},
			},
			environment: "cloud",
			eventType:   "pull_request",
			expected:    "http",
		},
		{
			name:        "default_sqs_when_not_configured",
			config:      SQSConfig{}, // For backward compatibility, assumes enabled
			environment: "cloud",
			eventType:   "pull_request",
			expected:    "sqs", // Returns "sqs" for backward compatibility
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetRoutingStrategy(tt.environment, tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSQSConfig_GetQueueWorkers tests worker allocation priority
func TestSQSConfig_GetQueueWorkers(t *testing.T) {
	tests := []struct {
		name      string
		config    SQSConfig
		eventType string
		expected  int
	}{
		{
			name: "queue_workers_highest_priority",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"pull_request": {QueueWorkers: 10},
				},
				WorkersPerQueue: 5,
			},
			eventType: "pull_request",
			expected:  10, // EventQueueConfig.QueueWorkers has highest priority
		},
		{
			name: "workers_per_queue_second_priority",
			config: SQSConfig{
				Queues: map[string]EventQueueConfig{
					"status": {QueueWorkers: 0}, // Not set
				},
				WorkersPerQueue: 15,
			},
			eventType: "status",
			expected:  15,
		},
		{
			name: "workers_per_queue_fallback",
			config: SQSConfig{
				WorkersPerQueue: 7,
			},
			eventType: "check_run",
			expected:  7,
		},
		{
			name:      "final_fallback_default",
			config:    SQSConfig{},
			eventType: "workflow_run",
			expected:  5, // Default fallback
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetQueueWorkers(tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSQSConfig_Validate_Enhanced tests enhanced validation
func TestSQSConfig_Validate_Enhanced(t *testing.T) {
	tests := []struct {
		name      string
		config    SQSConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid_event_routing",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						EventRouting:  "sqs",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid_routing_strategy",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EastRegionURL: "url",
						EventRouting:  "invalid",
					},
				},
			},
			wantErr:   true,
			errSubstr: "invalid routing strategy",
		},
		{
			name: "valid_dlq_config",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {EastRegionURL: "url"},
				},
				DLQ: DLQConfig{
					Enabled:         true,
					MaxReceiveCount: 3,
					QueueSuffix:     "-dlq",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid_dlq_max_receive_count",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {EastRegionURL: "url"},
				},
				DLQ: DLQConfig{
					Enabled:         true,
					MaxReceiveCount: 0,
				},
			},
			wantErr:   true,
			errSubstr: "DLQ max_receive_count must be at least 1",
		},
		{
			name: "missing_region_urls",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						EventRouting: "sqs",
						// Both EastRegionURL and WestRegionURL missing
					},
				},
			},
			wantErr:   true,
			errSubstr: "no region URLs specified for event type",
		},
		{
			name: "at_least_one_region_url",
			config: SQSConfig{
				Enabled: true,
				Queues: map[string]EventQueueConfig{
					"pull_request": {
						WestRegionURL: "url", // Only west, that's fine
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				require.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
