package invoke

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
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
			wantContent: "invoke method",
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
			wantContent: "invoke method",
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

			h, _ := newTestHandler(mockClient)

			result, _, err := h.invokeService(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	RegisterTools(server, mockClient, nil)
}

func newTestHandler(client InvokeClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

func TestInvokeServiceVerbAndContentType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		args            InvokeServiceArgs
		wantVerb        string
		wantContentType string
		wantErr         string
	}{
		{name: "default verb", args: InvokeServiceArgs{Data: `{}`}, wantVerb: DefaultHTTPVerb, wantContentType: toolkit.ContentTypeJSON},
		{name: "lowercase verb normalized", args: InvokeServiceArgs{HTTPVerb: " get "}, wantVerb: "GET", wantContentType: toolkit.ContentTypeText},
		{name: "plain text payload", args: InvokeServiceArgs{HTTPVerb: "put", Data: "hello"}, wantVerb: "PUT", wantContentType: toolkit.ContentTypeText},
		{name: "explicit content type wins", args: InvokeServiceArgs{Data: "a,b", ContentType: "text/csv"}, wantVerb: "POST", wantContentType: "text/csv"},
		{name: "unsupported verb", args: InvokeServiceArgs{HTTPVerb: "TRACE"}, wantErr: "unsupported httpVerb"},
		{name: "missing app and method", args: InvokeServiceArgs{}, wantErr: "appID, method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.wantErr == "" || tt.wantErr == "unsupported httpVerb" {
				tt.args.AppID, tt.args.Method = "app", "m"
			}
			mockClient := new(mocks.MockDaprClient)
			mockClient.On("InvokeMethodWithContent", mock.Anything, "app", "m", tt.wantVerb, mock.MatchedBy(func(c *client.DataContent) bool {
				return c.ContentType == tt.wantContentType
			})).Return([]byte(nil), nil).Maybe()

			h, _ := newTestHandler(mockClient)
			res, _, err := h.invokeService(context.Background(), nil, tt.args)
			require.NoError(t, err)
			if tt.wantErr != "" {
				require.True(t, res.IsError)
				assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, tt.wantErr)
				assert.Empty(t, mockClient.Calls)
				return
			}
			require.False(t, res.IsError)
			mockClient.AssertNumberOfCalls(t, "InvokeMethodWithContent", 1)
		})
	}
}

func TestInvokeServiceSendsHeadersAsOutgoingMetadata(t *testing.T) {
	t.Parallel()
	mockClient := new(mocks.MockDaprClient)
	mockClient.On("InvokeMethodWithContent", mock.MatchedBy(func(ctx context.Context) bool {
		md, ok := metadata.FromOutgoingContext(ctx)
		return ok && slices.Equal(md.Get("x-tenant"), []string{"acme"}) && slices.Equal(md.Get("authorization"), []string{"Bearer t"})
	}), "app", "m", "POST", mock.Anything).Return([]byte(`[1,2]`), nil)

	h, _ := newTestHandler(mockClient)
	res, structured, err := h.invokeService(context.Background(), nil, InvokeServiceArgs{
		AppID:    "app",
		Method:   "m",
		Metadata: map[string]string{"X-Tenant": "acme", "Authorization": "Bearer t"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, "[1,2]", structured.(map[string]any)["raw_response"])
	mockClient.AssertExpectations(t)
}

func TestInvokeServiceRejectsReservedHeaders(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"dapr-api-token", "Dapr-App-Id", "grpc-timeout", ":authority", " "} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			h, _ := newTestHandler(mockClient)

			res, _, err := h.invokeService(context.Background(), nil, InvokeServiceArgs{
				AppID:    "app",
				Method:   "m",
				Metadata: map[string]string{key: "v"},
			})
			require.NoError(t, err)
			require.True(t, res.IsError)
			mockClient.AssertNotCalled(t, "InvokeMethodWithContent")
		})
	}
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

	h, _ := newTestHandler(mockInvoke)

	args := InvokeServiceArgs{
		AppID:    "app",
		Method:   "method",
		Data:     "{}",
		HTTPVerb: "POST",
	}

	result, structured, err := h.invokeService(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	mockInvoke.AssertExpectations(t)
}
