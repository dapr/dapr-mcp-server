package pubsub

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

func TestPublishEventTool(t *testing.T) {
	tests := []struct {
		name        string
		args        PublishArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful publish",
			args: PublishArgs{
				PubsubName: "pubsub",
				Topic:      "orders",
				Message:    `{"orderId": "123"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published message to topic 'orders' on pubsub component 'pubsub'",
		},
		{
			name: "successful publish to different topic",
			args: PublishArgs{
				PubsubName: "kafka-pubsub",
				Topic:      "notifications",
				Message:    `{"type": "alert", "message": "hello"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "kafka-pubsub", "notifications", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published message to topic 'notifications' on pubsub component 'kafka-pubsub'",
		},
		{
			name: "publish failure - connection error",
			args: PublishArgs{
				PubsubName: "pubsub",
				Topic:      "orders",
				Message:    `{"orderId": "123"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).
					Return(errors.New("connection refused"))
			},
			wantErr:     false, // Note: this function returns error in content, not IsError
			wantContent: "Failed to publish event",
		},
		{
			name: "publish failure - pubsub not found",
			args: PublishArgs{
				PubsubName: "nonexistent",
				Topic:      "topic",
				Message:    `{}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "nonexistent", "topic", mock.Anything, mock.Anything).
					Return(errors.New("pubsub component not found"))
			},
			wantErr:     false,
			wantContent: "Failed to publish event",
		},
		{
			name: "publish empty message",
			args: PublishArgs{
				PubsubName: "pubsub",
				Topic:      "events",
				Message:    "",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "events", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			pubsubClient = mockClient

			result, _, err := publishEventTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

			assert.NoError(t, err)
			if len(result.Content) > 0 {
				textContent, ok := result.Content[0].(*mcp.TextContent)
				assert.True(t, ok)
				assert.Contains(t, textContent.Text, tt.wantContent)
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestPublishEventWithMetadataTool(t *testing.T) {
	tests := []struct {
		name        string
		args        PublishWithMetadataArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful publish with metadata",
			args: PublishWithMetadataArgs{
				PubsubName: "pubsub",
				Topic:      "orders",
				Message:    `{"orderId": "123"}`,
				Metadata:   map[string]string{"ttlInSeconds": "60"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published message with 1 metadata key(s)",
		},
		{
			name: "successful publish with multiple metadata",
			args: PublishWithMetadataArgs{
				PubsubName: "pubsub",
				Topic:      "events",
				Message:    `{"type": "test"}`,
				Metadata:   map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "events", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published message with 3 metadata key(s)",
		},
		{
			name: "successful publish with empty metadata",
			args: PublishWithMetadataArgs{
				PubsubName: "pubsub",
				Topic:      "orders",
				Message:    `{"orderId": "456"}`,
				Metadata:   map[string]string{},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully published message with 0 metadata key(s)",
		},
		{
			name: "publish with metadata failure",
			args: PublishWithMetadataArgs{
				PubsubName: "pubsub",
				Topic:      "orders",
				Message:    `{}`,
				Metadata:   map[string]string{"routing": "custom"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).
					Return(errors.New("broker unavailable"))
			},
			wantErr:     true,
			wantContent: "failed to publish event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			pubsubClient = mockClient

			result, _, err := publishEventWithMetadataTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	assert.Equal(t, mockClient, pubsubClient)
}

// mockPubSubClient implements PubSubClient for testing
type mockPubSubClient struct {
	mock.Mock
}

func (m *mockPubSubClient) PublishEvent(ctx context.Context, pubsubName, topicName string, data interface{}, opts ...dapr.PublishEventOption) error {
	args := m.Called(ctx, pubsubName, topicName, data, opts)
	return args.Error(0)
}

func TestPublishEventToolWithInterfaceMock(t *testing.T) {
	mockPubsub := new(mockPubSubClient)
	mockPubsub.On("PublishEvent", mock.Anything, "test-pubsub", "test-topic", mock.Anything, mock.Anything).
		Return(nil)

	pubsubClient = mockPubsub

	args := PublishArgs{
		PubsubName: "test-pubsub",
		Topic:      "test-topic",
		Message:    `{"test": true}`,
	}

	result, structured, err := publishEventTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "published", structuredMap["status"])

	mockPubsub.AssertExpectations(t)
}
