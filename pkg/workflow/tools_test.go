package workflow

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/dapr/durabletask-go/api/protos"
	wf "github.com/dapr/durabletask-go/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

var createdAt = time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC)

func newTestHandler(t *testing.T) (*handler, *mocks.MockWorkflowClient, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := new(mocks.MockWorkflowClient)
	t.Cleanup(func() { client.AssertExpectations(t) })
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, client, &buf
}

func text(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

func metadata(id, name string, status protos.OrchestrationStatus) *wf.WorkflowMetadata {
	return (*wf.WorkflowMetadata)(&protos.WorkflowMetadata{
		InstanceId:    id,
		Name:          name,
		RuntimeStatus: status,
		CreatedAt:     timestamppb.New(createdAt),
	})
}

func idList(token string, ids ...string) *wf.ListInstanceIDsResponse {
	resp := &protos.ListInstanceIDsResponse{InstanceIds: ids}
	if token != "" {
		resp.ContinuationToken = &token
	}
	return (*wf.ListInstanceIDsResponse)(resp)
}

// fetchesPayloads matches the FetchWorkflowMetadata options that leave the
// request with GetInputsAndOutputs set to want. The request starts out the way
// the durabletask-go client builds it: with payloads on.
func fetchesPayloads(want bool) any {
	return mock.MatchedBy(func(opts []wf.FetchWorkflowMetadataOptions) bool {
		req := &protos.GetInstanceRequest{GetInputsAndOutputs: true}
		for _, opt := range opts {
			opt(req)
		}
		return req.GetInputsAndOutputs == want
	})
}

func TestGetWorkflowStatus(t *testing.T) {
	t.Parallel()

	t.Run("reports status, custom status, output and failure", func(t *testing.T) {
		t.Parallel()
		h, client, _ := newTestHandler(t)
		meta := (*wf.WorkflowMetadata)(&protos.WorkflowMetadata{
			InstanceId:     "order-1",
			Name:           "order",
			RuntimeStatus:  protos.OrchestrationStatus_ORCHESTRATION_STATUS_FAILED,
			CreatedAt:      timestamppb.New(createdAt),
			CustomStatus:   wrapperspb.String("step 3/3"),
			Output:         wrapperspb.String(`{"shipped":false}`),
			FailureDetails: &protos.TaskFailureDetails{ErrorMessage: "boom"},
		})
		client.On("FetchWorkflowMetadata", mock.Anything, "order-1", fetchesPayloads(true)).Return(meta, nil)

		result, structured, err := h.getWorkflowStatus(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})

		require.NoError(t, err)
		assert.False(t, result.IsError)
		got := text(t, result)
		assert.Contains(t, got, "Workflow instance 'order-1' (workflow 'order') is FAILED.")
		assert.Contains(t, got, "Custom status: step 3/3")
		assert.Contains(t, got, "Failure: boom")
		s, ok := structured.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "FAILED", s["status"])
		assert.Equal(t, "2026-07-14T06:00:00Z", s["created_at"])
		assert.NotContains(t, s, "last_updated_at", "a nil timestamp is omitted, not rendered as the zero time")
	})

	t.Run("dapr failure", func(t *testing.T) {
		t.Parallel()
		h, client, _ := newTestHandler(t)
		client.On("FetchWorkflowMetadata", mock.Anything, "order-1", mock.Anything).Return(nil, errors.New("not found"))

		result, _, err := h.getWorkflowStatus(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})

		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, text(t, result), `get status of workflow instance "order-1": not found`)
	})
}

