package conversation

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
)

// mockConversationClient implements ConversationClient for testing.
type mockConversationClient struct {
	mock.Mock
}

func (m *mockConversationClient) ConverseAlpha2(ctx context.Context, req dapr.ConversationRequestAlpha2) (*dapr.ConversationResponseAlpha2, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.ConversationResponseAlpha2), args.Error(1)
}

func TestConverseTool(t *testing.T) {
	tests := []struct {
		name        string
		args        ConverseArgs
		setupMock   func(*mockConversationClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful conversation with message response",
			args: ConverseArgs{
				Name:        "ollama",
				Prompt:      "Hello, how are you?",
				ContextID:   "ctx-123",
				Temperature: ptr(0.7),
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{
										Message:      &dapr.ConversationResultMessageAlpha2{Content: "I'm doing well, thank you!"},
										FinishReason: "stop",
									},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "LLM Conversation completed successfully with component 'ollama'",
		},
		{
			name: "successful conversation without context ID (generates one)",
			args: ConverseArgs{
				Name:        "openai",
				Prompt:      "What is 2+2?",
				ContextID:   "",
				Temperature: ptr(0.5),
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{
										Message:      &dapr.ConversationResultMessageAlpha2{Content: "2+2 equals 4"},
										FinishReason: "stop",
									},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "LLM Conversation completed successfully",
		},
		{
			name: "successful conversation with default temperature",
			args: ConverseArgs{
				Name:   "llm",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{
										Message:      &dapr.ConversationResultMessageAlpha2{Content: "Response"},
										FinishReason: "stop",
									},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "LLM Conversation completed successfully",
		},
		{
			name: "conversation with tool calls",
			args: ConverseArgs{
				Name:   "gpt-4",
				Prompt: "Get the weather in New York",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{
										Message: &dapr.ConversationResultMessageAlpha2{
											ToolCalls: []*dapr.ConversationToolCallsAlpha2{
												{ID: "call_1"},
											},
										},
										FinishReason: "tool_calls",
									},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "TOOL CALL",
		},
		{
			name: "conversation failure - API error",
			args: ConverseArgs{
				Name:   "ollama",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: `converse with LLM component "ollama"`,
		},
		{
			name: "conversation failure - component not found",
			args: ConverseArgs{
				Name:   "nonexistent-llm",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(nil, errors.New("conversation component not found"))
			},
			wantErr:     true,
			wantContent: "converse with LLM component",
		},
		{
			name: "conversation failure - empty outputs",
			args: ConverseArgs{
				Name:   "ollama",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{},
					}, nil)
			},
			wantErr:     true,
			wantContent: `LLM component "ollama" returned an empty outputs list`,
		},
		{
			name: "conversation failure - empty choices",
			args: ConverseArgs{
				Name:   "ollama",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{},
							},
						},
					}, nil)
			},
			wantErr:     true,
			wantContent: `LLM component "ollama" returned no choices`,
		},
		{
			name: "conversation with multiple choices",
			args: ConverseArgs{
				Name:   "gpt",
				Prompt: "Give me options",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{Message: &dapr.ConversationResultMessageAlpha2{Content: "Option 1"}, FinishReason: "stop"},
									{Message: &dapr.ConversationResultMessageAlpha2{Content: "Option 2"}, FinishReason: "stop"},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "Choice 0",
		},
		{
			name: "conversation with nil message in choice",
			args: ConverseArgs{
				Name:   "llm",
				Prompt: "Test",
			},
			setupMock: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
					Return(&dapr.ConversationResponseAlpha2{
						Outputs: []*dapr.ConversationResultAlpha2{
							{
								Choices: []*dapr.ConversationResultChoicesAlpha2{
									{Message: nil, FinishReason: "stop"},
									{Message: &dapr.ConversationResultMessageAlpha2{Content: "Valid"}, FinishReason: "stop"},
								},
							},
						},
					}, nil)
			},
			wantErr:     false,
			wantContent: "LLM Conversation completed successfully",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mockConversationClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.converse(context.Background(), &mcp.CallToolRequest{}, tt.args)

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
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)

	// RegisterTools expects dapr.Client which we can't easily mock due to unexported types.
	// Just verify it doesn't panic with a nil client (edge case testing).
	// The real integration is tested via the converseTool tests.
	assert.NotPanics(t, func() {
		RegisterTools(server, nil, nil)
	})
}

func TestConverseToolStructuredResult(t *testing.T) {
	mockClient := new(mockConversationClient)
	mockClient.On("ConverseAlpha2", mock.Anything, mock.AnythingOfType("client.ConversationRequestAlpha2")).
		Return(&dapr.ConversationResponseAlpha2{
			Outputs: []*dapr.ConversationResultAlpha2{
				{
					Choices: []*dapr.ConversationResultChoicesAlpha2{
						{
							Message:      &dapr.ConversationResultMessageAlpha2{Content: "Test response"},
							FinishReason: "stop",
						},
					},
				},
			},
		}, nil)

	h, _ := newTestHandler(mockClient)

	args := ConverseArgs{
		Name:   "test-llm",
		Prompt: "Test prompt",
	}

	result, structured, err := h.converse(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	// Structured result should be a map from the JSON response
	assert.NotNil(t, structured)

	mockClient.AssertExpectations(t)
}

func TestConverseToolRequestConstruction(t *testing.T) {
	mockClient := new(mockConversationClient)
	mockClient.On("ConverseAlpha2", mock.Anything, mock.MatchedBy(func(req dapr.ConversationRequestAlpha2) bool {
		// Verify the request is properly constructed
		return req.Name == "test-component" &&
			req.Temperature != nil && *req.Temperature == 0.7 &&
			req.ScrubPII != nil && *req.ScrubPII == false &&
			len(req.Inputs) == 1
	})).Return(&dapr.ConversationResponseAlpha2{
		Outputs: []*dapr.ConversationResultAlpha2{
			{
				Choices: []*dapr.ConversationResultChoicesAlpha2{
					{Message: &dapr.ConversationResultMessageAlpha2{Content: "OK"}, FinishReason: "stop"},
				},
			},
		},
	}, nil)

	h, _ := newTestHandler(mockClient)

	args := ConverseArgs{
		Name:   "test-component",
		Prompt: "Test",
	}

	result, _, err := h.converse(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)

	mockClient.AssertExpectations(t)
}

func ptr[T any](v T) *T { return &v }

func newTestHandler(client ConversationClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}, newID: uuid.NewRandom}, &buf
}

