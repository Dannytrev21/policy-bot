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
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegration_SQSBurstPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 2
	config.WorkerPoolSize = 4

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
		WaitTimeout:     15 * time.Second,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	testHandler := NewTestEventHandler([]string{"pull_request", "pull_request_review", "issue_comment", "status"})

	srv, _, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()

	scenario := LoadScenario{
		Name:        "burst-smoke",
		TotalEvents: 20,
		WaitTimeout: 10 * time.Second,
	}

	timer := StartRun(scenario)
	for i := 0; i < scenario.TotalEvents; i++ {
		event := GitHubEvent{Type: "pull_request", Action: "synchronize", Number: i + 1000}
		sendSQSMessage(t, sqsClient, config.SQSQueueURLs[event.Type], event)
	}

	waitForEvents(t, testHandler, scenario.TotalEvents, scenario.WaitTimeout)

	stats := timer.Finish(scenario.TotalEvents)
	stats.Log(t)

	if stats.Duration > 5*time.Second {
		t.Fatalf("expected to process %d events in under 5s, took %s", scenario.TotalEvents, stats.Duration)
	}

	if count := testHandler.GetEventCount(); count != scenario.TotalEvents {
		t.Fatalf("expected %d events, got %d", scenario.TotalEvents, count)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("server shutdown error: %v", err)
	}
	shutdownCancel()

	select {
	case err := <-serverErrChan:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("server error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("server shutdown timeout")
	}
}