func TestGetWorkflowHistory(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	// IDs as Dapr records them: only the events a workflow schedules carry a
	// sequence ID, everything else has -1 and points back at its scheduling event.
	resp := (*wf.GetInstanceHistoryResponse)(&protos.GetInstanceHistoryResponse{Events: []*protos.HistoryEvent{
		{EventId: -1, Timestamp: timestamppb.New(createdAt), EventType: &protos.HistoryEvent_ExecutionStarted{ExecutionStarted: &protos.ExecutionStartedEvent{Name: "order"}}},
		{EventId: 0, EventType: &protos.HistoryEvent_TaskScheduled{TaskScheduled: &protos.TaskScheduledEvent{Name: "reserve"}}},
		{EventId: -1, EventType: &protos.HistoryEvent_TaskCompleted{TaskCompleted: &protos.TaskCompletedEvent{TaskScheduledId: 0}}},
		{EventId: 1, EventType: &protos.HistoryEvent_TaskScheduled{TaskScheduled: &protos.TaskScheduledEvent{Name: "charge"}}},
		{EventId: -1, EventType: &protos.HistoryEvent_TaskFailed{TaskFailed: &protos.TaskFailedEvent{TaskScheduledId: 1, FailureDetails: &protos.TaskFailureDetails{ErrorMessage: "card declined"}}}},
	}})
	client.On("GetInstanceHistory", mock.Anything, "order-1", mock.Anything).Return(resp, nil)

	result, structured, err := h.getWorkflowHistory(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	got := text(t, result)
	assert.Contains(t, got, "has 5 history event(s)")
	assert.Contains(t, got, "\n- executionStarted (workflow: order)")
	assert.Contains(t, got, "\n- #0 taskScheduled (activity: reserve)")
	assert.Contains(t, got, "\n- taskCompleted (task_scheduled_id: 0)")
	assert.Contains(t, got, "\n- #1 taskScheduled (activity: charge)")
	assert.Contains(t, got, "\n- taskFailed (task_scheduled_id: 1, error: card declined)")
	assert.NotContains(t, got, "#-1", "an event without a sequence ID is not given one")

	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 5, s["count"])
	assert.Equal(t, []map[string]any{
		{"type": "executionStarted", "timestamp": "2026-07-14T06:00:00Z", "detail": "workflow: order"},
		{"event_id": int32(0), "type": "taskScheduled", "detail": "activity: reserve"},
		{"type": "taskCompleted", "task_scheduled_id": int32(0), "detail": "task_scheduled_id: 0"},
		{"event_id": int32(1), "type": "taskScheduled", "detail": "activity: charge"},
		{"type": "taskFailed", "task_scheduled_id": int32(1), "detail": "task_scheduled_id: 1, error: card declined"},
	}, s["events"])
}

func TestGetWorkflowHistoryDaprFailure(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("GetInstanceHistory", mock.Anything, "order-1", mock.Anything).Return(nil, errors.New("not found"))

	result, _, err := h.getWorkflowHistory(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})

	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), `get history of workflow instance "order-1": not found`)
}

