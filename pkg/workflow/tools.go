// Package workflow exposes Dapr Workflow management operations as MCP tools.
package workflow

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/dapr/durabletask-go/api/protos"
	wf "github.com/dapr/durabletask-go/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "workflow"

	toolGetWorkflowStatus  = "get_workflow_status"
	toolGetWorkflowHistory = "get_workflow_history"
	toolListWorkflows      = "list_workflows"
	toolStartWorkflow      = "start_workflow"
	toolRerunWorkflow      = "rerun_workflow"
	toolPauseWorkflow      = "pause_workflow"
	toolResumeWorkflow     = "resume_workflow"
	toolTerminateWorkflow  = "terminate_workflow"
	toolRaiseWorkflowEvent = "raise_workflow_event"
	toolPurgeWorkflow      = "purge_workflow"

	attrInstanceID   = "dapr.workflow.instance_id"
	attrWorkflowName = "dapr.workflow.name"
	attrEventName    = "dapr.workflow.event_name"
	attrEventID      = "dapr.workflow.event_id"

	defaultListLimit = 100
	maxListLimit     = 500

	statusPrefix = "ORCHESTRATION_STATUS_"

	rerunEventIDHint = "use the event_id of a scheduling event (e.g., taskScheduled) from get_workflow_history"
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

// StartWorkflowArgs are the arguments of the start_workflow tool.
type StartWorkflowArgs struct {
	WorkflowName string `json:"workflowName" jsonschema:"The name of the workflow to start, as registered by the workflow application (e.g., 'order_processing_workflow')."`
	InstanceID   string `json:"instanceID,omitempty" jsonschema:"Optional unique instance ID for the new workflow. If omitted, Dapr generates one."`
	Input        string `json:"input,omitempty" jsonschema:"Optional input for the workflow, typically a JSON string."`
	StartTime    string `json:"startTime,omitempty" jsonschema:"Optional scheduled start time in RFC 3339 format (e.g., '2026-07-15T06:00:00Z'). If omitted, the workflow starts immediately."`
}

// ReasonArgs are the arguments of the pause_workflow and resume_workflow tools.
type ReasonArgs struct {
	InstanceID string `json:"instanceID" jsonschema:"The workflow instance ID."`
	Reason     string `json:"reason,omitempty" jsonschema:"Optional short reason, recorded for operators."`
}

// RerunWorkflowArgs are the arguments of the rerun_workflow tool.
type RerunWorkflowArgs struct {
	InstanceID    string `json:"instanceID" jsonschema:"The instance ID of the (typically failed) workflow to rerun."`
	EventID       int64  `json:"eventID" jsonschema:"The event_id of a scheduling event (e.g., taskScheduled) from get_workflow_history to rerun from."`
	NewInstanceID string `json:"newInstanceID,omitempty" jsonschema:"Optional instance ID for the new instance. If omitted, Dapr generates one."`
	Input         string `json:"input,omitempty" jsonschema:"Optional replacement input for the rerun, typically a JSON string. If omitted, the original input is reused."`
}

// RaiseWorkflowEventArgs are the arguments of the raise_workflow_event tool.
type RaiseWorkflowEventArgs struct {
	InstanceID string `json:"instanceID" jsonschema:"The instance ID of the workflow waiting for the event."`
	EventName  string `json:"eventName" jsonschema:"The event name the workflow is waiting for (must match the name used in the workflow code)."`
	EventData  string `json:"eventData,omitempty" jsonschema:"Optional event payload, typically a JSON string."`
}

func (h *handler) startWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args StartWorkflowArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolStartWorkflow, packageName, attribute.String(attrWorkflowName, args.WorkflowName))
	defer call.End()

	if res := call.Require(toolkit.Field{Name: "workflowName", Value: args.WorkflowName}); res != nil {
		return res, nil, nil
	}

	var opts []wf.NewWorkflowOptions
	// A whitespace-only ID counts as omitted, so Dapr generates one.
	if strings.TrimSpace(args.InstanceID) != "" {
		opts = append(opts, wf.WithInstanceID(args.InstanceID))
	}
	if args.Input != "" {
		// Raw, because the input already is a JSON string; WithInput would encode it again.
		opts = append(opts, wf.WithRawInput(wrapperspb.String(args.Input)))
	}
	if args.StartTime != "" {
		startTime, err := time.Parse(time.RFC3339, args.StartTime)
		if err != nil {
			return call.Fail(fmt.Errorf("invalid startTime %q: expected RFC 3339 (e.g., '2026-07-15T06:00:00Z'): %w", args.StartTime, err)), nil, nil
		}
		opts = append(opts, wf.WithStartTime(startTime))
	}

	id, err := h.client.ScheduleWorkflow(ctx, args.WorkflowName, opts...)
	if err != nil {
		return call.Fail(fmt.Errorf("start workflow %q: %w", args.WorkflowName, err)), nil, nil
	}

	call.Succeed("workflow", args.WorkflowName, "instance_id", id)
	structured := map[string]any{"workflow_name": args.WorkflowName, "instance_id": id}
	if args.StartTime != "" {
		structured["start_time"] = args.StartTime
		text := fmt.Sprintf("Successfully scheduled workflow '%s' with instance ID '%s' to start at %s.", args.WorkflowName, id, args.StartTime)
		return toolkit.TextResult(text), structured, nil
	}
	text := fmt.Sprintf("Successfully started workflow '%s' with instance ID '%s'.", args.WorkflowName, id)
	return toolkit.TextResult(text), structured, nil
}

