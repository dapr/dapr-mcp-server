package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// buildingBlock names a Dapr building block whose tools register only
// when the sidecar has at least one component of that kind.
type buildingBlock string

const (
	blockState        buildingBlock = "state"
	blockPubSub       buildingBlock = "pubsub"
	blockBindings     buildingBlock = "bindings"
	blockSecrets      buildingBlock = "secrets"
	blockLock         buildingBlock = "lock"
	blockConversation buildingBlock = "conversation"
	blockCrypto       buildingBlock = "crypto"
)

// Dapr component type prefixes, as reported by the sidecar metadata API.
const (
	stateComponentPrefix        = "state."
	pubsubComponentPrefix       = "pubsub."
	bindingsComponentPrefix     = "bindings."
	secretStoreComponentPrefix  = "secretstores."
	lockComponentPrefix         = "lock."
	conversationComponentPrefix = "conversation."
	cryptoComponentPrefix       = "crypto."
)

var componentPrefixes = []struct {
	prefix string
	block  buildingBlock
}{
	{stateComponentPrefix, blockState},
	{pubsubComponentPrefix, blockPubSub},
	{bindingsComponentPrefix, blockBindings},
	{secretStoreComponentPrefix, blockSecrets},
	{lockComponentPrefix, blockLock},
	{conversationComponentPrefix, blockConversation},
	{cryptoComponentPrefix, blockCrypto},
}

// presentBuildingBlocks reports which building blocks have at least one component loaded.
func presentBuildingBlocks(components []metadata.ComponentInfo) map[buildingBlock]bool {
	present := make(map[buildingBlock]bool)
	for _, c := range components {
		for _, p := range componentPrefixes {
			if strings.HasPrefix(c.Type, p.prefix) {
				present[p.block] = true
				break
			}
		}
	}
	return present
}

// registerTools registers the core tools and then the tools for each
// building block that has at least one component loaded in the sidecar.
func registerTools(ctx context.Context, server *mcp.Server, client dapr.Client, toolMetrics *telemetry.ToolMetrics, logger *slog.Logger) error {
	metadata.RegisterTools(server, client, toolMetrics)
	invoke.RegisterTools(server, client, toolMetrics)
	actor.RegisterTools(server, client, toolMetrics)

	components, err := metadata.GetLiveComponentList(ctx, client)
	if err != nil {
		return fmt.Errorf("get components: %w", err)
	}

	present := presentBuildingBlocks(components)
	logger.Info("Discovered Dapr components", "components", present)

	if present[blockPubSub] {
		pubsub.RegisterTools(server, client, toolMetrics)
	}
	if present[blockBindings] {
		binding.RegisterTools(server, client, toolMetrics)
	}
	if present[blockState] {
		state.RegisterTools(server, client, toolMetrics)
	}
	if present[blockSecrets] {
		secret.RegisterTools(server, client, toolMetrics)
	}
	if present[blockConversation] {
		conversation.RegisterTools(server, client, toolMetrics)
	}
	if present[blockCrypto] {
		crypto.RegisterTools(server, client, toolMetrics)
	}
	if present[blockLock] {
		lock.RegisterTools(server, client, toolMetrics)
	}
	return nil
}