func TestIntegration_SQSHighVolume(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	scenario := LoadScenario{
		Name:               "sqs-high-volume",
		TotalEvents:        400,
		WaitTimeout:        60 * time.Second,
		TargetEventsPerMin: 800,
	}

	config := DefaultIntegrationTestConfig()
	config.SQSWorkersPerQueue = 4
	config.WorkerPoolSize = 8
	config.SQSMaxMessages = 10
	config.SQSWaitTimeSeconds = 1

	localStack := NewLocalStackManager(t, LocalStackOptions{
		URL:             config.LocalStackURL,
		Region:          "us-east-1",
		RequirePresence: true,
		WaitTimeout:     20 * time.Second,
	})
	defer localStack.Cleanup()

	config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
	for _, queueURL := range config.SQSQueueURLs {
		localStack.PurgeQueue(QueueNameFromURL(queueURL))
	}

	testHandler := NewTestEventHandler([]string{"pull_request", "pull_request_review", "issue_comment", "status"})

	srv, _, cleanup := setupTestServer(t, config, testHandler)
	defer cleanup()

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- srv.Start()
	}()

	time.Sleep(2 * time.Second)

	sqsClient := localStack.Client()
	queueURL := config.SQSQueueURLs["pull_request"]

	timer := StartRun(scenario)
	for i := 0; i < scenario.TotalEvents; i++ {
		event := GitHubEvent{Type: "pull_request", Action: "synchronize", Number: i + 2000}
		sendSQSMessage(t, sqsClient, queueURL, event)
		if scenario.DelayBetweenSends > 0 {
			time.Sleep(scenario.DelayBetweenSends)
		}
	}

	waitForEvents(t, testHandler, scenario.TotalEvents, scenario.WaitTimeout)

	stats := timer.Finish(scenario.TotalEvents)
	stats.Log(t)

	if scenario.TargetEventsPerMin > 0 {
		require.GreaterOrEqual(t, stats.EventsPerMinute, scenario.TargetEventsPerMin,
			"expected aggregated throughput >= %.0f events/min", scenario.TargetEventsPerMin)
	}

	var depth int64
	for i := 0; i < 6; i++ {
		var err error
		depth, err = QueueDepth(context.Background(), sqsClient, queueURL)
		require.NoError(t, err)
		if depth <= 5 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.LessOrEqual(t, depth, int64(5), "queue should be nearly drained after test")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("server shutdown error: %v", err)
	}
	shutdownCancel()

	select {
	case err := <-serverErrChan:
		if err != nil && err != http.ErrServerClosed {
			t.Logf("server error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("server shutdown timeout")
	}
}

func TestIntegration_SQSWorkerScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	workerTargets := []int{1, 4}
	results := make(map[int]RunStats, len(workerTargets))

	baseScenario := LoadScenario{
		Name:               "worker-scaling",
		TotalEvents:        200,
		WaitTimeout:        60 * time.Second,
		TargetEventsPerMin: 300,
	}

	for _, workers := range workerTargets {
		workers := workers
		t.Run(fmt.Sprintf("%d_workers", workers), func(t *testing.T) {
			config := DefaultIntegrationTestConfig()
			config.SQSWorkersPerQueue = workers
			if workers*2 > 0 {
				config.WorkerPoolSize = workers * 2
			}
			config.SQSMaxMessages = 10
			config.SQSWaitTimeSeconds = 1

			localStack := NewLocalStackManager(t, LocalStackOptions{
				URL:             config.LocalStackURL,
				Region:          "us-east-1",
				RequirePresence: true,
				WaitTimeout:     20 * time.Second,
			})
			defer localStack.Cleanup()

			config.SQSQueueURLs = localStack.EnsureQueues(config.SQSQueueURLs)
			for _, queueURL := range config.SQSQueueURLs {
				localStack.PurgeQueue(QueueNameFromURL(queueURL))
			}

			testHandler := NewTestEventHandler([]string{"pull_request", "pull_request_review", "issue_comment", "status"})

			srv, _, cleanup := setupTestServer(t, config, testHandler)
			defer cleanup()

			serverErrChan := make(chan error, 1)
			go func() {
				serverErrChan <- srv.Start()
			}()

			time.Sleep(2 * time.Second)

			sqsClient := localStack.Client()
			queueURL := config.SQSQueueURLs["pull_request"]

			scenario := ScenarioWithWorkers(baseScenario, workers)
			sendStart := time.Now()
			for i := 0; i < scenario.TotalEvents; i++ {
				event := GitHubEvent{Type: "pull_request", Action: "synchronize", Number: i + workers*3000}
				sendSQSMessage(t, sqsClient, queueURL, event)
				if scenario.DelayBetweenSends > 0 {
					time.Sleep(scenario.DelayBetweenSends)
				}
			}

			t.Logf("%s: enqueued %d events in %s", scenario.Name, scenario.TotalEvents, time.Since(sendStart).Truncate(10*time.Millisecond))

			timer := StartRun(scenario)

			waitForEvents(t, testHandler, scenario.TotalEvents, scenario.WaitTimeout)

			stats := timer.Finish(scenario.TotalEvents)
			stats.Log(t)
			if scenario.TargetEventsPerMin > 0 {
				require.GreaterOrEqual(t, stats.EventsPerMinute, scenario.TargetEventsPerMin,
					"expected throughput >= %.0f events/min", scenario.TargetEventsPerMin)
			}
			results[workers] = stats

			var depth int64
			for i := 0; i < 6; i++ {
				var err error
				depth, err = QueueDepth(context.Background(), sqsClient, queueURL)
				require.NoError(t, err)
				if depth <= 5 {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			require.LessOrEqual(t, depth, int64(5))

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.Shutdown(shutdownCtx); err != nil {
				t.Logf("server shutdown error: %v", err)
			}
			shutdownCancel()

			select {
			case err := <-serverErrChan:
				if err != nil && err != http.ErrServerClosed {
					t.Logf("server error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Log("server shutdown timeout")
			}
		})
	}

	if len(results) != len(workerTargets) {
		t.Fatalf("expected results for %d worker configurations, got %d", len(workerTargets), len(results))
	}

	slow, ok := results[1]
	if !ok {
		t.Fatalf("missing results for 1 worker configuration")
	}
	fast, ok := results[4]
	if !ok {
		t.Fatalf("missing results for 4 worker configuration")
	}

	if slow.Duration == 0 || fast.Duration == 0 {
		t.Fatalf("invalid durations: slow=%s fast=%s", slow.Duration, fast.Duration)
	}

	ratio := float64(fast.Duration) / float64(slow.Duration)
	if ratio >= 1.0 {
		t.Logf("worker scaling produced minimal speed-up (ratio %.3f)", ratio)
	}
	require.LessOrEqual(t, ratio, 1.1, "additional workers should be within 10%% of single-worker duration")
	require.GreaterOrEqual(t, fast.EventsPerMinute, slow.EventsPerMinute*0.9,
		"additional workers should maintain comparable throughput")
}