// withRawRerunInput sets the rerun input verbatim.
// wf.WithRerunInput would JSON-encode it, double-encoding an input that already is a JSON string.
func withRawRerunInput(input string) wf.RerunOptions {
	return func(req *protos.RerunWorkflowFromEventRequest) error {
		req.Input = wrapperspb.String(input)
		req.OverwriteInput = true
		return nil
	}
}

func (h *handler) rerunWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args RerunWorkflowArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolRerunWorkflow, args.InstanceID, attribute.Int64(attrEventID, args.EventID))
	defer call.End()
	if res != nil {
		return res, nil, nil
	}
	switch {
	case args.EventID < 0:
		return call.Fail(fmt.Errorf("invalid eventID %d: events without a sequence ID cannot be rerun from; %s", args.EventID, rerunEventIDHint)), nil, nil
	case args.EventID > math.MaxUint32:
		return call.Fail(fmt.Errorf("invalid eventID %d: out of range; %s", args.EventID, rerunEventIDHint)), nil, nil
	}

	var opts []wf.RerunOptions
	// A whitespace-only ID counts as omitted, so Dapr generates one.
	if strings.TrimSpace(args.NewInstanceID) != "" {
		opts = append(opts, wf.WithRerunNewInstanceID(args.NewInstanceID))
	}
	if args.Input != "" {
		opts = append(opts, withRawRerunInput(args.Input))
	}

	newID, err := h.client.RerunWorkflowFromEvent(ctx, args.InstanceID, uint32(args.EventID), opts...) //nolint:gosec // range-checked above
	if err != nil {
		return call.Fail(fmt.Errorf("rerun workflow instance %q from event %d: %w", args.InstanceID, args.EventID, err)), nil, nil
	}

	call.Succeed("instance_id", args.InstanceID, "event_id", args.EventID, "new_instance_id", newID)
	text := fmt.Sprintf("Successfully reran workflow instance '%s' from event %d as new instance '%s'.", args.InstanceID, args.EventID, newID)
	return toolkit.TextResult(text), map[string]any{"source_instance_id": args.InstanceID, "event_id": args.EventID, "new_instance_id": newID}, nil
}