func okResponse(contextID, content string) *dapr.ConversationResponseAlpha2 {
	return &dapr.ConversationResponseAlpha2{
		ContextID: contextID,
		Outputs: []*dapr.ConversationResultAlpha2{{
			Choices: []*dapr.ConversationResultChoicesAlpha2{{
				Message:      &dapr.ConversationResultMessageAlpha2{Content: content},
				FinishReason: "stop",
			}},
		}},
	}
}

func TestConverseTemperature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		temperature *float64
		want        float64
	}{
		{name: "unset uses default", temperature: nil, want: DefaultTemperature},
		{name: "zero is honored", temperature: ptr(0.0), want: 0.0},
		{name: "explicit value", temperature: ptr(0.3), want: 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mockConversationClient)
			mockClient.On("ConverseAlpha2", mock.Anything, mock.MatchedBy(func(req dapr.ConversationRequestAlpha2) bool {
				return req.Temperature != nil && *req.Temperature == tt.want &&
					req.Parameters == nil && req.Metadata == nil && req.Tools == nil
			})).Return(okResponse("", "ok"), nil)

			h, _ := newTestHandler(mockClient)
			res, _, err := h.converse(context.Background(), nil, ConverseArgs{Name: "llm", Prompt: "hi", Temperature: tt.temperature})
			require.NoError(t, err)
			assert.False(t, res.IsError)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestConverseContextID(t *testing.T) {
	t.Parallel()
	fixed := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	tests := []struct {
		name         string
		argContextID string
		respContext  string
		newID        func() (uuid.UUID, error)
		wantSent     string
		wantReturned string
		wantErr      bool
	}{
		{
			name:         "caller context is honored",
			argContextID: "ctx-1",
			newID:        func() (uuid.UUID, error) { return uuid.Nil, errors.New("must not be called") },
			wantSent:     "ctx-1",
			wantReturned: "ctx-1",
		},
		{
			name:         "generated when missing",
			newID:        func() (uuid.UUID, error) { return fixed, nil },
			wantSent:     fixed.String(),
			wantReturned: fixed.String(),
		},
		{
			name:         "sidecar context wins",
			argContextID: "ctx-1",
			respContext:  "ctx-from-dapr",
			newID:        uuid.NewRandom,
			wantSent:     "ctx-1",
			wantReturned: "ctx-from-dapr",
		},
		{
			name:    "uuid failure is an error",
			newID:   func() (uuid.UUID, error) { return uuid.Nil, errors.New("no entropy") },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mockConversationClient)
			if !tt.wantErr {
				mockClient.On("ConverseAlpha2", mock.Anything, mock.MatchedBy(func(req dapr.ConversationRequestAlpha2) bool {
					return req.ContextID != nil && *req.ContextID == tt.wantSent
				})).Return(okResponse(tt.respContext, "ok"), nil)
			}
			h, _ := newTestHandler(mockClient)
			h.newID = tt.newID

			res, structured, err := h.converse(context.Background(), nil, ConverseArgs{Name: "llm", Prompt: "hi", ContextID: tt.argContextID})
			require.NoError(t, err)
			if tt.wantErr {
				assert.True(t, res.IsError)
				assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "no entropy")
				mockClient.AssertNotCalled(t, "ConverseAlpha2")
				return
			}
			require.False(t, res.IsError)
			assert.Equal(t, tt.wantReturned, structured.(map[string]any)[resultKeyContextID])
			assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, tt.wantReturned)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestConverseValidationAndNilResponse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		args  ConverseArgs
		setup func(*mockConversationClient)
		want  string
	}{
		{name: "missing name and prompt", args: ConverseArgs{}, setup: func(*mockConversationClient) {}, want: "name, prompt"},
		{
			name: "nil response",
			args: ConverseArgs{Name: "llm", Prompt: "hi"},
			setup: func(m *mockConversationClient) {
				m.On("ConverseAlpha2", mock.Anything, mock.Anything).Return(nil, nil)
			},
			want: "empty outputs",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mockConversationClient)
			tt.setup(mockClient)
			h, _ := newTestHandler(mockClient)
			res, _, err := h.converse(context.Background(), nil, tt.args)
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, tt.want)
		})
	}
}

func TestConverseResponseNotLogged(t *testing.T) {
	t.Parallel()
	const reply = "private-llm-reply-text"
	mockClient := new(mockConversationClient)
	mockClient.On("ConverseAlpha2", mock.Anything, mock.Anything).Return(okResponse("", reply), nil)

	h, logs := newTestHandler(mockClient)
	res, _, err := h.converse(context.Background(), nil, ConverseArgs{Name: "llm", Prompt: "secret prompt"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, reply)
	assert.NotContains(t, logs.String(), reply)
	assert.NotContains(t, logs.String(), "secret prompt")
}
