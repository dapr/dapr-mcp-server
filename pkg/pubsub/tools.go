// Package pubsub exposes the Dapr publish and subscribe building block as
// MCP tools.
package pubsub

import (
	"context"
	"fmt"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "pubsub"

	toolPublishEvent             = "publish_event"
	toolPublishEventWithMetadata = "publish_event_with_metadata"

	attrTopic = "dapr.pubsub.topic"

	statusPublished             = "published"
	statusPublishedWithMetadata = "published_with_metadata"
)

// PubSubClient defines the interface for pub/sub operations.
type PubSubClient interface {
	PublishEvent(ctx context.Context, pubsubName, topicName string, data interface{}, opts ...dapr.PublishEventOption) error
}

// PublishArgs are the arguments of the publish_event tool.
type PublishArgs struct {
	PubsubName  string `json:"pubsubName" jsonschema:"The name of the Dapr pubsub component (e.g., 'pubsub')."`
	Topic       string `json:"topic" jsonschema:"The topic to publish the message to (e.g., 'orders')."`
	Message     string `json:"message" jsonschema:"The message payload to publish, typically a JSON string."`
	ContentType string `json:"contentType,omitempty" jsonschema:"Optional content type of message. Defaults to application/json when message is valid JSON, text/plain otherwise."`
}

// PublishWithMetadataArgs are the arguments of the publish_event_with_metadata tool.
type PublishWithMetadataArgs struct {
	PubsubName  string            `json:"pubsubName" jsonschema:"The name of the Dapr pubsub component (e.g., 'pubsub')."`
	Topic       string            `json:"topic" jsonschema:"The topic to publish the message to (e.g., 'orders')."`
	Message     string            `json:"message" jsonschema:"The message payload to publish, typically a JSON string."`
	ContentType string            `json:"contentType,omitempty" jsonschema:"Optional content type of message. Defaults to application/json when message is valid JSON, text/plain otherwise."`
	Metadata    map[string]string `json:"metadata,omitempty" jsonschema:"Optional key-value pairs to send as message headers or routing data (e.g., 'ttlInSeconds': '60')."`
}

type handler struct {
	client PubSubClient
	inst   toolkit.Instrumentation
}

// publish sends one message and reports it under the given tool name.
func (h *handler) publish(ctx context.Context, tool string, args PublishWithMetadataArgs) (*mcp.CallToolResult, bool) {
	ctx, call := h.inst.Start(ctx, tool, packageName,
		attribute.String(toolkit.AttrComponentName, args.PubsubName),
		attribute.String(attrTopic, args.Topic),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "pubsubName", Value: args.PubsubName},
		toolkit.Field{Name: "topic", Value: args.Topic},
		toolkit.Field{Name: "message", Value: args.Message},
	); res != nil {
		return res, false
	}

	data := []byte(args.Message)
	contentType := args.ContentType
	if contentType == "" {
		contentType = toolkit.ContentTypeFor(data)
	}
	opts := []dapr.PublishEventOption{dapr.PublishEventWithContentType(contentType)}
	if len(args.Metadata) > 0 {
		opts = append(opts, dapr.PublishEventWithMetadata(args.Metadata))
	}

	if err := h.client.PublishEvent(ctx, args.PubsubName, args.Topic, data, opts...); err != nil {
		return call.Fail(fmt.Errorf("publish to topic %q on pubsub %q: %w", args.Topic, args.PubsubName, err)), false
	}

	call.Succeed("pubsub", args.PubsubName, "topic", args.Topic, "metadata_keys", len(args.Metadata))
	return nil, true
}

func (h *handler) publishEvent(ctx context.Context, _ *mcp.CallToolRequest, args PublishArgs) (*mcp.CallToolResult, any, error) {
	if res, ok := h.publish(ctx, toolPublishEvent, PublishWithMetadataArgs{
		PubsubName:  args.PubsubName,
		Topic:       args.Topic,
		Message:     args.Message,
		ContentType: args.ContentType,
	}); !ok {
		return res, nil, nil
	}

	text := fmt.Sprintf("Successfully published message to topic '%s' on pubsub component '%s'.", args.Topic, args.PubsubName)
	return toolkit.TextResult(text), map[string]any{
		"status":      statusPublished,
		"pubsub_name": args.PubsubName,
		"topic":       args.Topic,
	}, nil
}

func (h *handler) publishEventWithMetadata(ctx context.Context, _ *mcp.CallToolRequest, args PublishWithMetadataArgs) (*mcp.CallToolResult, any, error) {
	if res, ok := h.publish(ctx, toolPublishEventWithMetadata, args); !ok {
		return res, nil, nil
	}

	text := fmt.Sprintf("Successfully published message with %d metadata key(s) to topic '%s' on pubsub component '%s'.", len(args.Metadata), args.Topic, args.PubsubName)
	return toolkit.TextResult(text), map[string]any{
		"status":        statusPublishedWithMetadata,
		"pubsub_name":   args.PubsubName,
		"topic":         args.Topic,
		"metadata_keys": len(args.Metadata),
	}, nil
}

// RegisterTools registers the publish_event and publish_event_with_metadata
// tools on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client PubSubClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolPublishEvent,
		Title: "Publish Event (Simple)",
		Description: "Publishes a message to a topic using the Dapr Pub/Sub building block. **This is a SIDE-EFFECT action that triggers decoupled, asynchronous workflows.** Publishing is additive (non-destructive) but NOT IDEMPOTENT (sending twice results in two messages).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use the `get_components` tool to discover available pubsub components and their names before invoking this tool.\n" +
			"2. Ensure the `pubsubName` matches a valid pubsub component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `pubsubName`, `topic`, and `message`.\n" +
			"2. **NEVER INVENT**: You must NOT invent `pubsubName` or `topic` names.\n" +
			"3. **MESSAGE RULE**: The `message` MUST be the content the user wishes to publish and should reflect user intent.\n" +
			"4. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.publishEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolPublishEventWithMetadata,
		Title: "Publish Event (With Metadata)",
		Description: "Publishes a message to a topic including optional metadata/headers (e.g., routing headers, 'ttlInSeconds'). **This is a SIDE-EFFECT action that is NOT IDEMPOTENT.** Use this tool when you need granular control over message delivery or routing.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use the `get_components` tool to discover available pubsub components and their names before invoking this tool.\n" +
			"2. Ensure the `pubsubName` matches a valid pubsub component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `pubsubName`, `topic`, and `message`.\n" +
			"2. **METADATA RULE**: The `metadata` field MUST be a dictionary/map containing valid key-value pairs for the pubsub component (e.g., message time-to-live).\n" +
			"3. **DEFAULTS**: If `metadata` is empty, the message will be published without additional headers or routing data.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.publishEventWithMetadata)
}

// ToolNames returns the names of the tools RegisterTools adds.
func ToolNames() []string {
	return []string{toolPublishEvent, toolPublishEventWithMetadata}
}
