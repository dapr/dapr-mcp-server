package bindings

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

func TestInvokeOutputBindingTool(t *testing.T) {
	tests := []struct {
		name        string
		args        InvokeBindingArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful binding invocation with response",
			args: InvokeBindingArgs{
				BindingName: "storage-binding",
				Operation:   "create",
				Data:        `{"content": "file content"}`,
				Metadata:    map[string]string{"key": "myfile.txt"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte(`{"success": true}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked output binding 'storage-binding' with operation 'create'",
		},
		{
			name: "successful binding invocation without response",
			args: InvokeBindingArgs{
				BindingName: "email-binding",
				Operation:   "send",
				Data:        `{"to": "user@example.com", "body": "Hello"}`,
				Metadata:    nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte{}}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked output binding 'email-binding' with operation 'send'",
		},
		{
			name: "successful binding invocation with empty data",
			args: InvokeBindingArgs{
				BindingName: "queue-binding",
				Operation:   "get",
				Data:        "",
				Metadata:    map[string]string{"queue": "myqueue"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte(`{"message": "queued message"}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked output binding 'queue-binding'",
		},
		{
			name: "binding invocation with JSON response",
			args: InvokeBindingArgs{
				BindingName: "http-binding",
				Operation:   "get",
				Data:        "",
				Metadata:    map[string]string{"url": "https://api.example.com"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte(`{"id": 1, "name": "test"}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Response Data:",
		},
		{
			name: "binding invocation with non-JSON response",
			args: InvokeBindingArgs{
				BindingName: "raw-binding",
				Operation:   "read",
				Data:        "",
				Metadata:    nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte("raw text data")}, nil)
			},
			wantErr:     false,
			wantContent: "Response Data (Raw):",
		},
		{
			name: "binding invocation failure - binding not found",
			args: InvokeBindingArgs{
				BindingName: "nonexistent-binding",
				Operation:   "create",
				Data:        `{}`,
				Metadata:    nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(nil, errors.New("binding not found"))
			},
			wantErr:     true,
			wantContent: "Failed to invoke binding 'nonexistent-binding' with operation 'create'",
		},
		{
			name: "binding invocation failure - connection error",
			args: InvokeBindingArgs{
				BindingName: "blob-binding",
				Operation:   "upload",
				Data:        `{"data": "content"}`,
				Metadata:    nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: "Failed to invoke binding",
		},
		{
			name: "binding invocation failure - invalid operation",
			args: InvokeBindingArgs{
				BindingName: "storage-binding",
				Operation:   "invalid-op",
				Data:        `{}`,
				Metadata:    nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(nil, errors.New("operation not supported"))
			},
			wantErr:     true,
			wantContent: "Failed to invoke binding 'storage-binding' with operation 'invalid-op'",
		},
		{
			name: "binding invocation with multiple metadata",
			args: InvokeBindingArgs{
				BindingName: "s3-binding",
				Operation:   "create",
				Data:        `{"content": "file data"}`,
				Metadata:    map[string]string{"key": "path/to/file.txt", "contentType": "text/plain"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
					Return(&dapr.BindingEvent{Data: []byte(`{"etag": "abc123"}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked output binding 's3-binding'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			bindingsClient = mockClient

			result, _, err := invokeOutputBindingTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	assert.Equal(t, mockClient, bindingsClient)
}

// mockBindingsClient implements BindingsClient for testing
type mockBindingsClient struct {
	mock.Mock
}

func (m *mockBindingsClient) InvokeBinding(ctx context.Context, in *dapr.InvokeBindingRequest) (*dapr.BindingEvent, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.BindingEvent), args.Error(1)
}

func TestInvokeOutputBindingToolWithInterfaceMock(t *testing.T) {
	mockBinding := new(mockBindingsClient)
	mockBinding.On("InvokeBinding", mock.Anything, mock.AnythingOfType("*client.InvokeBindingRequest")).
		Return(&dapr.BindingEvent{Data: []byte(`{"result": "ok"}`)}, nil)

	bindingsClient = mockBinding

	args := InvokeBindingArgs{
		BindingName: "test-binding",
		Operation:   "test-op",
		Data:        `{"test": true}`,
		Metadata:    map[string]string{"key": "value"},
	}

	result, structured, err := invokeOutputBindingTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]string)
	assert.True(t, ok)
	assert.Equal(t, "test-binding", structuredMap["binding_name"])
	assert.Equal(t, "test-op", structuredMap["operation"])

	mockBinding.AssertExpectations(t)
}

func TestInvokeOutputBindingToolRequestContent(t *testing.T) {
	// Test that empty data results in nil
	mockBinding := new(mockBindingsClient)
	mockBinding.On("InvokeBinding", mock.Anything, mock.MatchedBy(func(req *dapr.InvokeBindingRequest) bool {
		return req.Name == "test-binding" &&
			req.Operation == "get" &&
			req.Data == nil
	})).Return(&dapr.BindingEvent{Data: []byte{}}, nil)

	bindingsClient = mockBinding

	args := InvokeBindingArgs{
		BindingName: "test-binding",
		Operation:   "get",
		Data:        "",
		Metadata:    nil,
	}

	result, _, err := invokeOutputBindingTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)

	mockBinding.AssertExpectations(t)
}