func TestDescribeEvent(t *testing.T) {
	t.Parallel()
	fireAt := timestamppb.New(createdAt)
	tests := []struct {
		name       string
		event      *protos.HistoryEvent
		wantType   string
		wantDetail string
		wantRefKey string
		wantRefID  int32
	}{
		{
			name:     "executionCompleted",
			event:    &protos.HistoryEvent{EventId: 3, EventType: &protos.HistoryEvent_ExecutionCompleted{ExecutionCompleted: &protos.ExecutionCompletedEvent{WorkflowStatus: protos.OrchestrationStatus_ORCHESTRATION_STATUS_COMPLETED}}},
			wantType: "executionCompleted", wantDetail: "COMPLETED",
		},
		{
			name: "executionCompleted with failure",
			event: &protos.HistoryEvent{EventId: 3, EventType: &protos.HistoryEvent_ExecutionCompleted{ExecutionCompleted: &protos.ExecutionCompletedEvent{
				WorkflowStatus: protos.OrchestrationStatus_ORCHESTRATION_STATUS_FAILED,
				FailureDetails: &protos.TaskFailureDetails{ErrorMessage: "card declined"},
			}}},
			wantType: "executionCompleted", wantDetail: "FAILED: card declined",
		},
		{
			name:     "childWorkflowInstanceCreated",
			event:    &protos.HistoryEvent{EventId: 2, EventType: &protos.HistoryEvent_ChildWorkflowInstanceCreated{ChildWorkflowInstanceCreated: &protos.ChildWorkflowInstanceCreatedEvent{Name: "shipping"}}},
			wantType: "childWorkflowInstanceCreated", wantDetail: "child workflow: shipping",
		},
		{
			name:     "childWorkflowInstanceCompleted",
			event:    &protos.HistoryEvent{EventId: -1, EventType: &protos.HistoryEvent_ChildWorkflowInstanceCompleted{ChildWorkflowInstanceCompleted: &protos.ChildWorkflowInstanceCompletedEvent{TaskScheduledId: 2}}},
			wantType: "childWorkflowInstanceCompleted", wantDetail: "task_scheduled_id: 2", wantRefKey: "task_scheduled_id", wantRefID: 2,
		},
		{
			name: "childWorkflowInstanceFailed",
			event: &protos.HistoryEvent{EventId: -1, EventType: &protos.HistoryEvent_ChildWorkflowInstanceFailed{ChildWorkflowInstanceFailed: &protos.ChildWorkflowInstanceFailedEvent{
				TaskScheduledId: 2, FailureDetails: &protos.TaskFailureDetails{ErrorMessage: "no carrier"},
			}}},
			wantType: "childWorkflowInstanceFailed", wantDetail: "task_scheduled_id: 2, error: no carrier", wantRefKey: "task_scheduled_id", wantRefID: 2,
		},
		{
			name:     "eventRaised",
			event:    &protos.HistoryEvent{EventId: -1, EventType: &protos.HistoryEvent_EventRaised{EventRaised: &protos.EventRaisedEvent{Name: "approval"}}},
			wantType: "eventRaised", wantDetail: "event: approval",
		},
		{
			name:     "timerCreated",
			event:    &protos.HistoryEvent{EventId: 4, EventType: &protos.HistoryEvent_TimerCreated{TimerCreated: &protos.TimerCreatedEvent{FireAt: fireAt}}},
			wantType: "timerCreated", wantDetail: "fires at: 2026-07-14T06:00:00Z",
		},
		{
			name:     "timerCreated without a fire time",
			event:    &protos.HistoryEvent{EventId: 4, EventType: &protos.HistoryEvent_TimerCreated{TimerCreated: &protos.TimerCreatedEvent{}}},
			wantType: "timerCreated",
		},
		{
			name:     "timerFired",
			event:    &protos.HistoryEvent{EventId: -1, EventType: &protos.HistoryEvent_TimerFired{TimerFired: &protos.TimerFiredEvent{TimerId: 4, FireAt: fireAt}}},
			wantType: "timerFired", wantDetail: "timer_id: 4", wantRefKey: "timer_id", wantRefID: 4,
		},
		{
			name:     "an event type without a detail",
			event:    &protos.HistoryEvent{EventId: -1, EventType: &protos.HistoryEvent_ExecutionSuspended{ExecutionSuspended: &protos.ExecutionSuspendedEvent{}}},
			wantType: "executionSuspended",
		},
		{
			name:     "no event type set",
			event:    &protos.HistoryEvent{EventId: -1},
			wantType: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := describeEvent(tt.event)
			assert.Equal(t, tt.wantType, got.eventType)
			assert.Equal(t, tt.wantDetail, got.detail)
			assert.Equal(t, tt.wantRefKey, got.refKey)
			assert.Equal(t, tt.wantRefID, got.refID)
		})
	}
}

func TestInstanceToolsRequireInstanceID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "   "} {
		h, _, _ := newTestHandler(t) // no expectations: the client must not be called
		calls := map[string]func() (*mcp.CallToolResult, any, error){
			"get_workflow_status": func() (*mcp.CallToolResult, any, error) {
				return h.getWorkflowStatus(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: id})
			},
			"get_workflow_history": func() (*mcp.CallToolResult, any, error) {
				return h.getWorkflowHistory(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: id})
			},
		}
		for name, call := range calls {
			result, _, err := call()
			require.NoError(t, err, name)
			assert.True(t, result.IsError, name)
			assert.Contains(t, text(t, result), "missing required argument: instanceID", name)
		}
	}
}

