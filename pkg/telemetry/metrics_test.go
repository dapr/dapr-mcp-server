package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newManualMeterProvider returns a provider whose data is read on demand from the returned reader.
func newManualMeterProvider(t *testing.T) (*sdkmetric.MeterProvider, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider, reader
}

// collectMetric returns the named metric, failing the test when it is absent.
func collectMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	require.Failf(t, "metric not found", "%s", name)
	return metricdata.Metrics{}
}

// hasMetric reports whether the named metric has any data points.
func hasMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return true
			}
		}
	}
	return false
}

func sumPoints(t *testing.T, m metricdata.Metrics) []metricdata.DataPoint[int64] {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "%s is not an int64 sum", m.Name)
	return sum.DataPoints
}

func histogramPoints(t *testing.T, m metricdata.Metrics) []metricdata.HistogramDataPoint[float64] {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "%s is not a float64 histogram", m.Name)
	return h.DataPoints
}

func attrValue(set attribute.Set, key string) (string, bool) {
	v, ok := set.Value(attribute.Key(key))
	if !ok {
		return "", false
	}
	return v.Emit(), true
}

func newTestToolMetrics(t *testing.T) (*ToolMetrics, *sdkmetric.ManualReader) {
	t.Helper()
	provider, reader := newManualMeterProvider(t)
	m, err := newToolMetrics(provider.Meter("test"))
	require.NoError(t, err)
	return m, reader
}

func TestNewToolMetrics(t *testing.T) {
	m, err := NewToolMetrics()

	require.NoError(t, err)
	require.NotNil(t, m)
	assert.NotNil(t, m.invocations)
	assert.NotNil(t, m.errors)
	assert.NotNil(t, m.duration)
	assert.NotNil(t, m.inProgress)
}

func TestToolMetricsRecordAttributes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		inv           ToolInvocation
		wantComponent bool
		wantOutcome   bool
	}{
		{
			name:          "all fields",
			inv:           ToolInvocation{ToolName: "save_state", ToolPackage: "state", ComponentType: "state.redis", Outcome: OutcomeSuccess},
			wantComponent: true,
			wantOutcome:   true,
		},
		{
			name: "optional fields empty",
			inv:  ToolInvocation{ToolName: "get_state", ToolPackage: "state"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, reader := newTestToolMetrics(t)
			ctx := context.Background()
			m.RecordInvocation(ctx, tt.inv)
			m.RecordDuration(ctx, tt.inv, 12.5)
			m.RecordError(ctx, tt.inv, "connection_error")

			inv := sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.invocations"))
			require.Len(t, inv, 1)
			assert.Equal(t, int64(1), inv[0].Value)
			name, _ := attrValue(inv[0].Attributes, attrToolName)
			assert.Equal(t, tt.inv.ToolName, name)
			_, hasComponent := attrValue(inv[0].Attributes, attrComponentType)
			assert.Equal(t, tt.wantComponent, hasComponent)
			_, hasOutcome := attrValue(inv[0].Attributes, attrOutcome)
			assert.Equal(t, tt.wantOutcome, hasOutcome)

			dur := histogramPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.duration"))
			require.Len(t, dur, 1)
			assert.InDelta(t, 12.5, dur[0].Sum, 1e-9)

			errs := sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.errors"))
			require.Len(t, errs, 1)
			errType, _ := attrValue(errs[0].Attributes, attrErrorType)
			assert.Equal(t, "connection_error", errType)
			_, errHasOutcome := attrValue(errs[0].Attributes, attrOutcome)
			assert.False(t, errHasOutcome)
		})
	}
}

func TestTimerRecordsOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		outcome    string
		wantErrors bool
	}{
		{name: "success", outcome: OutcomeSuccess},
		{name: "error", outcome: OutcomeError, wantErrors: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, reader := newTestToolMetrics(t)
			timer := m.StartTimer(context.Background(), "tool", "pkg")

			inProgress := sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.in_progress"))
			require.Len(t, inProgress, 1)
			assert.Equal(t, int64(1), inProgress[0].Value)

			time.Sleep(2 * time.Millisecond)
			timer.Stop(tt.outcome, "state.redis")
			timer.Stop(tt.outcome, "state.redis")

			inProgress = sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.in_progress"))
			require.Len(t, inProgress, 1)
			assert.Equal(t, int64(0), inProgress[0].Value, "a second Stop must not decrement again")

			inv := sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.invocations"))
			require.Len(t, inv, 1)
			assert.Equal(t, int64(1), inv[0].Value)
			outcome, _ := attrValue(inv[0].Attributes, attrOutcome)
			assert.Equal(t, tt.outcome, outcome)

			dur := histogramPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.duration"))
			require.Len(t, dur, 1)
			assert.Equal(t, uint64(1), dur[0].Count)
			assert.GreaterOrEqual(t, dur[0].Sum, 2.0)

			assert.Equal(t, tt.wantErrors, hasMetric(t, reader, "dapr-mcp-server.tool.errors"))
		})
	}
}

func TestTimerSurvivesCanceledContext(t *testing.T) {
	t.Parallel()

	m, reader := newTestToolMetrics(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := m.StartTimer(ctx, "tool", "pkg")
	cancel()
	timer.Stop(OutcomeSuccess, "")

	inv := sumPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.invocations"))
	require.Len(t, inv, 1)
	assert.Equal(t, int64(1), inv[0].Value)
}

func TestTimerDurationHasSubMillisecondPrecision(t *testing.T) {
	t.Parallel()

	m, reader := newTestToolMetrics(t)
	timer := m.StartTimer(context.Background(), "tool", "pkg")
	timer.Stop(OutcomeSuccess, "")

	dur := histogramPoints(t, collectMetric(t, reader, "dapr-mcp-server.tool.duration"))
	require.Len(t, dur, 1)
	assert.Greater(t, dur[0].Sum, 0.0, "fast calls must not truncate to 0 ms")
}

func TestNilToolMetricsIsNoOp(t *testing.T) {
	t.Parallel()

	var m *ToolMetrics
	ctx := context.Background()
	inv := ToolInvocation{ToolName: "t", ToolPackage: "p"}

	assert.NotPanics(t, func() {
		m.RecordInvocation(ctx, inv)
		m.RecordError(ctx, inv, "x")
		m.RecordDuration(ctx, inv, 1)
		m.StartInProgress(ctx, "t", "p")
		m.EndInProgress(ctx, "t", "p")

		timer := m.StartTimer(ctx, "t", "p")
		require.NotNil(t, timer)
		timer.Stop(OutcomeError, "")
		timer.Stop(OutcomeError, "")

		var nilTimer *Timer
		nilTimer.Stop(OutcomeSuccess, "")
	})
}
