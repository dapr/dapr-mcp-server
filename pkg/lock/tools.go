// Package lock exposes the Dapr distributed lock building block as MCP tools.
package lock

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "lock"

	toolAcquireLock = "acquire_lock"
	toolReleaseLock = "release_lock"

	attrResourceID = "dapr.lock.resource_id"
	attrOwner      = "dapr.lock.owner"

	// RPCTimeout bounds each call to the lock store.
	RPCTimeout = 5 * time.Second
)

var (
	errNonPositiveExpiry = errors.New("expiryInSeconds must be greater than zero")
	errNilLockResponse   = errors.New("lock store returned no response")
)

// LockClient defines the interface for lock operations.
type LockClient interface {
	TryLockAlpha1(ctx context.Context, storeName string, req *dapr.LockRequest) (*dapr.LockResponse, error)
	UnlockAlpha1(ctx context.Context, storeName string, req *dapr.UnlockRequest) (*dapr.UnlockResponse, error)
}

// AcquireLockArgs are the arguments of the acquire_lock tool.
type AcquireLockArgs struct {
	StoreName       string `json:"storeName" jsonschema:"The name of the Dapr lock store component (e.g., 'redis-lock')."`
	ResourceID      string `json:"resourceID" jsonschema:"The unique name of the resource to lock (e.g., 'inventory-update-lock')."`
	LockOwner       string `json:"lockOwner" jsonschema:"A unique identifier for the entity trying to acquire the lock (e.g., 'ai-agent-42')."`
	ExpiryInSeconds int32  `json:"expiryInSeconds" jsonschema:"The lock duration in seconds, greater than zero. If not released, the lock will automatically expire after this time (recommended to set between 5 and 60 seconds)."`
}

// ReleaseLockArgs are the arguments of the release_lock tool.
type ReleaseLockArgs struct {
	StoreName  string `json:"storeName" jsonschema:"The name of the Dapr lock store component."`
	ResourceID string `json:"resourceID" jsonschema:"The unique name of the resource whose lock should be released."`
	LockOwner  string `json:"lockOwner" jsonschema:"The unique identifier of the entity that currently holds the lock."`
}

type handler struct {
	client LockClient
	inst   toolkit.Instrumentation
}

func lockAttrs(storeName, resourceID, owner string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(toolkit.AttrComponentName, storeName),
		attribute.String(attrResourceID, resourceID),
		attribute.String(attrOwner, owner),
	}
}

func lockFields(storeName, resourceID, owner string) []toolkit.Field {
	return []toolkit.Field{
		{Name: "storeName", Value: storeName},
		{Name: "resourceID", Value: resourceID},
		{Name: "lockOwner", Value: owner},
	}
}

func (h *handler) acquireLock(ctx context.Context, _ *mcp.CallToolRequest, args AcquireLockArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolAcquireLock, packageName, args.StoreName, lockAttrs(args.StoreName, args.ResourceID, args.LockOwner)...)
	defer call.End()

	if res := call.Require(lockFields(args.StoreName, args.ResourceID, args.LockOwner)...); res != nil {
		return res, nil, nil
	}
	if args.ExpiryInSeconds <= 0 {
		return call.Fail(errNonPositiveExpiry), nil, nil
	}

	rpcCtx, cancel := context.WithTimeout(ctx, RPCTimeout)
	defer cancel()

	resp, err := h.client.TryLockAlpha1(rpcCtx, args.StoreName, &dapr.LockRequest{
		LockOwner:       args.LockOwner,
		ResourceID:      args.ResourceID,
		ExpiryInSeconds: args.ExpiryInSeconds,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("acquire lock on resource %q in store %q: %w", args.ResourceID, args.StoreName, err)), nil, nil
	}
	if resp == nil {
		return call.Fail(errNilLockResponse), nil, nil
	}

	call.Succeed("store", args.StoreName, "resource_id", args.ResourceID, "acquired", resp.Success)

	text := fmt.Sprintf("Failed to acquire lock for resource **'%s'** on store '%s'. The lock is currently held by another entity.",
		args.ResourceID, args.StoreName)
	if resp.Success {
		text = fmt.Sprintf("Successfully **acquired** lock for resource **'%s'** on store '%s'. Owner: %s. Expires in %d seconds.",
			args.ResourceID, args.StoreName, args.LockOwner, args.ExpiryInSeconds)
	}
	return toolkit.TextResult(text), map[string]any{
		"lock_acquired": resp.Success,
		"resource_id":   args.ResourceID,
		"owner_id":      args.LockOwner,
	}, nil
}

