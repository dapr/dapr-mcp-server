package invoke

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

// InvokeClient defines the interface for service invocation operations.
type InvokeClient interface {
	InvokeMethodWithContent(ctx context.Context, appID, methodName, verb string, content *dapr.DataContent) ([]byte, error)
}

type InvokeServiceArgs struct {
	AppID    string            `json:"appID" jsonschema:"The Dapr application ID of the service to call (e.g., 'order-processor')."`
	Method   string            `json:"method" jsonschema:"The method/endpoint on the target service to call (e.g., 'status')."`
	Data     string            `json:"data" jsonschema:"The body payload for the request, typically a JSON string."`
	HTTPVerb string            `json:"httpVerb" jsonschema:"The HTTP verb to use (e.g., 'GET', 'POST', 'PUT'). Default is 'POST'."`
	Metadata map[string]string `json:"metadata,omitempty" jsonschema:"Optional key-value pairs to send as HTTP headers."`
}

var (
	invokeClient InvokeClient
	toolMetrics  *telemetry.ToolMetrics
)

func invokeServiceTool(ctx context.Context, req *mcp.CallToolRequest, args InvokeServiceArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "invoke_service", "invoke")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "invoke_service")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "invoke_service"),
		attribute.String("mcp.tool.package", "invoke"),
		attribute.String("dapr.invoke.app_id", args.AppID),
		attribute.String("dapr.invoke.method", args.Method),
		attribute.String("dapr.invoke.verb", args.HTTPVerb),
	)

	if args.HTTPVerb == "" {
		args.HTTPVerb = "POST"
	}

	content := &dapr.DataContent{
		ContentType: "application/json",
		Data:        []byte(args.Data),
	}

	// Merge user metadata with baggage
	metadata := make(map[string]string)
	for k, v := range args.Metadata {
		metadata[k] = v
	}
	propagator := otel.GetTextMapPropagator()
	propagator.Inject(ctx, propagation.MapCarrier(metadata))

	resp, err := invokeClient.InvokeMethodWithContent(ctx, args.AppID, args.Method, args.HTTPVerb, content)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.AppID)
		}
		log.Printf("Dapr InvokeMethod failed for app %s/%s: %v", args.AppID, args.Method, err)
		toolErrorMessage := fmt.Errorf("failed to invoke service method: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.AppID)
	}

	var resultData bytes.Buffer
	if json.Indent(&resultData, resp, "", "  ") != nil {
		resultData.Write(resp)
	}

	successMessage := fmt.Sprintf(
		"Successfully invoked service '%s' method '%s' (%s). The service responded with data/status.",
		args.AppID, args.Method, args.HTTPVerb,
	)
	log.Println(successMessage)
	var structuredResult map[string]interface{}
	if len(resp) > 0 {
		if err := json.Unmarshal(resp, &structuredResult); err != nil {
			structuredResult = map[string]interface{}{
				"raw_response": string(resp),
				"app_id":       args.AppID,
				"method":       args.Method,
			}
		}
	} else {
		structuredResult = map[string]interface{}{
			"status": "success_no_content",
			"app_id": args.AppID,
			"method": args.Method,
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage + "\n\nResponse:\n" + resultData.String()}},
	}, structuredResult, nil
}

func RegisterTools(server *mcp.Server, client InvokeClient, metrics *telemetry.ToolMetrics) {
	invokeClient = client
	toolMetrics = metrics

	isDestructive := true
	notReadOnly := false
	notIdempotent := false
	isOpenWorld := true

	mcp.AddTool(server, &mcp.Tool{
		Name:  "invoke_service",
		Title: "Execute Inter-Service Request",
		Description: "Calls a method (endpoint) on another Dapr-enabled service. **This is a SIDE-EFFECT action that can be DESTRUCTIVE and is NOT IDEMPOTENT for POST/PUT calls.** Use this tool to perform transactional business logic (e.g., updating data, creating resources, triggering workflows).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `AppID` of the target service.\n" +
			"2. For `HTTPVerb`, use 'GET' for read-only status checks, 'POST' for creation, and 'DELETE' for removal. Default is 'POST'.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `AppID`, `Method`, and `HTTPVerb`.\n" +
			"2. **NEVER INVENT**: You must NOT invent `AppID` or `Method` names; they must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**SECURITY WARNING**: This tool bypasses the standard Resource/Tool abstraction and directly executes service logic. Ensure user intent is clear and the operation is authorized.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &isDestructive,
			ReadOnlyHint:    notReadOnly,
			IdempotentHint:  notIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, invokeServiceTool)
}
