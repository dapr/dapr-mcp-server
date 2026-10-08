// Package conversation exposes the Dapr conversation building block as an
// MCP tool.
package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "conversation"

	toolConverseWithLLM = "converse_with_llm"

	attrConversationName = "dapr.conversation.name"

	// DefaultTemperature is the temperature used when the caller sets none.
	DefaultTemperature = 0.7

	resultKeyContextID = "context_id"
)

// ConversationClient defines the interface for conversation operations.
// This allows for dependency injection and easier testing.
type ConversationClient interface {
	ConverseAlpha2(ctx context.Context, req dapr.ConversationRequestAlpha2) (*dapr.ConversationResponseAlpha2, error)
}

// daprClientAdapter wraps a dapr.Client to implement ConversationClient.
// This is needed because the Dapr SDK's ConverseAlpha2 uses unexported option types.
type daprClientAdapter struct {
	client dapr.Client
}

func (a *daprClientAdapter) ConverseAlpha2(ctx context.Context, req dapr.ConversationRequestAlpha2) (*dapr.ConversationResponseAlpha2, error) {
	return a.client.ConverseAlpha2(ctx, req)
}

// ConverseArgs are the arguments of the converse_with_llm tool.
type ConverseArgs struct {
	Name        string   `json:"name" jsonschema:"The Dapr component name of the LLM service (e.g., 'ollama', 'openai')."`
	Prompt      string   `json:"prompt" jsonschema:"The user's direct question or instruction to the LLM."`
	ContextID   string   `json:"contextId,omitempty" jsonschema:"Optional: Unique ID for continuing a specific conversation context/history. Pass back the context_id returned by a previous call."`
	Temperature *float64 `json:"temperature,omitempty" jsonschema:"Optional: LLM temperature setting (0.0 to 1.0). 0.0 is deterministic. Default is 0.7."`
}

type handler struct {
	client ConversationClient
	inst   toolkit.Instrumentation
	newID  func() (uuid.UUID, error)
}

func (h *handler) converse(ctx context.Context, _ *mcp.CallToolRequest, args ConverseArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolConverseWithLLM, packageName,
		attribute.String(attrConversationName, args.Name),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "name", Value: args.Name},
		toolkit.Field{Name: "prompt", Value: args.Prompt},
	); res != nil {
		return res, nil, nil
	}

	contextID := args.ContextID
	if contextID == "" {
		id, err := h.newID()
		if err != nil {
			return call.Fail(fmt.Errorf("generate conversation context ID: %w", err)), nil, nil
		}
		contextID = id.String()
	}

	temperature := DefaultTemperature
	if args.Temperature != nil {
		temperature = *args.Temperature
	}
	scrubPII := false
	toolChoice := dapr.ToolChoiceNoneAlpha2
	prompt := args.Prompt

	resp, err := h.client.ConverseAlpha2(ctx, dapr.ConversationRequestAlpha2{
		Name:      args.Name,
		ContextID: &contextID,
		Inputs: []*dapr.ConversationInputAlpha2{{
			Messages: []*dapr.ConversationMessageAlpha2{{
				ConversationMessageOfUser: &dapr.ConversationMessageOfUserAlpha2{
					Content: []*dapr.ConversationMessageContentAlpha2{{Text: &prompt}},
				},
			}},
		}},
		ScrubPII:    &scrubPII,
		Temperature: &temperature,
		ToolChoice:  &toolChoice,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("converse with LLM component %q: %w", args.Name, err)), nil, nil
	}
	if resp == nil || len(resp.Outputs) == 0 || resp.Outputs[len(resp.Outputs)-1] == nil {
		return call.Fail(fmt.Errorf("LLM component %q returned an empty outputs list", args.Name)), nil, nil
	}
	lastOutput := resp.Outputs[len(resp.Outputs)-1]
	if len(lastOutput.Choices) == 0 {
		return call.Fail(fmt.Errorf("LLM component %q returned no choices in the last output", args.Name)), nil, nil
	}
	if resp.ContextID != "" {
		contextID = resp.ContextID
	}

	text, err := formatChoices(args.Name, contextID, lastOutput.Choices)
	if err != nil {
		return call.Fail(err), nil, nil
	}
	structured, err := structuredResponse(resp, contextID)
	if err != nil {
		return call.Fail(err), nil, nil
	}

	call.Succeed("component", args.Name, "context_id", contextID, "choices", len(lastOutput.Choices))
	return toolkit.TextResult(text), structured, nil
}

func formatChoices(component, contextID string, choices []*dapr.ConversationResultChoicesAlpha2) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "LLM Conversation completed successfully with component '%s'.\nContext ID: %s\n", component, contextID)

	for i, choice := range choices {
		if choice == nil || choice.Message == nil {
			continue
		}
		fmt.Fprintf(&b, "\n--- Choice %d ---\n", i)

		if len(choice.Message.ToolCalls) > 0 {
			toolCalls, err := toolkit.MarshalIndent(choice.Message.ToolCalls)
			if err != nil {
				return "", fmt.Errorf("format tool calls: %w", err)
			}
			fmt.Fprintf(&b, "Status: **TOOL CALL** (Reason: %s)\n", choice.FinishReason)
			fmt.Fprintf(&b, "Tool Calls:\n%s\n", toolCalls)
		}
		if choice.Message.Content != "" {
			fmt.Fprintf(&b, "Status: **MESSAGE** (Reason: %s)\n", choice.FinishReason)
			fmt.Fprintf(&b, "Response Content:\n%s\n", choice.Message.Content)
		}
	}
	return b.String(), nil
}

func structuredResponse(resp *dapr.ConversationResponseAlpha2, contextID string) (map[string]any, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("encode LLM response: %w", err)
	}
	structured := map[string]any{}
	if err := json.Unmarshal(raw, &structured); err != nil {
		return nil, fmt.Errorf("decode LLM response: %w", err)
	}
	structured[resultKeyContextID] = contextID
	return structured, nil
}

// RegisterTools registers the converse_with_llm tool on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client dapr.Client, metrics *telemetry.ToolMetrics) {
	registerTools(server, &daprClientAdapter{client: client}, metrics)
}

func registerTools(server *mcp.Server, client ConversationClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics), newID: uuid.NewRandom}

	mcp.AddTool(server, &mcp.Tool{
		Name:        toolConverseWithLLM,
		Title:       "Delegate Task to External Reasoning Engine",
		InputSchema: toolkit.InputSchema[ConverseArgs](),
		Description: "Delegates a single, immediate reasoning or text generation task to a secondary LLM component. **This is a READ-ONLY and IDEMPOTENT operation.** The server handles complex message history formatting internally, accepting only the user's direct prompt, the component name, and an optional context ID for session continuity.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `name` of the LLM component.\n" +
			"2. For `temperature`, use a value between 0.0 (deterministic) and 1.0 (creative). Default is 0.7.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide the Dapr component `name` and the user's `prompt`.\n" +
			"2. **NEVER INVENT**: You must NOT invent the component `name`; it must be provided by the user or discovered via the `get_components` tool.\n" +
			"3. **CONTEXT**: If provided, the `contextId` is used to maintain history. If omitted, a new session is started. The result always carries the `context_id` to pass on the next call.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.converse)
}
