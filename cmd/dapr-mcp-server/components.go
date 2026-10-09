package main

import (
	"strings"

	metadata "github.com/dapr/dapr-mcp-server/pkg/metadata"
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
