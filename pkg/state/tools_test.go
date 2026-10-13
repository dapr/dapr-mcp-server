package state

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestSaveStateTool(t *testing.T) {
	tests := []struct {
		name        string
		args        SaveStateArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful save",
			args: SaveStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
				Value:     `{"data": "test"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("SaveState", mock.Anything, "statestore", "test-key", []byte(`{"data": "test"}`), mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully saved key 'test-key' to state store 'statestore'.",
		},
		{
			name: "save failure",
			args: SaveStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
				Value:     `{"data": "test"}`,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("SaveState", mock.Anything, "statestore", "test-key", []byte(`{"data": "test"}`), mock.Anything, mock.Anything).
					Return(errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: `save key "test-key" to state store "statestore"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.saveState(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestGetStateTool(t *testing.T) {
	tests := []struct {
		name        string
		args        GetStateArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful get with value",
			args: GetStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetState", mock.Anything, "statestore", "test-key", mock.Anything).
					Return(&client.StateItem{
						Key:   "test-key",
						Value: []byte(`{"data": "test"}`),
					}, nil)
			},
			wantErr:     false,
			wantContent: "Retrieved key 'test-key' from 'statestore'",
		},
		{
			name: "key not found",
			args: GetStateArgs{
				StoreName: "statestore",
				Key:       "nonexistent-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetState", mock.Anything, "statestore", "nonexistent-key", mock.Anything).
					Return(&client.StateItem{
						Key:   "nonexistent-key",
						Value: []byte{},
					}, nil)
			},
			wantErr:     false,
			wantContent: "Key 'nonexistent-key' not found",
		},
		{
			name: "get failure",
			args: GetStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetState", mock.Anything, "statestore", "test-key", mock.Anything).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: `get key "test-key" from state store "statestore"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.getState(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestDeleteStateTool(t *testing.T) {
	tests := []struct {
		name        string
		args        DeleteStateArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful delete",
			args: DeleteStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("DeleteState", mock.Anything, "statestore", "test-key", mock.Anything, mock.Anything).
					Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully deleted key 'test-key'",
		},
		{
			name: "delete failure",
			args: DeleteStateArgs{
				StoreName: "statestore",
				Key:       "test-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("DeleteState", mock.Anything, "statestore", "test-key", mock.Anything, mock.Anything).
					Return(errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: `delete key "test-key"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.deleteState(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestExecuteTransactionTool(t *testing.T) {
	tests := []struct {
		name        string
		args        ExecuteTransactionArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful transaction with save and delete",
			args: ExecuteTransactionArgs{
				StoreName: "statestore",
				Items: []TransactionItem{
					{Key: "key1", Value: "value1", IsDelete: false},
					{Key: "key2", Value: "", IsDelete: true},
				},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("ExecuteStateTransaction", mock.Anything, "statestore", mock.Anything, mock.MatchedBy(func(ops []*client.StateOperation) bool {
					return len(ops) == 2
				})).Return(nil)
			},
			wantErr:     false,
			wantContent: "Successfully executed 2 state operations",
		},
		{
			name: "transaction failure",
			args: ExecuteTransactionArgs{
				StoreName: "statestore",
				Items: []TransactionItem{
					{Key: "key1", Value: "value1", IsDelete: false},
				},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("ExecuteStateTransaction", mock.Anything, "statestore", mock.Anything, mock.Anything).
					Return(errors.New("transaction failed"))
			},
			wantErr:     true,
			wantContent: `execute transaction on state store "statestore"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.executeTransaction(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func newTestHandler(client StateClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

func TestGetStateNilItemAndValueNotLogged(t *testing.T) {
	t.Parallel()
	const value = "sensitive-state-value"
	tests := []struct {
		name      string
		item      *client.StateItem
		wantFound bool
	}{
		{name: "nil item", item: nil, wantFound: false},
		{name: "value present", item: &client.StateItem{Key: "k", Value: []byte(value)}, wantFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			if tt.item == nil {
				mockClient.On("GetState", mock.Anything, "store", "k", mock.Anything).Return(nil, nil)
			} else {
				mockClient.On("GetState", mock.Anything, "store", "k", mock.Anything).Return(tt.item, nil)
			}
			h, logs := newTestHandler(mockClient)

			res, structured, err := h.getState(context.Background(), nil, GetStateArgs{StoreName: "store", Key: "k"})
			require.NoError(t, err)
			require.False(t, res.IsError)
			assert.Equal(t, tt.wantFound, structured.(map[string]any)["found"])
			assert.NotContains(t, logs.String(), value)
		})
	}
}

func TestStateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*handler) *mcp.CallToolResult
		want string
	}{
		{
			name: "save_state missing all",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.saveState(context.Background(), nil, SaveStateArgs{})
				return res
			},
			want: "storeName, key, value",
		},
		{
			name: "get_state missing key",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.getState(context.Background(), nil, GetStateArgs{StoreName: "s"})
				return res
			},
			want: "key",
		},
		{
			name: "delete_state missing store",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.deleteState(context.Background(), nil, DeleteStateArgs{Key: "k"})
				return res
			},
			want: "storeName",
		},
		{
			name: "execute_transaction no items",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.executeTransaction(context.Background(), nil, ExecuteTransactionArgs{StoreName: "s"})
				return res
			},
			want: "items",
		},
		{
			name: "execute_transaction item without key",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.executeTransaction(context.Background(), nil, ExecuteTransactionArgs{
					StoreName: "s",
					Items:     []TransactionItem{{Key: "a"}, {Value: "v"}},
				})
				return res
			},
			want: "items[1].key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			h, _ := newTestHandler(mockClient)
			res := tt.call(h)
			require.True(t, res.IsError)
			assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, tt.want)
			assert.Empty(t, mockClient.Calls)
		})
	}
}

