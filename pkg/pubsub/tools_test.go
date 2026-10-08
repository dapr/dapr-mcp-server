package pubsub

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	pb "github.com/dapr/dapr/pkg/proto/runtime/v1"
	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func newTestHandler(client PubSubClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

// appliedRequest applies publish options to an empty request so tests can
// inspect what would be sent.
func appliedRequest(opts []dapr.PublishEventOption) *pb.PublishEventRequest {
	req := &pb.PublishEventRequest{}
	for _, opt := range opts {
		opt(req)
	}
	return req
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func TestPublishEventTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		args        PublishArgs
		publishErr  error
		wantCalled  bool
		wantErr     bool
		wantContent string
	}{
		{
			name:        "successful publish",
			args:        PublishArgs{PubsubName: "pubsub", Topic: "orders", Message: `{"orderId": "123"}`},
			wantCalled:  true,
			wantContent: "Successfully published message to topic 'orders' on pubsub component 'pubsub'",
		},
		{
			name:        "publish failure is an error result",
			args:        PublishArgs{PubsubName: "pubsub", Topic: "orders", Message: `{"orderId": "123"}`},
			publishErr:  errors.New("connection refused"),
			wantCalled:  true,
			wantErr:     true,
			wantContent: `publish to topic "orders" on pubsub "pubsub": connection refused`,
		},
		{
			name:        "empty message rejected",
			args:        PublishArgs{PubsubName: "pubsub", Topic: "events"},
			wantErr:     true,
			wantContent: "message",
		},
		{
			name:        "missing pubsub and topic",
			args:        PublishArgs{Message: "x"},
			wantErr:     true,
			wantContent: "pubsubName, topic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			if tt.wantCalled {
				mockClient.On("PublishEvent", mock.Anything, tt.args.PubsubName, tt.args.Topic, []byte(tt.args.Message), mock.Anything).
					Return(tt.publishErr)
			}
			h, _ := newTestHandler(mockClient)

			result, structured, err := h.publishEvent(context.Background(), &mcp.CallToolRequest{}, tt.args)

			require.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			assert.Contains(t, textOf(t, result), tt.wantContent)
			if !tt.wantErr {
				assert.Equal(t, statusPublished, structured.(map[string]any)["status"])
			}
			mockClient.AssertExpectations(t)
			if !tt.wantCalled {
				assert.Empty(t, mockClient.Calls)
			}
		})
	}
}

func TestPublishEventWithMetadataTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		args        PublishWithMetadataArgs
		publishErr  error
		wantErr     bool
		wantContent string
	}{
		{
			name:        "successful publish with metadata",
			args:        PublishWithMetadataArgs{PubsubName: "pubsub", Topic: "orders", Message: `{"orderId": "123"}`, Metadata: map[string]string{"ttlInSeconds": "60"}},
			wantContent: "Successfully published message with 1 metadata key(s)",
		},
		{
			name:        "successful publish with empty metadata",
			args:        PublishWithMetadataArgs{PubsubName: "pubsub", Topic: "orders", Message: `{"orderId": "456"}`, Metadata: map[string]string{}},
			wantContent: "Successfully published message with 0 metadata key(s)",
		},
		{
			name:        "publish with metadata failure",
			args:        PublishWithMetadataArgs{PubsubName: "pubsub", Topic: "orders", Message: `{}`, Metadata: map[string]string{"routing": "custom"}},
			publishErr:  errors.New("broker unavailable"),
			wantErr:     true,
			wantContent: `publish to topic "orders"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			mockClient.On("PublishEvent", mock.Anything, "pubsub", "orders", mock.Anything, mock.Anything).Return(tt.publishErr)
			h, _ := newTestHandler(mockClient)

			result, _, err := h.publishEventWithMetadata(context.Background(), &mcp.CallToolRequest{}, tt.args)

			require.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			assert.Contains(t, textOf(t, result), tt.wantContent)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestPublishOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		message         string
		contentType     string
		metadata        map[string]string
		wantContentType string
		wantMetadata    map[string]string
	}{
		{name: "json payload", message: `{"a":1}`, wantContentType: toolkit.ContentTypeJSON},
		{name: "text payload", message: "hello", wantContentType: toolkit.ContentTypeText},
		{name: "explicit content type", message: "<a/>", contentType: "application/xml", wantContentType: "application/xml"},
		{
			name:            "metadata passed unchanged",
			message:         "hello",
			metadata:        map[string]string{"ttlInSeconds": "60"},
			wantContentType: toolkit.ContentTypeText,
			wantMetadata:    map[string]string{"ttlInSeconds": "60"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			mockClient.On("PublishEvent", mock.Anything, "p", "t", mock.Anything, mock.MatchedBy(func(opts []dapr.PublishEventOption) bool {
				req := appliedRequest(opts)
				return req.GetDataContentType() == tt.wantContentType && assert.ObjectsAreEqual(tt.wantMetadata, req.GetMetadata())
			})).Return(nil)
			h, _ := newTestHandler(mockClient)

			res, _, err := h.publishEventWithMetadata(context.Background(), nil, PublishWithMetadataArgs{
				PubsubName: "p", Topic: "t", Message: tt.message, ContentType: tt.contentType, Metadata: tt.metadata,
			})
			require.NoError(t, err)
			require.False(t, res.IsError)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestRegisterTools(t *testing.T) {
	t.Parallel()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)
	RegisterTools(server, new(mocks.MockDaprClient), nil)
}
