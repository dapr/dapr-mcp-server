package actors

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

func TestInvokeActorMethodTool(t *testing.T) {
	tests := []struct {
		name        string
		args        InvokeActorMethodArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful actor invocation",
			args: InvokeActorMethodArgs{
				ActorType: "payment-processor",
				ActorID:   "user-1001",
				Method:    "ProcessOrder",
				Data:      `{"orderId": "order-123", "amount": 99.99}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(&dapr.InvokeActorResponse{Data: []byte(`{"status": "processed"}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked method 'ProcessOrder' on actor type 'payment-processor'",
		},
		{
			name: "actor invocation with empty response",
			args: InvokeActorMethodArgs{
				ActorType: "notification-actor",
				ActorID:   "notifier-1",
				Method:    "SendNotification",
				Data:      `{"message": "Hello"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(&dapr.InvokeActorResponse{Data: []byte{}}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked method 'SendNotification'",
		},
		{
			name: "actor invocation with string response",
			args: InvokeActorMethodArgs{
				ActorType: "greeter",
				ActorID:   "greeter-1",
				Method:    "SayHello",
				Data:      `{"name": "World"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(&dapr.InvokeActorResponse{Data: []byte("Hello, World!")}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked method 'SayHello' on actor type 'greeter'",
		},
		{
			name: "actor invocation failure - actor not found",
			args: InvokeActorMethodArgs{
				ActorType: "nonexistent-actor",
				ActorID:   "id-123",
				Method:    "SomeMethod",
				Data:      `{}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(nil, errors.New("actor type not registered"))
			},
			wantErr:     true,
			wantContent: "dapr InvokeActor failed",
		},
		{
			name: "actor invocation failure - connection error",
			args: InvokeActorMethodArgs{
				ActorType: "order-processor",
				ActorID:   "processor-1",
				Method:    "ProcessOrder",
				Data:      `{"orderId": "123"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: "dapr InvokeActor failed",
		},
		{
			name: "actor invocation failure - method error",
			args: InvokeActorMethodArgs{
				ActorType: "calculator",
				ActorID:   "calc-1",
				Method:    "Divide",
				Data:      `{"numerator": 10, "denominator": 0}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(nil, errors.New("division by zero"))
			},
			wantErr:     true,
			wantContent: "dapr InvokeActor failed",
		},
		{
			name: "actor invocation with empty data",
			args: InvokeActorMethodArgs{
				ActorType: "stateful-actor",
				ActorID:   "state-1",
				Method:    "GetState",
				Data:      "",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
					Return(&dapr.InvokeActorResponse{Data: []byte(`{"count": 42}`)}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully invoked method 'GetState'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			actorClient = mockClient

			result, _, err := invokeActorMethodTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	assert.Equal(t, mockClient, actorClient)
}

// mockActorClient implements ActorClient for testing
type mockActorClient struct {
	mock.Mock
}

func (m *mockActorClient) InvokeActor(ctx context.Context, req *dapr.InvokeActorRequest) (*dapr.InvokeActorResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.InvokeActorResponse), args.Error(1)
}

func TestInvokeActorMethodToolWithInterfaceMock(t *testing.T) {
	mockActor := new(mockActorClient)
	mockActor.On("InvokeActor", mock.Anything, mock.AnythingOfType("*client.InvokeActorRequest")).
		Return(&dapr.InvokeActorResponse{Data: []byte(`{"result": "success"}`)}, nil)

	actorClient = mockActor

	args := InvokeActorMethodArgs{
		ActorType: "test-actor",
		ActorID:   "test-id",
		Method:    "TestMethod",
		Data:      `{"input": "test"}`,
	}

	result, structured, err := invokeActorMethodTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]string)
	assert.True(t, ok)
	assert.Equal(t, "test-actor", structuredMap["actor_type"])
	assert.Equal(t, "test-id", structuredMap["actor_id"])
	assert.Equal(t, "TestMethod", structuredMap["actor_method"])

	mockActor.AssertExpectations(t)
}

func TestInvokeActorMethodToolRequestContent(t *testing.T) {
	// Test that the request is properly constructed
	mockActor := new(mockActorClient)
	mockActor.On("InvokeActor", mock.Anything, mock.MatchedBy(func(req *dapr.InvokeActorRequest) bool {
		return req.ActorType == "test-type" &&
			req.ActorID == "test-id" &&
			req.Method == "test-method" &&
			string(req.Data) == `{"key": "value"}`
	})).Return(&dapr.InvokeActorResponse{Data: []byte(`{}`)}, nil)

	actorClient = mockActor

	args := InvokeActorMethodArgs{
		ActorType: "test-type",
		ActorID:   "test-id",
		Method:    "test-method",
		Data:      `{"key": "value"}`,
	}

	result, _, err := invokeActorMethodTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)

	mockActor.AssertExpectations(t)
}
