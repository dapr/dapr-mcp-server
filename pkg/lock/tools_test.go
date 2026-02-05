package lock

import (
	"context"
	"errors"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

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
			wantContent: "dapr API error while trying to acquire lock",
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
			wantContent: "dapr API error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			lockClient = mockClient

			result, _, err := acquireLockTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestReleaseLockTool(t *testing.T) {
	tests := []struct {
		name        string
		args        ReleaseLockArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful lock release",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "inventory-123",
				LockOwner:  "agent-1",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(&dapr.UnlockResponse{Status: "SUCCESS"}, nil)
			},
			wantErr:     false,
			wantContent: "SUCCESS: The lock was successfully released",
		},
		{
			name: "lock does not exist",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "nonexistent-resource",
				LockOwner:  "agent-1",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(&dapr.UnlockResponse{Status: "LOCK_UNEXIST"}, nil)
			},
			wantErr:     false,
			wantContent: "LOCK_UNEXIST: The lock specified by ResourceID does not exist",
		},
		{
			name: "lock belongs to others",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "shared-resource",
				LockOwner:  "wrong-agent",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(&dapr.UnlockResponse{Status: "LOCK_BELONG_TO_OTHERS"}, nil)
			},
			wantErr:     false,
			wantContent: "LOCK_BELONG_TO_OTHERS: The lock is held by a different owner",
		},
		{
			name: "internal error",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "resource",
				LockOwner:  "agent",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(&dapr.UnlockResponse{Status: "INTERNAL_ERROR"}, nil)
			},
			wantErr:     false,
			wantContent: "INTERNAL_ERROR: An internal error occurred",
		},
		{
			name: "unknown status",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "resource",
				LockOwner:  "agent",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(&dapr.UnlockResponse{Status: "UNEXPECTED_STATUS"}, nil)
			},
			wantErr:     false,
			wantContent: "UNKNOWN_STATUS: UNEXPECTED_STATUS",
		},
		{
			name: "unlock API error",
			args: ReleaseLockArgs{
				StoreName:  "redis-lock",
				ResourceID: "resource",
				LockOwner:  "agent",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("UnlockAlpha1", mock.Anything, "redis-lock", mock.AnythingOfType("*client.UnlockRequest")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: "dapr API error while trying to release lock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			lockClient = mockClient

			result, _, err := releaseLockTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	// Should not panic
	RegisterTools(server, mockClient, nil)

	assert.Equal(t, mockClient, lockClient)
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

	lockClient = mockLock

	args := AcquireLockArgs{
		StoreName:       "test-store",
		ResourceID:      "test-resource",
		LockOwner:       "test-owner",
		ExpiryInSeconds: 30,
	}

	result, structured, err := acquireLockTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, true, structuredMap["lock_acquired"])

	mockLock.AssertExpectations(t)
}
