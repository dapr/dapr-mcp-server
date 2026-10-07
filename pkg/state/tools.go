package state

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

// StateClient defines the interface for state operations.
// This allows for easier testing with mocks.
type StateClient interface {
	SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...dapr.StateOption) error
	GetState(ctx context.Context, storeName, key string, meta map[string]string) (*dapr.StateItem, error)
	DeleteState(ctx context.Context, storeName, key string, meta map[string]string) error
	ExecuteStateTransaction(ctx context.Context, storeName string, meta map[string]string, ops []*dapr.StateOperation) error
}

type SaveStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key under which to save the state."`
	Value     string `json:"value" jsonschema:"The value (typically a JSON string) to save."`
}

type GetStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key whose value should be retrieved."`
}

type DeleteStateArgs struct {
	StoreName string `json:"storeName" jsonschema:"The name of the Dapr state store component (e.g., 'statestore')."`
	Key       string `json:"key" jsonschema:"The key to delete."`
}

type TransactionItem struct {
	Key      string `json:"key" jsonschema:"The state key."`
	Value    string `json:"value" jsonschema:"The value to set (or empty for delete)."`
	IsDelete bool   `json:"isDelete" jsonschema:"Set to true to delete the key, false to save/update it."`
}

type ExecuteTransactionArgs struct {
	StoreName string            `json:"storeName" jsonschema:"The name of the Dapr state store component."`
	Items     []TransactionItem `json:"items" jsonschema:"A list of save and/or delete operations to execute atomically."`
}

var (
	stateClient StateClient
	toolMetrics *telemetry.ToolMetrics
)

func saveStateTool(ctx context.Context, req *mcp.CallToolRequest, args SaveStateArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "save_state", "state")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "save_state")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "save_state"),
		attribute.String("mcp.tool.package", "state"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.String("dapr.state.key", args.Key),
	)

	data := []byte(args.Value)

	if err := stateClient.SaveState(ctx, args.StoreName, args.Key, data, nil); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		toolErrorMessage := fmt.Errorf("failed to save state to store '%s'. Final error: %v", args.StoreName, err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	successMessage := fmt.Sprintf("Successfully saved key '%s' to state store '%s'.", args.Key, args.StoreName)
	log.Println(successMessage)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, map[string]string{"key_saved": args.Key, "store_name": args.StoreName}, nil
}

func getStateTool(ctx context.Context, req *mcp.CallToolRequest, args GetStateArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "get_state", "state")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "get_state")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "get_state"),
		attribute.String("mcp.tool.package", "state"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.String("dapr.state.key", args.Key),
	)

	item, err := stateClient.GetState(ctx, args.StoreName, args.Key, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		log.Printf("Dapr GetState failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr GetState failed: %v", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	result := string(item.Value)
	log.Println(result)

	var structuredResult map[string]string

	if result == "" {
		result = fmt.Sprintf("Key '%s' not found in state store '%s'.", args.Key, args.StoreName)
		structuredResult = nil
	} else {
		result = fmt.Sprintf("Retrieved key '%s' from '%s'. Value:\n%s", args.Key, args.StoreName, result)
		structuredResult = map[string]string{
			"key":   args.Key,
			"value": string(item.Value),
		}
	}

	log.Println(result)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, structuredResult, nil
}

func deleteStateTool(ctx context.Context, req *mcp.CallToolRequest, args DeleteStateArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "delete_state", "state")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "delete_state")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "delete_state"),
		attribute.String("mcp.tool.package", "state"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.String("dapr.state.key", args.Key),
	)

	if err := stateClient.DeleteState(ctx, args.StoreName, args.Key, nil); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		log.Printf("Dapr DeleteState failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr DeleteState failed: %v", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	successMessage := fmt.Sprintf("Successfully deleted key '%s' from state store '%s'.", args.Key, args.StoreName)
	log.Println(successMessage)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, map[string]string{"key_deleted": args.Key, "store_name": args.StoreName}, nil
}

