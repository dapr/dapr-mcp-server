package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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
)

const (
	// toolRefreshIntervalEnv sets how often the tools are re-synced with the sidecar's components.
	// It takes a Go duration, and 0 turns the periodic refresh off.
	toolRefreshIntervalEnv     = "DAPR_MCP_TOOL_REFRESH_INTERVAL"
	defaultToolRefreshInterval = 30 * time.Second
)

// errInvalidToolRefreshInterval reports a DAPR_MCP_TOOL_REFRESH_INTERVAL that is not a non-negative duration.
var errInvalidToolRefreshInterval = errors.New("invalid " + toolRefreshIntervalEnv)

// toolRefreshInterval parses the value of DAPR_MCP_TOOL_REFRESH_INTERVAL,
// returning the default when it is empty.
func toolRefreshInterval(value string) (time.Duration, error) {
	if value == "" {
		return defaultToolRefreshInterval, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval < 0 {
		return 0, fmt.Errorf("%w: %q must be a non-negative duration such as 30s, or 0 to disable", errInvalidToolRefreshInterval, value)
	}
	return interval, nil
}

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

	mu         sync.Mutex
	registered map[buildingBlock]bool
}

// registerTools registers the core tools and the tools of each building block
// that has at least one component loaded in the sidecar.
// The returned syncer updates the building-block tools as components change.
func registerTools(ctx context.Context, server *mcp.Server, client dapr.Client, toolMetrics *telemetry.ToolMetrics, logger *slog.Logger) (*toolSyncer, error) {
	syncer := &toolSyncer{
		server:     server,
		client:     client,
		metrics:    toolMetrics,
		logger:     logger,
		registered: make(map[buildingBlock]bool),
	}

	metadata.RegisterTools(server, client, toolMetrics, metadata.WithComponentsObserver(syncer.apply))
	invoke.RegisterTools(server, client, toolMetrics)
	actor.RegisterTools(server, client, toolMetrics)

	if err := syncer.refresh(ctx); err != nil {
		return nil, err
	}
	return syncer, nil
}

// refresh fetches the sidecar's components and updates the registered tools to match.
func (s *toolSyncer) refresh(ctx context.Context) error {
	components, err := metadata.GetLiveComponentList(ctx, s.client)
	if err != nil {
		return fmt.Errorf("get components: %w", err)
	}
	s.apply(components)
	return nil
}

// apply registers the tools of building blocks that gained their first component
// and removes the tools of building blocks that lost their last one.
func (s *toolSyncer) apply(components []metadata.ComponentInfo) {
	present := presentBuildingBlocks(components)

	s.mu.Lock()
	defer s.mu.Unlock()
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

// run refreshes the tools every interval until ctx is done.
// A failed refresh keeps the current tools and is retried on the next tick.
func (s *toolSyncer) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.refresh(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("Failed to refresh tools from Dapr components, keeping current tools", "error", err)
			}
		}
	}
}
