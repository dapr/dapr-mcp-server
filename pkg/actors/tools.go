// Package actors exposes Dapr virtual actor invocation as an MCP tool.
package actors

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
	packageName = "actors"

	toolInvokeActorMethod = "invoke_actor_method"

	attrActorType   = "dapr.actor.type"
	attrActorID     = "dapr.actor.id"
	attrActorMethod = "dapr.actor.method"
)

// ActorClient defines the interface for actor operations.
type ActorClient interface {
	InvokeActor(ctx context.Context, req *dapr.InvokeActorRequest) (*dapr.InvokeActorResponse, error)
}

// InvokeActorMethodArgs are the arguments of the invoke_actor_method tool.
type InvokeActorMethodArgs struct {
	ActorType string `json:"actorType" jsonschema:"The registered actor type (e.g., 'payment-processor')."`
	ActorID   string `json:"actorID" jsonschema:"The unique ID of the actor instance (e.g., 'user-1001')."`
	Method    string `json:"method" jsonschema:"The method name on the actor to call (e.g., 'ProcessOrder')."`
	Data      string `json:"data,omitempty" jsonschema:"The payload to pass to the actor method (e.g., order details)."`
}

type handler struct {
	client ActorClient
	inst   toolkit.Instrumentation
}

func (h *handler) invokeActorMethod(ctx context.Context, _ *mcp.CallToolRequest, args InvokeActorMethodArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolInvokeActorMethod, packageName,
		attribute.String(attrActorType, args.ActorType),
		attribute.String(attrActorID, args.ActorID),
		attribute.String(attrActorMethod, args.Method),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "actorType", Value: args.ActorType},
		toolkit.Field{Name: "actorID", Value: args.ActorID},
		toolkit.Field{Name: "method", Value: args.Method},
	); res != nil {
		return res, nil, nil
	}

	resp, err := h.client.InvokeActor(ctx, &dapr.InvokeActorRequest{
		ActorType: args.ActorType,
		ActorID:   args.ActorID,
		Method:    args.Method,
		Data:      []byte(args.Data),
	})
	if err != nil {
		return call.Fail(fmt.Errorf("invoke method %q on actor %s/%s: %w", args.Method, args.ActorType, args.ActorID, err)), nil, nil
	}

	var respData string
	if resp != nil {
		respData = string(resp.Data)
	}
	call.Succeed("actor_type", args.ActorType, "actor_id", args.ActorID, "method", args.Method, "response_bytes", len(respData))

	text := fmt.Sprintf(
		"Successfully invoked method '%s' on actor type '%s' with ID '%s'. Actor responded with data/status.\n\nResponse:\n%s",
		args.Method, args.ActorType, args.ActorID, respData,
	)
	return toolkit.TextResult(text), map[string]string{
		"actor_type":     args.ActorType,
		"actor_id":       args.ActorID,
		"actor_method":   args.Method,
		"actor_response": respData,
	}, nil
}

// RegisterTools registers the invoke_actor_method tool on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client ActorClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolInvokeActorMethod,
		Title: "Execute Stateful Actor Method",
		Description: "Executes a method on a Dapr Virtual Actor instance, providing durability and concurrency control. **This is a DESTRUCTIVE SIDE-EFFECT action that alters state (e.g., creating an order, updating a payment status) and is NOT IDEMPOTENT.** Use this tool exclusively for requests that require stateful, single-threaded execution.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `actorType` of the actor.\n" +
			"2. Ensure `actorID` and `method` are explicitly provided by the user.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `actorType`, `actorID`, and `method`. Provide `data` whenever the method takes input.\n" +
			"2. **NEVER INVENT**: You must NOT invent the `actorType`, `actorID`, or `method` names; they must be provided by the user or discovered via another tool.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification before generating the tool call.\n\n" +
			"**DATA FORMAT**: The `data` payload MUST be a single string (often JSON) representing the input parameters for the actor method.",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.invokeActorMethod)
}
