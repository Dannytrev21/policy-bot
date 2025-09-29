package test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// LoadScenario describes a synthetic workload for performance testing.
type LoadScenario struct {
	Name               string
	TotalEvents        int
	DelayBetweenSends  time.Duration
	WaitTimeout        time.Duration
	TargetEventsPerMin float64
}

// RunTimer tracks a scenario start time and produces RunStats on completion.
type RunTimer struct {
	scenario LoadScenario
	start    time.Time
}

// RunStats contains summary metrics for a load execution.
type RunStats struct {
	Scenario        LoadScenario
	Started         time.Time
	Completed       time.Time
	Duration        time.Duration
	TotalEvents     int
	EventsPerSecond float64
	EventsPerMinute float64
}

// StartRun begins timing for the provided scenario.
func StartRun(scenario LoadScenario) *RunTimer {
	if scenario.WaitTimeout == 0 {
		scenario.WaitTimeout = 30 * time.Second
	}
	return &RunTimer{
		scenario: scenario,
		start:    time.Now(),
	}
}

// Finish finalises the run and computes aggregate stats.
func (rt *RunTimer) Finish(totalEvents int) RunStats {
	if totalEvents == 0 {
		totalEvents = rt.scenario.TotalEvents
	}

	finished := time.Now()
	duration := finished.Sub(rt.start)
	if duration <= 0 {
		duration = time.Millisecond
	}
	eventsPerSecond := float64(totalEvents) / duration.Seconds()

	return RunStats{
		Scenario:        rt.scenario,
		Started:         rt.start,
		Completed:       finished,
		Duration:        duration,
		TotalEvents:     totalEvents,
		EventsPerSecond: eventsPerSecond,
		EventsPerMinute: eventsPerSecond * 60.0,
	}
}

// Log outputs a concise, human-readable summary of the run.
func (rs RunStats) Log(t *testing.T) {
	t.Helper()
	t.Logf("%s: processed %d events in %s (%.1f events/sec, %.0f events/min)",
		rs.Scenario.Name,
		rs.TotalEvents,
		rs.Duration.Truncate(10*time.Millisecond),
		rs.EventsPerSecond,
		rs.EventsPerMinute,
	)
}

// QueueDepth returns the approximate number of visible messages in the queue.
func QueueDepth(ctx context.Context, client *sqs.Client, queueURL string) (int64, error) {
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return 0, err
	}

	val, ok := out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]
	if !ok {
		return 0, fmt.Errorf("attribute %s not returned", types.QueueAttributeNameApproximateNumberOfMessages)
	}
	depth, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse queue depth: %w", err)
	}

	inFlightVal, ok := out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)]
	if ok {
		inFlight, parseErr := strconv.ParseInt(inFlightVal, 10, 64)
		if parseErr == nil {
			depth += inFlight
		}
	}

	return depth, nil
}

// ScenarioWithWorkers adjusts the scenario name to reflect worker counts for log readability.
func ScenarioWithWorkers(base LoadScenario, workers int) LoadScenario {
	copy := base
	if base.Name == "" {
		copy.Name = fmt.Sprintf("%d-workers", workers)
	} else {
		copy.Name = fmt.Sprintf("%s-%d-workers", base.Name, workers)
	}
	return copy
}
