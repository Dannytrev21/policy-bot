package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/palantir/go-githubapp/githubapp"
	gometrics "github.com/rcrowley/go-metrics"
	"go.opentelemetry.io/otel/attribute"
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

	if err := b.registerInstallationClientMetrics(m); err != nil {
		return nil, err
	}

	if err := b.registerInstallationFilterMetrics(m); err != nil {
		return nil, err
	}

	if err := b.registerInstallationRegistryMetrics(m); err != nil {
		return nil, err
	}

	if err := b.registerSQSMetrics(m); err != nil {
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

func (b *Bridge) registerInstallationClientMetrics(m metric.Meter) error {
	// Import metric keys from handler package
	const (
		MetricsKeyInstallationClientSuccess          = "installation.client.success"
		MetricsKeyInstallationClientFailure          = "installation.client.failure"
		MetricsKeyInstallationV4ClientSuccess        = "installation.v4client.success"
		MetricsKeyInstallationV4ClientFailure        = "installation.v4client.failure"
		MetricsKeyInstallationClientRetrySuccess     = "installation.client.retry_success"
		MetricsKeyInstallationClientRetryExhausted   = "installation.client.retry_exhausted"
		MetricsKeyInstallationV4ClientRetrySuccess   = "installation.v4client.retry_success"
		MetricsKeyInstallationV4ClientRetryExhausted = "installation.v4client.retry_exhausted"
		MetricsKeyCircuitBreakerOpened               = "installation.circuit_breaker.opened_total"
		MetricsKeyCircuitBreakerClosed               = "installation.circuit_breaker.closed_total"
		MetricsKeyCircuitBreakerState                = "installation.circuit_breaker.state"
	)

	clientSuccess, err := m.Int64ObservableCounter(
		MetricsKeyInstallationClientSuccess,
		metric.WithDescription("Total successful GitHub installation client (v3) creations"),
		metric.WithUnit("{client}"),
	)
	if err != nil {
		return fmt.Errorf("create installation client success counter: %w", err)
	}

	clientFailure, err := m.Int64ObservableCounter(
		MetricsKeyInstallationClientFailure,
		metric.WithDescription("Total failed GitHub installation client (v3) creations"),
		metric.WithUnit("{client}"),
	)
	if err != nil {
		return fmt.Errorf("create installation client failure counter: %w", err)
	}

	v4ClientSuccess, err := m.Int64ObservableCounter(
		MetricsKeyInstallationV4ClientSuccess,
		metric.WithDescription("Total successful GitHub installation v4 client (GraphQL) creations"),
		metric.WithUnit("{client}"),
	)
	if err != nil {
		return fmt.Errorf("create installation v4 client success counter: %w", err)
	}

	v4ClientFailure, err := m.Int64ObservableCounter(
		MetricsKeyInstallationV4ClientFailure,
		metric.WithDescription("Total failed GitHub installation v4 client (GraphQL) creations"),
		metric.WithUnit("{client}"),
	)
	if err != nil {
		return fmt.Errorf("create installation v4 client failure counter: %w", err)
	}

	clientRetrySuccess, err := m.Int64ObservableCounter(
		MetricsKeyInstallationClientRetrySuccess,
		metric.WithDescription("Total successful retries for GitHub installation client (v3) creations"),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return fmt.Errorf("create installation client retry success counter: %w", err)
	}

	clientRetryExhausted, err := m.Int64ObservableCounter(
		MetricsKeyInstallationClientRetryExhausted,
		metric.WithDescription("Total exhausted retries for GitHub installation client (v3) creations"),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return fmt.Errorf("create installation client retry exhausted counter: %w", err)
	}

	v4ClientRetrySuccess, err := m.Int64ObservableCounter(
		MetricsKeyInstallationV4ClientRetrySuccess,
		metric.WithDescription("Total successful retries for GitHub installation v4 client (GraphQL) creations"),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return fmt.Errorf("create installation v4 client retry success counter: %w", err)
	}

	v4ClientRetryExhausted, err := m.Int64ObservableCounter(
		MetricsKeyInstallationV4ClientRetryExhausted,
		metric.WithDescription("Total exhausted retries for GitHub installation v4 client (GraphQL) creations"),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return fmt.Errorf("create installation v4 client retry exhausted counter: %w", err)
	}

	circuitBreakerOpened, err := m.Int64ObservableCounter(
		MetricsKeyCircuitBreakerOpened,
		metric.WithDescription("Total times circuit breaker opened due to consecutive failures"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create circuit breaker opened counter: %w", err)
	}

	circuitBreakerClosed, err := m.Int64ObservableCounter(
		MetricsKeyCircuitBreakerClosed,
		metric.WithDescription("Total times circuit breaker closed after successful recovery"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create circuit breaker closed counter: %w", err)
	}

	circuitBreakerState, err := m.Int64ObservableGauge(
		MetricsKeyCircuitBreakerState,
		metric.WithDescription("Current circuit breaker state (0=closed, 1=open, 2=half-open)"),
		metric.WithUnit("{state}"),
	)
	if err != nil {
		return fmt.Errorf("create circuit breaker state gauge: %w", err)
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationClientSuccess); ok {
			observer.ObserveInt64(clientSuccess, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationClientFailure); ok {
			observer.ObserveInt64(clientFailure, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationV4ClientSuccess); ok {
			observer.ObserveInt64(v4ClientSuccess, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationV4ClientFailure); ok {
			observer.ObserveInt64(v4ClientFailure, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationClientRetrySuccess); ok {
			observer.ObserveInt64(clientRetrySuccess, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationClientRetryExhausted); ok {
			observer.ObserveInt64(clientRetryExhausted, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationV4ClientRetrySuccess); ok {
			observer.ObserveInt64(v4ClientRetrySuccess, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyInstallationV4ClientRetryExhausted); ok {
			observer.ObserveInt64(v4ClientRetryExhausted, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyCircuitBreakerOpened); ok {
			observer.ObserveInt64(circuitBreakerOpened, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyCircuitBreakerClosed); ok {
			observer.ObserveInt64(circuitBreakerClosed, counter.Count())
		}

		if gauge, ok := getMetric[gometrics.Gauge](b.registry, MetricsKeyCircuitBreakerState); ok {
			observer.ObserveInt64(circuitBreakerState, gauge.Value())
		}

		return nil
	}

	registration, err := m.RegisterCallback(
		callback,
		clientSuccess,
		clientFailure,
		v4ClientSuccess,
		v4ClientFailure,
		clientRetrySuccess,
		clientRetryExhausted,
		v4ClientRetrySuccess,
		v4ClientRetryExhausted,
		circuitBreakerOpened,
		circuitBreakerClosed,
		circuitBreakerState,
	)
	if err != nil {
		return fmt.Errorf("register installation client metrics callback: %w", err)
	}
	b.registrations = append(b.registrations, registration)

	return nil
}

func (b *Bridge) registerInstallationFilterMetrics(m metric.Meter) error {
	// Import metric keys from handler package
	const (
		MetricsKeyFilterEventsFiltered    = "installation.filter.events_filtered_total"
		MetricsKeyFilterEventsPassed      = "installation.filter.events_passed_total"
		MetricsKeyFilterCacheHitsPositive = "installation.filter.cache_hits.positive"
		MetricsKeyFilterCacheHitsNegative = "installation.filter.cache_hits.negative"
	)

	eventsFiltered, err := m.Int64ObservableCounter(
		MetricsKeyFilterEventsFiltered,
		metric.WithDescription("Total events filtered by InstallationFilterHandler (app not installed)"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create filter events filtered counter: %w", err)
	}

	eventsPassed, err := m.Int64ObservableCounter(
		MetricsKeyFilterEventsPassed,
		metric.WithDescription("Total events passed through by InstallationFilterHandler"),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return fmt.Errorf("create filter events passed counter: %w", err)
	}

	cacheHitsPositive, err := m.Int64ObservableCounter(
		MetricsKeyFilterCacheHitsPositive,
		metric.WithDescription("Total installation registry cache hits for installed status"),
		metric.WithUnit("{hit}"),
	)
	if err != nil {
		return fmt.Errorf("create filter cache hits positive counter: %w", err)
	}

	cacheHitsNegative, err := m.Int64ObservableCounter(
		MetricsKeyFilterCacheHitsNegative,
		metric.WithDescription("Total installation registry cache hits for not-installed status"),
		metric.WithUnit("{hit}"),
	)
	if err != nil {
		return fmt.Errorf("create filter cache hits negative counter: %w", err)
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyFilterEventsFiltered); ok {
			observer.ObserveInt64(eventsFiltered, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyFilterEventsPassed); ok {
			observer.ObserveInt64(eventsPassed, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyFilterCacheHitsPositive); ok {
			observer.ObserveInt64(cacheHitsPositive, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyFilterCacheHitsNegative); ok {
			observer.ObserveInt64(cacheHitsNegative, counter.Count())
		}

		return nil
	}

	registration, err := m.RegisterCallback(
		callback,
		eventsFiltered,
		eventsPassed,
		cacheHitsPositive,
		cacheHitsNegative,
	)
	if err != nil {
		return fmt.Errorf("register installation filter metrics callback: %w", err)
	}
	b.registrations = append(b.registrations, registration)

	return nil
}

func (b *Bridge) registerInstallationRegistryMetrics(m metric.Meter) error {
	// Import metric keys from handler package
	const (
		MetricsKeyRegistryCacheHits     = "installation.registry.cache_hits_total"
		MetricsKeyRegistryCacheMisses   = "installation.registry.cache_misses_total"
		MetricsKeyRegistryAPICalls      = "installation.registry.api_calls_total"
		MetricsKeyRegistryCacheSize     = "installation.registry.cache_size"
		MetricsKeyRegistryPositiveCache = "installation.registry.positive_entries"
		MetricsKeyRegistryNegativeCache = "installation.registry.negative_entries"
	)

	cacheHits, err := m.Int64ObservableCounter(
		MetricsKeyRegistryCacheHits,
		metric.WithDescription("Total installation registry cache hits"),
		metric.WithUnit("{hit}"),
	)
	if err != nil {
		return fmt.Errorf("create registry cache hits counter: %w", err)
	}

	cacheMisses, err := m.Int64ObservableCounter(
		MetricsKeyRegistryCacheMisses,
		metric.WithDescription("Total installation registry cache misses"),
		metric.WithUnit("{miss}"),
	)
	if err != nil {
		return fmt.Errorf("create registry cache misses counter: %w", err)
	}

	apiCalls, err := m.Int64ObservableCounter(
		MetricsKeyRegistryAPICalls,
		metric.WithDescription("Total GitHub API calls made for installation verification"),
		metric.WithUnit("{call}"),
	)
	if err != nil {
		return fmt.Errorf("create registry API calls counter: %w", err)
	}

	cacheSize, err := m.Int64ObservableGauge(
		MetricsKeyRegistryCacheSize,
		metric.WithDescription("Current number of entries in the installation registry cache"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return fmt.Errorf("create registry cache size gauge: %w", err)
	}

	positiveEntries, err := m.Int64ObservableGauge(
		MetricsKeyRegistryPositiveCache,
		metric.WithDescription("Number of positive (installed) entries in the installation registry cache"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return fmt.Errorf("create registry positive entries gauge: %w", err)
	}

	negativeEntries, err := m.Int64ObservableGauge(
		MetricsKeyRegistryNegativeCache,
		metric.WithDescription("Number of negative (not installed) entries in the installation registry cache"),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return fmt.Errorf("create registry negative entries gauge: %w", err)
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyRegistryCacheHits); ok {
			observer.ObserveInt64(cacheHits, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyRegistryCacheMisses); ok {
			observer.ObserveInt64(cacheMisses, counter.Count())
		}

		if counter, ok := getMetric[gometrics.Counter](b.registry, MetricsKeyRegistryAPICalls); ok {
			observer.ObserveInt64(apiCalls, counter.Count())
		}

		if gauge, ok := getMetric[gometrics.Gauge](b.registry, MetricsKeyRegistryCacheSize); ok {
			observer.ObserveInt64(cacheSize, gauge.Value())
		}

		if gauge, ok := getMetric[gometrics.Gauge](b.registry, MetricsKeyRegistryPositiveCache); ok {
			observer.ObserveInt64(positiveEntries, gauge.Value())
		}

		if gauge, ok := getMetric[gometrics.Gauge](b.registry, MetricsKeyRegistryNegativeCache); ok {
			observer.ObserveInt64(negativeEntries, gauge.Value())
		}

		return nil
	}

	registration, err := m.RegisterCallback(
		callback,
		cacheHits,
		cacheMisses,
		apiCalls,
		cacheSize,
		positiveEntries,
		negativeEntries,
	)
	if err != nil {
		return fmt.Errorf("register installation registry metrics callback: %w", err)
	}
	b.registrations = append(b.registrations, registration)

	return nil
}

func (b *Bridge) registerSQSMetrics(m metric.Meter) error {
	// Import metric key prefixes from sqsconsumer package
	const (
		MetricsKeyMessagesProcessed      = "sqs.messages.processed"
		MetricsKeyMessagesFailed         = "sqs.messages.failed"
		MetricsKeyProcessingTime         = "sqs.processing.time"
		MetricsKeyDLQMessages            = "sqs.dlq.messages"
		MetricsKeyActiveWorkers          = "sqs.worker_pool.active_workers"
		MetricsKeyPoolCapacity           = "sqs.worker_pool.capacity"
		MetricsKeyPoolUtilization        = "sqs.worker_pool.utilization"
		MetricsKeyPoolRejected           = "sqs.worker_pool.rejected_total"
		MetricsKeyPoolProcessingTime     = "sqs.worker_pool.processing_time"
		MetricsKeyPoolPanics             = "sqs.worker_pool.panics_total"
	)

	// Message Processing Metrics
	messagesProcessed, err := m.Int64ObservableCounter(
		MetricsKeyMessagesProcessed,
		metric.WithDescription("Total SQS messages successfully processed by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs messages processed counter: %w", err)
	}

	messagesFailed, err := m.Int64ObservableCounter(
		MetricsKeyMessagesFailed,
		metric.WithDescription("Total SQS messages that failed processing by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs messages failed counter: %w", err)
	}

	processingTimeMean, err := m.Float64ObservableGauge(
		MetricsKeyProcessingTime+".mean_ms",
		metric.WithDescription("Average SQS message processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs processing time mean gauge: %w", err)
	}

	processingTimeP95, err := m.Float64ObservableGauge(
		MetricsKeyProcessingTime+".p95_ms",
		metric.WithDescription("95th percentile SQS message processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs processing time p95 gauge: %w", err)
	}

	processingTimeMax, err := m.Float64ObservableGauge(
		MetricsKeyProcessingTime+".max_ms",
		metric.WithDescription("Maximum SQS message processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs processing time max gauge: %w", err)
	}

	processingTimeCount, err := m.Int64ObservableCounter(
		MetricsKeyProcessingTime+".count",
		metric.WithDescription("Total SQS messages sampled for processing time by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs processing time count counter: %w", err)
	}

	dlqMessages, err := m.Int64ObservableGauge(
		MetricsKeyDLQMessages,
		metric.WithDescription("Current number of messages in SQS Dead Letter Queue by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs dlq messages gauge: %w", err)
	}

	// Worker Pool Metrics
	activeWorkers, err := m.Int64ObservableGauge(
		MetricsKeyActiveWorkers,
		metric.WithDescription("Current number of active SQS worker pool workers by event type"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs active workers gauge: %w", err)
	}

	poolCapacity, err := m.Int64ObservableGauge(
		MetricsKeyPoolCapacity,
		metric.WithDescription("Maximum capacity of SQS worker pool by event type"),
		metric.WithUnit("{worker}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool capacity gauge: %w", err)
	}

	poolUtilization, err := m.Float64ObservableGauge(
		MetricsKeyPoolUtilization,
		metric.WithDescription("Current utilization percentage of SQS worker pool by event type"),
		metric.WithUnit("{percent}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool utilization gauge: %w", err)
	}

	poolRejected, err := m.Int64ObservableCounter(
		MetricsKeyPoolRejected,
		metric.WithDescription("Total SQS messages rejected due to worker pool saturation by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool rejected counter: %w", err)
	}

	poolProcessingTimeMean, err := m.Float64ObservableGauge(
		MetricsKeyPoolProcessingTime+".mean_ms",
		metric.WithDescription("Average SQS worker pool processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool processing time mean gauge: %w", err)
	}

	poolProcessingTimeP95, err := m.Float64ObservableGauge(
		MetricsKeyPoolProcessingTime+".p95_ms",
		metric.WithDescription("95th percentile SQS worker pool processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool processing time p95 gauge: %w", err)
	}

	poolProcessingTimeMax, err := m.Float64ObservableGauge(
		MetricsKeyPoolProcessingTime+".max_ms",
		metric.WithDescription("Maximum SQS worker pool processing time by event type"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool processing time max gauge: %w", err)
	}

	poolProcessingTimeCount, err := m.Int64ObservableCounter(
		MetricsKeyPoolProcessingTime+".count",
		metric.WithDescription("Total SQS messages sampled for worker pool processing time by event type"),
		metric.WithUnit("{message}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool processing time count counter: %w", err)
	}

	poolPanics, err := m.Int64ObservableCounter(
		MetricsKeyPoolPanics,
		metric.WithDescription("Total panics recovered in SQS worker pool by event type"),
		metric.WithUnit("{panic}"),
	)
	if err != nil {
		return fmt.Errorf("create sqs pool panics counter: %w", err)
	}

	callback := func(ctx context.Context, observer metric.Observer) error {
		// Helper to extract event type from metric name (e.g., "sqs.messages.processed.pull_request" -> "pull_request")
		extractEventType := func(metricName, prefix string) string {
			if len(metricName) > len(prefix)+1 {
				return metricName[len(prefix)+1:] // +1 for the dot separator
			}
			return ""
		}

		// Iterate through all metrics in the registry to find SQS metrics
		b.registry.Each(func(name string, registryMetric interface{}) {
			eventType := ""

			// Message Processing Metrics
			if strings.HasPrefix(name, MetricsKeyMessagesProcessed+".") {
				eventType = extractEventType(name, MetricsKeyMessagesProcessed)
				if counter, ok := registryMetric.(gometrics.Counter); ok {
					observer.ObserveInt64(messagesProcessed, counter.Count(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyMessagesFailed+".") {
				eventType = extractEventType(name, MetricsKeyMessagesFailed)
				if counter, ok := registryMetric.(gometrics.Counter); ok {
					observer.ObserveInt64(messagesFailed, counter.Count(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyProcessingTime+".") && !strings.Contains(name, ".mean_ms") && !strings.Contains(name, ".p95_ms") && !strings.Contains(name, ".max_ms") && !strings.Contains(name, ".count") {
				eventType = extractEventType(name, MetricsKeyProcessingTime)
				if timer, ok := registryMetric.(gometrics.Timer); ok {
					snapshot := timer.Snapshot()
					count := snapshot.Count()
					if count > 0 {
						attrs := metric.WithAttributes(attribute.String("event_type", eventType))
						observer.ObserveFloat64(processingTimeMean, snapshot.Mean()/1e6, attrs) // Convert to milliseconds
						observer.ObserveFloat64(processingTimeMax, float64(snapshot.Max())/1e6, attrs)
						percentiles := snapshot.Percentiles([]float64{0.95})
						if len(percentiles) == 1 {
							observer.ObserveFloat64(processingTimeP95, percentiles[0]/1e6, attrs)
						}
						observer.ObserveInt64(processingTimeCount, count, attrs)
					}
				}
			} else if strings.HasPrefix(name, MetricsKeyDLQMessages+".") {
				eventType = extractEventType(name, MetricsKeyDLQMessages)
				if gauge, ok := registryMetric.(gometrics.Gauge); ok {
					observer.ObserveInt64(dlqMessages, gauge.Value(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			}

			// Worker Pool Metrics
			if strings.HasPrefix(name, MetricsKeyActiveWorkers+".") {
				eventType = extractEventType(name, MetricsKeyActiveWorkers)
				if gauge, ok := registryMetric.(gometrics.Gauge); ok {
					observer.ObserveInt64(activeWorkers, gauge.Value(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyPoolCapacity+".") {
				eventType = extractEventType(name, MetricsKeyPoolCapacity)
				if gauge, ok := registryMetric.(gometrics.Gauge); ok {
					observer.ObserveInt64(poolCapacity, gauge.Value(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyPoolUtilization+".") {
				eventType = extractEventType(name, MetricsKeyPoolUtilization)
				if gauge, ok := registryMetric.(gometrics.GaugeFloat64); ok {
					observer.ObserveFloat64(poolUtilization, gauge.Value(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyPoolRejected+".") {
				eventType = extractEventType(name, MetricsKeyPoolRejected)
				if counter, ok := registryMetric.(gometrics.Counter); ok {
					observer.ObserveInt64(poolRejected, counter.Count(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			} else if strings.HasPrefix(name, MetricsKeyPoolProcessingTime+".") && !strings.Contains(name, ".mean_ms") && !strings.Contains(name, ".p95_ms") && !strings.Contains(name, ".max_ms") && !strings.Contains(name, ".count") {
				eventType = extractEventType(name, MetricsKeyPoolProcessingTime)
				if timer, ok := registryMetric.(gometrics.Timer); ok {
					snapshot := timer.Snapshot()
					count := snapshot.Count()
					if count > 0 {
						attrs := metric.WithAttributes(attribute.String("event_type", eventType))
						observer.ObserveFloat64(poolProcessingTimeMean, snapshot.Mean()/1e6, attrs)
						observer.ObserveFloat64(poolProcessingTimeMax, float64(snapshot.Max())/1e6, attrs)
						percentiles := snapshot.Percentiles([]float64{0.95})
						if len(percentiles) == 1 {
							observer.ObserveFloat64(poolProcessingTimeP95, percentiles[0]/1e6, attrs)
						}
						observer.ObserveInt64(poolProcessingTimeCount, count, attrs)
					}
				}
			} else if strings.HasPrefix(name, MetricsKeyPoolPanics+".") {
				eventType = extractEventType(name, MetricsKeyPoolPanics)
				if counter, ok := registryMetric.(gometrics.Counter); ok {
					observer.ObserveInt64(poolPanics, counter.Count(), metric.WithAttributes(
						attribute.String("event_type", eventType),
					))
				}
			}
		})

		return nil
	}

	registration, err := m.RegisterCallback(
		callback,
		messagesProcessed,
		messagesFailed,
		processingTimeMean,
		processingTimeP95,
		processingTimeMax,
		processingTimeCount,
		dlqMessages,
		activeWorkers,
		poolCapacity,
		poolUtilization,
		poolRejected,
		poolProcessingTimeMean,
		poolProcessingTimeP95,
		poolProcessingTimeMax,
		poolProcessingTimeCount,
		poolPanics,
	)
	if err != nil {
		return fmt.Errorf("register sqs metrics callback: %w", err)
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
