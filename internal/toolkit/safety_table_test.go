package toolkit_test

import (
	"context"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/actors"
	"github.com/dapr/dapr-mcp-server/pkg/bindings"
	"github.com/dapr/dapr-mcp-server/pkg/conversation"
	cryptography "github.com/dapr/dapr-mcp-server/pkg/crypto"
	"github.com/dapr/dapr-mcp-server/pkg/invoke"
	"github.com/dapr/dapr-mcp-server/pkg/lock"
	"github.com/dapr/dapr-mcp-server/pkg/metadata"
	"github.com/dapr/dapr-mcp-server/pkg/pubsub"
	"github.com/dapr/dapr-mcp-server/pkg/secrets"
	"github.com/dapr/dapr-mcp-server/pkg/state"
)

// unusedClient satisfies dapr.Client for registration only.
// Calling any of its methods panics.
type unusedClient struct{ dapr.Client }

type safety struct {
	readOnly, destructive, idempotent, openWorld bool
}

// agentsSafetyTable mirrors the "Safety defaults" table in AGENTS.md.
var agentsSafetyTable = map[string]safety{
	"get_components":              {readOnly: true, idempotent: true},
	"get_state":                   {readOnly: true, idempotent: true, openWorld: true},
	"get_secret":                  {readOnly: true, idempotent: true, openWorld: true},
	"get_bulk_secrets":            {readOnly: true, idempotent: true, openWorld: true},
	"converse_with_llm":           {readOnly: true, idempotent: true, openWorld: true},
	"decrypt_data":                {readOnly: true, idempotent: true, openWorld: true},
	"save_state":                  {idempotent: true, openWorld: true},
	"acquire_lock":                {idempotent: true, openWorld: true},
	"delete_state":                {destructive: true, idempotent: true, openWorld: true},
	"invoke_service":              {destructive: true, openWorld: true},
	"invoke_actor_method":         {destructive: true, openWorld: true},
	"invoke_output_binding":       {destructive: true, openWorld: true},
	"execute_transaction":         {destructive: true, openWorld: true},
	"encrypt_data":                {destructive: true, openWorld: true},
	"publish_event":               {openWorld: true},
	"publish_event_with_metadata": {openWorld: true},
	"release_lock":                {openWorld: true},
}

func listRegisteredTools(t *testing.T) []*mcp.Tool {
	t.Helper()
	ctx := context.Background()
	client := unusedClient{}

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	metadata.RegisterTools(server, client, nil)
	invoke.RegisterTools(server, client, nil)
	actors.RegisterTools(server, client, nil)
	pubsub.RegisterTools(server, client, nil)
	bindings.RegisterTools(server, client, nil)
	state.RegisterTools(server, client, nil)
	secrets.RegisterTools(server, client, nil)
	conversation.RegisterTools(server, client, nil)
	cryptography.RegisterTools(server, client, nil)
	lock.RegisterTools(server, client, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0.0.0"}, nil).Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	return res.Tools
}

func TestToolAnnotationsMatchAgentsSafetyTable(t *testing.T) {
	t.Parallel()
	tools := listRegisteredTools(t)

	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		seen[tool.Name] = true
	}
	for name := range agentsSafetyTable {
		assert.True(t, seen[name], "tool %q from AGENTS.md is not registered", name)
	}

	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			t.Parallel()
			want, ok := agentsSafetyTable[tool.Name]
			require.True(t, ok, "tool %q is missing from the AGENTS.md safety table", tool.Name)

			a := tool.Annotations
			require.NotNil(t, a)
			require.NotNil(t, a.DestructiveHint)
			require.NotNil(t, a.OpenWorldHint)
			assert.Equal(t, want.readOnly, a.ReadOnlyHint, "readOnlyHint")
			assert.Equal(t, want.destructive, *a.DestructiveHint, "destructiveHint")
			assert.Equal(t, want.idempotent, a.IdempotentHint, "idempotentHint")
			assert.Equal(t, want.openWorld, *a.OpenWorldHint, "openWorldHint")
		})
	}
}
