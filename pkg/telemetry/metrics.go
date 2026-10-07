package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ToolMetrics provides metrics instrumentation for tool invocations.
type ToolMetrics struct {
	invocations metric.Int64Counter
	errors      metric.Int64Counter
	duration    metric.Float64Histogram
	inProgress  metric.Int64UpDownCounter
	meter       metric.Meter
}

// NewToolMetrics creates a new ToolMetrics instance.
func NewToolMetrics() (*ToolMetrics, error) {
	meter := otel.Meter("dapr-mcp-server")

	invocations, err := meter.Int64Counter(
		"dapr-mcp-server.tool.invocations",
		metric.WithDescription("Total number of tool invocations"),
		metric.WithUnit("{invocation}"),
	)
	if err != nil {
		return nil, err
	}

	errors, err := meter.Int64Counter(
		"dapr-mcp-server.tool.errors",
		metric.WithDescription("Total number of failed tool invocations"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, err
	}

	duration, err := meter.Float64Histogram(
		"dapr-mcp-server.tool.duration",
		metric.WithDescription("Tool execution duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	if err != nil {
		return nil, err
	}

	inProgress, err := meter.Int64UpDownCounter(
		"dapr-mcp-server.tool.in_progress",
		metric.WithDescription("Number of tools currently executing"),
		metric.WithUnit("{tool}"),
	)
	if err != nil {
		return nil, err
	}

	return &ToolMetrics{
		invocations: invocations,
		errors:      errors,
		duration:    duration,
		inProgress:  inProgress,
		meter:       meter,
	}, nil
}

// ToolInvocation represents attributes for a tool invocation.
type ToolInvocation struct {
	ToolName      string
	ToolPackage   string
	ComponentType string
	Outcome       string
}

// RecordInvocation records a tool invocation with its attributes.
func (m *ToolMetrics) RecordInvocation(ctx context.Context, inv ToolInvocation) {
	attrs := []attribute.KeyValue{
		attribute.String("tool.name", inv.ToolName),
		attribute.String("tool.package", inv.ToolPackage),
	}
	if inv.ComponentType != "" {
		attrs = append(attrs, attribute.String("dapr.component.type", inv.ComponentType))
	}
	if inv.Outcome != "" {
		attrs = append(attrs, attribute.String("outcome", inv.Outcome))
	}

	m.invocations.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordError records a tool error.
func (m *ToolMetrics) RecordError(ctx context.Context, inv ToolInvocation, errorType string) {
	attrs := []attribute.KeyValue{
		attribute.String("tool.name", inv.ToolName),
		attribute.String("tool.package", inv.ToolPackage),
		attribute.String("error.type", errorType),
	}
	if inv.ComponentType != "" {
		attrs = append(attrs, attribute.String("dapr.component.type", inv.ComponentType))
	}

	m.errors.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordDuration records tool execution duration.
func (m *ToolMetrics) RecordDuration(ctx context.Context, inv ToolInvocation, durationMs float64) {
	attrs := []attribute.KeyValue{
		attribute.String("tool.name", inv.ToolName),
		attribute.String("tool.package", inv.ToolPackage),
	}
	if inv.ComponentType != "" {
		attrs = append(attrs, attribute.String("dapr.component.type", inv.ComponentType))
	}
	if inv.Outcome != "" {
		attrs = append(attrs, attribute.String("outcome", inv.Outcome))
	}

	m.duration.Record(ctx, durationMs, metric.WithAttributes(attrs...))
}

// StartInProgress marks a tool as in-progress.
func (m *ToolMetrics) StartInProgress(ctx context.Context, toolName, toolPackage string) {
	attrs := []attribute.KeyValue{
		attribute.String("tool.name", toolName),
		attribute.String("tool.package", toolPackage),
	}
	m.inProgress.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// EndInProgress marks a tool as completed.
func (m *ToolMetrics) EndInProgress(ctx context.Context, toolName, toolPackage string) {
	attrs := []attribute.KeyValue{
		attribute.String("tool.name", toolName),
		attribute.String("tool.package", toolPackage),
	}
	m.inProgress.Add(ctx, -1, metric.WithAttributes(attrs...))
}

// Timer is a helper for measuring duration.
type Timer struct {
	start   time.Time
	metrics *ToolMetrics
	inv     ToolInvocation
	ctx     context.Context
}

// StartTimer creates a new timer for measuring tool execution.
func (m *ToolMetrics) StartTimer(ctx context.Context, toolName, toolPackage string) *Timer {
	inv := ToolInvocation{
		ToolName:    toolName,
		ToolPackage: toolPackage,
	}
	m.StartInProgress(ctx, toolName, toolPackage)
	return &Timer{
		start:   time.Now(),
		metrics: m,
		inv:     inv,
		ctx:     ctx,
	}
}

// Stop stops the timer and records the duration.
func (t *Timer) Stop(outcome string, componentType string) {
	durationMs := float64(time.Since(t.start).Milliseconds())
	t.inv.Outcome = outcome
	t.inv.ComponentType = componentType

	t.metrics.RecordInvocation(t.ctx, t.inv)
	t.metrics.RecordDuration(t.ctx, t.inv, durationMs)
	t.metrics.EndInProgress(t.ctx, t.inv.ToolName, t.inv.ToolPackage)

	if outcome == "error" {
		t.metrics.RecordError(t.ctx, t.inv, "execution_error")
	}
}
