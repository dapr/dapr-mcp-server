// Package toolkit holds the plumbing shared by every MCP tool handler:
// tracing and metrics for a call, argument validation, result construction,
// payload helpers and the safety annotations advertised to clients.
package toolkit

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

// TracerName is the OpenTelemetry tracer name used for tool spans.
const TracerName = "dapr-mcp-server"

// Span attribute keys shared by all tools.
const (
	AttrToolName      = "mcp.tool.name"
	AttrToolPackage   = "mcp.tool.package"
	AttrComponentName = "dapr.component.name"
)

const (
	outcomeSuccess = "success"
	outcomeError   = "error"
)

// Instrumentation carries the metrics and logger a tool handler reports to.
type Instrumentation struct {
	Metrics *telemetry.ToolMetrics
	Logger  *slog.Logger
}

// NewInstrumentation returns an Instrumentation that records to metrics,
// which may be nil, and logs through the process-wide default logger.
func NewInstrumentation(metrics *telemetry.ToolMetrics) Instrumentation {
	return Instrumentation{Metrics: metrics, Logger: slog.Default()}
}

// Call tracks the span, metrics timer and logging of one tool invocation.
// Create it with Instrumentation.Start and always defer End.
type Call struct {
	ctx      context.Context
	tool     string
	span     trace.Span
	timer    *telemetry.Timer
	logger   *slog.Logger
	finished bool
}

// Start begins a tool call named tool in package pkg.
// Metrics carry only the tool name, package and outcome, because anything an
// agent supplies (component, app or actor names) is unbounded as a metric label.
// Pass such values as span attributes instead.
// The returned context carries the span and the trace context as outgoing
// gRPC metadata, so the Dapr sidecar joins the same trace.
func (in Instrumentation) Start(ctx context.Context, tool, pkg string, attrs ...attribute.KeyValue) (context.Context, *Call) {
	var timer *telemetry.Timer
	if in.Metrics != nil {
		timer = in.Metrics.StartTimer(ctx, tool, pkg)
	}

	ctx, span := otel.Tracer(TracerName).Start(ctx, tool)
	span.SetAttributes(attribute.String(AttrToolName, tool), attribute.String(AttrToolPackage, pkg))
	span.SetAttributes(attrs...)

	logger := in.Logger
	if logger == nil {
		logger = slog.Default()
	}

	ctx = withOutgoingTraceContext(ctx)
	return ctx, &Call{ctx: ctx, tool: tool, span: span, timer: timer, logger: logger}
}

// withOutgoingTraceContext copies the active trace context into the outgoing
// gRPC metadata, which is where the Dapr sidecar reads it from.
func withOutgoingTraceContext(ctx context.Context) context.Context {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if len(carrier) == 0 {
		return ctx
	}
	kv := make([]string, 0, 2*len(carrier))
	for k, v := range carrier {
		kv = append(kv, k, v)
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

// Require checks that every field is non-blank.
// It returns nil when all are present, or a failed error result naming the
// missing fields.
func (c *Call) Require(fields ...Field) *mcp.CallToolResult {
	if err := ValidateRequired(fields...); err != nil {
		return c.Fail(err)
	}
	return nil
}

// Fail records err on the span and metrics, logs it and returns it as an
// error result for the client.
func (c *Call) Fail(err error) *mcp.CallToolResult {
	c.span.RecordError(err)
	c.span.SetStatus(codes.Error, err.Error())
	c.stop(outcomeError)
	c.logger.WarnContext(c.ctx, "tool call failed", "tool", c.tool, "error", err)
	return ErrorResult(err.Error())
}

// Succeed marks the call successful and logs it with the given attributes.
// Callers must only pass attributes that are safe to log, never payloads or
// secret values.
func (c *Call) Succeed(logAttrs ...any) {
	c.span.SetStatus(codes.Ok, "")
	c.stop(outcomeSuccess)
	c.logger.InfoContext(c.ctx, "tool call succeeded", append([]any{"tool", c.tool}, logAttrs...)...)
}

// End ends the span.
// A call that was neither failed nor succeeded is counted as an error, so the
// in-progress metric never leaks.
func (c *Call) End() {
	if !c.finished {
		c.stop(outcomeError)
	}
	c.span.End()
}

func (c *Call) stop(outcome string) {
	if c.finished {
		return
	}
	c.finished = true
	if c.timer != nil {
		c.timer.Stop(outcome)
	}
}
