// Package telemetry provides OpenTelemetry initialization and configuration.
package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// OTELHandler bridges slog to OpenTelemetry logs.
type OTELHandler struct {
	logger log.Logger
	inner  slog.Handler
	attrs  []slog.Attr
	group  string
}

// NewOTELHandler creates a new OTELHandler that sends logs to both OTEL and the inner handler.
func NewOTELHandler(provider *sdklog.LoggerProvider, inner slog.Handler) *OTELHandler {
	return &OTELHandler{
		logger: provider.Logger("dapr-mcp-server"),
		inner:  inner,
	}
}

// Enabled implements slog.Handler.
func (h *OTELHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *OTELHandler) Handle(ctx context.Context, record slog.Record) error {
	// Emit to OTEL
	var rec log.Record
	rec.SetTimestamp(record.Time)
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(slogLevelToOTEL(record.Level))
	rec.SetSeverityText(record.Level.String())
	rec.SetBody(attribute.StringValue(record.Message))

	// Add pre-configured attributes
	for _, a := range h.attrs {
		rec.AddAttributes(slogAttrToOTEL(a))
	}

	// Add record attributes
	record.Attrs(func(a slog.Attr) bool {
		rec.AddAttributes(slogAttrToOTEL(a))
		return true
	})

	h.logger.Emit(ctx, rec)

	// Also log to inner handler (stdout)
	return h.inner.Handle(ctx, record)
}

// WithAttrs implements slog.Handler.
func (h *OTELHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &OTELHandler{
		logger: h.logger,
		inner:  h.inner.WithAttrs(attrs),
		attrs:  newAttrs,
		group:  h.group,
	}
}

// WithGroup implements slog.Handler.
func (h *OTELHandler) WithGroup(name string) slog.Handler {
	return &OTELHandler{
		logger: h.logger,
		inner:  h.inner.WithGroup(name),
		attrs:  h.attrs,
		group:  name,
	}
}

// slogLevelToOTEL converts slog.Level to OTEL log.Severity.
func slogLevelToOTEL(level slog.Level) log.Severity {
	switch {
	case level >= slog.LevelError:
		return log.SeverityError
	case level >= slog.LevelWarn:
		return log.SeverityWarn
	case level >= slog.LevelInfo:
		return log.SeverityInfo
	default:
		return log.SeverityDebug
	}
}

// slogAttrToOTEL converts a slog.Attr to an OTEL attribute.KeyValue.
func slogAttrToOTEL(a slog.Attr) attribute.KeyValue {
	key := a.Key
	val := a.Value

	switch val.Kind() {
	case slog.KindString:
		return attribute.String(key, val.String())
	case slog.KindInt64:
		return attribute.Int64(key, val.Int64())
	case slog.KindFloat64:
		return attribute.Float64(key, val.Float64())
	case slog.KindBool:
		return attribute.Bool(key, val.Bool())
	case slog.KindTime:
		return attribute.String(key, val.Time().Format(time.RFC3339))
	case slog.KindDuration:
		return attribute.String(key, val.Duration().String())
	default:
		return attribute.String(key, val.String())
	}
}
