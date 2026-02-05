package bindings

import (
	"bytes"
	"context"
	"encoding/json"
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

// BindingsClient defines the interface for bindings operations.
type BindingsClient interface {
	InvokeBinding(ctx context.Context, in *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error)
}

type InvokeBindingArgs struct {
	BindingName string            `json:"bindingName" jsonschema:"The name of the Dapr output binding component (e.g., 'storage-binding')."`
	Operation   string            `json:"operation" jsonschema:"The operation to perform on the binding (e.g., 'create', 'get', 'delete'). Must be supported by the component."`
	Data        string            `json:"data" jsonschema:"The message or data payload to send to the external system, typically a JSON string."`
	Metadata    map[string]string `json:"metadata" jsonschema:"Optional key-value pairs required by the specific binding component for the operation (e.g., 'key' for a storage binding)."`
}

var (
	bindingsClient BindingsClient
	toolMetrics    *telemetry.ToolMetrics
)

func invokeOutputBindingTool(ctx context.Context, req *mcp.CallToolRequest, args InvokeBindingArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "invoke_binding", "bindings")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "invoke_binding")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "invoke_binding"),
		attribute.String("mcp.tool.package", "bindings"),
		attribute.String("dapr.component.name", args.BindingName),
		attribute.String("dapr.binding.operation", args.Operation),
	)

	data := []byte(args.Data)

	if args.Data == "" {
		data = nil
	}

	// Merge user metadata with baggage
	metadata := make(map[string]string)
	for k, v := range args.Metadata {
		metadata[k] = v
	}
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.MapCarrier(metadata))

	bindingReq := &dapr.InvokeBindingRequest{
		Name:      args.BindingName,
		Operation: args.Operation,
		Data:      data,
		Metadata:  metadata,
	}

	resp, err := bindingsClient.InvokeBinding(ctx, bindingReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.BindingName)
		}
		log.Printf("Dapr InvokeOutputBinding failed for binding %s: %v", args.BindingName, err)
		toolErrorMessage := fmt.Sprintf("Failed to invoke binding '%s' with operation '%s'. Dapr Error: %v", args.BindingName, args.Operation, err)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.BindingName)
	}

	resultData := ""
	if resp != nil && len(resp.Data) > 0 {
		var prettyJSON bytes.Buffer
		if json.Indent(&prettyJSON, resp.Data, "", "  ") == nil {
			resultData = "\n\nResponse Data:\n" + prettyJSON.String()
		} else {
			resultData = "\n\nResponse Data (Raw):\n" + string(resp.Data)
		}
	}

	successMessage := fmt.Sprintf("Successfully invoked output binding '%s' with operation '%s'.%s", args.BindingName, args.Operation, resultData)

	log.Println(successMessage)
	structuredResult := map[string]string{
		"binding_name":  args.BindingName,
		"operation":     args.Operation,
		"response_data": string(resp.Data),
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

func RegisterTools(server *mcp.Server, client BindingsClient, metrics *telemetry.ToolMetrics) {
	bindingsClient = client
	toolMetrics = metrics

	isDestructive := true
	notReadOnly := false
	notIdempotent := false
	isOpenWorld := true

	mcp.AddTool(server, &mcp.Tool{
		Name:  "invoke_output_binding",
		Title: "Interact with External System via Binding",
		Description: "Invokes an operation on a Dapr output binding component to interact with external systems (e.g., queues, databases, webhooks). **This is a SIDE-EFFECT action that can be DESTRUCTIVE.** Use this tool to perform I/O actions.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `BindingName` of the output binding.\n" +
			"2. Ensure `Operation` and `Data` are explicitly provided by the user.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `BindingName`, `Operation`, and the `Data` payload.\n" +
			"2. **NEVER INVENT**: You must NOT invent `BindingName` or `Operation` names; they must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification before generating the tool call.\n\n" +
			"**METADATA**: The `Metadata` field MUST be used to pass headers or component-specific settings (e.g., overriding the target URL for an HTTP binding).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &isDestructive,
			ReadOnlyHint:    notReadOnly,
			IdempotentHint:  notIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, invokeOutputBindingTool)
}
