// Package bindings exposes Dapr output bindings as an MCP tool.
package bindings

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
	packageName = "bindings"

	toolInvokeOutputBinding = "invoke_output_binding"

	attrOperation = "dapr.binding.operation"
)

// BindingsClient defines the interface for bindings operations.
type BindingsClient interface {
	InvokeBinding(ctx context.Context, in *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error)
}

// InvokeBindingArgs are the arguments of the invoke_output_binding tool.
type InvokeBindingArgs struct {
	BindingName string            `json:"bindingName" jsonschema:"The name of the Dapr output binding component (e.g., 'storage-binding')."`
	Operation   string            `json:"operation" jsonschema:"The operation to perform on the binding (e.g., 'create', 'get', 'delete'). Must be supported by the component."`
	Data        string            `json:"data,omitempty" jsonschema:"The message or data payload to send to the external system, typically a JSON string."`
	Metadata    map[string]string `json:"metadata,omitempty" jsonschema:"Optional key-value pairs required by the specific binding component for the operation (e.g., 'key' for a storage binding)."`
}

type handler struct {
	client BindingsClient
	inst   toolkit.Instrumentation
}

func (h *handler) invokeOutputBinding(ctx context.Context, _ *mcp.CallToolRequest, args InvokeBindingArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolInvokeOutputBinding, packageName, args.BindingName,
		attribute.String(toolkit.AttrComponentName, args.BindingName),
		attribute.String(attrOperation, args.Operation),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "bindingName", Value: args.BindingName},
		toolkit.Field{Name: "operation", Value: args.Operation},
	); res != nil {
		return res, nil, nil
	}

	var data []byte
	if args.Data != "" {
		data = []byte(args.Data)
	}

	resp, err := h.client.InvokeBinding(ctx, &dapr.InvokeBindingRequest{
		Name:      args.BindingName,
		Operation: args.Operation,
		Data:      data,
		Metadata:  args.Metadata,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("invoke binding %q with operation %q: %w", args.BindingName, args.Operation, err)), nil, nil
	}

	var respData []byte
	if resp != nil {
		respData = resp.Data
	}
	call.Succeed("binding", args.BindingName, "operation", args.Operation, "response_bytes", len(respData))

	text := fmt.Sprintf("Successfully invoked output binding '%s' with operation '%s'.", args.BindingName, args.Operation)
	if len(respData) > 0 {
		text += "\n\nResponse Data:\n" + toolkit.IndentJSON(respData)
	}
	return toolkit.TextResult(text), map[string]string{
		"binding_name":  args.BindingName,
		"operation":     args.Operation,
		"response_data": string(respData),
	}, nil
}

// RegisterTools registers the invoke_output_binding tool on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client BindingsClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolInvokeOutputBinding,
		Title: "Interact with External System via Binding",
		Description: "Invokes an operation on a Dapr output binding component to interact with external systems (e.g., queues, databases, webhooks). **This is a DESTRUCTIVE SIDE-EFFECT action that is NOT IDEMPOTENT.** Use this tool to perform I/O actions.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `bindingName` of the output binding.\n" +
			"2. Ensure `operation` and `data` are explicitly provided by the user.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `bindingName` and `operation`. Provide the `data` payload whenever the operation needs one.\n" +
			"2. **NEVER INVENT**: You must NOT invent `bindingName` or `operation` names; they must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification before generating the tool call.\n\n" +
			"**METADATA**: The `metadata` field MUST be used to pass headers or component-specific settings (e.g., overriding the target URL for an HTTP binding).",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.invokeOutputBinding)
}
