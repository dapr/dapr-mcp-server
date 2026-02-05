package pubsub

import (
	"context"
	"fmt"
	"log"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

// PubSubClient defines the interface for pub/sub operations.
type PubSubClient interface {
	PublishEvent(ctx context.Context, pubsubName, topicName string, data interface{}, opts ...dapr.PublishEventOption) error
}

type PublishArgs struct {
	PubsubName string `json:"pubsubName" jsonschema:"The name of the Dapr pubsub component (e.g., 'pubsub')."`
	Topic      string `json:"topic" jsonschema:"The topic to publish the message to (e.g., 'orders')."`
	Message    string `json:"message" jsonschema:"The message payload to publish, typically a JSON string."`
}

var (
	pubsubClient PubSubClient
	toolMetrics  *telemetry.ToolMetrics
)

func publishEventTool(ctx context.Context, req *mcp.CallToolRequest, args PublishArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "publish_event", "pubsub")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "publish_event")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "publish_event"),
		attribute.String("mcp.tool.package", "pubsub"),
		attribute.String("dapr.component.name", args.PubsubName),
		attribute.String("dapr.pubsub.topic", args.Topic),
	)

	data := []byte(args.Message)

	propagator := otel.GetTextMapPropagator()
	metadata := make(map[string]string)
	propagator.Inject(ctx, propagation.MapCarrier(metadata))

	opts := []dapr.PublishEventOption{
		dapr.PublishEventWithContentType("application/json"),
		dapr.PublishEventWithMetadata(metadata),
	}

	if err := pubsubClient.PublishEvent(ctx, args.PubsubName, args.Topic, data, opts...); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.PubsubName)
		}
		log.Printf("Dapr PublishEvent failed: %v", err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Failed to publish event to topic '%s' on pubsub '%s'. Dapr Error: %v", args.Topic, args.PubsubName, err),
			}},
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.PubsubName)
	}

	successMessage := fmt.Sprintf("Successfully published message to topic '%s' on pubsub component '%s'.", args.Topic, args.PubsubName)

	log.Println(successMessage)
	structuredResult := map[string]interface{}{
		"status":      "published",
		"pubsub_name": args.PubsubName,
		"topic":       args.Topic,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

type PublishWithMetadataArgs struct {
	PubsubName string            `json:"pubsubName" jsonschema:"The name of the Dapr pubsub component (e.g., 'pubsub')."`
	Topic      string            `json:"topic" jsonschema:"The topic to publish the message to (e.g., 'orders')."`
	Message    string            `json:"message" jsonschema:"The message payload to publish, typically a JSON string."`
	Metadata   map[string]string `json:"metadata" jsonschema:"Optional key-value pairs to send as message headers or routing data (e.g., 'ttlInSeconds': '60')."`
}

func publishEventWithMetadataTool(ctx context.Context, req *mcp.CallToolRequest, args PublishWithMetadataArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "publish_event_with_metadata", "pubsub")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "publish_event_with_metadata")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "publish_event_with_metadata"),
		attribute.String("mcp.tool.package", "pubsub"),
		attribute.String("dapr.component.name", args.PubsubName),
		attribute.String("dapr.pubsub.topic", args.Topic),
	)
	data := []byte(args.Message)

	opts := make([]dapr.PublishEventOption, 0)
	if len(args.Metadata) > 0 {
		opts = append(opts, dapr.PublishEventWithMetadata(args.Metadata))
	}
	opts = append(opts, dapr.PublishEventWithContentType("application/json"))

	if err := pubsubClient.PublishEvent(ctx, args.PubsubName, args.Topic, data, opts...); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.PubsubName)
		}
		log.Printf("Dapr PublishEventWithMetadata failed: %v", err)
		toolErrorMessage := fmt.Sprintf("failed to publish event to topic '%s' on pubsub '%s' with metadata. Dapr Error: %v", args.Topic, args.PubsubName, err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.PubsubName)
	}

	successMessage := fmt.Sprintf("Successfully published message with %d metadata key(s) to topic '%s' on pubsub component '%s'.", len(args.Metadata), args.Topic, args.PubsubName)

	log.Println(successMessage)
	structuredResult := map[string]interface{}{
		"status":        "published_with_metadata",
		"pubsub_name":   args.PubsubName,
		"topic":         args.Topic,
		"metadata_keys": len(args.Metadata),
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

func RegisterTools(server *mcp.Server, client PubSubClient, metrics *telemetry.ToolMetrics) {
	pubsubClient = client
	toolMetrics = metrics

	notDestructive := false
	notIdempotent := false
	isOpenWorld := true

	mcp.AddTool(server, &mcp.Tool{
		Name:  "publish_event",
		Title: "Publish Event (Simple)",
		Description: "Publishes a message to a topic using the Dapr Pub/Sub building block. **This is a SIDE-EFFECT action that triggers decoupled, asynchronous workflows.** Publishing is additive (non-destructive) but NOT IDEMPOTENT (sending twice results in two messages).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use the `get_components` tool to discover available pubsub components and their names before invoking this tool.\n" +
			"2. Ensure the `PubsubName` matches a valid pubsub component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `PubsubName`, `Topic`, and `Message`.\n" +
			"2. **NEVER INVENT**: You must NOT invent `PubsubName` or `Topic` names.\n" +
			"3. **MESSAGE RULE**: The `Message` MUST be the content the user wishes to publish and should reflect user intent.\n" +
			"4. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &notDestructive,
			IdempotentHint:  notIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, publishEventTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "publish_event_with_metadata",
		Title: "Publish Event (With Metadata)",
		Description: "Publishes a message to a topic including optional metadata/headers (e.g., routing headers, 'ttlInSeconds'). **This is a SIDE-EFFECT action.** Use this tool when you need granular control over message delivery or routing.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use the `get_components` tool to discover available pubsub components and their names before invoking this tool.\n" +
			"2. Ensure the `PubsubName` matches a valid pubsub component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `PubsubName`, `Topic`, and `Message`.\n" +
			"2. **METADATA RULE**: The `Metadata` field MUST be a dictionary/map containing valid key-value pairs for the pubsub component (e.g., message time-to-live).\n" +
			"3. **DEFAULTS**: If `Metadata` is empty, the message will be published without additional headers or routing data.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &notDestructive,
			IdempotentHint:  notIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, publishEventWithMetadataTool)
}
