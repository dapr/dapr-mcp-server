package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/metadata"
)

var (
	coreTools         = []string{"get_components", "invoke_actor_method", "invoke_service"}
	stateTools        = []string{"delete_state", "execute_transaction", "get_state", "save_state"}
	pubsubTools       = []string{"publish_event", "publish_event_with_metadata"}
	bindingsTools     = []string{"invoke_output_binding"}
	secretsTools      = []string{"get_bulk_secrets", "get_secret"}
	lockTools         = []string{"acquire_lock", "release_lock"}
	conversationTools = []string{"converse_with_llm"}
	cryptoTools       = []string{"decrypt_data", "encrypt_data"}
)

// testDaprClient implements dapr.Client for code that only needs GetMetadata and Close.
// The shared test/mocks client does not satisfy dapr.Client, so it cannot be passed here.
// Any other method panics on the nil embedded interface,
// which flags a test reaching further than intended.
type testDaprClient struct {
	dapr.Client
	mock.Mock
}

func newTestDaprClient() *testDaprClient {
	return &testDaprClient{}
}

func (c *testDaprClient) GetMetadata(ctx context.Context) (*dapr.GetMetadataResponse, error) {
	args := c.Called(ctx)
	resp, _ := args.Get(0).(*dapr.GetMetadataResponse)
	return resp, args.Error(1)
}

func (c *testDaprClient) Close() {}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func componentsOfTypes(types ...string) []metadata.ComponentInfo {
	components := make([]metadata.ComponentInfo, 0, len(types))
	for _, typ := range types {
		components = append(components, metadata.ComponentInfo{Name: typ, Type: typ})
	}
	return components
}

func TestPresentBuildingBlocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		types []string
		want  map[buildingBlock]bool
	}{
		{name: "state", types: []string{"state.redis"}, want: map[buildingBlock]bool{blockState: true}},
		{name: "pubsub", types: []string{"pubsub.kafka"}, want: map[buildingBlock]bool{blockPubSub: true}},
		{name: "bindings", types: []string{"bindings.http"}, want: map[buildingBlock]bool{blockBindings: true}},
		{name: "secret stores", types: []string{"secretstores.local.file"}, want: map[buildingBlock]bool{blockSecrets: true}},
		{name: "lock", types: []string{"lock.redis"}, want: map[buildingBlock]bool{blockLock: true}},
		{name: "conversation", types: []string{"conversation.openai"}, want: map[buildingBlock]bool{blockConversation: true}},
		{name: "crypto", types: []string{"crypto.dapr.localstorage"}, want: map[buildingBlock]bool{blockCrypto: true}},
		{name: "unknown type", types: []string{"configuration.redis", "middleware.http.oauth2"}, want: map[buildingBlock]bool{}},
		{name: "prefix without dot is not a match", types: []string{"statestore"}, want: map[buildingBlock]bool{}},
		{name: "empty", types: nil, want: map[buildingBlock]bool{}},
		{name: "duplicates", types: []string{"state.redis", "state.postgresql", "pubsub.redis"}, want: map[buildingBlock]bool{blockState: true, blockPubSub: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, presentBuildingBlocks(componentsOfTypes(tt.types...)))
		})
	}
}

// listTools connects an in-memory client to server and returns its tools.
func listTools(t *testing.T, server *mcp.Server) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)
	return result.Tools
}

// listToolNames returns the sorted names of the tools registered on server.
func listToolNames(t *testing.T, server *mcp.Server) []string {
	t.Helper()
	tools := listTools(t, server)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func metadataWithTypes(types ...string) *dapr.GetMetadataResponse {
	resp := &dapr.GetMetadataResponse{}
	for _, typ := range types {
		resp.RegisteredComponents = append(resp.RegisteredComponents, &dapr.MetadataRegisteredComponents{Name: typ, Type: typ})
	}
	return resp
}

func sortedConcat(groups ...[]string) []string {
	out := slices.Concat(groups...)
	slices.Sort(out)
	return out
}

// TestRegisterTools is not parallel: the tool packages keep their client in package variables.
func TestRegisterTools(t *testing.T) {
	tests := []struct {
		name  string
		types []string
		want  []string
	}{
		{name: "no components registers core tools only", want: coreTools},
		{name: "state", types: []string{"state.redis"}, want: sortedConcat(coreTools, stateTools)},
		{name: "pubsub", types: []string{"pubsub.redis"}, want: sortedConcat(coreTools, pubsubTools)},
		{name: "bindings", types: []string{"bindings.http"}, want: sortedConcat(coreTools, bindingsTools)},
		{name: "secrets", types: []string{"secretstores.local.env"}, want: sortedConcat(coreTools, secretsTools)},
		{name: "lock", types: []string{"lock.redis"}, want: sortedConcat(coreTools, lockTools)},
		{name: "conversation", types: []string{"conversation.echo"}, want: sortedConcat(coreTools, conversationTools)},
		{name: "crypto", types: []string{"crypto.dapr.localstorage"}, want: sortedConcat(coreTools, cryptoTools)},
		{
			name: "all building blocks",
			types: []string{
				"state.redis", "pubsub.redis", "bindings.http", "secretstores.local.env",
				"lock.redis", "conversation.echo", "crypto.dapr.localstorage",
			},
			want: sortedConcat(coreTools, stateTools, pubsubTools, bindingsTools, secretsTools, lockTools, conversationTools, cryptoTools),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestDaprClient()
			client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(tt.types...), nil)
			server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)

			require.NoError(t, registerTools(context.Background(), server, client, nil, discardLogger()))
			assert.Equal(t, tt.want, listToolNames(t, server))
		})
	}
}

// TestToolInputSchemasAreNotNullable guards against slice arguments being typed ["null","array"],
// which some LLM clients reject when they convert the schema into a function definition.
func TestToolInputSchemasAreNotNullable(t *testing.T) {
	client := newTestDaprClient()
	client.On("GetMetadata", mock.Anything).Return(metadataWithTypes(
		"state.redis", "pubsub.redis", "bindings.http", "secretstores.local.env",
		"lock.redis", "conversation.echo", "crypto.dapr.localstorage",
	), nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	require.NoError(t, registerTools(context.Background(), server, client, nil, discardLogger()))

	for _, tool := range listTools(t, server) {
		schema, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		assert.NotContains(t, string(schema), `"null"`, "tool %s has a nullable input type", tool.Name)
	}
}

func TestRegisterToolsMetadataError(t *testing.T) {
	client := newTestDaprClient()
	sidecarErr := errors.New("sidecar unavailable")
	client.On("GetMetadata", mock.Anything).Return(nil, sidecarErr)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)

	err := registerTools(context.Background(), server, client, nil, discardLogger())
	require.ErrorIs(t, err, sidecarErr)
	assert.Equal(t, coreTools, listToolNames(t, server), "core tools register before component discovery")
}
