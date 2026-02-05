package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

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
			wantContent: "failed to get secret 'nonexistent'",
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
			wantContent: "failed to get secret",
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
			wantContent: "failed to get secret 'restricted'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			secretsClient = mockClient

			result, _, err := getSecretTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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
			wantContent: "failed to get bulk secrets from store 'vault'",
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
			wantContent: "failed to get bulk secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			secretsClient = mockClient

			result, _, err := getBulkSecretTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	assert.Equal(t, mockClient, secretsClient)
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

	secretsClient = mockSecrets

	args := GetSecretArgs{
		StoreName:  "test-store",
		SecretName: "test-secret",
	}

	result, structured, err := getSecretTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)
	assert.Equal(t, "test-value", structured["test-secret"])

	mockSecrets.AssertExpectations(t)
}
