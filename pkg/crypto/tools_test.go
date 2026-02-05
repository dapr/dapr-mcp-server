package cryptography

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestEncryptTool(t *testing.T) {
	tests := []struct {
		name        string
		args        EncryptArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful encryption",
			args: EncryptArgs{
				ComponentName: "crypto-vault",
				PlainText:     "secret message",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
					Return(io.NopCloser(strings.NewReader("encrypted-data-base64")), nil)
			},
			wantErr:     false,
			wantContent: "Successfully encrypted message using component 'crypto-vault'",
		},
		{
			name: "encryption with empty text",
			args: EncryptArgs{
				ComponentName: "crypto-vault",
				PlainText:     "",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
					Return(io.NopCloser(strings.NewReader("encrypted-empty")), nil)
			},
			wantErr:     false,
			wantContent: "Successfully encrypted message",
		},
		{
			name: "encryption failure - component not found",
			args: EncryptArgs{
				ComponentName: "nonexistent-crypto",
				PlainText:     "secret",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
					Return(nil, errors.New("crypto component not found"))
			},
			wantErr:     true,
			wantContent: "dapr Encrypt failed",
		},
		{
			name: "encryption failure - key error",
			args: EncryptArgs{
				ComponentName: "crypto-vault",
				PlainText:     "secret",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
					Return(nil, errors.New("key not found"))
			},
			wantErr:     true,
			wantContent: "dapr Encrypt failed",
		},
		{
			name: "encryption with long text",
			args: EncryptArgs{
				ComponentName: "crypto-vault",
				PlainText:     strings.Repeat("long message ", 100),
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
					Return(io.NopCloser(strings.NewReader("encrypted-long-message")), nil)
			},
			wantErr:     false,
			wantContent: "Successfully encrypted message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			cryptoClient = mockClient

			result, _, err := encryptTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

func TestDecryptTool(t *testing.T) {
	tests := []struct {
		name        string
		args        DecryptArgs
		setupMock   func(*mocks.MockDaprClient)
		wantErr     bool
		wantContent string
	}{
		{
			name: "successful decryption",
			args: DecryptArgs{
				ComponentName: "crypto-vault",
				CipherText:    "encrypted-data-base64",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
					Return(io.NopCloser(strings.NewReader("decrypted message")), nil)
			},
			wantErr:     false,
			wantContent: "Successfully decrypted message using component 'crypto-vault'",
		},
		{
			name: "decryption failure - component not found",
			args: DecryptArgs{
				ComponentName: "nonexistent-crypto",
				CipherText:    "encrypted-data",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
					Return(nil, errors.New("crypto component not found"))
			},
			wantErr:     true,
			wantContent: "dapr Decrypt failed",
		},
		{
			name: "decryption failure - invalid cipher text",
			args: DecryptArgs{
				ComponentName: "crypto-vault",
				CipherText:    "invalid-cipher",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
					Return(nil, errors.New("invalid cipher text"))
			},
			wantErr:     true,
			wantContent: "dapr Decrypt failed",
		},
		{
			name: "decryption failure - key mismatch",
			args: DecryptArgs{
				ComponentName: "crypto-vault",
				CipherText:    "encrypted-with-different-key",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
					Return(nil, errors.New("decryption failed: key mismatch"))
			},
			wantErr:     true,
			wantContent: "dapr Decrypt failed",
		},
		{
			name: "decryption with empty cipher text",
			args: DecryptArgs{
				ComponentName: "crypto-vault",
				CipherText:    "",
			},
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
					Return(io.NopCloser(strings.NewReader("")), nil)
			},
			wantErr:     false,
			wantContent: "Successfully decrypted message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			cryptoClient = mockClient

			result, _, err := decryptTool(context.Background(), &mcp.CallToolRequest{}, tt.args)

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

	assert.Equal(t, mockClient, cryptoClient)
}

// mockCryptoClient implements CryptoClient for testing
type mockCryptoClient struct {
	mock.Mock
}

func (m *mockCryptoClient) Encrypt(ctx context.Context, data io.Reader, opts dapr.EncryptOptions) (io.Reader, error) {
	args := m.Called(ctx, data, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.Reader), args.Error(1)
}

func (m *mockCryptoClient) Decrypt(ctx context.Context, data io.Reader, opts dapr.DecryptOptions) (io.Reader, error) {
	args := m.Called(ctx, data, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(io.Reader), args.Error(1)
}

func TestEncryptToolWithInterfaceMock(t *testing.T) {
	mockCrypto := new(mockCryptoClient)
	mockCrypto.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
		Return(io.NopCloser(strings.NewReader("test-cipher-text")), nil)

	cryptoClient = mockCrypto

	args := EncryptArgs{
		ComponentName: "test-crypto",
		PlainText:     "test plain text",
	}

	result, structured, err := encryptTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]string)
	assert.True(t, ok)
	assert.Equal(t, "test-cipher-text", structuredMap["cipher_text"])

	mockCrypto.AssertExpectations(t)
}

func TestDecryptToolWithInterfaceMock(t *testing.T) {
	mockCrypto := new(mockCryptoClient)
	mockCrypto.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
		Return(io.NopCloser(strings.NewReader("decrypted plain text")), nil)

	cryptoClient = mockCrypto

	args := DecryptArgs{
		ComponentName: "test-crypto",
		CipherText:    "test-cipher-text",
	}

	result, structured, err := decryptTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.False(t, result.IsError)
	assert.NotNil(t, structured)

	structuredMap, ok := structured.(map[string]string)
	assert.True(t, ok)
	assert.Equal(t, "decrypted plain text", structuredMap["plain_text"])
	assert.Equal(t, "test-crypto", structuredMap["component_name"])

	mockCrypto.AssertExpectations(t)
}

// errorReader is a reader that always returns an error
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("read error")
}

func TestEncryptToolReadError(t *testing.T) {
	mockCrypto := new(mockCryptoClient)
	mockCrypto.On("Encrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.EncryptOptions")).
		Return(&errorReader{}, nil)

	cryptoClient = mockCrypto

	args := EncryptArgs{
		ComponentName: "test-crypto",
		PlainText:     "test",
	}

	result, _, err := encryptTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.True(t, result.IsError)
	if len(result.Content) > 0 {
		textContent, ok := result.Content[0].(*mcp.TextContent)
		assert.True(t, ok)
		assert.Contains(t, textContent.Text, "failed to read encrypted stream")
	}

	mockCrypto.AssertExpectations(t)
}

func TestDecryptToolReadError(t *testing.T) {
	mockCrypto := new(mockCryptoClient)
	mockCrypto.On("Decrypt", mock.Anything, mock.Anything, mock.AnythingOfType("client.DecryptOptions")).
		Return(&errorReader{}, nil)

	cryptoClient = mockCrypto

	args := DecryptArgs{
		ComponentName: "test-crypto",
		CipherText:    "test",
	}

	result, _, err := decryptTool(context.Background(), &mcp.CallToolRequest{}, args)

	assert.NoError(t, err)
	assert.True(t, result.IsError)
	if len(result.Content) > 0 {
		textContent, ok := result.Content[0].(*mcp.TextContent)
		assert.True(t, ok)
		assert.Contains(t, textContent.Text, "failed to read decrypted stream")
	}

	mockCrypto.AssertExpectations(t)
}
