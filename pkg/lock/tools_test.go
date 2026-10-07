package lock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestAcquireLockTool(t *testing.T) {
	tests := []struct {
		name        string
		args        AcquireLockArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful lock acquisition",
			args: AcquireLockArgs{
				StoreName:       "redis-lock",
				ResourceID:      "inventory-123",
				LockOwner:       "agent-1",
				ExpiryInSeconds: 30,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("TryLockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.LockRequest")).
					Return(&dapr.LockResponse{Success: true}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully **acquired** lock for resource **'inventory-123'**",
		},
		{
			name: "lock already held",
			args: AcquireLockArgs{
				StoreName:       "redis-lock",
				ResourceID:      "busy-resource",
				LockOwner:       "agent-2",
				ExpiryInSeconds: 60,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("TryLockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.LockRequest")).
					Return(&dapr.LockResponse{Success: false}, nil)
			},
			wantErr:     false,
			wantContent: "Failed to acquire lock for resource **'busy-resource'**",
		},
		{
			name: "lock acquisition with short expiry",
			args: AcquireLockArgs{
				StoreName:       "redis-lock",
				ResourceID:      "quick-task",
				LockOwner:       "agent-3",
				ExpiryInSeconds: 5,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("TryLockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.LockRequest")).
					Return(&dapr.LockResponse{Success: true}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully **acquired** lock",
		},
		{
			name: "lock API error",
			args: AcquireLockArgs{
				StoreName:       "redis-lock",
				ResourceID:      "resource",
				LockOwner:       "agent",
				ExpiryInSeconds: 30,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("TryLockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.LockRequest")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: `acquire lock on resource`,
		},
		{
			name: "lock store not found",
			args: AcquireLockArgs{
				StoreName:       "nonexistent-store",
				ResourceID:      "resource",
				LockOwner:       "agent",
				ExpiryInSeconds: 30,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("TryLockAlpha1", mock.Anything, "nonexistent-store", mock.AnythingOfType("*client.LockRequest")).
					Return(nil, errors.New("lock store not found"))
			},
			wantErr:     true,
			wantContent: "lock store not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.acquireLock(context.Background(), &mcp.CallToolRequest{}, tt.args)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			if len(result.Content) > 0 {
				textContent, ok := result.Content[0].(*mcp.TextContent)
				assert.True(t, ok)
				assert.Contains(t, textContent.Text, tt.wantContent)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestRegisterTools(t *testing.T) {
	mockClient := new(mocks.MockDaprClient)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)

	RegisterTools(server, mockClient, nil)
}

func newTestHandler(client LockClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

func unlockResponse(status pb.UnlockResponse_Status) *dapr.UnlockResponse {
	return &dapr.UnlockResponse{StatusCode: int32(status), Status: status.String()}
}

func TestReleaseLockTool(t *testing.T) {
	t.Parallel()
	validArgs := ReleaseLockArgs{StoreName: "redis-lock", ResourceID: "inventory-123", LockOwner: "agent-1"}
	tests := []struct {
		name        string
		args        ReleaseLockArgs
		resp        *dapr.UnlockResponse
		unlockErr   error
		callsSDK    bool
		wantErr     bool
		wantContent string
	}{
		{
			name:        "success",
			args:        validArgs,
			resp:        unlockResponse(pb.UnlockResponse_SUCCESS),
			callsSDK:    true,
			wantContent: "Released lock on resource 'inventory-123'",
		},
		{
			name:        "lock does not exist",
			args:        validArgs,
			resp:        unlockResponse(pb.UnlockResponse_LOCK_DOES_NOT_EXIST),
			callsSDK:    true,
			wantErr:     true,
			wantContent: "LOCK_DOES_NOT_EXIST: the lock does not exist",
		},
		{
			name:        "lock belongs to others",
			args:        validArgs,
			resp:        unlockResponse(pb.UnlockResponse_LOCK_BELONGS_TO_OTHERS),
			callsSDK:    true,
			wantErr:     true,
			wantContent: `held by a different owner and cannot be released by "agent-1"`,
		},
		{
			name:        "internal error",
			args:        validArgs,
			resp:        unlockResponse(pb.UnlockResponse_INTERNAL_ERROR),
			callsSDK:    true,
			wantErr:     true,
			wantContent: "INTERNAL_ERROR",
		},
		{
			name:        "unknown status",
			args:        validArgs,
			resp:        &dapr.UnlockResponse{StatusCode: 42, Status: "MYSTERY"},
			callsSDK:    true,
			wantErr:     true,
			wantContent: `unknown unlock status 42 "MYSTERY"`,
		},
		{
			name:        "nil response",
			args:        validArgs,
			callsSDK:    true,
			wantErr:     true,
			wantContent: "no response",
		},
		{
			name:        "api error",
			args:        validArgs,
			unlockErr:   errors.New("connection refused"),
			callsSDK:    true,
			wantErr:     true,
			wantContent: "connection refused",
		},
		{
			name:        "missing owner",
			args:        ReleaseLockArgs{StoreName: "s", ResourceID: "r"},
			wantErr:     true,
			wantContent: "lockOwner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			if tt.callsSDK {
				var ret any
				if tt.resp != nil {
					ret = tt.resp
				}
				mockClient.On("UnlockAlpha1", mock.Anything, "redis-lock", &dapr.UnlockRequest{
					ResourceID: tt.args.ResourceID,
					LockOwner:  tt.args.LockOwner,
				}).Return(ret, tt.unlockErr)
			}
			h, _ := newTestHandler(mockClient)

			result, _, err := h.releaseLock(context.Background(), nil, tt.args)

			require.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			assert.Contains(t, result.Content[0].(*mcp.TextContent).Text, tt.wantContent)
			mockClient.AssertExpectations(t)
			if !tt.callsSDK {
				assert.Empty(t, mockClient.Calls)
			}
		})
	}
}

func TestAcquireLockValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args AcquireLockArgs
		want string
	}{
		{name: "missing all", args: AcquireLockArgs{ExpiryInSeconds: 5}, want: "storeName, resourceID, lockOwner"},
		{name: "zero expiry", args: AcquireLockArgs{StoreName: "s", ResourceID: "r", LockOwner: "o"}, want: "expiryInSeconds must be greater than zero"},
		{name: "negative expiry", args: AcquireLockArgs{StoreName: "s", ResourceID: "r", LockOwner: "o", ExpiryInSeconds: -1}, want: "expiryInSeconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			h, _ := newTestHandler(mockClient)
			res, _, err := h.acquireLock(context.Background(), nil, tt.args)
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, tt.want)
			assert.Empty(t, mockClient.Calls)
		})
	}
}