func executeTransactionTool(ctx context.Context, req *mcp.CallToolRequest, args ExecuteTransactionArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "execute_transaction", "state")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "execute_transaction")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "execute_transaction"),
		attribute.String("mcp.tool.package", "state"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.Int("dapr.operations_count", len(args.Items)),
	)

	propagator := otel.GetTextMapPropagator()
	meta := make(map[string]string)
	propagator.Inject(ctx, propagation.MapCarrier(meta))

	ops := make([]*dapr.StateOperation, 0, len(args.Items))

	for _, item := range args.Items {
		var opType dapr.OperationType
		var setItem *dapr.SetStateItem

		if item.IsDelete {
			opType = dapr.StateOperationTypeDelete
			setItem = &dapr.SetStateItem{Key: item.Key}
		} else {
			opType = dapr.StateOperationTypeUpsert
			setItem = &dapr.SetStateItem{
				Key:   item.Key,
				Value: []byte(item.Value),
			}
		}

		ops = append(ops, &dapr.StateOperation{
			Type: opType,
			Item: setItem,
		})
	}

	if err := stateClient.ExecuteStateTransaction(ctx, args.StoreName, meta, ops); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		log.Printf("Dapr ExecuteStateTransaction failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr ExecuteStateTransaction failed: %v", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	successMessage := fmt.Sprintf("Successfully executed %d state operations in a transaction on store '%s'.", len(args.Items), args.StoreName)
	log.Println(successMessage)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, map[string]interface{}{"operations_executed": len(args.Items), "store_name": args.StoreName}, nil
}

func RegisterTools(server *mcp.Server, client StateClient, metrics *telemetry.ToolMetrics) {
	stateClient = client
	toolMetrics = metrics

	isReadOnly := true
	isIdempotent := true

	notReadOnly := false
	isDestructive := true
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:  "save_state",
		Title: "Save Single Key-Value State",
		Description: "Saves a single key-value pair to a Dapr state store. **This is a SIDE-EFFECT action that alters application state and IS IDEMPOTENT.** Use only when the agent needs to persist data or update an entity.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `StoreName` of the state store.\n" +
			"2. For `Key`, use a meaningful identifier (e.g., `<AppID>:<ResourceURI>:<Index>`).\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `StoreName`, `Key`, and `Value`.\n" +
			"2. **KEY RULE**: The key SHOULD follow `<AppID>||<ResourceURI>||<Index>` when possible for discoverability.\n" +
			"3. **VALUE RULE**: The `Value` must be a string (plain or JSON-encoded).\n" +
			"4. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    notReadOnly,
			DestructiveHint: &notDestructive,
			IdempotentHint:  isIdempotent,
		},
	}, saveStateTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "get_state",
		Title: "Retrieve Single Key State",
		Description: "Retrieves the value for a single key from a Dapr state store. **This is a Data Retrieval operation and IS IDEMPOTENT.** Use to access current application state or previously saved context.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `StoreName` of the state store.\n" +
			"2. Ensure `Key` is explicitly provided by the user or use the key previously used for save.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `StoreName` and `Key`.\n" +
			"2. **NEVER INVENT**: Never invent a `Key`; it must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   isReadOnly,
			IdempotentHint: isIdempotent,
		},
	}, getStateTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "delete_state",
		Title: "Delete State Key",
		Description: "Deletes a key-value pair from a Dapr state store. **This is a critical, DESTRUCTIVE SIDE-EFFECT action that IS IDEMPOTENT.** Use only when instructed to remove specific, whitelisted application data.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `StoreName` of the state store.\n" +
			"2. Ensure `Key` is explicitly provided by the user or use the key previously used for save.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `StoreName` and `Key`.\n" +
			"2. **SECURITY WARNING**: This operation can cause data loss. Ensure user intent is clear and the key is authorized for deletion.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    notReadOnly,
			DestructiveHint: &isDestructive,
			IdempotentHint:  isIdempotent,
		},
	}, deleteStateTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "execute_transaction",
		Title: "Execute Atomic State Transaction",
		Description: "Executes multiple save and/or delete operations atomically (all or nothing) on state stores that support transactions. **This is a complex, high-impact DESTRUCTIVE SIDE-EFFECT action that is NOT IDEMPOTENT.** Use only for batch updates or when strict data consistency is required across multiple keys.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `StoreName` of the state store.\n" +
			"2. Ensure `Items` contains valid save/delete operations.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `StoreName` and a non-empty list of `Items`.\n" +
			"2. **SECURITY WARNING**: Due to the complexity and potential for destructive operations within the transaction, ensure all actions are fully understood and authorized.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    notReadOnly,
			DestructiveHint: &isDestructive,
			IdempotentHint:  false,
		},
	}, executeTransactionTool)
}
