package cryptography

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func newTestHandler(client CryptoClient) (*handler, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &handler{client: client, inst: toolkit.Instrumentation{Logger: logger}}, &buf
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read error") }

func TestEncryptTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		args          EncryptArgs
		stream        io.Reader
		encryptErr    error
		wantKey       string
		wantAlgorithm string
		wantErr       string
		wantCipher    string
	}{
		{
			name:          "defaults applied",
			args:          EncryptArgs{ComponentName: "vault", PlainText: "secret"},
			stream:        bytes.NewReader([]byte{0x00, 0xff, 0x10}),
			wantKey:       DefaultKeyName,
			wantAlgorithm: DefaultKeyWrapAlgorithm,
			wantCipher:    base64.StdEncoding.EncodeToString([]byte{0x00, 0xff, 0x10}),
		},
		{
			name:          "caller key and algorithm",
			args:          EncryptArgs{ComponentName: "vault", PlainText: "secret", KeyName: "mykey", KeyWrapAlgorithm: "A256KW"},
			stream:        bytes.NewReader([]byte("c")),
			wantKey:       "mykey",
			wantAlgorithm: "A256KW",
			wantCipher:    base64.StdEncoding.EncodeToString([]byte("c")),
		},
		{
			name:          "encrypt error",
			args:          EncryptArgs{ComponentName: "vault", PlainText: "secret"},
			encryptErr:    errors.New("key not found"),
			wantKey:       DefaultKeyName,
			wantAlgorithm: DefaultKeyWrapAlgorithm,
			wantErr:       `encrypt with component "vault": key not found`,
		},
		{
			name:          "read error",
			args:          EncryptArgs{ComponentName: "vault", PlainText: "secret"},
			stream:        errorReader{},
			wantKey:       DefaultKeyName,
			wantAlgorithm: DefaultKeyWrapAlgorithm,
			wantErr:       "read encrypted stream",
		},
		{name: "missing plain text", args: EncryptArgs{ComponentName: "vault"}, wantErr: "plainText"},
		{name: "missing component", args: EncryptArgs{PlainText: "x"}, wantErr: "componentName"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			if tt.wantKey != "" {
				var ret any
				if tt.stream != nil {
					ret = tt.stream
				}
				mockClient.On("Encrypt", mock.Anything, mock.Anything, dapr.EncryptOptions{
					ComponentName:    tt.args.ComponentName,
					KeyName:          tt.wantKey,
					KeyWrapAlgorithm: tt.wantAlgorithm,
				}).Return(ret, tt.encryptErr)
			}
			h, logs := newTestHandler(mockClient)

			res, structured, err := h.encrypt(context.Background(), nil, tt.args)
			require.NoError(t, err)
			mockClient.AssertExpectations(t)
			if tt.wantErr != "" {
				require.True(t, res.IsError)
				assert.Contains(t, textOf(t, res), tt.wantErr)
				return
			}
			require.False(t, res.IsError)
			assert.Equal(t, tt.wantCipher, structured.(map[string]string)["cipher_text"])
			assert.Contains(t, textOf(t, res), tt.wantCipher, "text content must carry the cipher text")
			assert.NotContains(t, logs.String(), tt.args.PlainText)
		})
	}
}

func TestDecryptTool(t *testing.T) {
	t.Parallel()
	validCipher := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02})
	tests := []struct {
		name       string
		args       DecryptArgs
		stream     io.Reader
		decryptErr error
		callsSDK   bool
		wantErr    string
		wantPlain  string
	}{
		{
			name:      "success uses header key by default",
			args:      DecryptArgs{ComponentName: "vault", CipherText: validCipher},
			stream:    bytes.NewReader([]byte("hello")),
			callsSDK:  true,
			wantPlain: "hello",
		},
		{
			name:      "explicit key name",
			args:      DecryptArgs{ComponentName: "vault", CipherText: validCipher, KeyName: "mykey"},
			stream:    bytes.NewReader([]byte("hello")),
			callsSDK:  true,
			wantPlain: "hello",
		},
		{
			name:       "decrypt error",
			args:       DecryptArgs{ComponentName: "vault", CipherText: validCipher},
			decryptErr: errors.New("key mismatch"),
			callsSDK:   true,
			wantErr:    `decrypt with component "vault": key mismatch`,
		},
		{
			name:     "read error",
			args:     DecryptArgs{ComponentName: "vault", CipherText: validCipher},
			stream:   errorReader{},
			callsSDK: true,
			wantErr:  "read decrypted stream",
		},
		{name: "invalid base64", args: DecryptArgs{ComponentName: "vault", CipherText: "not base64!"}, wantErr: "cipherText is not valid base64"},
		{name: "missing cipher text", args: DecryptArgs{ComponentName: "vault"}, wantErr: "cipherText"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockClient := new(mocks.MockDaprClient)
			if tt.callsSDK {
				var ret any
				if tt.stream != nil {
					ret = tt.stream
				}
				mockClient.On("Decrypt", mock.Anything, mock.Anything, dapr.DecryptOptions{
					ComponentName: tt.args.ComponentName,
					KeyName:       tt.args.KeyName,
				}).Return(ret, tt.decryptErr)
			}
			h, _ := newTestHandler(mockClient)

			res, structured, err := h.decrypt(context.Background(), nil, tt.args)
			require.NoError(t, err)
			mockClient.AssertExpectations(t)
			if !tt.callsSDK {
				assert.Empty(t, mockClient.Calls)
			}
			if tt.wantErr != "" {
				require.True(t, res.IsError)
				assert.Contains(t, textOf(t, res), tt.wantErr)
				return
			}
			require.False(t, res.IsError)
			assert.Equal(t, tt.wantPlain, structured.(map[string]string)["plain_text"])
			assert.Contains(t, textOf(t, res), tt.wantPlain, "text content must carry the plain text")
		})
	}
}

// TestEncryptDecryptRoundTrip checks that binary cipher bytes survive the
// trip through the tool results: decrypt must hand Dapr exactly the bytes
// encrypt received from Dapr.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	const plainText = "round trip message"
	cipher := []byte{0x00, 0x9f, 0xff, 0x22, '"', '\\', 0x7f}

	mockClient := new(mocks.MockDaprClient)
	mockClient.On("Encrypt", mock.Anything, mock.MatchedBy(func(r io.Reader) bool {
		b, err := io.ReadAll(r)
		return err == nil && string(b) == plainText
	}), mock.Anything).Return(bytes.NewReader(cipher), nil)
	mockClient.On("Decrypt", mock.Anything, mock.MatchedBy(func(r io.Reader) bool {
		b, err := io.ReadAll(r)
		return err == nil && bytes.Equal(b, cipher)
	}), mock.Anything).Return(bytes.NewReader([]byte(plainText)), nil)

	h, _ := newTestHandler(mockClient)

	encRes, encOut, err := h.encrypt(context.Background(), nil, EncryptArgs{ComponentName: "vault", PlainText: plainText})
	require.NoError(t, err)
	require.False(t, encRes.IsError)
	cipherText := encOut.(map[string]string)["cipher_text"]

	decRes, decOut, err := h.decrypt(context.Background(), nil, DecryptArgs{ComponentName: "vault", CipherText: cipherText})
	require.NoError(t, err)
	require.False(t, decRes.IsError, textOf(t, decRes))
	assert.Equal(t, plainText, decOut.(map[string]string)["plain_text"])
	mockClient.AssertExpectations(t)
}

func TestRegisterTools(t *testing.T) {
	t.Parallel()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)
	RegisterTools(server, new(mocks.MockDaprClient), nil)
}