func (h *handler) pauseWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args ReasonArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolPauseWorkflow, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}
	if err := h.client.SuspendWorkflow(ctx, args.InstanceID, args.Reason); err != nil {
		return call.Fail(fmt.Errorf("pause workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}
	call.Succeed("instance_id", args.InstanceID)
	text := fmt.Sprintf("Successfully paused workflow instance '%s'.", args.InstanceID)
	return toolkit.TextResult(text), map[string]any{"instance_id": args.InstanceID, "requested": true}, nil
}

func (h *handler) resumeWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args ReasonArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolResumeWorkflow, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}
	if err := h.client.ResumeWorkflow(ctx, args.InstanceID, args.Reason); err != nil {
		return call.Fail(fmt.Errorf("resume workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}
	call.Succeed("instance_id", args.InstanceID)
	text := fmt.Sprintf("Successfully resumed workflow instance '%s'.", args.InstanceID)
	return toolkit.TextResult(text), map[string]any{"instance_id": args.InstanceID, "requested": true}, nil
}

func (h *handler) terminateWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args InstanceArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolTerminateWorkflow, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}
	if err := h.client.TerminateWorkflow(ctx, args.InstanceID); err != nil {
		return call.Fail(fmt.Errorf("terminate workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}
	call.Succeed("instance_id", args.InstanceID)
	text := fmt.Sprintf("Successfully requested termination of workflow instance '%s'.", args.InstanceID)
	return toolkit.TextResult(text), map[string]any{"instance_id": args.InstanceID, "requested": true}, nil
}

func (h *handler) raiseWorkflowEvent(ctx context.Context, _ *mcp.CallToolRequest, args RaiseWorkflowEventArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolRaiseWorkflowEvent, packageName,
		attribute.String(attrInstanceID, args.InstanceID),
		attribute.String(attrEventName, args.EventName),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "instanceID", Value: args.InstanceID},
		toolkit.Field{Name: "eventName", Value: args.EventName},
	); res != nil {
		return res, nil, nil
	}

	var opts []wf.RaiseEventOptions
	if args.EventData != "" {
		opts = append(opts, wf.WithRawEventData(wrapperspb.String(args.EventData)))
	}
	if err := h.client.RaiseEvent(ctx, args.InstanceID, args.EventName, opts...); err != nil {
		return call.Fail(fmt.Errorf("raise event %q for workflow instance %q: %w", args.EventName, args.InstanceID, err)), nil, nil
	}
	call.Succeed("instance_id", args.InstanceID, "event", args.EventName)
	text := fmt.Sprintf("Successfully raised event '%s' for workflow instance '%s'.", args.EventName, args.InstanceID)
	return toolkit.TextResult(text), map[string]any{"instance_id": args.InstanceID, "event_name": args.EventName}, nil
}

func (h *handler) purgeWorkflow(ctx context.Context, _ *mcp.CallToolRequest, args InstanceArgs) (*mcp.CallToolResult, any, error) {
	ctx, call, res := h.startInstanceCall(ctx, toolPurgeWorkflow, args.InstanceID)
	defer call.End()
	if res != nil {
		return res, nil, nil
	}
	if err := h.client.PurgeWorkflowState(ctx, args.InstanceID); err != nil {
		return call.Fail(fmt.Errorf("purge workflow instance %q: %w", args.InstanceID, err)), nil, nil
	}
	call.Succeed("instance_id", args.InstanceID)
	text := fmt.Sprintf("Successfully purged the state of workflow instance '%s'.", args.InstanceID)
	return toolkit.TextResult(text), map[string]any{"instance_id": args.InstanceID, "purged": true}, nil
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
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolStartWorkflow,
		Title: "Start Workflow Instance",
		Description: "Starts a new Dapr Workflow instance, immediately or at a scheduled `startTime`. **This is a SIDE-EFFECT action that is NOT IDEMPOTENT** unless an explicit `instanceID` is provided.\n\n" +
			"**GUIDANCE:**\n" +
			"1. The workflow must be registered by the app this server's sidecar runs as.\n" +
			"2. Provide `input` as a JSON string when the workflow expects input.\n" +
			"3. Workflows run asynchronously: use `get_workflow_status` to track progress.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `workflowName`.\n" +
			"2. **NEVER INVENT**: Do NOT invent workflow names; ask the user if unknown.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.startWorkflow)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolRerunWorkflow,
		Title: "Rerun Workflow From Event",
		Description: "Reruns a workflow instance from an event in its history as a NEW instance, reusing the results recorded before that event. **This is a SIDE-EFFECT action that is NOT IDEMPOTENT** unless an explicit `newInstanceID` is provided.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID` and an `eventID`.\n" +
			"2. **NEVER INVENT**: Take `eventID` from `get_workflow_history`: use the `event_id` of a scheduling event (e.g., `taskScheduled`). Events without an `event_id` cannot be rerun from; use the `task_scheduled_id` of a failed task to find its scheduling event.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.rerunWorkflow)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolPauseWorkflow,
		Title: "Pause Workflow Instance",
		Description: "Pauses (suspends) a running workflow instance until it is resumed with `resume_workflow`. **This is a SIDE-EFFECT action that IS IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.",
		Annotations: toolkit.IdempotentWrite.Annotations(true),
	}, h.pauseWorkflow)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolResumeWorkflow,
		Title: "Resume Workflow Instance",
		Description: "Resumes a paused (suspended) workflow instance. **This is a SIDE-EFFECT action that IS IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.",
		Annotations: toolkit.IdempotentWrite.Annotations(true),
	}, h.resumeWorkflow)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolTerminateWorkflow,
		Title: "Terminate Workflow Instance",
		Description: "Forcefully terminates a running workflow instance without executing its remaining steps. It also terminates the child workflows the instance started. **This is a DESTRUCTIVE action that cannot be undone and IS IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.\n" +
			"2. **CONFIRMATION**: Confirm with the user before calling this tool, unless the user explicitly asked for it.",
		Annotations: toolkit.DestructiveIdempotent.Annotations(true),
	}, h.terminateWorkflow)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolRaiseWorkflowEvent,
		Title: "Raise Workflow Event",
		Description: "Delivers an external event (e.g., an approval) to a workflow instance waiting for it. **This is a SIDE-EFFECT action that is NOT IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty `instanceID` and `eventName` values.\n" +
			"2. **NEVER INVENT**: `eventName` must exactly match the name the workflow waits for.\n" +
			"3. Provide `eventData` as a JSON string when the workflow expects a payload.",
		Annotations: toolkit.AdditiveWrite.Annotations(true),
	}, h.raiseWorkflowEvent)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolPurgeWorkflow,
		Title: "Purge Workflow Instance State",
		Description: "Permanently deletes the state and history of a COMPLETED, FAILED, or TERMINATED workflow instance. The purge request is not recursive: child workflows keep their state, purge those separately. **This is a DESTRUCTIVE action that cannot be undone and is NOT IDEMPOTENT.**\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty `instanceID`.\n" +
			"2. **CONFIRMATION**: Confirm with the user before calling this tool, unless the user explicitly asked for it.",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.purgeWorkflow)
}
