package metrics

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/palantir/go-githubapp/githubapp"
	gometrics "github.com/rcrowley/go-metrics"
	"go.opentelemetry.io/otel/metric"
)

const (
	eventAgeMeanInstrument  = githubapp.MetricsKeyEventAge + ".mean_ms"
	eventAgeP95Instrument   = githubapp.MetricsKeyEventAge + ".p95_ms"
	eventAgeMaxInstrument   = githubapp.MetricsKeyEventAge + ".max_ms"
	eventAgeCountInstrument = githubapp.MetricsKeyEventAge + ".count"
)

// Bridge adapts scheduler metrics from a go-metrics registry to OpenTelemetry instruments.
// It registers asynchronous callbacks for the queue scheduler metrics so that the existing
// counters and gauges are exported via the configured OTEL pipeline without modifying
// the scheduler implementation.
type Bridge struct {
	registry gometrics.Registry

	mu            sync.Mutex
	registrations []metric.Registration
}

// NewBridge creates a Bridge that exports the scheduler metrics that Policy Bot relies on
// (queue depth, active workers, dropped events, and event age) into OpenTelemetry.
func NewBridge(m metric.Meter, registry gometrics.Registry) (*Bridge, error) {
	if m == nil {
		return nil, errors.New("otel metrics bridge requires a non-nil meter")
	}
	if registry == nil {
		return nil, errors.New("otel metrics bridge requires a non-nil registry")
	}

	b := &Bridge{
		registry: registry,
	}

	if err := b.registerSchedulerMetrics(m); err != nil {
		return nil, err
	}

	return b, nil
}

// Shutdown unregisters the callbacks that were installed by the bridge.
func (b *Bridge) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	registrations := b.registrations
	b.registrations = nil
	b.mu.Unlock()

	var err error
	for _, registration := range registrations {
		err = errors.Join(err, registration.Unregister())
	}
	return err
}

func (b *Bridge) registerSchedulerMetrics(m metric.Meter) error {
	dropped, err := m.Int64ObservableCounter(
		githubapp.MetricsKeyDroppedEvents,
		metric.WithDescription("Total webhook events dropped due to saturated queue"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create dropped events counter: %w", err)
	}

	queueDepth, err := m.Int64ObservableGauge(
		githubapp.MetricsKeyQueueLength,
		metric.WithDescription("Current depth of the webhook scheduler queue"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create queue depth gauge: %w", err)
	}

	activeWorkers, err := m.Int64ObservableGauge(
		githubapp.MetricsKeyActiveWorkers,
		metric.WithDescription("Number of active webhook worker goroutines"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("create active workers gauge: %w", err)
	}

	ageMean, err := m.Float64ObservableGauge(
		eventAgeMeanInstrument,
		metric.WithDescription("Average age of webhook events when dequeued"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create event age mean gauge: %w", err)
	}

	ageP95, err := m.Float64ObservableGauge(
		eventAgeP95Instrument,
		metric.WithDescription("95th percentile age of webhook events when dequeued"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create event age p95 gauge: %w", err)
	}

	ageMax, err := m.Float64ObservableGauge(
		eventAgeMaxInstrument,
		metric.WithDescription("Maximum age observed for webhook events when dequeued"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create event age max gauge: %w", err)
	}

	ageCount, err := m.Int64ObservableCounter(
		eventAgeCountInstrument,
		metric.WithDescription("Total events sampled for webhook event age"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create event age count counter: %w", err)
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		if counter, ok := getMetric[gometrics.Counter](b.registry, githubapp.MetricsKeyDroppedEvents); ok {
			observer.ObserveInt64(dropped, counter.Count())
		}

		if queue, ok := getMetric[gometrics.Gauge](b.registry, githubapp.MetricsKeyQueueLength); ok {
			observer.ObserveInt64(queueDepth, queue.Value())
		}

		if workers, ok := getMetric[gometrics.Gauge](b.registry, githubapp.MetricsKeyActiveWorkers); ok {
			observer.ObserveInt64(activeWorkers, workers.Value())
		}

		if histogram, ok := getMetric[gometrics.Histogram](b.registry, githubapp.MetricsKeyEventAge); ok {
			snapshot := histogram.Snapshot()
			count := snapshot.Count()

			if count > 0 {
				observer.ObserveFloat64(ageMean, snapshot.Mean())
				observer.ObserveFloat64(ageMax, float64(snapshot.Max()))

				const percentile = 0.95
				percentiles := snapshot.Percentiles([]float64{percentile})
				if len(percentiles) == 1 {
					observer.ObserveFloat64(ageP95, percentiles[0])
				}
			}

			observer.ObserveInt64(ageCount, count)
		}

		return nil
	}

	registration, err := m.RegisterCallback(
		callback,
		dropped,
		queueDepth,
		activeWorkers,
		ageMean,
		ageP95,
		ageMax,
		ageCount,
	)
	if err != nil {
		return fmt.Errorf("register scheduler metrics callback: %w", err)
	}
	b.registrations = append(b.registrations, registration)

	return nil
}

func getMetric[T any](registry gometrics.Registry, name string) (T, bool) {
	var zero T
	if registry == nil {
		return zero, false
	}

	value := registry.Get(name)
	if typed, ok := value.(T); ok {
		return typed, true
	}
	return zero, false
}
