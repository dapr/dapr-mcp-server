// Package state exposes the Dapr state management building block as MCP tools.
package state

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
	packageName = "state"

	toolSaveState          = "save_state"
	toolGetState           = "get_state"
	toolDeleteState        = "delete_state"
	toolExecuteTransaction = "execute_transaction"

	attrStateKey        = "dapr.state.key"
	attrOperationsCount = "dapr.operations_count"
)

// StateClient defines the interface for state operations.
// This allows for easier testing with mocks.
type StateClient interface {
	SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...dapr.StateOption) error
	GetState(ctx context.Context, storeName, key string, meta map[string]string) (*dapr.StateItem, error)
	DeleteState(ctx context.Context, storeName, key string, meta map[string]string) error
	ExecuteStateTransaction(ctx context.Context, storeName string, meta map[string]string, ops []*dapr.StateOperation) error
}

// SaveStateArgs are the arguments of the save_state tool.
type SaveStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key under which to save the state."`
	Value     string `json:"value" jsonschema:"The value (typically a JSON string) to save."`
}

// GetStateArgs are the arguments of the get_state tool.
type GetStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key whose value should be retrieved."`
}

// DeleteStateArgs are the arguments of the delete_state tool.
type DeleteStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key to delete."`
}

// TransactionItem is one save or delete operation within execute_transaction.
type TransactionItem struct {
	Key      string `json:"key" jsonschema:"The state key."`
	Value    string `json:"value,omitempty" jsonschema:"The value to set (or empty for delete)."`
	IsDelete bool   `json:"isDelete,omitempty" jsonschema:"Set to true to delete the key, false to save/update it."`
}

// ExecuteTransactionArgs are the arguments of the execute_transaction tool.
type ExecuteTransactionArgs struct {
	StoreName string            `json:"storeName" jsonschema:"The name of the Dapr state store component."`
	Items     []TransactionItem `json:"items" jsonschema:"A list of save and/or delete operations to execute atomically."`
}

type handler struct {
	client StateClient
	inst   toolkit.Instrumentation
}

func keyAttrs(storeName, key string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(toolkit.AttrComponentName, storeName),
		attribute.String(attrStateKey, key),
	}
}

func (h *handler) saveState(ctx context.Context, _ *mcp.CallToolRequest, args SaveStateArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolSaveState, packageName, keyAttrs(args.StoreName, args.Key)...)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "storeName", Value: args.StoreName},
		toolkit.Field{Name: "key", Value: args.Key},
		toolkit.Field{Name: "value", Value: args.Value},
	); res != nil {
		return res, nil, nil
	}

	if err := h.client.SaveState(ctx, args.StoreName, args.Key, []byte(args.Value), nil); err != nil {
		return call.Fail(fmt.Errorf("save key %q to state store %q: %w", args.Key, args.StoreName, err)), nil, nil
	}

	call.Succeed("store", args.StoreName, "key", args.Key)
	text := fmt.Sprintf("Successfully saved key '%s' to state store '%s'.", args.Key, args.StoreName)
	return toolkit.TextResult(text), map[string]string{"key_saved": args.Key, "store_name": args.StoreName}, nil
}

func (h *handler) getState(ctx context.Context, _ *mcp.CallToolRequest, args GetStateArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolGetState, packageName, keyAttrs(args.StoreName, args.Key)...)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "storeName", Value: args.StoreName},
		toolkit.Field{Name: "key", Value: args.Key},
	); res != nil {
		return res, nil, nil
	}

	item, err := h.client.GetState(ctx, args.StoreName, args.Key, nil)
	if err != nil {
		return call.Fail(fmt.Errorf("get key %q from state store %q: %w", args.Key, args.StoreName, err)), nil, nil
	}

	found := item != nil && len(item.Value) > 0
	call.Succeed("store", args.StoreName, "key", args.Key, "found", found)

	if !found {
		text := fmt.Sprintf("Key '%s' not found in state store '%s'.", args.Key, args.StoreName)
		return toolkit.TextResult(text), map[string]any{"key": args.Key, "found": false}, nil
	}

	value := string(item.Value)
	text := fmt.Sprintf("Retrieved key '%s' from '%s'. Value:\n%s", args.Key, args.StoreName, value)
	return toolkit.TextResult(text), map[string]any{"key": args.Key, "found": true, "value": value}, nil
}

func (h *handler) deleteState(ctx context.Context, _ *mcp.CallToolRequest, args DeleteStateArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolDeleteState, packageName, keyAttrs(args.StoreName, args.Key)...)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "storeName", Value: args.StoreName},
		toolkit.Field{Name: "key", Value: args.Key},
	); res != nil {
		return res, nil, nil
	}

	if err := h.client.DeleteState(ctx, args.StoreName, args.Key, nil); err != nil {
		return call.Fail(fmt.Errorf("delete key %q from state store %q: %w", args.Key, args.StoreName, err)), nil, nil
	}

	call.Succeed("store", args.StoreName, "key", args.Key)
	text := fmt.Sprintf("Successfully deleted key '%s' from state store '%s'.", args.Key, args.StoreName)
	return toolkit.TextResult(text), map[string]string{"key_deleted": args.Key, "store_name": args.StoreName}, nil
}