func TestAcquireLockNilResponseAndDeadline(t *testing.T) {
	t.Parallel()
	mockClient := new(mocks.MockDaprClient)
	mockClient.On("TryLockAlpha1", mock.MatchedBy(func(ctx context.Context) bool {
		deadline, ok := ctx.Deadline()
		return ok && time.Until(deadline) <= RPCTimeout
	}), "s", mock.Anything).Return(nil, nil)

	h, _ := newTestHandler(mockClient)
	res, _, err := h.acquireLock(context.Background(), nil, AcquireLockArgs{StoreName: "s", ResourceID: "r", LockOwner: "o", ExpiryInSeconds: 5})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "no response")
	mockClient.AssertExpectations(t)
}

// mockLockClient implements LockClient for testing
type mockLockClient struct {
	mock.Mock
}

func (m *mockLockClient) TryLockAlpha1(ctx context.Context, storeName string, req *dapr.LockRequest) (*dapr.LockResponse, error) {
	args := m.Called(ctx, storeName, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.LockResponse), args.Error(1)
}

func (m *mockLockClient) UnlockAlpha1(ctx context.Context, storeName string, req *dapr.UnlockRequest) (*dapr.UnlockResponse, error) {
	args := m.Called(ctx, storeName, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.UnlockResponse), args.Error(1)
}

func TestAcquireLockToolWithInterfaceMock(t *testing.T) {
	mockLock := new(mockLockClient)
	mockLock.On("TryLockAlpha1", mock.Anything, "test-store", mock.AnythingOfType("*client.LockRequest")).
		Return(&dapr.LockResponse{Success: true}, nil)

	h, _ := newTestHandler(mockLock)

	args := AcquireLockArgs{
		StoreName:       "test-store",
		ResourceID:      "test-resource",
		LockOwner:       "test-owner",
		ExpiryInSeconds: 30,
	}

	result, structured, err := h.acquireLock(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, true, structuredMap["lock_acquired"])

	mockLock.AssertExpectations(t)
}
