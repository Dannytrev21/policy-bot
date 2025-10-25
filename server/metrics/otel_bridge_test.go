package metrics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/palantir/go-githubapp/githubapp"
	gometrics "github.com/rcrowley/go-metrics"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestBridgeExportsSchedulerMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := gometrics.NewRegistry()

	dropped := gometrics.NewRegisteredCounter(githubapp.MetricsKeyDroppedEvents, registry)
	dropped.Inc(5)

	queueGauge := gometrics.NewGauge()
	require.NoError(t, registry.Register(githubapp.MetricsKeyQueueLength, queueGauge))
	queueGauge.Update(7)

	workerGauge := gometrics.NewGauge()
	require.NoError(t, registry.Register(githubapp.MetricsKeyActiveWorkers, workerGauge))
	workerGauge.Update(3)

	histSample := gometrics.NewUniformSample(1024)
	hist := gometrics.NewHistogram(histSample)
	require.NoError(t, registry.Register(githubapp.MetricsKeyEventAge, hist))
	for _, v := range []int64{10, 20, 30, 40, 50} {
		hist.Update(v)
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(ctx))
	})

	meter := provider.Meter("test")
	bridge, err := NewBridge(meter, registry)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, bridge.Shutdown(ctx))
	})

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	require.Equal(t, int64(5), findInt64Sum(t, rm, githubapp.MetricsKeyDroppedEvents))
	require.Equal(t, int64(7), findInt64Gauge(t, rm, githubapp.MetricsKeyQueueLength))
	require.Equal(t, int64(3), findInt64Gauge(t, rm, githubapp.MetricsKeyActiveWorkers))

	mean := findFloat64Gauge(t, rm, eventAgeMeanInstrument)
	require.InDelta(t, 30.0, mean, 0.01)

	p95 := findFloat64Gauge(t, rm, eventAgeP95Instrument)
	require.InDelta(t, 50.0, p95, 0.01)

	max := findFloat64Gauge(t, rm, eventAgeMaxInstrument)
	require.InDelta(t, 50.0, max, 0.01)

	require.Equal(t, int64(5), findInt64Sum(t, rm, eventAgeCountInstrument))
}

func TestBridgeExportsInstallationClientMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := gometrics.NewRegistry()

	// Metric keys for installation client creation
	const (
		MetricsKeyInstallationClientSuccess   = "installation.client.success"
		MetricsKeyInstallationClientFailure   = "installation.client.failure"
		MetricsKeyInstallationV4ClientSuccess = "installation.v4client.success"
		MetricsKeyInstallationV4ClientFailure = "installation.v4client.failure"
	)

	// Create and populate counters
	v3Success := gometrics.NewRegisteredCounter(MetricsKeyInstallationClientSuccess, registry)
	v3Success.Inc(10)

	v3Failure := gometrics.NewRegisteredCounter(MetricsKeyInstallationClientFailure, registry)
	v3Failure.Inc(2)

	v4Success := gometrics.NewRegisteredCounter(MetricsKeyInstallationV4ClientSuccess, registry)
	v4Success.Inc(8)

	v4Failure := gometrics.NewRegisteredCounter(MetricsKeyInstallationV4ClientFailure, registry)
	v4Failure.Inc(1)

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(ctx))
	})

	meter := provider.Meter("test")
	bridge, err := NewBridge(meter, registry)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, bridge.Shutdown(ctx))
	})

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	// Verify all installation client metrics are exported
	require.Equal(t, int64(10), findInt64Sum(t, rm, MetricsKeyInstallationClientSuccess))
	require.Equal(t, int64(2), findInt64Sum(t, rm, MetricsKeyInstallationClientFailure))
	require.Equal(t, int64(8), findInt64Sum(t, rm, MetricsKeyInstallationV4ClientSuccess))
	require.Equal(t, int64(1), findInt64Sum(t, rm, MetricsKeyInstallationV4ClientFailure))
}

