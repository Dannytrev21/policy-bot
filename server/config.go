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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-baseapp/baseapp/datadog"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server/handler"
	"github.com/palantir/policy-bot/server/metricsbridge"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

const (
	DefaultEnvPrefix = "POLICYBOT_"
)

// GithubAppConfig extends githubapp.Config with additional app-specific settings
// for middleware routing and event processing control.
type GithubAppConfig struct {
	githubapp.Config `yaml:",inline" json:",inline"`

	// WebhookRoute allows custom webhook route per app (optional)
	// Defaults to githubapp.DefaultWebhookRoute if not specified
	WebhookRoute string `yaml:"webhook_route" json:"webhook_route"`

	// EventRouting controls which events go to SQS vs HTTP
	// Maps event type to routing strategy: "sqs", "http", or "both"
	EventRouting map[string]string `yaml:"event_routing" json:"event_routing"`
}

type Config struct {
	Server  baseapp.HTTPConfig `yaml:"server"`
	Logging LoggingConfig      `yaml:"logging"`
	Cache   CachingConfig      `yaml:"cache"`

	// GithubEnterprise configuration for GitHub Enterprise Server
	GithubEnterprise GithubAppConfig `yaml:"github_enterprise"`

	// GithubCloud configuration for GitHub Enterprise Cloud
	GithubCloud GithubAppConfig `yaml:"github_cloud"`

	Sessions          SessionsConfig                `yaml:"sessions"`
	CloudOptions      handler.PullEvaluationOptions `yaml:"cloud_options"`
	EnterpriseOptions handler.PullEvaluationOptions `yaml:"enterprise_options"`
	Files             handler.FilesConfig           `yaml:"files"`
	Datadog           datadog.Config                `yaml:"datadog"`
	Prometheus        metricsbridge.Config          `yaml:"prometheus"`
	Workers           WorkerConfig                  `yaml:"workers"`
	SQS               SQSConfig                     `yaml:"sqs"`
}

type LoggingConfig struct {
	Level string `yaml:"level" json:"level"`
	Text  bool   `yaml:"text" json:"text"`
}

func (c *LoggingConfig) SetValuesFromEnv(prefix string) {
	if v, ok := os.LookupEnv(prefix + "LOG_LEVEL"); ok {
		c.Level = v
	}
	if v, ok := os.LookupEnv(prefix + "LOG_TEXT"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Text = b
		}
	}
}

type CachingConfig struct {
	// The maximum size of the the HTTP cache associated with each GitHub
	// client. The total amount of memory used for caching is approximately
	// this value multiplied by the total number of active GitHub clients.
	MaxSize datasize.ByteSize `yaml:"max_size"`

	// The size of the global cache for commit push times. Each entry uses
	// roughly 100 bytes of memory.
	PushedAtSize int `yaml:"pushed_at_size"`
}

type WorkerConfig struct {
	Workers       int           `yaml:"workers"`
	QueueSize     int           `yaml:"queue_size"`
	GithubTimeout time.Duration `yaml:"github_timeout"`
}

type SessionsConfig struct {
	Key      string `yaml:"key"`
	Lifetime string `yaml:"lifetime"`
}