func TestListPageSize(t *testing.T) {
	t.Parallel()
	tests := map[int]uint32{0: 100, -5: 100, 1: 1, 500: 500, 501: 500, 1 << 40: 500}
	for limit, want := range tests {
		assert.Equal(t, want, listPageSize(limit), "limit %d", limit)
	}
}

func TestListWorkflows(t *testing.T) {
	t.Parallel()

	t.Run("lists with counts and continuation token", func(t *testing.T) {
		t.Parallel()
		h, client, _ := newTestHandler(t)
		client.On("ListInstanceIDs", mock.Anything, mock.MatchedBy(func(opts []wf.ListInstanceIDsOptions) bool {
			req := &protos.ListInstanceIDsRequest{}
			for _, opt := range opts {
				require.NoError(t, opt(req))
			}
			return req.GetPageSize() == 2 && req.GetContinuationToken() == "page-1"
		})).Return(idList("page-2", "o-1", "o-2"), nil)
		client.On("FetchWorkflowMetadata", mock.Anything, "o-1", fetchesPayloads(false)).
			Return(metadata("o-1", "order", protos.OrchestrationStatus_ORCHESTRATION_STATUS_RUNNING), nil)
		client.On("FetchWorkflowMetadata", mock.Anything, "o-2", fetchesPayloads(false)).
			Return(metadata("o-2", "order", protos.OrchestrationStatus_ORCHESTRATION_STATUS_COMPLETED), nil)

		result, structured, err := h.listWorkflows(context.Background(), &mcp.CallToolRequest{}, ListWorkflowsArgs{Limit: 2, ContinuationToken: "page-1"})

		require.NoError(t, err)
		assert.False(t, result.IsError)
		got := text(t, result)
		assert.Contains(t, got, "Found 2 workflow instance(s).")
		assert.Contains(t, got, "Counts by status: COMPLETED=1 RUNNING=1")
		assert.Contains(t, got, "- o-1 (workflow: order, status: RUNNING)")
		assert.Contains(t, got, "continuationToken 'page-2'")
		s, ok := structured.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "page-2", s["continuation_token"])
		assert.Equal(t, map[string]int{"order": 2}, s["counts_by_workflow"])
	})

	t.Run("empty app returns an empty list, not an error", func(t *testing.T) {
		t.Parallel()
		h, client, _ := newTestHandler(t)
		client.On("ListInstanceIDs", mock.Anything, mock.Anything).Return(idList(""), nil)

		result, structured, err := h.listWorkflows(context.Background(), &mcp.CallToolRequest{}, ListWorkflowsArgs{})

		require.NoError(t, err)
		assert.False(t, result.IsError)
		assert.Contains(t, text(t, result), "Found 0 workflow instance(s).")
		s, ok := structured.(map[string]any)
		require.True(t, ok)
		assert.NotNil(t, s["instances"], "instances is serialized as [], not null")
	})

	t.Run("dapr failure", func(t *testing.T) {
		t.Parallel()
		h, client, _ := newTestHandler(t)
		client.On("ListInstanceIDs", mock.Anything, mock.Anything).Return(nil, errors.New("down"))

		result, _, err := h.listWorkflows(context.Background(), &mcp.CallToolRequest{}, ListWorkflowsArgs{})

		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, text(t, result), "list workflow instances: down")
	})
}

func TestListWorkflowsSkipsUnfetchableInstances(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("ListInstanceIDs", mock.Anything, mock.Anything).Return(idList("", "o-1", "o-2"), nil)
	client.On("FetchWorkflowMetadata", mock.Anything, "o-1", fetchesPayloads(false)).
		Return(metadata("o-1", "order", protos.OrchestrationStatus_ORCHESTRATION_STATUS_RUNNING), nil)
	client.On("FetchWorkflowMetadata", mock.Anything, "o-2", fetchesPayloads(false)).Return(nil, errors.New("gone"))

	result, structured, err := h.listWorkflows(context.Background(), &mcp.CallToolRequest{}, ListWorkflowsArgs{})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	got := text(t, result)
	assert.Contains(t, got, "Found 1 workflow instance(s).")
	assert.Contains(t, got, "WARNING: metadata of 1 instance(s) could not be fetched; they are missing from this list.")
	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Contains(t, s, "warning")
}

