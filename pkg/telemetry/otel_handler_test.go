package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// memoryLogExporter keeps every exported record in memory.
type memoryLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memoryLogExporter) Export(_ context.Context, records []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		e.records = append(e.records, r.Clone())
	}
	return nil
}

func (e *memoryLogExporter) Shutdown(context.Context) error   { return nil }
func (e *memoryLogExporter) ForceFlush(context.Context) error { return nil }

func (e *memoryLogExporter) snapshot() []sdklog.Record {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]sdklog.Record(nil), e.records...)
}

func recordAttrs(r sdklog.Record) map[string]attribute.Value {
	out := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value
		return true
	})
	return out
}

// newTestOTELHandler returns a handler whose OTEL records land in the returned
// exporter and whose inner JSON output lands in the returned buffer.
func newTestOTELHandler(t *testing.T, level slog.Level) (*OTELHandler, *memoryLogExporter, *syncBuffer) {
	t.Helper()
	exp := &memoryLogExporter{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	buf := &syncBuffer{}
	inner := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	return NewOTELHandler(provider, inner), exp, buf
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

func TestSlogLevelToOTEL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level slog.Level
		want  log.Severity
	}{
		{level: slog.LevelDebug, want: log.SeverityDebug},
		{level: slog.LevelInfo, want: log.SeverityInfo},
		{level: slog.LevelWarn, want: log.SeverityWarn},
		{level: slog.LevelError, want: log.SeverityError},
		{level: slog.LevelInfo + 1, want: log.SeverityInfo2},
		{level: slog.LevelError + 4, want: log.SeverityFatal},
		{level: slog.Level(-100), want: log.SeverityTrace1},
		{level: slog.Level(100), want: log.SeverityFatal4},
	}
	for _, tt := range tests {
		t.Run(tt.level.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, slogLevelToOTEL(tt.level))
		})
	}
}

type stringerValue struct{}

func (stringerValue) String() string { return "stringer!" }

type lazyValue struct{ v string }

func (l lazyValue) LogValue() slog.Value { return slog.StringValue(l.v) }

type plainStruct struct{ A int }

func TestAppendOTELAttrEveryKind(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	tests := []struct {
		name string
		attr slog.Attr
		want []attribute.KeyValue
	}{
		{name: "string", attr: slog.String("k", "v"), want: []attribute.KeyValue{attribute.String("k", "v")}},
		{name: "int64", attr: slog.Int64("k", -3), want: []attribute.KeyValue{attribute.Int64("k", -3)}},
		{name: "uint64 fits", attr: slog.Uint64("k", 42), want: []attribute.KeyValue{attribute.Int64("k", 42)}},
		{
			name: "uint64 overflow",
			attr: slog.Uint64("k", math.MaxUint64),
			want: []attribute.KeyValue{attribute.String("k", "18446744073709551615")},
		},
		{name: "float64", attr: slog.Float64("k", 1.5), want: []attribute.KeyValue{attribute.Float64("k", 1.5)}},
		{name: "bool", attr: slog.Bool("k", true), want: []attribute.KeyValue{attribute.Bool("k", true)}},
		{
			name: "time",
			attr: slog.Time("k", now),
			want: []attribute.KeyValue{attribute.String("k", "2026-01-02T03:04:05.000000006Z")},
		},
		{name: "duration", attr: slog.Duration("k", 1500*time.Millisecond), want: []attribute.KeyValue{attribute.String("k", "1.5s")}},
		{name: "error", attr: slog.Any("err", errors.New("boom")), want: []attribute.KeyValue{attribute.String("err", "boom")}},
		{name: "stringer", attr: slog.Any("k", stringerValue{}), want: []attribute.KeyValue{attribute.String("k", "stringer!")}},
		{name: "bytes", attr: slog.Any("k", []byte("raw")), want: []attribute.KeyValue{attribute.String("k", "raw")}},
		{name: "struct", attr: slog.Any("k", plainStruct{A: 1}), want: []attribute.KeyValue{attribute.String("k", "{A:1}")}},
		{name: "log valuer", attr: slog.Any("k", lazyValue{v: "resolved"}), want: []attribute.KeyValue{attribute.String("k", "resolved")}},
		{name: "empty attr dropped", attr: slog.Attr{}, want: nil},
		{name: "empty group dropped", attr: slog.Group("g"), want: nil},
		{
			name: "group flattened",
			attr: slog.Group("req", slog.String("method", "GET"), slog.Group("user", slog.Int("id", 7))),
			want: []attribute.KeyValue{attribute.String("req.method", "GET"), attribute.Int64("req.user.id", 7)},
		},
		{
			name: "group with empty key inlined",
			attr: slog.Group("", slog.String("a", "1")),
			want: []attribute.KeyValue{attribute.String("a", "1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, appendOTELAttr(nil, "", tt.attr))
		})
	}
}

func TestAppendOTELAttrPrefix(t *testing.T) {
	t.Parallel()

	got := appendOTELAttr(nil, "a.b", slog.String("c", "v"))
	assert.Equal(t, []attribute.KeyValue{attribute.String("a.b.c", "v")}, got)
}

func TestOTELHandlerEnabledHonorsInnerLevel(t *testing.T) {
	t.Parallel()

	h, _, _ := newTestOTELHandler(t, slog.LevelWarn)
	ctx := context.Background()

	assert.False(t, h.Enabled(ctx, slog.LevelInfo))
	assert.True(t, h.Enabled(ctx, slog.LevelWarn))
	assert.True(t, h.Enabled(ctx, slog.LevelError))
}

func TestOTELHandlerEmitsToBothSinks(t *testing.T) {
	t.Parallel()

	h, exp, buf := newTestOTELHandler(t, slog.LevelDebug)
	logger := slog.New(h).With("service", "svc")

	logger.Warn("hello", "n", 3)

	records := exp.snapshot()
	require.Len(t, records, 1)
	rec := records[0]
	assert.Equal(t, "hello", rec.Body().AsString())
	assert.Equal(t, log.SeverityWarn, rec.Severity())
	assert.Equal(t, "WARN", rec.SeverityText())
	assert.False(t, rec.Timestamp().IsZero())
	assert.False(t, rec.ObservedTimestamp().IsZero())
	attrs := recordAttrs(rec)
	assert.Equal(t, "svc", attrs["service"].AsString())
	assert.Equal(t, int64(3), attrs["n"].AsInt64())

	lines := buf.lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, "hello", lines[0]["msg"])
	assert.Equal(t, "svc", lines[0]["service"])
}