// unlockStatusError explains a non-success unlock status, or returns nil.
func unlockStatusError(resp *dapr.UnlockResponse, owner string) error {
	switch pb.UnlockResponse_Status(resp.StatusCode) {
	case pb.UnlockResponse_SUCCESS:
		return nil
	case pb.UnlockResponse_LOCK_DOES_NOT_EXIST:
		return fmt.Errorf("%s: the lock does not exist", resp.Status)
	case pb.UnlockResponse_LOCK_BELONGS_TO_OTHERS:
		return fmt.Errorf("%s: the lock is held by a different owner and cannot be released by %q", resp.Status, owner)
	case pb.UnlockResponse_INTERNAL_ERROR:
		return fmt.Errorf("%s: an internal error occurred in the lock component", resp.Status)
	default:
		return fmt.Errorf("unknown unlock status %d %q", resp.StatusCode, resp.Status)
	}
}

func (h *handler) releaseLock(ctx context.Context, _ *mcp.CallToolRequest, args ReleaseLockArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolReleaseLock, packageName, args.StoreName, lockAttrs(args.StoreName, args.ResourceID, args.LockOwner)...)
	defer call.End()

	if res := call.Require(lockFields(args.StoreName, args.ResourceID, args.LockOwner)...); res != nil {
		return res, nil, nil
	}

	rpcCtx, cancel := context.WithTimeout(ctx, RPCTimeout)
	defer cancel()

	resp, err := h.client.UnlockAlpha1(rpcCtx, args.StoreName, &dapr.UnlockRequest{
		LockOwner:  args.LockOwner,
		ResourceID: args.ResourceID,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("release lock on resource %q in store %q: %w", args.ResourceID, args.StoreName, err)), nil, nil
	}
	if resp == nil {
		return call.Fail(errNilLockResponse), nil, nil
	}
	if err := unlockStatusError(resp, args.LockOwner); err != nil {
		return call.Fail(fmt.Errorf("release lock on resource %q in store %q: %w", args.ResourceID, args.StoreName, err)), nil, nil
	}

	call.Succeed("store", args.StoreName, "resource_id", args.ResourceID)
	text := fmt.Sprintf("Released lock on resource '%s' (Owner: %s). Result: %s", args.ResourceID, args.LockOwner, resp.Status)
	return toolkit.TextResult(text), map[string]any{
		"release_status_code": resp.Status,
		"resource_id":         args.ResourceID,
	}, nil
}

// RegisterTools registers the acquire_lock and release_lock tools on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client LockClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolAcquireLock,
		Title: "Acquire Resource Coordination Lock",
		Description: "Tries to acquire a distributed lock on a named resource for exclusive access. **This is a SIDE-EFFECT action that IS IDEMPOTENT.** Use only when the agent must ensure no other entity is concurrently modifying a shared resource (e.g., before writing to a database).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the lock store.\n" +
			"2. For `resourceID`, use a unique identifier for the resource (e.g., 'client-file-lock').\n" +
			"3. For `lockOwner`, use a unique identifier for the entity (e.g., 'ai-agent-42').\n" +
			"4. For `expiryInSeconds`, use a short positive duration (5 to 60 seconds) if unsure.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName`, `resourceID`, `lockOwner`, and a positive `expiryInSeconds`.\n" +
			"2. **NEVER INVENT**: You must NOT invent lock owners or resource IDs.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**SECURITY WARNING**: Misuse can cause system-wide deadlocks or race conditions. Ensure the lock is released promptly.",
		Annotations: toolkit.IdempotentWrite.Annotations(true),
	}, h.acquireLock)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolReleaseLock,
		Title: "Release Resource Coordination Lock",
		Description: "Releases a previously acquired distributed lock on a resource. **This is a SIDE-EFFECT action that is NOT IDEMPOTENT.** It MUST be called immediately after the critical section of code is complete to prevent deadlocks.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the lock store.\n" +
			"2. Ensure the `resourceID` and `lockOwner` match the values used during acquisition.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide `storeName`, `resourceID`, and the correct `lockOwner`.\n" +
			"2. **OWNERSHIP**: Only the entity that acquired the lock can release it.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**WORKFLOW RULE**: This tool must be used as the final step in a critical concurrency workflow.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.releaseLock)
}