func TestListWorkflowsFailsWhenEveryLookupFails(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("ListInstanceIDs", mock.Anything, mock.Anything).Return(idList("", "o-1", "o-2"), nil)
	client.On("FetchWorkflowMetadata", mock.Anything, mock.Anything, fetchesPayloads(false)).Return(nil, errors.New("unavailable"))

	result, _, err := h.listWorkflows(context.Background(), &mcp.CallToolRequest{}, ListWorkflowsArgs{})

	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), "metadata of all 2 instance(s) on the page could not be fetched: unavailable")
}

func TestListWorkflowsFailsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	client.On("ListInstanceIDs", mock.Anything, mock.Anything).Return(idList("", "o-1", "o-2"), nil)
	client.On("FetchWorkflowMetadata", mock.Anything, "o-1", fetchesPayloads(false)).
		Return(metadata("o-1", "order", protos.OrchestrationStatus_ORCHESTRATION_STATUS_RUNNING), nil).
		Run(func(mock.Arguments) { cancel() })
	client.On("FetchWorkflowMetadata", mock.Anything, "o-2", fetchesPayloads(false)).Return(nil, context.Canceled)

	result, _, err := h.listWorkflows(ctx, &mcp.CallToolRequest{}, ListWorkflowsArgs{})

	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), `fetch metadata of instance "o-2": context canceled`)
}

func TestStartWorkflowSendsRawInputAndDoesNotLogIt(t *testing.T) {
	t.Parallel()
	h, client, logs := newTestHandler(t)
	const input = `{"qty":1,"note":"sensitive-input"}`
	client.On("ScheduleWorkflow", mock.Anything, "order", mock.MatchedBy(func(opts []wf.NewWorkflowOptions) bool {
		req := &protos.CreateInstanceRequest{}
		for _, opt := range opts {
			require.NoError(t, opt(req))
		}
		return len(opts) == 2 && req.GetInstanceId() == "order-1" && req.GetInput().GetValue() == input
	})).Return("order-1", nil)

	result, structured, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{
		WorkflowName: "order", InstanceID: "order-1", Input: input,
	})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, text(t, result), "Successfully started workflow 'order' with instance ID 'order-1'.")
	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "order-1", s["instance_id"])
	assert.NotContains(t, logs.String(), "sensitive-input")
}

func TestStartWorkflowSchedulesAtStartTime(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("ScheduleWorkflow", mock.Anything, "order", mock.MatchedBy(func(opts []wf.NewWorkflowOptions) bool {
		req := &protos.CreateInstanceRequest{}
		for _, opt := range opts {
			require.NoError(t, opt(req))
		}
		return req.GetScheduledStartTimestamp().AsTime().Equal(createdAt)
	})).Return("generated", nil)

	result, structured, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{
		WorkflowName: "order", StartTime: "2026-07-14T06:00:00Z",
	})

	require.NoError(t, err)
	assert.Contains(t, text(t, result), "to start at 2026-07-14T06:00:00Z")
	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "2026-07-14T06:00:00Z", s["start_time"])
}

func TestStartWorkflowRequiresName(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHandler(t)
	result, _, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{WorkflowName: " "})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), "missing required argument: workflowName")
}

func TestStartWorkflowRejectsBadStartTime(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHandler(t)
	result, _, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{WorkflowName: "order", StartTime: "tomorrow"})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), `invalid startTime "tomorrow"`)
}

