// Package workflow exposes Dapr Workflow management operations as MCP tools.
package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dapr/durabletask-go/api/protos"
	wf "github.com/dapr/durabletask-go/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "workflow"

	toolGetWorkflowStatus  = "get_workflow_status"
	toolGetWorkflowHistory = "get_workflow_history"
	toolListWorkflows      = "list_workflows"

	attrInstanceID = "dapr.workflow.instance_id"

	defaultListLimit = 100
	maxListLimit     = 500

	statusPrefix = "ORCHESTRATION_STATUS_"
)

// WorkflowClient defines the workflow operations used by the tools.
// *workflow.Client from durabletask-go satisfies this interface.
type WorkflowClient interface {
	ScheduleWorkflow(ctx context.Context, workflow string, opts ...wf.NewWorkflowOptions) (string, error)
	FetchWorkflowMetadata(ctx context.Context, id string, opts ...wf.FetchWorkflowMetadataOptions) (*wf.WorkflowMetadata, error)
	GetInstanceHistory(ctx context.Context, id string, opts ...wf.GetInstanceHistoryOptions) (*wf.GetInstanceHistoryResponse, error)
	RerunWorkflowFromEvent(ctx context.Context, id string, eventID uint32, opts ...wf.RerunOptions) (string, error)
	ListInstanceIDs(ctx context.Context, opts ...wf.ListInstanceIDsOptions) (*wf.ListInstanceIDsResponse, error)
	SuspendWorkflow(ctx context.Context, id, reason string) error
	ResumeWorkflow(ctx context.Context, id, reason string) error
	TerminateWorkflow(ctx context.Context, id string, opts ...wf.TerminateOptions) error
	RaiseEvent(ctx context.Context, id, eventName string, opts ...wf.RaiseEventOptions) error
	PurgeWorkflowState(ctx context.Context, id string, opts ...wf.PurgeOptions) error
}

// InstanceArgs are the arguments of the tools that take only an instance ID.
type InstanceArgs struct {
	InstanceID string `json:"instanceID" jsonschema:"The workflow instance ID."`
}

// ListWorkflowsArgs are the arguments of the list_workflows tool.
type ListWorkflowsArgs struct {
	Limit             int    `json:"limit,omitempty" jsonschema:"Maximum number of instances to return (default 100, max 500)."`
	ContinuationToken string `json:"continuationToken,omitempty" jsonschema:"Optional continuation token from a previous list_workflows call."`
}

type handler struct {
	client WorkflowClient
	inst   toolkit.Instrumentation
}

// startInstanceCall starts a call for a tool that targets one instance and
// validates the instance ID. A non-nil result is the validation failure.
func (h *handler) startInstanceCall(ctx context.Context, tool, instanceID string, attrs ...attribute.KeyValue) (context.Context, *toolkit.Call, *mcp.CallToolResult) {
	attrs = append([]attribute.KeyValue{attribute.String(attrInstanceID, instanceID)}, attrs...)
	ctx, call := h.inst.Start(ctx, tool, packageName, attrs...)
	return ctx, call, call.Require(toolkit.Field{Name: "instanceID", Value: instanceID})
}

// statusName turns ORCHESTRATION_STATUS_RUNNING into RUNNING.
func statusName(status protos.OrchestrationStatus) string {
	return strings.TrimPrefix(status.String(), statusPrefix)
}

func formatTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().UTC().Format(time.RFC3339)
}

