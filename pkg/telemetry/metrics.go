package telemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Tool outcomes passed to Timer.Stop and recorded on ToolInvocation.Outcome.
const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
)

const (
	attrToolName      = "tool.name"
	attrToolPackage   = "tool.package"
	attrComponentType = "dapr.component.type"
	attrOutcome       = "outcome"
	attrErrorType     = "error.type"

	errorTypeExecution = "execution_error"
)

// ToolMetrics provides metrics instrumentation for tool invocations.
// A nil *ToolMetrics is valid and records nothing.
type ToolMetrics struct {
	invocations metric.Int64Counter
	errors      metric.Int64Counter
	duration    metric.Float64Histogram
	inProgress  metric.Int64UpDownCounter
}

// NewToolMetrics creates a new ToolMetrics instance on the global meter provider.
func NewToolMetrics() (*ToolMetrics, error) {
	return newToolMetrics(otel.Meter(instrumentationName))
}

func newToolMetrics(meter metric.Meter) (*ToolMetrics, error) {
	invocations, err := meter.Int64Counter(
		"dapr-mcp-server.tool.invocations",
		metric.WithDescription("Total number of tool invocations"),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool invocations counter: %w", err)
	}

	errorCounter, err := meter.Int64Counter(
		"dapr-mcp-server.tool.errors",
		metric.WithDescription("Total number of failed tool invocations"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool errors counter: %w", err)
	}

	duration, err := meter.Float64Histogram(
		"dapr-mcp-server.tool.duration",
		metric.WithDescription("Tool execution duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool duration histogram: %w", err)
	}

	inProgress, err := meter.Int64UpDownCounter(
		"dapr-mcp-server.tool.in_progress",
		metric.WithDescription("Number of tools currently executing"),
		metric.WithUnit("{tool}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool in-progress counter: %w", err)
	}

	return &ToolMetrics{
		invocations: invocations,
		errors:      errorCounter,
		duration:    duration,
		inProgress:  inProgress,
	}, nil
}

// ToolInvocation represents attributes for a tool invocation.
type ToolInvocation struct {
	ToolName      string
	ToolPackage   string
	ComponentType string
	Outcome       string
}

// attrs returns the tool identity attributes, plus the component type
// and outcome when set and includeOutcome is true.
func (inv ToolInvocation) attrs(includeOutcome bool, extra ...attribute.KeyValue) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4+len(extra))
	attrs = append(attrs,
		attribute.String(attrToolName, inv.ToolName),
		attribute.String(attrToolPackage, inv.ToolPackage),
	)
	attrs = append(attrs, extra...)
	if inv.ComponentType != "" {
		attrs = append(attrs, attribute.String(attrComponentType, inv.ComponentType))
	}
	if includeOutcome && inv.Outcome != "" {
		attrs = append(attrs, attribute.String(attrOutcome, inv.Outcome))
	}
	return attrs
}

// RecordInvocation records a tool invocation with its attributes.
func (m *ToolMetrics) RecordInvocation(ctx context.Context, inv ToolInvocation) {
	if m == nil {
		return
	}
	m.invocations.Add(ctx, 1, metric.WithAttributes(inv.attrs(true)...))
}

// RecordError records a tool error.
func (m *ToolMetrics) RecordError(ctx context.Context, inv ToolInvocation, errorType string) {
	if m == nil {
		return
	}
	m.errors.Add(ctx, 1, metric.WithAttributes(inv.attrs(false, attribute.String(attrErrorType, errorType))...))
}

// RecordDuration records tool execution duration.
func (m *ToolMetrics) RecordDuration(ctx context.Context, inv ToolInvocation, durationMs float64) {
	if m == nil {
		return
	}
	m.duration.Record(ctx, durationMs, metric.WithAttributes(inv.attrs(true)...))
}

// StartInProgress marks a tool as in-progress.
func (m *ToolMetrics) StartInProgress(ctx context.Context, toolName, toolPackage string) {
	if m == nil {
		return
	}
	inv := ToolInvocation{ToolName: toolName, ToolPackage: toolPackage}
	m.inProgress.Add(ctx, 1, metric.WithAttributes(inv.attrs(false)...))
}

// EndInProgress marks a tool as completed.
func (m *ToolMetrics) EndInProgress(ctx context.Context, toolName, toolPackage string) {
	if m == nil {
		return
	}
	inv := ToolInvocation{ToolName: toolName, ToolPackage: toolPackage}
	m.inProgress.Add(ctx, -1, metric.WithAttributes(inv.attrs(false)...))
}

// Timer measures one tool execution. Only the first Stop records anything,
// and a Timer from a nil *ToolMetrics is a usable no-op.
type Timer struct {
	start   time.Time
	metrics *ToolMetrics
	inv     ToolInvocation
	ctx     context.Context
	once    sync.Once
}

// StartTimer marks the tool in progress and starts timing it.
// The timer keeps ctx's values but not its cancellation,
// so a canceled request still has its outcome recorded.
func (m *ToolMetrics) StartTimer(ctx context.Context, toolName, toolPackage string) *Timer {
	ctx = context.WithoutCancel(ctx)
	m.StartInProgress(ctx, toolName, toolPackage)
	return &Timer{
		start:   time.Now(),
		metrics: m,
		inv:     ToolInvocation{ToolName: toolName, ToolPackage: toolPackage},
		ctx:     ctx,
	}
}

// Stop records the invocation, its duration and, for OutcomeError, an error.
// Calls after the first are ignored.
func (t *Timer) Stop(outcome string, componentType string) {
	if t == nil || t.metrics == nil {
		return
	}
	t.once.Do(func() {
		durationMs := float64(time.Since(t.start)) / float64(time.Millisecond)
		inv := t.inv
		inv.Outcome = outcome
		inv.ComponentType = componentType

		t.metrics.RecordInvocation(t.ctx, inv)
		t.metrics.RecordDuration(t.ctx, inv, durationMs)
		t.metrics.EndInProgress(t.ctx, inv.ToolName, inv.ToolPackage)

		if outcome == OutcomeError {
			t.metrics.RecordError(t.ctx, inv, errorTypeExecution)
		}
	})
}
