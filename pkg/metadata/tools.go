package metadata

import (
	"context"
	"fmt"
	"log"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

// MetadataClient defines the interface for metadata operations.
type MetadataClient interface {
	GetMetadata(ctx context.Context) (*dapr.GetMetadataResponse, error)
}

type ComponentListWrapper struct {
	Components []ComponentInfo `json:"components" jsonschema:"A list of Dapr components found in the sidecar."`
}

type ComponentInfo struct {
	Name         string   `json:"name" jsonschema:"The unique name of the component."`
	Type         string   `json:"type" jsonschema:"The type of the component (e.g., state.redis, pubsub.redis)."`
	Version      string   `json:"version,omitempty" jsonschema:"The version of the Component (e.g., v1)."`
	Capabilities []string `json:"capabilities" jsonschema:"The capabilities of the Component."`
}

var (
	metadataClient MetadataClient
	toolMetrics    *telemetry.ToolMetrics
)

func GetLiveComponentList(ctx context.Context, client MetadataClient) ([]ComponentInfo, error) {
	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "get_components")
	defer span.End()

	span.SetAttributes(
		attribute.String("mcp.tool.name", "get_components"),
		attribute.String("mcp.tool.package", "metadata"),
	)

	metadata, err := client.GetMetadata(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("failed to fetch Dapr metadata: %w", err)
	}

	var components []ComponentInfo
	for _, component := range metadata.RegisteredComponents {
		if strings.Contains(component.Type, "pubsub") ||
			strings.Contains(component.Type, "state") ||
			strings.Contains(component.Type, "binding") ||
			strings.Contains(component.Type, "conversation") ||
			strings.Contains(component.Type, "secretstores") ||
			strings.Contains(component.Type, "lock") ||
			strings.Contains(component.Type, "crypto") {

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
	}

	span.SetStatus(codes.Ok, "")
	return components, nil
}

func getMetadataTool(ctx context.Context, req *mcp.CallToolRequest, args any) (
	*mcp.CallToolResult,
	ComponentListWrapper,
	error,
) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "get_components", "metadata")
	}

	if metadataClient == nil {
		if timer != nil {
			timer.Stop("error", "metadata")
		}
		toolErrorMessage := "Dapr client not initialized on the server side."
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, ComponentListWrapper{}, nil
	}
	log.Printf("Request: %v", req)

	components, err := GetLiveComponentList(ctx, metadataClient)
	if err != nil {
		if timer != nil {
			timer.Stop("error", "metadata")
		}
		log.Printf("Error calling getMetadataTool: %v", err)
		toolErrorMessage := fmt.Sprintf("Error fetching live Dapr component list: %v", err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, ComponentListWrapper{}, nil
	}
	log.Printf("Components: %s", components)

	if timer != nil {
		timer.Stop("success", "metadata")
	}

	wrapper := ComponentListWrapper{
		Components: components,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("Successfully retrieved %d Dapr component(s). The details are returned in the structured result.", len(components)),
		}},
	}, wrapper, nil
}

func RegisterTools(server *mcp.Server, client MetadataClient, metrics *telemetry.ToolMetrics) {
	metadataClient = client
	toolMetrics = metrics

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_components",
		Title:       "Retrieve Live Dapr Component List (Call This First)",
		Description: "Call this tool first. It retrieves a detailed list of all currently running Dapr components (state stores, pub/sub brokers, bindings, conversations, secret stores, locks, cryptography, etc.) in the sidecar. Use the structured result of this call to discover valid component names (e.g., 'statestore-redis') and capabilities before invoking other tools.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, getMetadataTool)
}
