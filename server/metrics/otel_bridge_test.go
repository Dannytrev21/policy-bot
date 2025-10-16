package metrics

import (
	"context"
	"math"
	"testing"

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
