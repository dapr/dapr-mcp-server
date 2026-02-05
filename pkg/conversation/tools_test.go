package conversation

import (
	"context"
	"errors"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
				Temperature: 0.7,
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
				Temperature: 0.5,
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
				Name:        "llm",
				Prompt:      "Test",
				Temperature: 0.0, // Should default to 0.7
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
			wantContent: "dapr API error while conversing with LLM 'ollama'",
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
			wantContent: "dapr API error while conversing with LLM",
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
			wantContent: "LLM 'ollama' returned an empty outputs list",
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
			wantContent: "LLM 'ollama' returned no choices",
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

			daprClient = mockClient

			result, _, err := converseTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	daprClient = mockClient

	args := ConverseArgs{
		Name:   "test-llm",
		Prompt: "Test prompt",
	}

	result, structured, err := converseTool(context.Background(), &mcp.CallToolRequest{}, args)

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

	daprClient = mockClient

	args := ConverseArgs{
		Name:        "test-component",
		Prompt:      "Test",
		Temperature: 0.0, // Should default to 0.7
	}

	result, _, err := converseTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)

	mockClient.AssertExpectations(t)
}
