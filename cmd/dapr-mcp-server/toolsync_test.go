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

// syncWait bounds how long a test waits for the tool list changed notification.
const syncWait = 2 * time.Second

// noNotifyWait is how long a test waits to confirm no notification arrives,
// comfortably longer than the SDK's notification debounce.
const noNotifyWait = 300 * time.Millisecond

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

func TestListToolsSyncsTools(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("state.redis", "state.postgresql"), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("pubsub.redis"), nil)

	server, _ := newSyncedServer(t, client)
	session, changed := connectSession(t, server)

	assert.Equal(t, sortedConcat(coreTools, stateTools), sessionToolNames(t, session), "a state store hot-reloaded after startup")
	select {
	case <-changed:
	case <-time.After(syncWait):
		t.Fatal("client was not told the tool list changed")
	}

	assert.Equal(t, sortedConcat(coreTools, pubsubTools), sessionToolNames(t, session), "state tools go when the last state store does")
	select {
	case <-changed:
	case <-time.After(syncWait):
		t.Fatal("client was not told the tool list changed")
	}

	assert.Equal(t, sortedConcat(coreTools, pubsubTools), sessionToolNames(t, session), "an unchanged component list changes nothing")
	select {
	case <-changed:
		t.Fatal("an unchanged tool list must not notify clients")
	case <-time.After(noNotifyWait):
	}
}

func TestListToolsSyncErrorKeepsTools(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("state.redis"), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(nil, errors.New("sidecar unavailable"))

	server, _ := newSyncedServer(t, client)
	session, _ := connectSession(t, server)

	assert.Equal(t, sortedConcat(coreTools, stateTools), sessionToolNames(t, session))
}

func TestGetComponentsSyncsTools(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil).Once()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes("lock.redis"), nil).Once()

	server, syncer := newSyncedServer(t, client)
	session, _ := connectSession(t, server)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_components"})
	require.NoError(t, err)
	require.False(t, result.IsError)

	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	assert.Equal(t, map[buildingBlock]bool{blockLock: true}, syncer.registered)
}

func TestListToolsSyncsOncePerPagedList(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(), nil)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, &mcp.ServerOptions{PageSize: 2})
	_, err := registerTools(context.Background(), server, client, nil, discardLogger())
	require.NoError(t, err)
	session, _ := connectSession(t, server)

	client.Calls = nil
	var cursor string
	pages := 0
	for {
		result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{Cursor: cursor})
		require.NoError(t, err)
		pages++
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	require.Greater(t, pages, 1)
	client.AssertNumberOfCalls(t, "GetMetadata", 1)
}
