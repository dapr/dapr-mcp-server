package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// slogToOTELSeverityOffset maps slog levels onto OTEL severities:
// Debug (-4) becomes DEBUG1 (5), Info (0) INFO1 (9), Warn (4) WARN1 (13)
// and Error (8) ERROR1 (17), with custom levels landing in between.
const slogToOTELSeverityOffset = 9

// OTELHandler is a slog.Handler that sends every record to both an OTEL
// logger and an inner handler, such as a JSON handler on stderr.
//
// Groups opened with WithGroup qualify later keys with a dot-joined prefix
// in the OTEL record, matching how slog's built-in handlers render them.
type OTELHandler struct {
	logger log.Logger
	inner  slog.Handler
	// attrs are the WithAttrs attributes, already converted and qualified.
	attrs []attribute.KeyValue
	// prefix is the dot-joined WithGroup path, or "" outside any group.
	prefix string
}

// NewOTELHandler creates a new OTELHandler that sends logs to both OTEL and the inner handler.
func NewOTELHandler(provider *sdklog.LoggerProvider, inner slog.Handler) *OTELHandler {
	return &OTELHandler{
		logger: provider.Logger(instrumentationName),
		inner:  inner,
	}
}

// Enabled implements slog.Handler. The inner handler's level decides for both sinks.
func (h *OTELHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *OTELHandler) Handle(ctx context.Context, record slog.Record) error {
	var rec log.Record
	rec.SetTimestamp(record.Time)
	rec.SetObservedTimestamp(time.Now())
	rec.SetSeverity(slogLevelToOTEL(record.Level))
	rec.SetSeverityText(record.Level.String())
	rec.SetBody(attribute.StringValue(record.Message))

	attrs := make([]attribute.KeyValue, len(h.attrs), len(h.attrs)+record.NumAttrs())
	copy(attrs, h.attrs)
	record.Attrs(func(a slog.Attr) bool {
		attrs = appendOTELAttr(attrs, h.prefix, a)
		return true
	})
	rec.AddAttributes(attrs...)

	h.logger.Emit(ctx, rec)

	return h.inner.Handle(ctx, record)
}

// WithAttrs implements slog.Handler. It never modifies h.
func (h *OTELHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	merged := make([]attribute.KeyValue, len(h.attrs), len(h.attrs)+len(attrs))
	copy(merged, h.attrs)
	for _, a := range attrs {
		merged = appendOTELAttr(merged, h.prefix, a)
	}
	return &OTELHandler{
		logger: h.logger,
		inner:  h.inner.WithAttrs(attrs),
		attrs:  merged,
		prefix: h.prefix,
	}
}

// WithGroup implements slog.Handler. It never modifies h.
func (h *OTELHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &OTELHandler{
		logger: h.logger,
		inner:  h.inner.WithGroup(name),
		attrs:  h.attrs,
		prefix: joinKey(h.prefix, name),
	}
}

// slogLevelToOTEL converts slog.Level to OTEL log.Severity, clamped to the valid range.
func slogLevelToOTEL(level slog.Level) log.Severity {
	sev := int(level) + slogToOTELSeverityOffset
	switch {
	case sev < int(log.SeverityTrace1):
		return log.SeverityTrace1
	case sev > int(log.SeverityFatal4):
		return log.SeverityFatal4
	default:
		return log.Severity(sev)
	}
}

// appendOTELAttr converts a and appends the result to dst under prefix.
// It follows slog's handler rules: LogValuers are resolved, empty attributes
// are dropped, groups are flattened into dot-joined keys, and a group with an
// empty key is inlined into its parent.
func appendOTELAttr(dst []attribute.KeyValue, prefix string, a slog.Attr) []attribute.KeyValue {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}

	if a.Value.Kind() == slog.KindGroup {
		groupPrefix := prefix
		if a.Key != "" {
			groupPrefix = joinKey(prefix, a.Key)
		}
		for _, ga := range a.Value.Group() {
			dst = appendOTELAttr(dst, groupPrefix, ga)
		}
		return dst
	}

	return append(dst, slogValueToOTEL(joinKey(prefix, a.Key), a.Value))
}

// slogValueToOTEL converts a resolved, non-group slog.Value to an OTEL attribute.
func slogValueToOTEL(key string, val slog.Value) attribute.KeyValue {
	switch val.Kind() {
	case slog.KindString:
		return attribute.String(key, val.String())
	case slog.KindInt64:
		return attribute.Int64(key, val.Int64())
	case slog.KindUint64:
		if u := val.Uint64(); u <= math.MaxInt64 {
			return attribute.Int64(key, int64(u))
		}
		return attribute.String(key, strconv.FormatUint(val.Uint64(), 10))
	case slog.KindFloat64:
		return attribute.Float64(key, val.Float64())
	case slog.KindBool:
		return attribute.Bool(key, val.Bool())
	case slog.KindTime:
		return attribute.String(key, val.Time().Format(time.RFC3339Nano))
	case slog.KindDuration:
		return attribute.String(key, val.Duration().String())
	case slog.KindAny:
		return anyToOTEL(key, val.Any())
	default:
		return attribute.String(key, val.String())
	}
}

func anyToOTEL(key string, v any) attribute.KeyValue {
	switch x := v.(type) {
	case error:
		return attribute.String(key, x.Error())
	case fmt.Stringer:
		return attribute.String(key, x.String())
	case []byte:
		return attribute.String(key, string(x))
	default:
		return attribute.String(key, fmt.Sprintf("%+v", x))
	}
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