func (h *handler) executeTransaction(ctx context.Context, _ *mcp.CallToolRequest, args ExecuteTransactionArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolExecuteTransaction, packageName,
		attribute.String(toolkit.AttrComponentName, args.StoreName),
		attribute.Int(attrOperationsCount, len(args.Items)),
	)
	defer call.End()

	if len(args.Items) == 0 {
		return call.Fail(fmt.Errorf("%w: items", toolkit.ErrMissingArgument)), nil, nil
	}
	fields := make([]toolkit.Field, 0, len(args.Items)+1)
	fields = append(fields, toolkit.Field{Name: "storeName", Value: args.StoreName})
	for i, item := range args.Items {
		fields = append(fields, toolkit.Field{Name: fmt.Sprintf("items[%d].key", i), Value: item.Key})
	}
	if res := call.Require(fields...); res != nil {
		return res, nil, nil
	}

	ops := make([]*dapr.StateOperation, 0, len(args.Items))
	for _, item := range args.Items {
		op := &dapr.StateOperation{
			Type: dapr.StateOperationTypeUpsert,
			Item: &dapr.SetStateItem{Key: item.Key, Value: []byte(item.Value)},
		}
		if item.IsDelete {
			op.Type = dapr.StateOperationTypeDelete
			op.Item = &dapr.SetStateItem{Key: item.Key}
		}
		ops = append(ops, op)
	}

	if err := h.client.ExecuteStateTransaction(ctx, args.StoreName, nil, ops); err != nil {
		return call.Fail(fmt.Errorf("execute transaction on state store %q: %w", args.StoreName, err)), nil, nil
	}

	call.Succeed("store", args.StoreName, "operations", len(ops))
	text := fmt.Sprintf("Successfully executed %d state operations in a transaction on store '%s'.", len(ops), args.StoreName)
	return toolkit.TextResult(text), map[string]any{"operations_executed": len(ops), "store_name": args.StoreName}, nil
}

// RegisterTools registers the save_state, get_state, delete_state and
// execute_transaction tools on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client StateClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolSaveState,
		Title: "Save Single Key-Value State",
		Description: "Saves a single key-value pair to a Dapr state store. **This is a SIDE-EFFECT action that alters application state and IS IDEMPOTENT.** Use only when the agent needs to persist data or update an entity.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the state store.\n" +
			"2. For `key`, use a meaningful identifier. Dapr applies the state store's key prefix itself (by default the app ID), so do not add one.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName`, `key`, and `value`.\n" +
			"2. **VALUE RULE**: The `value` must be a string (plain or JSON-encoded).\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: toolkit.IdempotentWrite.Annotations(true),
	}, h.saveState)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolGetState,
		Title: "Retrieve Single Key State",
		Description: "Retrieves the value for a single key from a Dapr state store. **This is a READ-ONLY Data Retrieval operation and IS IDEMPOTENT.** Use to access current application state or previously saved context.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the state store.\n" +
			"2. Ensure `key` is explicitly provided by the user or use the key previously used for save.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName` and `key`.\n" +
			"2. **NEVER INVENT**: Never invent a `key`; it must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.getState)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolDeleteState,
		Title: "Delete State Key",
		Description: "Deletes a key-value pair from a Dapr state store. **This is a critical, DESTRUCTIVE SIDE-EFFECT action that IS IDEMPOTENT.** Use only when instructed to remove specific, whitelisted application data.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the state store.\n" +
			"2. Ensure `key` is explicitly provided by the user or use the key previously used for save.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName` and `key`.\n" +
			"2. **SECURITY WARNING**: This operation can cause data loss. Ensure user intent is clear and the key is authorized for deletion.",
		Annotations: toolkit.DestructiveIdempotent.Annotations(true),
	}, h.deleteState)
	mcp.AddTool(server, &mcp.Tool{
		Name:        toolExecuteTransaction,
		Title:       "Execute Atomic State Transaction",
		InputSchema: toolkit.InputSchema[ExecuteTransactionArgs](),
		Description: "Executes multiple save and/or delete operations atomically (all or nothing) on state stores that support transactions. **This is a complex, high-impact DESTRUCTIVE SIDE-EFFECT action that is NOT IDEMPOTENT.** Use only for batch updates or when strict data consistency is required across multiple keys.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the state store.\n" +
			"2. Ensure `items` contains valid save/delete operations.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `storeName` and a non-empty list of `items`, each with a non-empty `key`.\n" +
			"2. **SECURITY WARNING**: Due to the complexity and potential for destructive operations within the transaction, ensure all actions are fully understood and authorized.",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.executeTransaction)
}

// ToolNames returns the names of the tools RegisterTools adds.
func ToolNames() []string {
	return []string{toolSaveState, toolGetState, toolDeleteState, toolExecuteTransaction}
}
