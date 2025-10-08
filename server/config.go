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
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/palantir/go-baseapp/appmetrics/emitter/datadog"
	"github.com/palantir/go-baseapp/appmetrics/emitter/prometheus"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/palantir/policy-bot/server/handler"
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
	Prometheus        prometheus.Config             `yaml:"prometheus"`
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

	// Map of GitHub event type to SQS queue URL
	Queues map[string]string `yaml:"queues"`

	// Event routing: specify which events to process via SQS vs HTTP
	// If not specified, all events configured in Queues are processed via SQS
	EventRouting map[string]string `yaml:"event_routing"` // event_type -> "sqs" | "http" | "both"

	// Number of workers per queue (defaults to 5)
	WorkersPerQueue int `yaml:"workers_per_queue"`

	// Per-queue worker allocation (overrides WorkersPerQueue for specific event types)
	QueueWorkers map[string]int `yaml:"queue_workers"`

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
