package secrets

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestGetSecretTool(t *testing.T) {
	tests := []struct {
		name        string
		args        GetSecretArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful get secret",
			args: GetSecretArgs{
				StoreName:  "vault",
				SecretName: "db-password",
				Metadata:   nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "vault", "db-password", mock.Anything).
					Return(map[string]string{"db-password": "secret123"}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved secret 'db-password' from store 'vault'",
		},
		{
			name: "get secret with multiple keys",
			args: GetSecretArgs{
				StoreName:  "kubernetes",
				SecretName: "api-keys",
				Metadata:   nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "kubernetes", "api-keys", mock.Anything).
					Return(map[string]string{"key1": "value1", "key2": "value2"}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved secret 'api-keys'",
		},
		{
			name: "get secret with metadata",
			args: GetSecretArgs{
				StoreName:  "vault",
				SecretName: "versioned-secret",
				Metadata:   map[string]string{"version_id": "2"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "vault", "versioned-secret", mock.Anything).
					Return(map[string]string{"versioned-secret": "v2-value"}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved secret 'versioned-secret'",
		},
		{
			name: "get secret failure - not found",
			args: GetSecretArgs{
				StoreName:  "vault",
				SecretName: "nonexistent",
				Metadata:   nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "vault", "nonexistent", mock.Anything).
					Return(nil, errors.New("secret not found"))
			},
			wantErr:     true,
			wantContent: `get secret "nonexistent" from store "vault"`,
		},
		{
			name: "get secret failure - store not found",
			args: GetSecretArgs{
				StoreName:  "nonexistent-store",
				SecretName: "secret",
				Metadata:   nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "nonexistent-store", "secret", mock.Anything).
					Return(nil, errors.New("secret store not found"))
			},
			wantErr:     true,
			wantContent: "get secret",
		},
		{
			name: "get secret failure - access denied",
			args: GetSecretArgs{
				StoreName:  "vault",
				SecretName: "restricted",
				Metadata:   nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetSecret", mock.Anything, "vault", "restricted", mock.Anything).
					Return(nil, errors.New("access denied"))
			},
			wantErr:     true,
			wantContent: `get secret "restricted"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.getSecret(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestGetBulkSecretTool(t *testing.T) {
	tests := []struct {
		name        string
		args        GetBulkSecretArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful get bulk secrets",
			args: GetBulkSecretArgs{
				StoreName: "vault",
				Metadata:  nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkSecret", mock.Anything, "vault", mock.Anything).
					Return(map[string]map[string]string{
						"secret1": {"key": "value1"},
						"secret2": {"key": "value2"},
					}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved 2 secret(s) in bulk",
		},
		{
			name: "get bulk secrets - empty store",
			args: GetBulkSecretArgs{
				StoreName: "empty-store",
				Metadata:  nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkSecret", mock.Anything, "empty-store", mock.Anything).
					Return(map[string]map[string]string{}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved 0 secret(s)",
		},
		{
			name: "get bulk secrets with metadata",
			args: GetBulkSecretArgs{
				StoreName: "vault",
				Metadata:  map[string]string{"namespace": "production"},
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkSecret", mock.Anything, "vault", mock.Anything).
					Return(map[string]map[string]string{
						"prod-secret": {"value": "production-value"},
					}, nil)
			},
			wantErr:     false,
			wantContent: "Successfully retrieved 1 secret(s)",
		},
		{
			name: "get bulk secrets failure",
			args: GetBulkSecretArgs{
				StoreName: "vault",
				Metadata:  nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkSecret", mock.Anything, "vault", mock.Anything).
					Return(nil, errors.New("connection timeout"))
			},
			wantErr:     true,
			wantContent: `get bulk secrets from store "vault"`,
		},
		{
			name: "get bulk secrets - store not found",
			args: GetBulkSecretArgs{
				StoreName: "nonexistent",
				Metadata:  nil,
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetBulkSecret", mock.Anything, "nonexistent", mock.Anything).
					Return(nil, errors.New("secret store not found"))
			},
			wantErr:     true,
			wantContent: "get bulk secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			h, _ := newTestHandler(mockClient)

			result, _, err := h.getBulkSecrets(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func newTestHandler(client SecretsClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

func TestSecretValuesNeverLogged(t *testing.T) {
	t.Parallel()
	const secretValue = "s3cr3t-value-that-must-not-leak"

	mockClient := new(mocks.MockDaprClient)
	mockClient.On("GetSecret", mock.Anything, "vault", "db", mock.Anything).
		Return(map[string]string{"password": secretValue}, nil)
	mockClient.On("GetBulkSecret", mock.Anything, "vault", mock.Anything).
		Return(map[string]map[string]string{"db": {"password": secretValue}}, nil)

	h, logs := newTestHandler(mockClient)

	res, structured, err := h.getSecret(context.Background(), &mcp.CallToolRequest{}, GetSecretArgs{StoreName: "vault", SecretName: "db"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, secretValue, structured["password"])

	res, bulk, err := h.getBulkSecrets(context.Background(), &mcp.CallToolRequest{}, GetBulkSecretArgs{StoreName: "vault"})
	require.NoError(t, err)
	require.False(t, res.IsError)
	assert.Equal(t, secretValue, bulk["db"]["password"])

	assert.Contains(t, logs.String(), "tool call succeeded")
	assert.NotContains(t, logs.String(), secretValue)
}

func TestSecretsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(*handler) *mcp.CallToolResult
		want string
	}{
		{
			name: "get_secret missing both",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.getSecret(context.Background(), nil, GetSecretArgs{})
				return res
			},
			want: "storeName, secretName",
		},
		{
			name: "get_secret missing secret name",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.getSecret(context.Background(), nil, GetSecretArgs{StoreName: "vault"})
				return res
			},
			want: "secretName",
		},
		{
			name: "get_bulk_secrets missing store",
			call: func(h *handler) *mcp.CallToolResult {
				res, _, _ := h.getBulkSecrets(context.Background(), nil, GetBulkSecretArgs{StoreName: " "})
				return res
			},
			want: "storeName",
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
			mockClient.AssertNotCalled(t, "GetSecret")
			mockClient.AssertNotCalled(t, "GetBulkSecret")
		})
	}
}

func TestSecretsMetadataPassedThroughUnchanged(t *testing.T) {
	t.Parallel()
	meta := map[string]string{"version_id": "2"}
	mockClient := new(mocks.MockDaprClient)
	mockClient.On("GetSecret", mock.Anything, "vault", "db", meta).Return(map[string]string{}, nil)
	mockClient.On("GetBulkSecret", mock.Anything, "vault", meta).Return(map[string]map[string]string{}, nil)

	h, _ := newTestHandler(mockClient)
	_, _, _ = h.getSecret(context.Background(), nil, GetSecretArgs{StoreName: "vault", SecretName: "db", Metadata: meta})
	_, _, _ = h.getBulkSecrets(context.Background(), nil, GetBulkSecretArgs{StoreName: "vault", Metadata: meta})

	mockClient.AssertExpectations(t)
}

// mockSecretsClient implements SecretsClient for testing
type mockSecretsClient struct {
	mock.Mock
}

func (m *mockSecretsClient) GetSecret(ctx context.Context, storeName, key string, meta map[string]string) (map[string]string, error) {
	args := m.Called(ctx, storeName, key, meta)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]string), args.Error(1)
}

func (m *mockSecretsClient) GetBulkSecret(ctx context.Context, storeName string, meta map[string]string) (map[string]map[string]string, error) {
	args := m.Called(ctx, storeName, meta)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]map[string]string), args.Error(1)
}

func TestGetSecretToolWithInterfaceMock(t *testing.T) {
	mockSecrets := new(mockSecretsClient)
	mockSecrets.On("GetSecret", mock.Anything, "test-store", "test-secret", mock.Anything).
		Return(map[string]string{"test-secret": "test-value"}, nil)

	h, _ := newTestHandler(mockSecrets)

	args := GetSecretArgs{
		StoreName:  "test-store",
		SecretName: "test-secret",
	}

	result, structured, err := h.getSecret(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)
	assert.Equal(t, "test-value", structured["test-secret"])

	mockSecrets.AssertExpectations(t)
}