func TestStartWorkflowDaprFailure(t *testing.T) {
	t.Parallel()
	h, client, logs := newTestHandler(t)
	client.On("ScheduleWorkflow", mock.Anything, "order", mock.Anything).Return("", errors.New("not registered"))
	result, _, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{
		WorkflowName: "order", Input: `{"note":"sensitive-input"}`,
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), `start workflow "order": not registered`)
	assert.Contains(t, logs.String(), "tool call failed")
	assert.NotContains(t, logs.String(), "sensitive-input")
}

func TestStartWorkflowTreatsBlankInstanceIDAsOmitted(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("ScheduleWorkflow", mock.Anything, "order", mock.MatchedBy(func(opts []wf.NewWorkflowOptions) bool {
		req := &protos.CreateInstanceRequest{}
		for _, opt := range opts {
			require.NoError(t, opt(req))
		}
		return req.GetInstanceId() == ""
	})).Return("generated", nil)

	result, structured, err := h.startWorkflow(context.Background(), &mcp.CallToolRequest{}, StartWorkflowArgs{WorkflowName: "order", InstanceID: "   "})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "generated", s["instance_id"])
}

func TestRerunWorkflow(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("RerunWorkflowFromEvent", mock.Anything, "order-1", uint32(4), mock.MatchedBy(func(opts []wf.RerunOptions) bool {
		req := &protos.RerunWorkflowFromEventRequest{}
		for _, opt := range opts {
			require.NoError(t, opt(req))
		}
		return req.GetNewInstanceID() == "order-1-rerun" && req.GetInput().GetValue() == `{"qty":2}` && req.GetOverwriteInput()
	})).Return("order-1-rerun", nil)

	result, structured, err := h.rerunWorkflow(context.Background(), &mcp.CallToolRequest{}, RerunWorkflowArgs{
		InstanceID: "order-1", EventID: 4, NewInstanceID: "order-1-rerun", Input: `{"qty":2}`,
	})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, text(t, result), "from event 4 as new instance 'order-1-rerun'")
	s, ok := structured.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "order-1-rerun", s["new_instance_id"])
}

func TestRerunRejectsOutOfRangeEventID(t *testing.T) {
	t.Parallel()
	tests := map[int64]string{
		-1:      "invalid eventID -1: events without a sequence ID cannot be rerun from; use the event_id of a scheduling event (e.g., taskScheduled) from get_workflow_history",
		1 << 32: "invalid eventID 4294967296: out of range; use the event_id of a scheduling event (e.g., taskScheduled) from get_workflow_history",
	}
	for eventID, want := range tests {
		h, _, _ := newTestHandler(t) // the client must not be called
		result, _, err := h.rerunWorkflow(context.Background(), &mcp.CallToolRequest{}, RerunWorkflowArgs{InstanceID: "order-1", EventID: eventID})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Equal(t, want, text(t, result))
	}
}

func TestRerunAcceptsBoundaryEventIDs(t *testing.T) {
	t.Parallel()
	for _, eventID := range []int64{0, math.MaxUint32} {
		h, client, _ := newTestHandler(t)
		client.On("RerunWorkflowFromEvent", mock.Anything, "order-1", uint32(eventID), mock.Anything).Return("order-1-rerun", nil)
		result, structured, err := h.rerunWorkflow(context.Background(), &mcp.CallToolRequest{}, RerunWorkflowArgs{InstanceID: "order-1", EventID: eventID})
		require.NoError(t, err)
		assert.False(t, result.IsError, "eventID %d", eventID)
		s, ok := structured.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, eventID, s["event_id"])
	}
}

func TestRerunTreatsBlankNewInstanceIDAsOmitted(t *testing.T) {
	t.Parallel()
	h, client, _ := newTestHandler(t)
	client.On("RerunWorkflowFromEvent", mock.Anything, "order-1", uint32(4), mock.MatchedBy(func(opts []wf.RerunOptions) bool {
		req := &protos.RerunWorkflowFromEventRequest{}
		for _, opt := range opts {
			require.NoError(t, opt(req))
		}
		return req.NewInstanceID == nil
	})).Return("generated", nil)

	result, _, err := h.rerunWorkflow(context.Background(), &mcp.CallToolRequest{}, RerunWorkflowArgs{InstanceID: "order-1", EventID: 4, NewInstanceID: " \t "})

	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, text(t, result), "as new instance 'generated'")
}

