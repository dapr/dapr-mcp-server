package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	syncWait = 2 * time.Second
	syncTick = 10 * time.Millisecond
)

// connectSession connects a long-lived in-memory client to server and signals on the
// returned channel each time the server reports that its tool list changed.
func connectSession(t *testing.T, server *mcp.Server) (*mcp.ClientSession, <-chan struct{}) {
	t.Helper()
	ctx := context.Background()
	changed := make(chan struct{}, 16)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session, changed
}

func sessionToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func newSyncedServer(t *testing.T, client *testDaprClient) (*mcp.Server, *toolSyncer) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	syncer, err := registerTools(context.Background(), server, client, nil, discardLogger())
	require.NoError(t, err)
	return server, syncer
}

func TestToolNamesMatchRegisteredTools(t *testing.T) {
	for block, tools := range toolsByBlock {
		t.Run(string(block), func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
			tools.register(server, newTestDaprClient(), nil)

			want := slices.Clone(tools.names())
			slices.Sort(want)
			assert.Equal(t, want, listToolNames(t, server))
		})
	}
}

func TestToolSyncerRefreshAddsAndRemovesTools(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("state.redis", "state.postgresql"), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("pubsub.redis"), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("pubsub.redis"), nil).Once()

	server, syncer := newSyncedServer(t, client)
	session, changed := connectSession(t, server)
	assert.Equal(t, coreTools, sessionToolNames(t, session))

	require.NoError(t, syncer.refresh(context.Background()))
	assert.Equal(t, sortedConcat(coreTools, stateTools), sessionToolNames(t, session))
	select {
	case <-changed:
	case <-time.After(syncWait):
		t.Fatal("client was not told the tool list changed")
	}

	require.NoError(t, syncer.refresh(context.Background()))
	assert.Equal(t, sortedConcat(coreTools, pubsubTools), sessionToolNames(t, session), "state tools go when the last state store does")

	require.NoError(t, syncer.refresh(context.Background()))
	assert.Equal(t, sortedConcat(coreTools, pubsubTools), sessionToolNames(t, session), "an unchanged component list changes nothing")
}

func TestToolSyncerRefreshErrorKeepsTools(t *testing.T) {
	sidecarErr := errors.New("sidecar unavailable")
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("state.redis"), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(nil, sidecarErr).Once()

	server, syncer := newSyncedServer(t, client)

	require.ErrorIs(t, syncer.refresh(context.Background()), sidecarErr)
	assert.Equal(t, sortedConcat(coreTools, stateTools), listToolNames(t, server))
}

func TestGetComponentsSyncsTools(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("lock.redis"), nil).Once()

	server, _ := newSyncedServer(t, client)
	session, _ := connectSession(t, server)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_components"})
	require.NoError(t, err)
	require.False(t, result.IsError)

	assert.Equal(t, sortedConcat(coreTools, lockTools), sessionToolNames(t, session))
}

func TestToolSyncerRun(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("crypto.dapr.localstorage"), nil)

	server, syncer := newSyncedServer(t, client)
	session, _ := connectSession(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		syncer.run(ctx, syncTick)
		close(done)
	}()

	want := sortedConcat(coreTools, cryptoTools)
	assert.Eventually(t, func() bool { return slices.Equal(want, sessionToolNames(t, session)) }, syncWait, syncTick)

	cancel()
	select {
	case <-done:
	case <-time.After(syncWait):
		t.Fatal("run did not return after its context was canceled")
	}
}

func TestToolRefreshInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses the default", value: "", want: defaultToolRefreshInterval},
		{name: "duration", value: "5s", want: 5 * time.Second},
		{name: "zero disables", value: "0", want: 0},
		{name: "negative", value: "-1s", wantErr: true},
		{name: "not a duration", value: "often", wantErr: true},
		{name: "bare number", value: "30", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := toolRefreshInterval(tt.value)
			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidToolRefreshInterval)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