func TestExecuteTransactionDoesNotSendTraceMetadata(t *testing.T) {
	t.Parallel()
	mockClient := new(mocks.MockDaprClient)
	mockClient.On("ExecuteStateTransaction", mock.Anything, "s", map[string]string(nil), mock.MatchedBy(func(ops []*client.StateOperation) bool {
		return len(ops) == 2 &&
			ops[0].Type == client.StateOperationTypeUpsert && string(ops[0].Item.Value) == "v" &&
			ops[1].Type == client.StateOperationTypeDelete && ops[1].Item.Value == nil
	})).Return(nil)

	h, _ := newTestHandler(mockClient)
	res, _, err := h.executeTransaction(context.Background(), nil, ExecuteTransactionArgs{
		StoreName: "s",
		Items:     []TransactionItem{{Key: "a", Value: "v"}, {Key: "b", Value: "ignored", IsDelete: true}},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	mockClient.AssertExpectations(t)
}

func TestGetBulkStateTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		args        GetBulkStateArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent []string
		// wantItems is the expected structured "items"; nil when the call fails.
		wantItems []bulkStateEntry
	}{
		{
			name: "returns every key with its value in the text",
			args: GetBulkStateArgs{StoreName: "statestore", Keys: []string{"a", "b", "c"}},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkState", mock.Anything, "statestore", []string{"a", "b", "c"}, mock.Anything, int32(0)).
					Return([]*client.BulkStateItem{
						{Key: "a", Value: []byte(`v1`), Etag: "1"},
						{Key: "b", Value: []byte(`v2`), Etag: "2"},
						{Key: "c"},
					}, nil)
			},
			wantContent: []string{
				"Read 3 key(s) from state store 'statestore' (2 found):",
				`"key": "a"`, `"found": true`, `"etag": "1"`, `"value": "v2"`, `"etag": "2"`,
				`"key": "c"`, `"found": false`,
			},
			wantItems: []bulkStateEntry{
				{Key: "a", Found: true, Value: "v1", Etag: "1"},
				{Key: "b", Found: true, Value: "v2", Etag: "2"},
				{Key: "c", Found: false},
			},
		},
		{
			name: "per-key error is surfaced without failing the call",
			args: GetBulkStateArgs{StoreName: "statestore", Keys: []string{"a"}},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkState", mock.Anything, "statestore", []string{"a"}, mock.Anything, int32(0)).
					Return([]*client.BulkStateItem{{Key: "a", Error: "key not found"}}, nil)
			},
			wantContent: []string{
				"Read 1 key(s) from state store 'statestore' (0 found):",
				`"found": false`, `"error": "key not found"`,
			},
			wantItems: []bulkStateEntry{{Key: "a", Found: false, Error: "key not found"}},
		},
		{
			name: "dapr failure",
			args: GetBulkStateArgs{StoreName: "statestore", Keys: []string{"a"}},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkState", mock.Anything, "statestore", []string{"a"}, mock.Anything, int32(0)).
					Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: []string{`get 1 key(s) from state store "statestore"`, "connection refused"},
		},
		{
			name:        "missing storeName",
			args:        GetBulkStateArgs{Keys: []string{"a"}},
			setupMock:   func(*mocks.MockDaprClient) {},
			wantErr:     true,
			wantContent: []string{"missing required argument: storeName"},
		},
		{
			name:        "empty keys",
			args:        GetBulkStateArgs{StoreName: "statestore"},
			setupMock:   func(*mocks.MockDaprClient) {},
			wantErr:     true,
			wantContent: []string{"missing required argument: keys"},
		},
		{
			name:        "blank key",
			args:        GetBulkStateArgs{StoreName: "statestore", Keys: []string{"a", " "}},
			setupMock:   func(*mocks.MockDaprClient) {},
			wantErr:     true,
			wantContent: []string{"missing required argument: keys[1]"},
		},
		{
			name:        "negative parallelism",
			args:        GetBulkStateArgs{StoreName: "statestore", Keys: []string{"a"}, Parallelism: -1},
			setupMock:   func(*mocks.MockDaprClient) {},
			wantErr:     true,
			wantContent: []string{"parallelism must not be negative"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)
			h, _ := newTestHandler(mockClient)

			result, structured, err := h.getBulkState(context.Background(), &mcp.CallToolRequest{}, tt.args)

			require.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			text, ok := result.Content[0].(*mcp.TextContent)
			require.True(t, ok)
			for _, want := range tt.wantContent {
				assert.Contains(t, text.Text, want)
			}
			if tt.wantItems == nil {
				assert.Nil(t, structured, "a failed call has no structured result")
			} else {
				assert.Equal(t, map[string]any{"store_name": "statestore", "items": tt.wantItems}, structured)
			}
			mockClient.AssertExpectations(t)
		})
	}
}

func TestGetBulkStateValuesNotLogged(t *testing.T) {
	t.Parallel()
	mockClient := new(mocks.MockDaprClient)
	mockClient.On("GetBulkState", mock.Anything, "store", []string{"k"}, mock.Anything, int32(0)).
		Return([]*client.BulkStateItem{{Key: "k", Value: []byte("sensitive-state-value")}}, nil)
	h, logs := newTestHandler(mockClient)

	_, _, err := h.getBulkState(context.Background(), &mcp.CallToolRequest{}, GetBulkStateArgs{StoreName: "store", Keys: []string{"k"}})

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "sensitive-state-value")
}
