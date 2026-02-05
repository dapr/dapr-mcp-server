package lock

import (
	"context"
	"fmt"
	"log"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

// LockClient defines the interface for lock operations.
type LockClient interface {
	TryLockAlpha1(ctx context.Context, storeName string, req *dapr.LockRequest) (*dapr.LockResponse, error)
	UnlockAlpha1(ctx context.Context, storeName string, req *dapr.UnlockRequest) (*dapr.UnlockResponse, error)
}

type AcquireLockArgs struct {
	StoreName       string `json:"storeName" jsonschema:"The name of the Dapr lock store component (e.g., 'redis-lock')."`
	ResourceID      string `json:"resourceID" jsonschema:"The unique name of the resource to lock (e.g., 'inventory-update-lock')."`
	LockOwner       string `json:"lockOwner" jsonschema:"A unique identifier for the entity trying to acquire the lock (e.g., 'ai-agent-42')."`
	ExpiryInSeconds int32  `json:"expiryInSeconds" jsonschema:"The lock duration in seconds. If not released, the lock will automatically expire after this time (recommended to set between 5 and 60 seconds)."`
}

type ReleaseLockArgs struct {
	StoreName  string `json:"storeName" jsonschema:"The name of the Dapr lock store component."`
	ResourceID string `json:"resourceID" jsonschema:"The unique name of the resource whose lock should be released."`
	LockOwner  string `json:"lockOwner" jsonschema:"The unique identifier of the entity that currently holds the lock."`
}

var (
	lockClient  LockClient
	toolMetrics *telemetry.ToolMetrics
)

func acquireLockTool(ctx context.Context, req *mcp.CallToolRequest, args AcquireLockArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "acquire_lock", "lock")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "acquire_lock")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "acquire_lock"),
		attribute.String("mcp.tool.package", "lock"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.String("dapr.lock.resource_id", args.ResourceID),
		attribute.String("dapr.lock.owner", args.LockOwner),
	)

	lockReq := &dapr.LockRequest{
		LockOwner:       args.LockOwner,
		ResourceID:      args.ResourceID,
		ExpiryInSeconds: args.ExpiryInSeconds,
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := lockClient.TryLockAlpha1(rpcCtx, args.StoreName, lockReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		log.Printf("Dapr TryLockAlpha1 failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr API error while trying to acquire lock: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	var successMessage string

	if resp.Success {
		successMessage = fmt.Sprintf("Successfully **acquired** lock for resource **'%s'** on store '%s'. Owner: %s. Expires in %d seconds.",
			args.ResourceID, args.StoreName, args.LockOwner, args.ExpiryInSeconds)
	} else {
		successMessage = fmt.Sprintf("Failed to acquire lock for resource **'%s'** on store '%s'. The lock is currently held by another entity.",
			args.ResourceID, args.StoreName)
	}

	log.Println(successMessage)
	structuredResult := map[string]interface{}{
		"lock_acquired": resp.Success,
		"resource_id":   args.ResourceID,
		"owner_id":      args.LockOwner,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

func releaseLockTool(ctx context.Context, req *mcp.CallToolRequest, args ReleaseLockArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "release_lock", "lock")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "release_lock")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "release_lock"),
		attribute.String("mcp.tool.package", "lock"),
		attribute.String("dapr.component.name", args.StoreName),
		attribute.String("dapr.lock.resource_id", args.ResourceID),
		attribute.String("dapr.lock.owner", args.LockOwner),
	)

	unlockReq := &dapr.UnlockRequest{
		LockOwner:  args.LockOwner,
		ResourceID: args.ResourceID,
	}

	resp, err := lockClient.UnlockAlpha1(ctx, args.StoreName, unlockReq)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.StoreName)
		}
		log.Printf("Dapr UnlockAlpha1 failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr API error while trying to release lock: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.StoreName)
	}

	var statusMessage string

	const (
		StatusSuccess            = "SUCCESS"
		StatusLockUnexist        = "LOCK_UNEXIST"
		StatusLockBelongToOthers = "LOCK_BELONG_TO_OTHERS"
		StatusInternalError      = "INTERNAL_ERROR"
	)

	switch resp.Status {
	case StatusSuccess:
		statusMessage = "SUCCESS: The lock was successfully released."
	case StatusLockUnexist:
		statusMessage = "LOCK_UNEXIST: The lock specified by ResourceID does not exist."
	case StatusLockBelongToOthers:
		statusMessage = fmt.Sprintf("LOCK_BELONG_TO_OTHERS: The lock is held by a different owner. Cannot be released by owner '%s'.", args.LockOwner)
	case StatusInternalError:
		statusMessage = "INTERNAL_ERROR: An internal error occurred in the lock component."
	default:
		statusMessage = fmt.Sprintf("UNKNOWN_STATUS: %s", resp.Status)
	}

	finalMessage := fmt.Sprintf("Attempted to release lock on resource '%s' (Owner: %s). Result: %s", args.ResourceID, args.LockOwner, statusMessage)

	log.Println(finalMessage)
	structuredResult := map[string]interface{}{
		"release_status_code": resp.Status,
		"release_status_text": statusMessage,
		"resource_id":         args.ResourceID,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: finalMessage}},
	}, structuredResult, nil
}

func RegisterTools(server *mcp.Server, client LockClient, metrics *telemetry.ToolMetrics) {
	lockClient = client
	toolMetrics = metrics

	notDestructive := false
	acquireIsIdempotent := true
	releaseIsIdempotent := false
	isOpenWorld := true

	mcp.AddTool(server, &mcp.Tool{
		Name:  "acquire_lock",
		Title: "Acquire Resource Coordination Lock",
		Description: "Tries to acquire a distributed lock on a named resource for exclusive access. **This is a SIDE-EFFECT action that IS IDEMPOTENT.** Use only when the agent must ensure no other entity is concurrently modifying a shared resource (e.g., before writing to a database).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `Lock Store Name`.\n" +
			"2. For `Resource ID`, use a unique identifier for the resource (e.g., 'client-file-lock').\n" +
			"3. For `Lock Owner`, use a unique identifier for the entity (e.g., 'ai-agent-42').\n" +
			"4. For `Expiry Time`, don't set expiry if unsure.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName`, `resourceID`, `lockOwner`, and `expiryInSeconds`.\n" +
			"2. **NEVER INVENT**: You must NOT invent lock owners or resource IDs.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**SECURITY WARNING**: Misuse can cause system-wide deadlocks or race conditions. Ensure the lock is released promptly.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &notDestructive,
			IdempotentHint:  acquireIsIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, acquireLockTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:  "release_lock",
		Title: "Release Resource Coordination Lock",
		Description: "Releases a previously acquired distributed lock on a resource. **This is a SIDE-EFFECT action that is NOT IDEMPOTENT.** It MUST be called immediately after the critical section of code is complete to prevent deadlocks.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `Lock Store Name`.\n" +
			"2. Ensure the `Resource ID` and `Lock Owner` match the values used during acquisition.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide `storeName`, `resourceID`, and the correct `lockOwner`.\n" +
			"2. **OWNERSHIP**: Only the entity that acquired the lock can release it.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**WORKFLOW RULE**: This tool must be used as the final step in a critical concurrency workflow.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &notDestructive,
			IdempotentHint:  releaseIsIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, releaseLockTool)
}
