package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/eventstore"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func parseTransportFlags(t *testing.T, args ...string) *transportFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerTransportFlags(fs)
	require.NoError(t, fs.Parse(args))
	return f
}

func TestTransportFlagDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parseTransportFlags(t).resolve(true)
	require.NoError(t, err)

	assert.False(t, cfg.stateless)
	assert.Empty(t, cfg.eventStoreName)
	assert.Equal(t, mcp.SupportedProtocolVersions()[0], cfg.specVersion, "default is the newest version the SDK supports")
	assert.Nil(t, cfg.protocolVersions, "the default leaves the SDK's own version list in place")
	assert.Empty(t, cfg.warnings)
}

func TestEffectiveVersion(t *testing.T) {
	t.Parallel()
	newest := mcp.SupportedProtocolVersions()[0]
	tests := []struct {
		name string
		args []string
		http bool
		want string
	}{
		{name: "stdio serves the newest version", want: newest},
		{name: "stateless HTTP serves the newest version", args: []string{"--stateless"}, http: true, want: newest},
		{name: "session HTTP stops before the sessionless protocol", http: true, want: "2025-11-25"},
		{name: "an older pin is served as is", args: []string{"--spec-version", "2025-06-18"}, http: true, want: "2025-06-18"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseTransportFlags(t, tt.args...).resolve(tt.http)
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.effectiveVersion)
		})
	}
}

func TestTransportFlagParsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		args         []string
		http         bool
		wantErr      string
		wantVersions []string
		check        func(t *testing.T, cfg transportConfig)
	}{
		{
			name:  "stateless",
			args:  []string{"--stateless"},
			http:  true,
			check: func(t *testing.T, cfg transportConfig) { assert.True(t, cfg.stateless) },
		},
		{
			name:  "event store",
			args:  []string{"--event-store", "statestore"},
			http:  true,
			check: func(t *testing.T, cfg transportConfig) { assert.Equal(t, "statestore", cfg.eventStoreName) },
		},
		{
			name:         "spec version pins the newest negotiated version",
			args:         []string{"--spec-version", "2025-11-25"},
			http:         true,
			wantVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
		},
		{
			name:         "oldest spec version",
			args:         []string{"--spec-version", "2024-11-05"},
			wantVersions: []string{"2024-11-05"},
		},
		{
			name:    "unknown spec version",
			args:    []string{"--spec-version", "2099-01-01"},
			http:    true,
			wantErr: "supported versions",
		},
		{
			name:    "stateless with event store",
			args:    []string{"--stateless", "--event-store", "statestore"},
			http:    true,
			wantErr: "--event-store",
		},
		{
			name:    "blank event store name",
			args:    []string{"--event-store", " "},
			http:    true,
			wantErr: "--event-store",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseTransportFlags(t, tt.args...).resolve(tt.http)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantVersions != nil {
				assert.Equal(t, tt.wantVersions, cfg.protocolVersions)
				assert.Equal(t, tt.wantVersions[0], cfg.specVersion)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestTransportFlagWarnings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		http bool
		want string
	}{
		{name: "stateless over stdio", args: []string{"--stateless"}, want: "--stateless"},
		{name: "event store over stdio", args: []string{"--event-store", "statestore"}, want: "--event-store"},
		{name: "new spec over stateful HTTP", args: []string{"--spec-version", "2026-07-28"}, http: true, want: "--stateless"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseTransportFlags(t, tt.args...).resolve(tt.http)
			require.NoError(t, err)
			require.Len(t, cfg.warnings, 1)
			assert.Contains(t, cfg.warnings[0], tt.want)
		})
	}
}

func TestStreamableOptions(t *testing.T) {
	t.Parallel()
	state := mocks.NewFakeStateStore()

	logger := discardLogger()
	opts, err := transportConfig{}.streamableOptions(t.Context(), state, logger)
	require.NoError(t, err)
	assert.Same(t, logger, opts.Logger, "the SDK logs to the server's logger")
	assert.False(t, opts.Stateless)
	assert.Nil(t, opts.EventStore)
	assert.Zero(t, opts.SessionTimeout, "without an event store, sessions keep the SDK default")

	opts, err = transportConfig{stateless: true}.streamableOptions(t.Context(), state, discardLogger())
	require.NoError(t, err)
	assert.True(t, opts.Stateless)

	opts, err = transportConfig{eventStoreName: "statestore"}.streamableOptions(t.Context(), state, discardLogger())
	require.NoError(t, err)
	assert.NotNil(t, opts.EventStore)
	assert.Equal(t, eventstore.DefaultTTL, opts.SessionTimeout, "idle sessions close when their events expire")

	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	registerTransportFlags(fs)
	assert.Contains(t, fs.Lookup(eventStoreFlag).Usage, "no request for 60 minutes", "--help states the session timeout")

	_, err = transportConfig{eventStoreName: "statestore"}.streamableOptions(t.Context(), nil, discardLogger())
	require.ErrorContains(t, err, "event store")
}

func TestStreamableOptionsRejectsAnUnusableEventStore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		inject func(*mocks.FakeStateStore)
		want   string
	}{
		{
			name:   "missing store",
			inject: func(f *mocks.FakeStateStore) { f.SaveErr = errors.New("state store nosuchstore is not found") },
			want:   "is not found",
		},
		{
			name:   "unwritable store",
			inject: func(f *mocks.FakeStateStore) { f.SaveErr = errors.New("read-only replica") },
			want:   "read-only replica",
		},
		{
			name:   "store that cannot delete",
			inject: func(f *mocks.FakeStateStore) { f.BulkDeleteErr = errors.New("delete refused") },
			want:   "delete refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := mocks.NewFakeStateStore()
			tt.inject(state)
			_, err := transportConfig{eventStoreName: "nosuchstore"}.streamableOptions(t.Context(), state, discardLogger())
			require.ErrorContains(t, err, "--event-store \"nosuchstore\"")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestStreamableOptionsLogsEventStoreFailures(t *testing.T) {
	t.Parallel()
	state := mocks.NewFakeStateStore()
	var buf bytes.Buffer
	opts, err := transportConfig{eventStoreName: "statestore"}.streamableOptions(t.Context(), state, slog.New(slog.NewTextHandler(&buf, nil)))
	require.NoError(t, err)

	state.SaveErr = errors.New("state store unreachable")
	err = opts.EventStore.Open(t.Context(), "sess", "1")
	require.ErrorIs(t, err, eventstore.ErrUnavailable)
	assert.NotContains(t, err.Error(), "unreachable", "the SDK writes Open's error into the HTTP response")
	assert.Contains(t, buf.String(), "Event store failed")
	assert.Contains(t, buf.String(), "state store unreachable")
}

func TestNewMCPServerAppliesSpecVersion(t *testing.T) {
	t.Parallel()
	cfg, err := parseTransportFlags(t, "--spec-version", "2025-06-18").resolve(false)
	require.NoError(t, err)
	server := newMCPServer("test", cfg.protocolVersions)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx := t.Context()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	assert.Equal(t, "2025-06-18", session.InitializeResult().ProtocolVersion)
}