func TestRerunDaprFailureDoesNotLogInput(t *testing.T) {
	t.Parallel()
	h, client, logs := newTestHandler(t)
	client.On("RerunWorkflowFromEvent", mock.Anything, "order-1", uint32(4), mock.Anything).Return("", errors.New("not found"))

	result, _, err := h.rerunWorkflow(context.Background(), &mcp.CallToolRequest{}, RerunWorkflowArgs{
		InstanceID: "order-1", EventID: 4, Input: `{"note":"sensitive-input"}`,
	})

	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, text(t, result), `rerun workflow instance "order-1" from event 4: not found`)
	assert.Contains(t, logs.String(), "tool call failed")
	assert.NotContains(t, logs.String(), "sensitive-input")
}

func TestRaiseWorkflowEvent(t *testing.T) {
	t.Parallel()

	t.Run("sends raw event data", func(t *testing.T) {
		t.Parallel()
		h, client, logs := newTestHandler(t)
		client.On("RaiseEvent", mock.Anything, "order-1", "approval", mock.MatchedBy(func(opts []wf.RaiseEventOptions) bool {
			req := &protos.RaiseEventRequest{}
			for _, opt := range opts {
				require.NoError(t, opt(req))
			}
			return req.GetInput().GetValue() == `{"approved":true,"by":"sensitive-name"}`
		})).Return(nil)

		result, _, err := h.raiseWorkflowEvent(context.Background(), &mcp.CallToolRequest{}, RaiseWorkflowEventArgs{
			InstanceID: "order-1", EventName: "approval", EventData: `{"approved":true,"by":"sensitive-name"}`,
		})

		require.NoError(t, err)
		assert.False(t, result.IsError)
		assert.Contains(t, text(t, result), "Successfully raised event 'approval' for workflow instance 'order-1'.")
		assert.NotContains(t, logs.String(), "sensitive-name")
	})

	t.Run("dapr failure does not log the event data", func(t *testing.T) {
		t.Parallel()
		h, client, logs := newTestHandler(t)
		client.On("RaiseEvent", mock.Anything, "order-1", "approval", mock.Anything).Return(errors.New("not found"))

		result, _, err := h.raiseWorkflowEvent(context.Background(), &mcp.CallToolRequest{}, RaiseWorkflowEventArgs{
			InstanceID: "order-1", EventName: "approval", EventData: `{"by":"sensitive-name"}`,
		})

		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, text(t, result), `raise event "approval" for workflow instance "order-1": not found`)
		assert.Contains(t, logs.String(), "tool call failed")
		assert.NotContains(t, logs.String(), "sensitive-name")
	})

	t.Run("requires eventName", func(t *testing.T) {
		t.Parallel()
		h, _, _ := newTestHandler(t)
		result, _, err := h.raiseWorkflowEvent(context.Background(), &mcp.CallToolRequest{}, RaiseWorkflowEventArgs{InstanceID: "order-1"})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, text(t, result), "missing required argument: eventName")
	})
}

func TestInstanceActionTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		method   string
		mockArgs []any
		call     func(h *handler) (*mcp.CallToolResult, any, error)
		wantText string
		// wantKey is the structured field that reports the outcome: "requested"
		// for the calls Dapr only accepts, "purged" for the one it completes.
		wantKey  string
		wantFail string
	}{
		{
			name: "pause", method: "SuspendWorkflow", mockArgs: []any{mock.Anything, "order-1", "maintenance"},
			call: func(h *handler) (*mcp.CallToolResult, any, error) {
				return h.pauseWorkflow(context.Background(), &mcp.CallToolRequest{}, ReasonArgs{InstanceID: "order-1", Reason: "maintenance"})
			},
			wantText: "Successfully requested suspension of workflow instance 'order-1'.", wantKey: "requested",
			wantFail: `pause workflow instance "order-1": boom`,
		},
		{
			name: "resume", method: "ResumeWorkflow", mockArgs: []any{mock.Anything, "order-1", ""},
			call: func(h *handler) (*mcp.CallToolResult, any, error) {
				return h.resumeWorkflow(context.Background(), &mcp.CallToolRequest{}, ReasonArgs{InstanceID: "order-1"})
			},
			wantText: "Successfully requested resumption of workflow instance 'order-1'.", wantKey: "requested",
			wantFail: `resume workflow instance "order-1": boom`,
		},
		{
			name: "terminate", method: "TerminateWorkflow", mockArgs: []any{mock.Anything, "order-1", mock.Anything},
			call: func(h *handler) (*mcp.CallToolResult, any, error) {
				return h.terminateWorkflow(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})
			},
			wantText: "Successfully requested termination of workflow instance 'order-1'.", wantKey: "requested",
			wantFail: `terminate workflow instance "order-1": boom`,
		},
		{
			name: "purge", method: "PurgeWorkflowState", mockArgs: []any{mock.Anything, "order-1", mock.Anything},
			call: func(h *handler) (*mcp.CallToolResult, any, error) {
				return h.purgeWorkflow(context.Background(), &mcp.CallToolRequest{}, InstanceArgs{InstanceID: "order-1"})
			},
			wantText: "Successfully purged the state of workflow instance 'order-1'.", wantKey: "purged",
			wantFail: `purge workflow instance "order-1": boom`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+" success", func(t *testing.T) {
			t.Parallel()
			h, client, _ := newTestHandler(t)
			client.On(tt.method, tt.mockArgs...).Return(nil)
			result, structured, err := tt.call(h)
			require.NoError(t, err)
			assert.False(t, result.IsError)
			assert.Contains(t, text(t, result), tt.wantText)
			s, ok := structured.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, map[string]any{"instance_id": "order-1", tt.wantKey: true}, s,
				"the state is not read back, so no status is reported")
		})
		t.Run(tt.name+" failure", func(t *testing.T) {
			t.Parallel()
			h, client, _ := newTestHandler(t)
			client.On(tt.method, tt.mockArgs...).Return(errors.New("boom"))
			result, _, err := tt.call(h)
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.Contains(t, text(t, result), tt.wantFail)
		})
	}
}

func TestMutatingToolsRequireInstanceID(t *testing.T) {
	t.Parallel()
	ctx, req := context.Background(), &mcp.CallToolRequest{}
	for _, id := range []string{"", "   "} {
		h, _, _ := newTestHandler(t) // the client must not be called
		calls := map[string]func() (*mcp.CallToolResult, any, error){
			"rerun": func() (*mcp.CallToolResult, any, error) {
				return h.rerunWorkflow(ctx, req, RerunWorkflowArgs{InstanceID: id, EventID: 1})
			},
			"pause": func() (*mcp.CallToolResult, any, error) {
				return h.pauseWorkflow(ctx, req, ReasonArgs{InstanceID: id})
			},
			"resume": func() (*mcp.CallToolResult, any, error) {
				return h.resumeWorkflow(ctx, req, ReasonArgs{InstanceID: id})
			},
			"terminate": func() (*mcp.CallToolResult, any, error) {
				return h.terminateWorkflow(ctx, req, InstanceArgs{InstanceID: id})
			},
			"raise": func() (*mcp.CallToolResult, any, error) {
				return h.raiseWorkflowEvent(ctx, req, RaiseWorkflowEventArgs{InstanceID: id, EventName: "approval"})
			},
			"purge": func() (*mcp.CallToolResult, any, error) {
				return h.purgeWorkflow(ctx, req, InstanceArgs{InstanceID: id})
			},
		}
		for name, call := range calls {
			result, _, err := call()
			require.NoError(t, err, name)
			assert.True(t, result.IsError, name)
			assert.Equal(t, "missing required argument: instanceID", text(t, result), "%s with instanceID %q", name, id)
		}
	}
}
