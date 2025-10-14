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

package test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"
)

// LocalStackOptions configures LocalStack connectivity for tests.
type LocalStackOptions struct {
	URL             string
	Region          string
	AccessKey       string
	SecretKey       string
	SessionToken    string
	WaitTimeout     time.Duration
	RequirePresence bool
}

// LocalStackManager manages LocalStack SQS queues during tests.
type LocalStackManager struct {
	t         *testing.T
	url       string
	region    string
	client    *sqs.Client
	queueURLs map[string]string
}

// NewLocalStackManager creates a LocalStack manager and ensures availability when requested.
func NewLocalStackManager(t *testing.T, opts LocalStackOptions) *LocalStackManager {
	t.Helper()

	if opts.URL == "" {
		opts.URL = "http://localhost:4566"
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if opts.AccessKey == "" {
		opts.AccessKey = "test"
	}
	if opts.SecretKey == "" {
		opts.SecretKey = "test"
	}
	if opts.WaitTimeout == 0 {
		opts.WaitTimeout = 10 * time.Second
	}

	if opts.RequirePresence && !WaitForLocalStack(opts.URL, opts.WaitTimeout) {
		t.Skipf("LocalStack is not available at %s", opts.URL)
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(opts.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(opts.AccessKey, opts.SecretKey, opts.SessionToken)),
	)
	require.NoError(t, err)

	client := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(opts.URL)
	})

	return &LocalStackManager{
		t:         t,
		url:       opts.URL,
		region:    opts.Region,
		client:    client,
		queueURLs: make(map[string]string),
	}
}

// Client exposes the underlying SQS client for advanced use cases.
func (m *LocalStackManager) Client() *sqs.Client {
	return m.client
}

// EnsureQueues ensures that the provided event-to-queue mapping exists and returns updated URLs.
func (m *LocalStackManager) EnsureQueues(eventQueueMap map[string]string) map[string]string {
	result := make(map[string]string, len(eventQueueMap))
	for eventType, queueRef := range eventQueueMap {
		queueName := queueRef
		if strings.HasPrefix(queueRef, "http") {
			queueName = QueueNameFromURL(queueRef)
		}
		queueURL := m.EnsureQueue(queueName)
		result[eventType] = queueURL
	}
	return result
}

// EnsureQueue creates or retrieves a queue and returns its URL.
func (m *LocalStackManager) EnsureQueue(queueName string) string {
	if url, ok := m.queueURLs[queueName]; ok {
		return url
	}

	out, err := m.client.CreateQueue(context.Background(), &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	if err != nil {
		var existsErr *types.QueueNameExists
		if errors.As(err, &existsErr) {
			getOut, getErr := m.client.GetQueueUrl(context.Background(), &sqs.GetQueueUrlInput{
				QueueName: aws.String(queueName),
			})
			require.NoError(m.t, getErr)
			url := aws.ToString(getOut.QueueUrl)
			m.queueURLs[queueName] = url
			return url
		}
		require.NoError(m.t, err)
	}

	url := aws.ToString(out.QueueUrl)
	m.queueURLs[queueName] = url
	return url
}

// PurgeQueue removes all messages from a queue when possible.
func (m *LocalStackManager) PurgeQueue(queueName string) {
	if queueURL, ok := m.queueURLs[queueName]; ok {
		_, err := m.client.PurgeQueue(context.Background(), &sqs.PurgeQueueInput{QueueUrl: aws.String(queueURL)})
		if err != nil {
			var inProgress *types.PurgeQueueInProgress
			if !errors.As(err, &inProgress) {
				m.t.Logf("failed to purge queue %s: %v", queueName, err)
			}
		}
	}
}

// Cleanup removes queues created by the manager.
func (m *LocalStackManager) Cleanup() {
	for queueName, queueURL := range m.queueURLs {
		_, err := m.client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
		if err != nil {
			m.t.Logf("failed to delete queue %s: %v", queueName, err)
		}
	}
}

// WaitForLocalStack checks LocalStack availability within the provided timeout.
func WaitForLocalStack(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			status := resp.StatusCode
			if cerr := resp.Body.Close(); cerr != nil {
				continue
			}
			if status < 500 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	return false
}

// QueueNameFromURL extracts the queue name from a LocalStack SQS URL.
func QueueNameFromURL(queueURL string) string {
	parts := strings.Split(queueURL, "/")
	if len(parts) == 0 {
		return queueURL
	}
	return parts[len(parts)-1]
}
