package mocks

import (
	"context"

	wf "github.com/dapr/durabletask-go/workflow"
	"github.com/stretchr/testify/mock"
)

// MockWorkflowClient is a mock of the durabletask-go workflow client subset
// used by pkg/workflow. Variadic options are passed to Called as a single
// slice argument so tests can match or inspect them.
type MockWorkflowClient struct {
	mock.Mock
}

// ScheduleWorkflow mocks the ScheduleWorkflow method
func (m *MockWorkflowClient) ScheduleWorkflow(ctx context.Context, workflow string, opts ...wf.NewWorkflowOptions) (string, error) {
	args := m.Called(ctx, workflow, opts)
	return args.String(0), args.Error(1)
}

// FetchWorkflowMetadata mocks the FetchWorkflowMetadata method
func (m *MockWorkflowClient) FetchWorkflowMetadata(ctx context.Context, id string, opts ...wf.FetchWorkflowMetadataOptions) (*wf.WorkflowMetadata, error) {
	args := m.Called(ctx, id, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wf.WorkflowMetadata), args.Error(1)
}

// GetInstanceHistory mocks the GetInstanceHistory method
func (m *MockWorkflowClient) GetInstanceHistory(ctx context.Context, id string, opts ...wf.GetInstanceHistoryOptions) (*wf.GetInstanceHistoryResponse, error) {
	args := m.Called(ctx, id, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wf.GetInstanceHistoryResponse), args.Error(1)
}

// RerunWorkflowFromEvent mocks the RerunWorkflowFromEvent method
func (m *MockWorkflowClient) RerunWorkflowFromEvent(ctx context.Context, id string, eventID uint32, opts ...wf.RerunOptions) (string, error) {
	args := m.Called(ctx, id, eventID, opts)
	return args.String(0), args.Error(1)
}

// ListInstanceIDs mocks the ListInstanceIDs method
func (m *MockWorkflowClient) ListInstanceIDs(ctx context.Context, opts ...wf.ListInstanceIDsOptions) (*wf.ListInstanceIDsResponse, error) {
	args := m.Called(ctx, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wf.ListInstanceIDsResponse), args.Error(1)
}

// SuspendWorkflow mocks the SuspendWorkflow method
func (m *MockWorkflowClient) SuspendWorkflow(ctx context.Context, id, reason string) error {
	args := m.Called(ctx, id, reason)
	return args.Error(0)
}

// ResumeWorkflow mocks the ResumeWorkflow method
func (m *MockWorkflowClient) ResumeWorkflow(ctx context.Context, id, reason string) error {
	args := m.Called(ctx, id, reason)
	return args.Error(0)
}

// TerminateWorkflow mocks the TerminateWorkflow method
func (m *MockWorkflowClient) TerminateWorkflow(ctx context.Context, id string, opts ...wf.TerminateOptions) error {
	args := m.Called(ctx, id, opts)
	return args.Error(0)
}

// RaiseEvent mocks the RaiseEvent method
func (m *MockWorkflowClient) RaiseEvent(ctx context.Context, id, eventName string, opts ...wf.RaiseEventOptions) error {
	args := m.Called(ctx, id, eventName, opts)
	return args.Error(0)
}

// PurgeWorkflowState mocks the PurgeWorkflowState method
func (m *MockWorkflowClient) PurgeWorkflowState(ctx context.Context, id string, opts ...wf.PurgeOptions) error {
	args := m.Called(ctx, id, opts)
	return args.Error(0)
}
