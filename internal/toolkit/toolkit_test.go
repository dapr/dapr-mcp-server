package toolkit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/metadata"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

func TestValidateRequired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fields      []Field
		wantErr     bool
		wantMissing string
	}{
		{name: "no fields", fields: nil},
		{name: "all present", fields: []Field{{"storeName", "s"}, {"key", "k"}}},
		{name: "one empty", fields: []Field{{"storeName", "s"}, {"key", ""}}, wantErr: true, wantMissing: "key"},
		{name: "whitespace counts as empty", fields: []Field{{"key", "  \t"}}, wantErr: true, wantMissing: "key"},
		{name: "all empty listed in order", fields: []Field{{"a", ""}, {"b", ""}}, wantErr: true, wantMissing: "a, b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateRequired(tt.fields...)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrMissingArgument)
			assert.Contains(t, err.Error(), tt.wantMissing)
		})
	}
}

func TestContentTypeFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		payload string
		want    string
	}{
		{`{"a":1}`, ContentTypeJSON},
		{`[1,2]`, ContentTypeJSON},
		{`"quoted"`, ContentTypeJSON},
		{`hello world`, ContentTypeText},
		{``, ContentTypeText},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, ContentTypeFor([]byte(tt.payload)), tt.payload)
	}
}

func TestIndentJSON(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "{\n  \"a\": 1\n}", IndentJSON([]byte(`{"a":1}`)))
	assert.Equal(t, "not json", IndentJSON([]byte("not json")))
}

func TestMarshalIndent(t *testing.T) {
	t.Parallel()
	got, err := MarshalIndent(map[string]int{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"a\": 1\n}", got)

	_, err = MarshalIndent(make(chan int))
	assert.Error(t, err)
}

func TestResults(t *testing.T) {
	t.Parallel()
	ok := TextResult("fine")
	assert.False(t, ok.IsError)
	assert.Equal(t, "fine", ok.Content[0].(*mcp.TextContent).Text)

	bad := ErrorResult("broken")
	assert.True(t, bad.IsError)
	assert.Equal(t, "broken", bad.Content[0].(*mcp.TextContent).Text)
}

func TestSafetyAnnotations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		safety          Safety
		wantReadOnly    bool
		wantDestructive bool
		wantIdempotent  bool
	}{
		{ReadOnly, true, false, true},
		{IdempotentWrite, false, false, true},
		{DestructiveIdempotent, false, true, true},
		{DestructiveWrite, false, true, false},
		{AdditiveWrite, false, false, false},
	}
	for _, tt := range tests {
		a := tt.safety.Annotations(true)
		assert.Equal(t, tt.wantReadOnly, a.ReadOnlyHint)
		require.NotNil(t, a.DestructiveHint)
		assert.Equal(t, tt.wantDestructive, *a.DestructiveHint)
		assert.Equal(t, tt.wantIdempotent, a.IdempotentHint)
		require.NotNil(t, a.OpenWorldHint)
		assert.True(t, *a.OpenWorldHint)
	}
	assert.False(t, *ReadOnly.Annotations(false).OpenWorldHint)
}

func newTestInstrumentation(t *testing.T, buf *bytes.Buffer) Instrumentation {
	t.Helper()
	metrics, err := telemetry.NewToolMetrics()
	require.NoError(t, err)
	return Instrumentation{Metrics: metrics, Logger: slog.New(slog.NewTextHandler(buf, nil))}
}

func TestCallLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		run       func(*Call) *mcp.CallToolResult
		wantError bool
		wantLog   string
	}{
		{
			name: "succeed",
			run: func(c *Call) *mcp.CallToolResult {
				c.Succeed("key", "k1")
				return nil
			},
			wantLog: "tool call succeeded",
		},
		{
			name:      "fail",
			run:       func(c *Call) *mcp.CallToolResult { return c.Fail(errors.New("boom")) },
			wantError: true,
			wantLog:   "boom",
		},
		{
			name:      "require missing",
			run:       func(c *Call) *mcp.CallToolResult { return c.Require(Field{"key", ""}) },
			wantError: true,
			wantLog:   "missing required argument: key",
		},
		{
			name:    "require present",
			run:     func(c *Call) *mcp.CallToolResult { return c.Require(Field{"key", "k"}) },
			wantLog: "",
		},
		{
			name:    "end without outcome",
			run:     func(*Call) *mcp.CallToolResult { return nil },
			wantLog: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			in := newTestInstrumentation(t, &buf)
			_, call := in.Start(context.Background(), "tool", "pkg", "component")
			res := tt.run(call)
			call.End()
			call.End()

			if tt.wantError {
				require.NotNil(t, res)
				assert.True(t, res.IsError)
			} else {
				assert.Nil(t, res)
			}
			assert.Contains(t, buf.String(), tt.wantLog)
		})
	}
}

func TestStartWithoutMetricsOrLogger(t *testing.T) {
	t.Parallel()
	_, call := Instrumentation{}.Start(context.Background(), "tool", "pkg", "c")
	call.Succeed()
	call.End()
}

// TestStartPropagatesTraceContext mutates the global OpenTelemetry providers,
// so it must not run in parallel.
func TestStartPropagatesTraceContext(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx, call := NewInstrumentation(nil).Start(context.Background(), "tool", "pkg", "c")
	defer call.End()

	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok)
	assert.Len(t, md.Get("traceparent"), 1)
}