func TestOTELHandlerNestedGroups(t *testing.T) {
	t.Parallel()

	h, exp, buf := newTestOTELHandler(t, slog.LevelInfo)
	logger := slog.New(h).
		With("top", "t").
		WithGroup("a").
		With("x", 1).
		WithGroup("b").
		WithGroup("")

	logger.Info("msg", "y", 2, slog.Group("c", slog.String("z", "3")))

	records := exp.snapshot()
	require.Len(t, records, 1)
	attrs := recordAttrs(records[0])
	assert.Equal(t, "t", attrs["top"].AsString())
	assert.Equal(t, int64(1), attrs["a.x"].AsInt64())
	assert.Equal(t, int64(2), attrs["a.b.y"].AsInt64())
	assert.Equal(t, "3", attrs["a.b.c.z"].AsString())
	assert.Len(t, attrs, 4)

	lines := buf.lines(t)
	require.Len(t, lines, 1)
	a, ok := lines[0]["a"].(map[string]any)
	require.True(t, ok, "inner handler keeps its own grouping")
	assert.InDelta(t, 1, a["x"], 0)
}

func TestOTELHandlerWithAttrsDoesNotMutateParent(t *testing.T) {
	t.Parallel()

	h, exp, _ := newTestOTELHandler(t, slog.LevelInfo)
	parent := h.WithAttrs([]slog.Attr{slog.String("p", "1")})

	// Fan out siblings concurrently from the same parent; -race flags shared backing arrays.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := slog.New(parent.WithAttrs([]slog.Attr{slog.Int("child", i)}))
			child.Info("child")
		}()
	}
	wg.Wait()

	slog.New(parent).Info("parent")

	records := exp.snapshot()
	require.Len(t, records, 21)
	last := recordAttrs(records[len(records)-1])
	assert.Equal(t, map[string]attribute.Value{"p": attribute.StringValue("1")}, last)
	assert.Same(t, h, h.WithAttrs(nil), "no attrs returns the same handler")
	assert.Same(t, h, h.WithGroup(""), "empty group returns the same handler")
}

func TestOTELHandlerConcurrentHandle(t *testing.T) {
	t.Parallel()

	h, exp, buf := newTestOTELHandler(t, slog.LevelInfo)
	logger := slog.New(h).With("shared", "yes").WithGroup("g")

	const workers = 16
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("concurrent", "i", i)
		}()
	}
	wg.Wait()

	assert.Len(t, exp.snapshot(), workers)
	assert.Len(t, buf.lines(t), workers)
}
