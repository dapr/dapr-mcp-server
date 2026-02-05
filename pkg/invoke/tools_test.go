package invoke

import (
	"context"
	"errors"
	"testing"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestInvokeServiceTool(t *testing.T) {
	tests := []struct {
		name        string
		args        InvokeServiceArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful invoke with JSON response",
			args: InvokeServiceArgs{
				AppID:    "order-service",
				Method:   "getOrder",
				Data:     `{"orderId": "123"}`,
				HTTPVerb: "POST",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "order-service", "getOrder", "POST", mock.AnythingOfType("*client.DataContent")).
					Return([]byte(`{"status": "completed"}`), nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service 'order-service' method 'getOrder'",
		},
		{
			name: "successful invoke with empty response",
			args: InvokeServiceArgs{
				AppID:    "notification-service",
				Method:   "notify",
				Data:     `{"message": "hello"}`,
				HTTPVerb: "POST",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "notification-service", "notify", "POST", mock.AnythingOfType("*client.DataContent")).
					Return([]byte{}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service 'notification-service' method 'notify'",
		},
		{
			name: "default HTTP verb to POST",
			args: InvokeServiceArgs{
				AppID:    "test-service",
				Method:   "test",
				Data:     `{}`,
				HTTPVerb: "", // Should default to POST
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "test-service", "test", "POST", mock.AnythingOfType("*client.DataContent")).
					Return([]byte(`{}`), nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service",
		},
		{
			name: "GET request",
			args: InvokeServiceArgs{
				AppID:    "status-service",
				Method:   "health",
				Data:     "",
				HTTPVerb: "GET",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "status-service", "health", "GET", mock.AnythingOfType("*client.DataContent")).
					Return([]byte(`{"healthy": true}`), nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service 'status-service' method 'health' (GET)",
		},
		{
			name: "invoke with metadata",
			args: InvokeServiceArgs{
				AppID:    "secure-service",
				Method:   "protected",
				Data:     `{}`,
				HTTPVerb: "POST",
				Metadata: map[string]string{"X-Custom-Header": "value"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "secure-service", "protected", "POST", mock.AnythingOfType("*client.DataContent")).
					Return([]byte(`{"success": true}`), nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service",
		},
		{
			name: "invoke failure - connection refused",
			args: InvokeServiceArgs{
				AppID:    "offline-service",
				Method:   "action",
				Data:     `{}`,
				HTTPVerb: "POST",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "offline-service", "action", "POST", mock.AnythingOfType("*client.DataContent")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: "failed to invoke service method",
		},
		{
			name: "invoke failure - service not found",
			args: InvokeServiceArgs{
				AppID:    "nonexistent-service",
				Method:   "method",
				Data:     `{}`,
				HTTPVerb: "POST",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "nonexistent-service", "method", "POST", mock.AnythingOfType("*client.DataContent")).
					Return(nil, errors.New("service not found"))
			},
			wantErr:     true,
			wantContent: "failed to invoke service method",
		},
		{
			name: "response with non-JSON data",
			args: InvokeServiceArgs{
				AppID:    "text-service",
				Method:   "text",
				Data:     `{}`,
				HTTPVerb: "POST",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeMethodWithContent", mock.Anything, "text-service", "text", "POST", mock.AnythingOfType("*client.DataContent")).
					Return([]byte("plain text response"), nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			// Replace the package-level client
			invokeClient = mockClient

			result, _, err := invokeServiceTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

			assert.NoError(t, err) // The function doesn't return errors, it returns them in result
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

	assert.Equal(t, mockClient, invokeClient)
}

// mockInvokeClient implements InvokeClient for testing
type mockInvokeClient struct {
	mock.Mock
}

func (m *mockInvokeClient) InvokeMethodWithContent(ctx context.Context, appID, methodName, verb string, content *client.DataContent) ([]byte, error) {
	args := m.Called(ctx, appID, methodName, verb, content)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func TestInvokeServiceToolWithInterfaceMock(t *testing.T) {
	mockInvoke := new(mockInvokeClient)
	mockInvoke.On("InvokeMethodWithContent", mock.Anything, "app", "method", "POST", mock.Anything).
		Return([]byte(`{"result": "ok"}`), nil)

	invokeClient = mockInvoke

	args := InvokeServiceArgs{
		AppID:    "app",
		Method:   "method",
		Data:     "{}",
		HTTPVerb: "POST",
	}

	result, structured, err := invokeServiceTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	mockInvoke.AssertExpectations(t)
}
