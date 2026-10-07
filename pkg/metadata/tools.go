// Package metadata exposes the Dapr sidecar's component inventory as the
// get_components MCP tool.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "metadata"

	toolGetComponents = "get_components"

	spanGetLiveComponentList = "metadata.get_live_component_list"
)

// Dapr component type prefixes of the building blocks this server exposes.
const (
	TypePrefixState        = "state."
	TypePrefixPubSub       = "pubsub."
	TypePrefixBindings     = "bindings."
	TypePrefixConversation = "conversation."
	TypePrefixSecretStores = "secretstores."
	TypePrefixLock         = "lock."
	TypePrefixCrypto       = "crypto."
)

var supportedTypePrefixes = []string{
	TypePrefixState,
	TypePrefixPubSub,
	TypePrefixBindings,
	TypePrefixConversation,
	TypePrefixSecretStores,
	TypePrefixLock,
	TypePrefixCrypto,
}

var errClientNotInitialized = errors.New("dapr client not initialized on the server side")

// MetadataClient defines the interface for metadata operations.
type MetadataClient interface {
	GetMetadata(ctx context.Context) (*dapr.GetMetadataResponse, error)
}

// ComponentListWrapper is the structured result of the get_components tool.
type ComponentListWrapper struct {
	Components []ComponentInfo `json:"components" jsonschema:"A list of Dapr components found in the sidecar."`
}

// ComponentInfo describes one Dapr component loaded in the sidecar.
type ComponentInfo struct {
	Name         string   `json:"name" jsonschema:"The unique name of the component."`
	Type         string   `json:"type" jsonschema:"The type of the component (e.g., state.redis, pubsub.redis)."`
	Version      string   `json:"version,omitempty" jsonschema:"The version of the Component (e.g., v1)."`
	Capabilities []string `json:"capabilities" jsonschema:"The capabilities of the Component."`
}

func isSupportedType(componentType string) bool {
	for _, prefix := range supportedTypePrefixes {
		if strings.HasPrefix(componentType, prefix) {
			return true
		}
	}
	return false
}

// GetLiveComponentList asks the sidecar for its registered components and
// returns those belonging to a building block this server exposes.
// The result is never nil, so it marshals as an empty JSON array.
func GetLiveComponentList(ctx context.Context, client MetadataClient) ([]ComponentInfo, error) {
	ctx, span := otel.Tracer(toolkit.TracerName).Start(ctx, spanGetLiveComponentList)
	defer span.End()

	resp, err := client.GetMetadata(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("fetch Dapr metadata: %w", err)
	}

	components := []ComponentInfo{}
	if resp == nil {
		span.SetStatus(codes.Ok, "")
		return components, nil
	}
	for _, component := range resp.RegisteredComponents {
		if component == nil || !isSupportedType(component.Type) {
			continue
		}
		capabilities := component.Capabilities
		if capabilities == nil {
			capabilities = []string{}
		}
		components = append(components, ComponentInfo{
			Name:         component.Name,
			Type:         component.Type,
			Version:      component.Version,
			Capabilities: capabilities,
		})
	}

	span.SetStatus(codes.Ok, "")
	return components, nil
}

type handler struct {
	client MetadataClient
	inst   toolkit.Instrumentation
}

func (h *handler) getComponents(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, ComponentListWrapper, error) {
	ctx, call := h.inst.Start(ctx, toolGetComponents, packageName)
	defer call.End()

	if h.client == nil {
		return call.Fail(errClientNotInitialized), ComponentListWrapper{}, nil
	}

	components, err := GetLiveComponentList(ctx, h.client)
	if err != nil {
		return call.Fail(fmt.Errorf("fetch live Dapr component list: %w", err)), ComponentListWrapper{}, nil
	}

	call.Succeed("components", len(components))
	text := fmt.Sprintf("Successfully retrieved %d Dapr component(s). The details are returned in the structured result.", len(components))
	return toolkit.TextResult(text), ComponentListWrapper{Components: components}, nil
}

// RegisterTools registers the get_components tool on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client MetadataClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:        toolGetComponents,
		Title:       "Retrieve Live Dapr Component List (Call This First)",
		Description: "Call this tool first. It retrieves a detailed list of all currently running Dapr components (state stores, pub/sub brokers, bindings, conversations, secret stores, locks, cryptography, etc.) in the sidecar. **This is a READ-ONLY and IDEMPOTENT operation.** Use the structured result of this call to discover valid component names (e.g., 'statestore-redis') and capabilities before invoking other tools.",
		Annotations: toolkit.ReadOnly.Annotations(false),
	}, h.getComponents)
}