func findInt64Sum(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	var value int64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s not reported as int64 sum", name)
			require.Len(t, sum.DataPoints, 1, "expected single datapoint for %s", name)
			value = sum.DataPoints[0].Value
			found = true
		}
	}

	require.True(t, found, "metric %s not found", name)
	return value
}

func findInt64Gauge(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	var value int64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			gauge, ok := metric.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "metric %s not reported as int64 gauge", name)
			require.Len(t, gauge.DataPoints, 1, "expected single datapoint for %s", name)
			value = gauge.DataPoints[0].Value
			found = true
		}
	}

	require.True(t, found, "metric %s not found", name)
	return value
}

func findFloat64Gauge(t *testing.T, rm metricdata.ResourceMetrics, name string) float64 {
	t.Helper()
	var value float64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			gauge, ok := metric.Data.(metricdata.Gauge[float64])
			require.True(t, ok, "metric %s not reported as float64 gauge", name)
			require.Len(t, gauge.DataPoints, 1, "expected single datapoint for %s", name)
			value = gauge.DataPoints[0].Value
			found = true
		}
	}

	require.True(t, found, "metric %s not found", name)
	if math.IsNaN(value) {
		t.Fatalf("metric %s reported NaN", name)
	}
	return value
}

func TestBridgeExportsSQSMessageMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := gometrics.NewRegistry()

	// Create SQS message processing metrics for multiple event types
	processedPR := gometrics.NewRegisteredCounter("sqs.messages.processed.pull_request", registry)
	processedPR.Inc(100)

	processedStatus := gometrics.NewRegisteredCounter("sqs.messages.processed.status", registry)
	processedStatus.Inc(50)

	failedPR := gometrics.NewRegisteredCounter("sqs.messages.failed.pull_request", registry)
	failedPR.Inc(5)

	dlqGauge := gometrics.NewRegisteredGauge("sqs.dlq.messages.pull_request", registry)
	dlqGauge.Update(2)

	// Create a timer for processing time
	processingTimer := gometrics.NewRegisteredTimer("sqs.processing.time.pull_request", registry)
	for _, v := range []int64{1000000, 2000000, 3000000, 4000000, 5000000} { // nanoseconds
		processingTimer.Update(time.Duration(v))
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(ctx))
	})

	meter := provider.Meter("test")
	bridge, err := NewBridge(meter, registry)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, bridge.Shutdown(ctx))
	})

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	// Verify SQS message metrics are exported with attributes
	require.Equal(t, int64(100), findInt64SumWithAttribute(t, rm, "sqs.messages.processed", "event_type", "pull_request"))
	require.Equal(t, int64(50), findInt64SumWithAttribute(t, rm, "sqs.messages.processed", "event_type", "status"))
	require.Equal(t, int64(5), findInt64SumWithAttribute(t, rm, "sqs.messages.failed", "event_type", "pull_request"))
	require.Equal(t, int64(2), findInt64GaugeWithAttribute(t, rm, "sqs.dlq.messages", "event_type", "pull_request"))

	// Verify processing time metrics (converted from nanoseconds to milliseconds)
	mean := findFloat64GaugeWithAttribute(t, rm, "sqs.processing.time.mean_ms", "event_type", "pull_request")
	require.InDelta(t, 3.0, mean, 0.01) // 3ms average

	count := findInt64SumWithAttribute(t, rm, "sqs.processing.time.count", "event_type", "pull_request")
	require.Equal(t, int64(5), count)
}

func TestBridgeExportsSQSWorkerPoolMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	registry := gometrics.NewRegistry()

	// Create SQS worker pool metrics
	activeWorkers := gometrics.NewRegisteredGauge("sqs.worker_pool.active_workers.pull_request", registry)
	activeWorkers.Update(5)

	poolCapacity := gometrics.NewRegisteredGauge("sqs.worker_pool.capacity.pull_request", registry)
	poolCapacity.Update(10)

	utilization := gometrics.NewRegisteredGaugeFloat64("sqs.worker_pool.utilization.pull_request", registry)
	utilization.Update(50.0)

	rejected := gometrics.NewRegisteredCounter("sqs.worker_pool.rejected_total.pull_request", registry)
	rejected.Inc(3)

	panics := gometrics.NewRegisteredCounter("sqs.worker_pool.panics_total.pull_request", registry)
	panics.Inc(1)

	// Create worker pool processing time timer
	poolTimer := gometrics.NewRegisteredTimer("sqs.worker_pool.processing_time.pull_request", registry)
	for _, v := range []int64{500000, 1000000, 1500000, 2000000, 2500000} { // nanoseconds
		poolTimer.Update(time.Duration(v))
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(ctx))
	})

	meter := provider.Meter("test")
	bridge, err := NewBridge(meter, registry)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, bridge.Shutdown(ctx))
	})

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	// Verify worker pool metrics are exported with attributes
	require.Equal(t, int64(5), findInt64GaugeWithAttribute(t, rm, "sqs.worker_pool.active_workers", "event_type", "pull_request"))
	require.Equal(t, int64(10), findInt64GaugeWithAttribute(t, rm, "sqs.worker_pool.capacity", "event_type", "pull_request"))
	require.InDelta(t, 50.0, findFloat64GaugeWithAttribute(t, rm, "sqs.worker_pool.utilization", "event_type", "pull_request"), 0.01)
	require.Equal(t, int64(3), findInt64SumWithAttribute(t, rm, "sqs.worker_pool.rejected_total", "event_type", "pull_request"))
	require.Equal(t, int64(1), findInt64SumWithAttribute(t, rm, "sqs.worker_pool.panics_total", "event_type", "pull_request"))

	// Verify worker pool processing time metrics
	poolMean := findFloat64GaugeWithAttribute(t, rm, "sqs.worker_pool.processing_time.mean_ms", "event_type", "pull_request")
	require.InDelta(t, 1.5, poolMean, 0.01) // 1.5ms average
}

// Helper function to find int64 sum with specific attribute
func findInt64SumWithAttribute(t *testing.T, rm metricdata.ResourceMetrics, name, attrKey, attrValue string) int64 {
	t.Helper()
	var value int64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s not reported as int64 sum", name)

			for _, dp := range sum.DataPoints {
				for _, attr := range dp.Attributes.ToSlice() {
					if string(attr.Key) == attrKey && attr.Value.AsString() == attrValue {
						value = dp.Value
						found = true
						break
					}
				}
			}
		}
	}

	require.True(t, found, "metric %s with attribute %s=%s not found", name, attrKey, attrValue)
	return value
}

// Helper function to find int64 gauge with specific attribute
func findInt64GaugeWithAttribute(t *testing.T, rm metricdata.ResourceMetrics, name, attrKey, attrValue string) int64 {
	t.Helper()
	var value int64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			gauge, ok := metric.Data.(metricdata.Gauge[int64])
			require.True(t, ok, "metric %s not reported as int64 gauge", name)

			for _, dp := range gauge.DataPoints {
				for _, attr := range dp.Attributes.ToSlice() {
					if string(attr.Key) == attrKey && attr.Value.AsString() == attrValue {
						value = dp.Value
						found = true
						break
					}
				}
			}
		}
	}

	require.True(t, found, "metric %s with attribute %s=%s not found", name, attrKey, attrValue)
	return value
}

// Helper function to find float64 gauge with specific attribute
func findFloat64GaugeWithAttribute(t *testing.T, rm metricdata.ResourceMetrics, name, attrKey, attrValue string) float64 {
	t.Helper()
	var value float64
	var found bool

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}
			gauge, ok := metric.Data.(metricdata.Gauge[float64])
			require.True(t, ok, "metric %s not reported as float64 gauge", name)

			for _, dp := range gauge.DataPoints {
				for _, attr := range dp.Attributes.ToSlice() {
					if string(attr.Key) == attrKey && attr.Value.AsString() == attrValue {
						value = dp.Value
						found = true
						break
					}
				}
			}
		}
	}

	require.True(t, found, "metric %s with attribute %s=%s not found", name, attrKey, attrValue)
	if math.IsNaN(value) {
		t.Fatalf("metric %s reported NaN", name)
	}
	return value
}