func (h *handler) getWorkflowStatus(ctx context.Context, _ *mcp.CallToolRequest, args InstanceArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolGetWorkflowStatus, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}

	meta, err := h.client.FetchWorkflowMetadata(ctx, args.InstanceID, wf.WithFetchPayloads(true))
	if err != nil {
		return call.Fail(fmt.Errorf("get status of workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}

	// wf.WorkflowMetadata is a defined type over the proto message;
	// convert back to use its nil-safe getters.
	pm := (*protos.WorkflowMetadata)(meta)
	status := statusName(pm.GetRuntimeStatus())

	var sb strings.Builder
	fmt.Fprintf(&sb, "Workflow instance '%s' (workflow '%s') is %s.", args.InstanceID, pm.GetName(), status)
	structured := map[string]any{
		"instance_id":   args.InstanceID,
		"workflow_name": pm.GetName(),
		"status":        status,
	}
	if pm.GetCreatedAt() != nil {
		structured["created_at"] = formatTime(pm.GetCreatedAt())
	}
	if pm.GetLastUpdatedAt() != nil {
		structured["last_updated_at"] = formatTime(pm.GetLastUpdatedAt())
	}
	if custom := pm.GetCustomStatus().GetValue(); custom != "" {
		structured["custom_status"] = custom
		fmt.Fprintf(&sb, "\nCustom status: %s", custom)
	}
	if output := pm.GetOutput().GetValue(); output != "" {
		structured["output"] = output
		fmt.Fprintf(&sb, "\nOutput: %s", output)
	}
	if failure := pm.GetFailureDetails(); failure != nil {
		structured["failure"] = failure.GetErrorMessage()
		fmt.Fprintf(&sb, "\nFailure: %s", failure.GetErrorMessage())
	}

	call.Succeed("instance_id", args.InstanceID, "status", status)
	return toolkit.TextResult(sb.String()), structured, nil
}

// eventDescription is what get_workflow_history reports about one history event.
type eventDescription struct {
	// eventType is the name of the set eventType oneof field, e.g. taskScheduled.
	eventType string
	// detail is a short human-readable summary; empty when there is none.
	detail string
	// refKey and refID point back at the event that scheduled this one
	// (task_scheduled_id or timer_id); refKey is empty when there is none.
	refKey string
	refID  int32
}

const (
	refTaskScheduledID = "task_scheduled_id"
	refTimerID         = "timer_id"
)

// withRef records the back-reference and puts it in front of the detail.
func (d eventDescription) withRef(key string, id int32, detail string) eventDescription {
	d.refKey, d.refID = key, id
	d.detail = fmt.Sprintf("%s: %d", key, id)
	if detail != "" {
		d.detail += ", " + detail
	}
	return d
}

// describeEvent describes a history event for the event types most useful
// when diagnosing a workflow.
//
// Dapr gives a sequence ID mainly to the events that schedule work
// (taskScheduled, timerCreated, childWorkflowInstanceCreated, ...). The events
// that report an outcome carry event ID -1 and name their scheduling event in
// a field of their own, which is surfaced here so a failure can be traced back
// to the activity, child workflow or timer it belongs to.
func describeEvent(ev *protos.HistoryEvent) eventDescription {
	d := eventDescription{eventType: "unknown"}
	msg := ev.ProtoReflect()
	if oneof := msg.Descriptor().Oneofs().ByName("eventType"); oneof != nil {
		if field := msg.WhichOneof(oneof); field != nil {
			d.eventType = string(field.Name())
		}
	}

	switch {
	case ev.GetExecutionStarted() != nil:
		d.detail = "workflow: " + ev.GetExecutionStarted().GetName()
	case ev.GetExecutionCompleted() != nil:
		completed := ev.GetExecutionCompleted()
		d.detail = statusName(completed.GetWorkflowStatus())
		if failure := completed.GetFailureDetails(); failure != nil {
			d.detail += ": " + failure.GetErrorMessage()
		}
	case ev.GetTaskScheduled() != nil:
		d.detail = "activity: " + ev.GetTaskScheduled().GetName()
	case ev.GetTaskCompleted() != nil:
		return d.withRef(refTaskScheduledID, ev.GetTaskCompleted().GetTaskScheduledId(), "")
	case ev.GetTaskFailed() != nil:
		failed := ev.GetTaskFailed()
		return d.withRef(refTaskScheduledID, failed.GetTaskScheduledId(), "error: "+failed.GetFailureDetails().GetErrorMessage())
	case ev.GetChildWorkflowInstanceCreated() != nil:
		d.detail = "child workflow: " + ev.GetChildWorkflowInstanceCreated().GetName()
	case ev.GetChildWorkflowInstanceCompleted() != nil:
		return d.withRef(refTaskScheduledID, ev.GetChildWorkflowInstanceCompleted().GetTaskScheduledId(), "")
	case ev.GetChildWorkflowInstanceFailed() != nil:
		failed := ev.GetChildWorkflowInstanceFailed()
		return d.withRef(refTaskScheduledID, failed.GetTaskScheduledId(), "error: "+failed.GetFailureDetails().GetErrorMessage())
	case ev.GetEventRaised() != nil:
		d.detail = "event: " + ev.GetEventRaised().GetName()
	case ev.GetTimerCreated() != nil && ev.GetTimerCreated().GetFireAt() != nil:
		d.detail = "fires at: " + formatTime(ev.GetTimerCreated().GetFireAt())
	case ev.GetTimerFired() != nil:
		return d.withRef(refTimerID, ev.GetTimerFired().GetTimerId(), "")
	}
	return d
}

func (h *handler) getWorkflowHistory(ctx context.Context, _ *mcp.CallToolRequest, args InstanceArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolGetWorkflowHistory, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}

	resp, err := h.client.GetInstanceHistory(ctx, args.InstanceID)
	if err != nil {
		return call.Fail(fmt.Errorf("get history of workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}

	history := (*protos.GetInstanceHistoryResponse)(resp).GetEvents()
	events := make([]map[string]any, 0, len(history))

	var sb strings.Builder
	fmt.Fprintf(&sb, "Workflow instance '%s' has %d history event(s):", args.InstanceID, len(history))
	for _, ev := range history {
		d := describeEvent(ev)
		event := map[string]any{"type": d.eventType}
		sb.WriteString("\n- ")
		// A negative event ID means "no sequence ID": leave it out instead of
		// offering -1 as something to rerun from.
		if id := ev.GetEventId(); id >= 0 {
			event["event_id"] = id
			fmt.Fprintf(&sb, "#%d ", id)
		}
		sb.WriteString(d.eventType)
		if d.refKey != "" {
			event[d.refKey] = d.refID
		}
		if ev.GetTimestamp() != nil {
			event["timestamp"] = formatTime(ev.GetTimestamp())
		}
		if d.detail != "" {
			event["detail"] = d.detail
			fmt.Fprintf(&sb, " (%s)", d.detail)
		}
		events = append(events, event)
	}

	call.Succeed("instance_id", args.InstanceID, "events", len(events))
	return toolkit.TextResult(sb.String()), map[string]any{
		"instance_id": args.InstanceID,
		"count":       len(events),
		"events":      events,
	}, nil
}

// instanceSummary is one workflow instance in a list_workflows result.
type instanceSummary struct {
	InstanceID   string `json:"instance_id"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// listing is one page of a sidecar's workflow instances.
type listing struct {
	instances         []instanceSummary
	continuationToken string
	// skipped counts instances whose metadata could not be fetched.
	skipped int
}

// listPageSize clamps the requested limit to [1, maxListLimit],
// using defaultListLimit when none was given.
func listPageSize(limit int) uint32 {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	}
	return uint32(limit) //nolint:gosec // bounded to [1, maxListLimit] by the cases above
}

func listInstances(ctx context.Context, client WorkflowClient, pageSize uint32, continuationToken string) (listing, error) {
	opts := []wf.ListInstanceIDsOptions{wf.WithListInstanceIDsPageSize(pageSize)}
	if continuationToken != "" {
		opts = append(opts, wf.WithListInstanceIDsContinuationToken(continuationToken))
	}
	resp, err := client.ListInstanceIDs(ctx, opts...)
	if err != nil {
		return listing{}, err
	}

	pr := (*protos.ListInstanceIDsResponse)(resp)
	l := listing{
		instances:         make([]instanceSummary, 0, len(pr.GetInstanceIds())),
		continuationToken: pr.GetContinuationToken(),
	}
	for _, id := range pr.GetInstanceIds() {
		// The listing only reports name, status and creation time: do not make
		// the sidecar load every instance's input, output and custom status.
		meta, err := client.FetchWorkflowMetadata(ctx, id, wf.WithFetchPayloads(false))
		if err != nil {
			l.skipped++
			continue
		}
		pm := (*protos.WorkflowMetadata)(meta)
		instance := instanceSummary{
			InstanceID:   id,
			WorkflowName: pm.GetName(),
			Status:       statusName(pm.GetRuntimeStatus()),
		}
		if pm.GetCreatedAt() != nil {
			instance.CreatedAt = formatTime(pm.GetCreatedAt())
		}
		l.instances = append(l.instances, instance)
	}
	return l, nil
}

func writeCounts(sb *strings.Builder, label string, counts map[string]int) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(sb, "\n%s:", label)
	for _, k := range keys {
		fmt.Fprintf(sb, " %s=%d", k, counts[k])
	}
}

func (h *handler) listWorkflows(ctx context.Context, _ *mcp.CallToolRequest, args ListWorkflowsArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolListWorkflows, packageName)
	defer call.End()

	l, err := listInstances(ctx, h.client, listPageSize(args.Limit), args.ContinuationToken)
	if err != nil {
		return call.Fail(fmt.Errorf("list workflow instances: %w", err)), nil, nil
	}

	countsByWorkflow := make(map[string]int)
	countsByStatus := make(map[string]int)
	for _, instance := range l.instances {
		countsByWorkflow[instance.WorkflowName]++
		countsByStatus[instance.Status]++
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d workflow instance(s).", len(l.instances))
	if len(l.instances) > 0 {
		writeCounts(&sb, "Counts by workflow", countsByWorkflow)
		writeCounts(&sb, "Counts by status", countsByStatus)
	}
	for _, instance := range l.instances {
		fmt.Fprintf(&sb, "\n- %s (workflow: %s, status: %s)", instance.InstanceID, instance.WorkflowName, instance.Status)
	}

	structured := map[string]any{
		"instances":          l.instances,
		"count":              len(l.instances),
		"counts_by_workflow": countsByWorkflow,
		"counts_by_status":   countsByStatus,
	}
	if l.skipped > 0 {
		warning := fmt.Sprintf("metadata of %d instance(s) could not be fetched; they are missing from this list.", l.skipped)
		structured["warning"] = warning
		fmt.Fprintf(&sb, "\nWARNING: %s", warning)
	}
	if l.continuationToken != "" {
		structured["continuation_token"] = l.continuationToken
		fmt.Fprintf(&sb, "\nMore instances are available: call list_workflows again with continuationToken '%s'.", l.continuationToken)
	}

	call.Succeed("instances", len(l.instances), "skipped", l.skipped)
	return toolkit.TextResult(sb.String()), structured, nil
}

// RegisterTools registers the workflow tools on server.
// client targets the server's own sidecar.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client WorkflowClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolGetWorkflowStatus,
		Title: "Get Workflow Instance Status",
		Description: "Fetches the runtime status (RUNNING, COMPLETED, FAILED, SUSPENDED, TERMINATED, PENDING) of a workflow instance with its output, custom status, and failure details. **This is a READ-ONLY action and IS IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.\n" +
			"2. **NEVER INVENT**: Take `instanceID` from the user, from `start_workflow`, or from `list_workflows`.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.getWorkflowStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolGetWorkflowHistory,
		Title: "Get Workflow Instance History",
		Description: "Retrieves the event history of a workflow instance: scheduled and completed activities, timers, raised events, and failures. **This is a READ-ONLY action and IS IDEMPOTENT.**\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use this to diagnose WHY a workflow failed or is stuck.\n" +
			"2. Not every event has an `event_id`: mainly the events that schedule work (e.g., `taskScheduled`, `timerCreated`, `childWorkflowInstanceCreated`) do. The `event_id` of such a scheduling event is what `rerun_workflow` takes.\n" +
			"3. Events that report an outcome (e.g., `taskCompleted`, `taskFailed`, `timerFired`) have no `event_id`. Their `task_scheduled_id` or `timer_id` is the `event_id` of the event that scheduled them: use it to find which activity, child workflow, or timer the outcome belongs to.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.getWorkflowHistory)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolListWorkflows,
		Title: "List Workflow Instances",
		Description: "Lists the workflow instances of this server's Dapr app with their workflow name, runtime status, and creation time, plus counts per workflow and per status. **This is a READ-ONLY action and IS IDEMPOTENT.** The tool does NOT filter; filter the returned list yourself. Inputs, outputs, and custom status are not fetched: use `get_workflow_status` for those.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Dapr partitions workflows per app ID: only instances of the app this server's sidecar runs as are visible.\n" +
			"2. If a continuation token is returned, call again with it before drawing conclusions about totals.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.listWorkflows)
}
