package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	dtworkflow "github.com/dapr/durabletask-go/workflow"
	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	actor "github.com/dapr/dapr-mcp-server/pkg/actors"
	binding "github.com/dapr/dapr-mcp-server/pkg/bindings"
	conversation "github.com/dapr/dapr-mcp-server/pkg/conversation"
	crypto "github.com/dapr/dapr-mcp-server/pkg/crypto"
	invoke "github.com/dapr/dapr-mcp-server/pkg/invoke"
	lock "github.com/dapr/dapr-mcp-server/pkg/lock"
	metadata "github.com/dapr/dapr-mcp-server/pkg/metadata"
	pubsub "github.com/dapr/dapr-mcp-server/pkg/pubsub"
	secret "github.com/dapr/dapr-mcp-server/pkg/secrets"
	state "github.com/dapr/dapr-mcp-server/pkg/state"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
	workflow "github.com/dapr/dapr-mcp-server/pkg/workflow"
)

const (
	// methodListTools is the MCP method a client calls to list the server's tools.
	methodListTools = "tools/list"
	// toolSyncTimeout bounds the sidecar call made before answering tools/list,
	// so a hung sidecar delays a tool listing by at most this long.
	toolSyncTimeout = 5 * time.Second
)

// blockTools registers and names the tools of one building block.
type blockTools struct {
	register func(*mcp.Server, dapr.Client, *telemetry.ToolMetrics)
	names    func() []string
}

var toolsByBlock = map[buildingBlock]blockTools{
	blockState: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { state.RegisterTools(s, c, m) },
		names:    state.ToolNames,
	},
	blockPubSub: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { pubsub.RegisterTools(s, c, m) },
		names:    pubsub.ToolNames,
	},
	blockBindings: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { binding.RegisterTools(s, c, m) },
		names:    binding.ToolNames,
	},
	blockSecrets: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { secret.RegisterTools(s, c, m) },
		names:    secret.ToolNames,
	},
	blockLock: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { lock.RegisterTools(s, c, m) },
		names:    lock.ToolNames,
	},
	blockConversation: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { conversation.RegisterTools(s, c, m) },
		names:    conversation.ToolNames,
	},
	blockCrypto: {
		register: func(s *mcp.Server, c dapr.Client, m *telemetry.ToolMetrics) { crypto.RegisterTools(s, c, m) },
		names:    crypto.ToolNames,
	},
}

// toolSyncer keeps the building-block tools in step with the components loaded in the sidecar.
// Dapr hot-reloads components, so a block's tools are added when its first component appears
// and removed when its last one goes, and the SDK tells connected clients the tool list changed.
type toolSyncer struct {
	server  *mcp.Server
	client  dapr.Client
	metrics *telemetry.ToolMetrics
	logger  *slog.Logger

	// fetches numbers each component fetch as it starts,
	// so a fetch that finishes after a newer one cannot undo the newer result.
	fetches atomic.Uint64

	mu         sync.Mutex
	registered map[buildingBlock]bool
	applied    uint64 // number of the newest fetch applied
}

// registerTools registers the core tools (including workflow) and the tools of each building block
// that has at least one component loaded in the sidecar.
// The returned syncer updates the building-block tools as components change:
// before every tools/list request and after every successful get_components call.
func registerTools(ctx context.Context, server *mcp.Server, client dapr.Client, toolMetrics *telemetry.ToolMetrics, logger *slog.Logger) (*toolSyncer, error) {
	syncer := &toolSyncer{
		server:     server,
		client:     client,
		metrics:    toolMetrics,
		logger:     logger,
		registered: make(map[buildingBlock]bool),
	}

	server.AddReceivingMiddleware(syncer.syncOnListTools)
	metadata.RegisterTools(server, client, toolMetrics, metadata.WithComponentsObserver(syncer.startFetch))
	invoke.RegisterTools(server, client, toolMetrics)
	actor.RegisterTools(server, client, toolMetrics)
	// Workflows are part of the Dapr runtime, not a component,
	// so the client reuses the sidecar's gRPC connection.
	workflow.RegisterTools(server, dtworkflow.NewClient(client.GrpcClientConn()), toolMetrics)

	if err := syncer.refresh(ctx); err != nil {
		return nil, err
	}
	return syncer, nil
}

// refresh fetches the sidecar's components and updates the registered tools to match.
func (s *toolSyncer) refresh(ctx context.Context) error {
	done := s.startFetch()
	components, err := metadata.GetLiveComponentList(ctx, s.client)
	if err != nil {
		return fmt.Errorf("get components: %w", err)
	}
	done(components)
	return nil
}

// startFetch numbers a component fetch that is about to start
// and returns the function that applies its result.
func (s *toolSyncer) startFetch() func([]metadata.ComponentInfo) {
	fetch := s.fetches.Add(1)
	return func(components []metadata.ComponentInfo) { s.apply(fetch, components) }
}

// apply registers the tools of building blocks that gained their first component
// and removes the tools of building blocks that lost their last one.
// It ignores the result of a fetch older than the newest one already applied.
func (s *toolSyncer) apply(fetch uint64, components []metadata.ComponentInfo) {
	present := presentBuildingBlocks(components)

	s.mu.Lock()
	defer s.mu.Unlock()
	if fetch < s.applied {
		return
	}
	s.applied = fetch
	for _, p := range componentPrefixes {
		block, tools := p.block, toolsByBlock[p.block]
		switch {
		case present[block] && !s.registered[block]:
			tools.register(s.server, s.client, s.metrics)
			s.registered[block] = true
			s.logger.Info("Registered tools for Dapr building block", "block", block, "tools", tools.names())
		case !present[block] && s.registered[block]:
			s.server.RemoveTools(tools.names()...)
			delete(s.registered, block)
			s.logger.Info("Removed tools for Dapr building block with no components", "block", block, "tools", tools.names())
		}
	}
}

// isFirstPage reports whether req is not a follow-up page of a paginated list,
// so a client paging through the tools triggers one sync rather than one per page.
func isFirstPage(req mcp.Request) bool {
	params, ok := req.GetParams().(*mcp.ListToolsParams)
	return !ok || params == nil || params.Cursor == ""
}

// syncOnListTools re-syncs the tools with the sidecar's components before answering tools/list,
// so a client listing tools sees the components Dapr has hot-reloaded since the last sync.
// A failed sync is logged and the current tools are listed.
func (s *toolSyncer) syncOnListTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == methodListTools && isFirstPage(req) {
			syncCtx, cancel := context.WithTimeout(ctx, toolSyncTimeout)
			err := s.refresh(syncCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				s.logger.Warn("Failed to sync tools with Dapr components, listing current tools", "error", err)
			}
		}
		return next(ctx, method, req)
	}
}