type SQSConfig struct {
	// Enable SQS event consumption
	Enabled bool `yaml:"enabled"`

	// AWS region for SQS queues
	Region string `yaml:"region"`

	// AWS endpoint URL for LocalStack/testing (optional)
	EndpointURL string `yaml:"endpoint_url"`

	// Processing mode: "scheduler" (legacy) or "direct" (worker pools)
	// Default: "scheduler" for backward compatibility
	// "direct" mode bypasses the internal scheduler and uses dedicated worker pools
	ProcessingMode string `yaml:"processing_mode"`

	// Event-based queue configuration with region URLs and environment controls
	Queues map[string]EventQueueConfig `yaml:"queues"`

	// Number of workers per queue (defaults to 5 if not specified in EventQueueConfig)
	WorkersPerQueue int `yaml:"workers_per_queue"`

	// Maximum number of messages to receive in a single request (1-10)
	MaxMessages int `yaml:"max_messages"`

	// Message visibility timeout in seconds
	VisibilityTimeout int `yaml:"visibility_timeout"`

	// Wait time for long polling (0-20 seconds)
	WaitTimeSeconds int `yaml:"wait_time_seconds"`

	// Maximum time to wait for graceful shutdown
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`

	// Enable retry on message processing failure
	EnableRetry bool `yaml:"enable_retry"`

	// Maximum number of retries before sending to DLQ
	MaxRetries int `yaml:"max_retries"`

	// Dead Letter Queue configuration
	DLQ DLQConfig `yaml:"dlq"`

	// Adaptive polling configuration
	AdaptivePolling AdaptivePollingConfig `yaml:"adaptive_polling"`
}

// EventQueueConfig provides event-specific queue configuration with regional URLs and environment controls
type EventQueueConfig struct {
	// Regional queue URLs
	EastRegionURL string `yaml:"east_region_url"`
	WestRegionURL string `yaml:"west_region_url"`

	// Event routing strategy: "http", "sqs", or "both"
	EventRouting string `yaml:"event_routing"`

	// Environment-specific enablement
	GHECEnabled bool `yaml:"ghec_enabled"`
	GHESEnabled bool `yaml:"ghes_enabled"`

	// Number of workers for this event type
	QueueWorkers int `yaml:"queue_workers"`

	// Optional overrides
	VisibilityTimeout int `yaml:"visibility_timeout,omitempty"`
	MaxRetries        int `yaml:"max_retries,omitempty"`
}

// DLQConfig configures Dead Letter Queue behavior
type DLQConfig struct {
	// Enable DLQ processing
	Enabled bool `yaml:"enabled"`

	// Maximum times a message can be received before being sent to DLQ
	MaxReceiveCount int `yaml:"max_receive_count"`

	// Suffix to append to queue names for DLQ (e.g., "-dlq")
	QueueSuffix string `yaml:"queue_suffix"`
}

// AdaptivePollingConfig configures adaptive SQS polling based on worker availability
type AdaptivePollingConfig struct {
	// Enable adaptive polling based on worker availability
	Enabled bool `yaml:"enabled"`

	// Base backoff duration when workers are saturated
	BaseBackoff time.Duration `yaml:"base_backoff"`

	// Maximum backoff duration
	MaxBackoff time.Duration `yaml:"max_backoff"`

	// Enable per-event-type configuration
	EventTypeOverrides map[string]AdaptivePollingEventConfig `yaml:"event_overrides"`
}

// AdaptivePollingEventConfig configures adaptive polling for a specific event type
type AdaptivePollingEventConfig struct {
	Enabled     bool          `yaml:"enabled"`
	BaseBackoff time.Duration `yaml:"base_backoff"`
	MaxBackoff  time.Duration `yaml:"max_backoff"`
}

// Validate validates the SQS configuration
func (c *SQSConfig) Validate() error {
	if !c.Enabled {
		return nil // SQS disabled, no validation needed
	}

	// Check that at least one queue configuration exists
	if len(c.Queues) == 0 {
		return errors.New("SQS enabled but no queues configured")
	}

	// Validate EventQueueConfig format
	validStrategies := map[string]bool{"http": true, "sqs": true, "both": true}
	for eventType, queueConfig := range c.Queues {
		// At least one region URL must be specified
		if queueConfig.EastRegionURL == "" && queueConfig.WestRegionURL == "" {
			return errors.Errorf("no region URLs specified for event type: %s", eventType)
		}

		// Validate routing strategy if specified
		if queueConfig.EventRouting != "" {
			if !validStrategies[queueConfig.EventRouting] {
				return errors.Errorf("invalid routing strategy for %s: %s (must be 'http', 'sqs', or 'both')", eventType, queueConfig.EventRouting)
			}
		}
	}

	// Validate DLQ configuration
	if c.DLQ.Enabled {
		if c.DLQ.MaxReceiveCount < 1 {
			return errors.New("DLQ max_receive_count must be at least 1")
		}
		if c.DLQ.QueueSuffix == "" {
			c.DLQ.QueueSuffix = "-dlq" // Set default
		}
	}

	return nil
}

// GetRoutingStrategy determines the routing strategy for a specific event type and environment
// Returns "http", "sqs", or "both"
func (c *SQSConfig) GetRoutingStrategy(environment, eventType string) string {
	// Check EventQueueConfig routing
	if queueConfig, exists := c.Queues[eventType]; exists && queueConfig.EventRouting != "" {
		return queueConfig.EventRouting
	}

	// Default behavior: if queue configured and enabled for environment, use SQS
	if c.IsEventEnabledForEnvironment(eventType, environment) {
		return "sqs"
	}

	// Default to HTTP if not configured
	return "http"
}

// GetQueueURLForEnvironment returns the queue URL for a specific event type and environment
// considering the current AWS region
func (c *SQSConfig) GetQueueURLForEnvironment(eventType, environment string) string {
	queueConfig, exists := c.Queues[eventType]
	if !exists {
		return ""
	}

	// Check if this event is enabled for the environment
	if !c.IsEventEnabledForEnvironment(eventType, environment) {
		return ""
	}

	// Detect region and select appropriate URL
	region := c.DetectRegion()
	return c.SelectRegionURL(queueConfig, region)
}

// GetQueueURL returns the queue URL for a specific event type
// Supports both legacy Queues and new EventQueues configuration
func (c *SQSConfig) GetQueueURL(eventType string) string {
	// For backward compatibility, default to cloud environment
	return c.GetQueueURLForEnvironment(eventType, "cloud")
}

// DetectRegion determines the current AWS region from configuration or environment
func (c *SQSConfig) DetectRegion() string {
	// First check if Region is explicitly set in config
	if c.Region != "" {
		return c.Region
	}

	// Check AWS_REGION environment variable
	if region, ok := os.LookupEnv("AWS_REGION"); ok && region != "" {
		return region
	}

	// Check AWS_DEFAULT_REGION environment variable
	if region, ok := os.LookupEnv("AWS_DEFAULT_REGION"); ok && region != "" {
		return region
	}

	// Default to us-east-1
	return "us-east-1"
}

// SelectRegionURL selects the appropriate queue URL based on the region
func (c *SQSConfig) SelectRegionURL(queueConfig EventQueueConfig, region string) string {
	// If region contains "west", use west URL if available
	if strings.Contains(strings.ToLower(region), "west") {
		if queueConfig.WestRegionURL != "" {
			return queueConfig.WestRegionURL
		}
		// Fall back to east URL if west not available
		return queueConfig.EastRegionURL
	}

	// For east or any other region, prefer east URL
	if queueConfig.EastRegionURL != "" {
		return queueConfig.EastRegionURL
	}

	// Fall back to west URL if east not available
	return queueConfig.WestRegionURL
}

// IsEventEnabledForEnvironment checks if an event is enabled for a specific environment
func (c *SQSConfig) IsEventEnabledForEnvironment(eventType, environment string) bool {
	queueConfig, exists := c.Queues[eventType]
	if !exists {
		// If not in new config, assume enabled for backward compatibility
		return true
	}

	switch environment {
	case "cloud", "ghec":
		return queueConfig.GHECEnabled
	case "enterprise", "ghes":
		return queueConfig.GHESEnabled
	default:
		// Unknown environment, check both
		return queueConfig.GHECEnabled || queueConfig.GHESEnabled
	}
}

// GetQueueWorkers returns the number of workers for a specific event type
func (c *SQSConfig) GetQueueWorkers(eventType string) int {
	// Check EventQueueConfig.QueueWorkers
	if queueConfig, exists := c.Queues[eventType]; exists && queueConfig.QueueWorkers > 0 {
		return queueConfig.QueueWorkers
	}

	// Use WorkersPerQueue default
	if c.WorkersPerQueue > 0 {
		return c.WorkersPerQueue
	}

	// Final fallback default
	return 5
}

// GetVisibilityTimeout returns the visibility timeout for a specific event type
func (c *SQSConfig) GetVisibilityTimeout(eventType string) int {
	// Check EventQueueConfig.VisibilityTimeout
	if queueConfig, exists := c.Queues[eventType]; exists && queueConfig.VisibilityTimeout > 0 {
		return queueConfig.VisibilityTimeout
	}

	// Fall back to global VisibilityTimeout
	if c.VisibilityTimeout > 0 {
		return c.VisibilityTimeout
	}

	// Default to 30 seconds
	return 30
}

// GetMaxRetries returns the max retries for a specific event type
func (c *SQSConfig) GetMaxRetries(eventType string) int {
	// Check EventQueueConfig.MaxRetries
	if queueConfig, exists := c.Queues[eventType]; exists && queueConfig.MaxRetries > 0 {
		return queueConfig.MaxRetries
	}

	// Fall back to global MaxRetries
	if c.MaxRetries > 0 {
		return c.MaxRetries
	}

	// Default to 3 retries
	return 3
}

// GetEventRouting returns the routing strategy for a specific event type
func (c *SQSConfig) GetEventRouting(eventType string) string {
	// Check EventQueueConfig.EventRouting
	if queueConfig, exists := c.Queues[eventType]; exists && queueConfig.EventRouting != "" {
		return queueConfig.EventRouting
	}

	// Default to "sqs" if queue is configured
	if _, hasQueue := c.Queues[eventType]; hasQueue {
		return "sqs"
	}

	return "http"
}

// GetEnabledQueuesForEnvironment returns all enabled queue configurations for a specific environment
func (c *SQSConfig) GetEnabledQueuesForEnvironment(environment string) map[string]string {
	result := make(map[string]string)

	// Process EventQueueConfig format
	for eventType := range c.Queues {
		if c.IsEventEnabledForEnvironment(eventType, environment) {
			url := c.GetQueueURLForEnvironment(eventType, environment)
			if url != "" {
				result[eventType] = url
			}
		}
	}

	return result
}

func ParseConfig(bytes []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(bytes, &c); err != nil {
		return nil, errors.Wrapf(err, "failed unmarshalling yaml")
	}

	envPrefix := DefaultEnvPrefix
	if v, ok := os.LookupEnv("POLICYBOT_ENV_PREFIX"); ok {
		envPrefix = v
	}

	c.CloudOptions.SetValuesFromEnv(envPrefix + "OPTIONS_")
	c.EnterpriseOptions.SetValuesFromEnv(envPrefix + "ENTERPRISE_OPTIONS_")
	c.Server.SetValuesFromEnv(envPrefix)
	c.Logging.SetValuesFromEnv(envPrefix)
	c.GithubEnterprise.Config.SetValuesFromEnv("GITHUB_ENTERPRISE_")
	c.GithubCloud.Config.SetValuesFromEnv("GITHUB_CLOUD_")

	if v, ok := os.LookupEnv(envPrefix + "SESSIONS_KEY"); ok {
		c.Sessions.Key = v
	}

	// Set defaults for webhook routes if not specified
	if c.GithubEnterprise.App.IntegrationID != 0 && c.GithubEnterprise.WebhookRoute == "" {
		c.GithubEnterprise.WebhookRoute = "/api/github/hook"
	}
	if c.GithubCloud.App.IntegrationID != 0 && c.GithubCloud.WebhookRoute == "" {
		c.GithubCloud.WebhookRoute = "/api/github/hook"
	}

	return &c, nil
}

// ValidateConfig ensures at least one GitHub configuration is present and valid
func (c *Config) ValidateConfig() error {
	hasEnterprise := c.GithubEnterprise.App.IntegrationID != 0
	hasCloud := c.GithubCloud.App.IntegrationID != 0

	if !hasEnterprise && !hasCloud {
		return errors.New("no GitHub configuration found: must specify at least one of 'github_enterprise' or 'github_cloud'")
	}

	// Validate enterprise config if present
	if hasEnterprise {
		if err := validateGithubConfig("github_enterprise", &c.GithubEnterprise.Config); err != nil {
			return err
		}
	}

	// Validate cloud config if present
	if hasCloud {
		if err := validateGithubConfig("github_cloud", &c.GithubCloud.Config); err != nil {
			return err
		}
	}

	return nil
}

// validateGithubConfig validates a GitHub configuration for required fields
func validateGithubConfig(name string, config *githubapp.Config) error {
	// Validate URLs
	if config.WebURL == "" {
		return errors.Errorf("%s web_url is required", name)
	}
	if config.V3APIURL == "" {
		return errors.Errorf("%s v3_api_url is required", name)
	}
	if config.V4APIURL == "" {
		return errors.Errorf("%s v4_api_url is required", name)
	}

	// Validate App fields
	if config.App.IntegrationID == 0 {
		return errors.Errorf("%s app.integration_id is required", name)
	}
	if config.App.WebhookSecret == "" {
		return errors.Errorf("%s app.webhook_secret is required", name)
	}
	if config.App.PrivateKey == "" {
		return errors.Errorf("%s app.private_key is required", name)
	}

	// OAuth fields are optional, but if one is set, both should be set
	if config.OAuth.ClientID != "" || config.OAuth.ClientSecret != "" {
		if config.OAuth.ClientID == "" {
			return errors.Errorf("%s oauth.client_id is required when oauth is configured", name)
		}
		if config.OAuth.ClientSecret == "" {
			return errors.Errorf("%s oauth.client_secret is required when oauth is configured", name)
		}
	}

	return nil
}
