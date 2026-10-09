// Package cryptography exposes the Dapr cryptography building block as MCP
// tools.
// It is not named crypto so it does not shadow the standard library package.
package cryptography

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "crypto"

	toolEncryptData = "encrypt_data"
	toolDecryptData = "decrypt_data"

	attrAlgorithm = "dapr.crypto.algorithm"
	attrKeyName   = "dapr.crypto.key_name"

	// DefaultKeyName is the key encrypt_data uses when the caller names none.
	DefaultKeyName = "rsa-private-key.pem"
	// DefaultKeyWrapAlgorithm is the key wrap algorithm encrypt_data uses
	// when the caller names none.
	DefaultKeyWrapAlgorithm = "RSA"
)

// CryptoClient defines the interface for cryptography operations.
type CryptoClient interface {
	Encrypt(ctx context.Context, data io.Reader, opts dapr.EncryptOptions) (io.Reader, error)
	Decrypt(ctx context.Context, data io.Reader, opts dapr.DecryptOptions) (io.Reader, error)
}

// EncryptArgs are the arguments of the encrypt_data tool.
type EncryptArgs struct {
	ComponentName    string `json:"componentName" jsonschema:"The name of the Dapr Cryptography component."`
	PlainText        string `json:"plainText" jsonschema:"The plain text message to be encrypted."`
	KeyName          string `json:"keyName,omitempty" jsonschema:"Optional name (or name/version) of the key to encrypt with. Default is 'rsa-private-key.pem'."`
	KeyWrapAlgorithm string `json:"keyWrapAlgorithm,omitempty" jsonschema:"Optional key wrap algorithm (e.g., 'RSA', 'RSA-OAEP-256', 'A256KW'). Default is 'RSA'."`
}

// DecryptArgs are the arguments of the decrypt_data tool.
type DecryptArgs struct {
	ComponentName string `json:"componentName" jsonschema:"The name of the Dapr Cryptography component."`
	CipherText    string `json:"cipherText" jsonschema:"The base64-encoded cipher text returned by encrypt_data."`
	KeyName       string `json:"keyName,omitempty" jsonschema:"Optional name (or name/version) of the decryption key. Omit it to use the key named in the cipher text header."`
}

type handler struct {
	client CryptoClient
	inst   toolkit.Instrumentation
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (h *handler) encrypt(ctx context.Context, _ *mcp.CallToolRequest, args EncryptArgs) (*mcp.CallToolResult, any, error) {
	keyName := orDefault(args.KeyName, DefaultKeyName)
	algorithm := orDefault(args.KeyWrapAlgorithm, DefaultKeyWrapAlgorithm)

	ctx, call := h.inst.Start(ctx, toolEncryptData, packageName,
		attribute.String(toolkit.AttrComponentName, args.ComponentName),
		attribute.String(attrKeyName, keyName),
		attribute.String(attrAlgorithm, algorithm),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "componentName", Value: args.ComponentName},
		toolkit.Field{Name: "plainText", Value: args.PlainText},
	); res != nil {
		return res, nil, nil
	}

	cipherStream, err := h.client.Encrypt(ctx, strings.NewReader(args.PlainText), dapr.EncryptOptions{
		ComponentName:    args.ComponentName,
		KeyName:          keyName,
		KeyWrapAlgorithm: algorithm,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("encrypt with component %q: %w", args.ComponentName, err)), nil, nil
	}
	cipher, err := io.ReadAll(cipherStream)
	if err != nil {
		return call.Fail(fmt.Errorf("read encrypted stream: %w", err)), nil, nil
	}

	call.Succeed("component", args.ComponentName, "key", keyName, "cipher_bytes", len(cipher))
	cipherText := base64.StdEncoding.EncodeToString(cipher)
	// Clients that read only the text content must still receive the cipher text.
	text := fmt.Sprintf(
		"Successfully encrypted message using component '%s'. Base64-encoded cipher text:\n%s",
		args.ComponentName, cipherText,
	)
	return toolkit.TextResult(text), map[string]string{
		"cipher_text":    cipherText,
		"component_name": args.ComponentName,
	}, nil
}

func (h *handler) decrypt(ctx context.Context, _ *mcp.CallToolRequest, args DecryptArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolDecryptData, packageName,
		attribute.String(toolkit.AttrComponentName, args.ComponentName),
		attribute.String(attrKeyName, args.KeyName),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "componentName", Value: args.ComponentName},
		toolkit.Field{Name: "cipherText", Value: args.CipherText},
	); res != nil {
		return res, nil, nil
	}

	cipher, err := base64.StdEncoding.DecodeString(strings.TrimSpace(args.CipherText))
	if err != nil {
		return call.Fail(fmt.Errorf("cipherText is not valid base64: %w", err)), nil, nil
	}

	plainStream, err := h.client.Decrypt(ctx, bytes.NewReader(cipher), dapr.DecryptOptions{
		ComponentName: args.ComponentName,
		KeyName:       args.KeyName,
	})
	if err != nil {
		return call.Fail(fmt.Errorf("decrypt with component %q: %w", args.ComponentName, err)), nil, nil
	}
	plain, err := io.ReadAll(plainStream)
	if err != nil {
		return call.Fail(fmt.Errorf("read decrypted stream: %w", err)), nil, nil
	}

	call.Succeed("component", args.ComponentName, "plain_bytes", len(plain))
	// Clients that read only the text content must still receive the plain text.
	text := fmt.Sprintf(
		"Successfully decrypted message using component '%s'. Plain text:\n%s",
		args.ComponentName, plain,
	)
	return toolkit.TextResult(text), map[string]string{
		"plain_text":     string(plain),
		"component_name": args.ComponentName,
	}, nil
}

// RegisterTools registers the encrypt_data and decrypt_data tools on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client CryptoClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolEncryptData,
		Title: "Encrypt Sensitive Data for Confidentiality",
		Description: "Encrypts arbitrary plain text data using a Dapr cryptography component. **This is a SIDE-EFFECT action (mutates data form) that is NOT IDEMPOTENT.** Use ONLY when the user explicitly says 'encrypt this' or 'store this encrypted'.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `componentName` of the cryptography component.\n" +
			"2. `keyName` defaults to '" + DefaultKeyName + "' and `keyWrapAlgorithm` defaults to '" + DefaultKeyWrapAlgorithm + "'.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `componentName` and `plainText`.\n" +
			"2. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**WORKFLOW RULE**: The `cipher_text` output is base64-encoded. Pass it unchanged to `decrypt_data` or to subsequent storage or publication steps.",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.encrypt)

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolDecryptData,
		Title: "Decrypt Sensitive Cipher Text",
		Description: "Decrypts base64-encoded cipher text produced by `encrypt_data` back into its original plain text form. **This is a READ-ONLY Data Retrieval operation that IS IDEMPOTENT.** Use ONLY when the user explicitly requests to read data that was previously encrypted.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `componentName` of the cryptography component.\n" +
			"2. Ensure the `componentName` matches a valid cryptography component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `componentName` and `cipherText`.\n" +
			"2. **OPTIONAL INPUTS**: If the required key is not embedded in the cipher text header, you MUST ask the user for the explicit `keyName`.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**DEFAULTS:**\n" +
			"- If `keyName` is not provided, the key named in the cipher text header is used.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.decrypt)
}

// ToolNames returns the names of the tools RegisterTools adds.
func ToolNames() []string {
	return []string{toolEncryptData, toolDecryptData}
}
