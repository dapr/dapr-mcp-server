package cryptography

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

// CryptoClient defines the interface for cryptography operations.
type CryptoClient interface {
	Encrypt(ctx context.Context, data io.Reader, opts dapr.EncryptOptions) (io.Reader, error)
	Decrypt(ctx context.Context, data io.Reader, opts dapr.DecryptOptions) (io.Reader, error)
}

type EncryptArgs struct {
	ComponentName string `json:"componentName" jsonschema:"The name of the Dapr Cryptography component."`
	PlainText     string `json:"plainText" jsonschema:"The plain text message to be encrypted."`
}

type DecryptArgs struct {
	ComponentName string `json:"componentName" jsonschema:"The name of the Dapr Cryptography component."`
	CipherText    string `json:"cipherText" jsonschema:"The base64-encoded encrypted message to be decrypted."`
}

var (
	cryptoClient CryptoClient
	toolMetrics  *telemetry.ToolMetrics
)

func encryptTool(ctx context.Context, req *mcp.CallToolRequest, args EncryptArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "encrypt", "crypto")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "encrypt")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "encrypt"),
		attribute.String("mcp.tool.package", "crypto"),
		attribute.String("dapr.component.name", args.ComponentName),
		attribute.String("dapr.crypto.algorithm", "RSA"),
	)

	plainStream := strings.NewReader(args.PlainText)

	encryptOpts := dapr.EncryptOptions{
		ComponentName:    args.ComponentName,
		KeyName:          "rsa-private-key.pem",
		KeyWrapAlgorithm: "RSA",
	}

	cipherStream, err := cryptoClient.Encrypt(ctx, plainStream, encryptOpts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.ComponentName)
		}
		log.Printf("Dapr Encrypt failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr Encrypt failed: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	cipherBuf, err := io.ReadAll(cipherStream)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.ComponentName)
		}
		toolErrorMessage := fmt.Errorf("failed to read encrypted stream: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.ComponentName)
	}

	cipherText := string(cipherBuf)

	successMessage := fmt.Sprintf(
		"Successfully encrypted message using component '%s'. Cipher Text is returned in the tool result.",
		args.ComponentName,
	)
	log.Println(successMessage)
	structuredResult := map[string]string{
		"cipher_text": cipherText,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

func decryptTool(ctx context.Context, req *mcp.CallToolRequest, args DecryptArgs) (*mcp.CallToolResult, any, error) {
	// Start metrics timer
	var timer *telemetry.Timer
	if toolMetrics != nil {
		timer = toolMetrics.StartTimer(ctx, "decrypt", "crypto")
	}

	ctx, span := otel.Tracer("dapr-mcp-server").Start(ctx, "decrypt")
	defer span.End()
	span.SetAttributes(
		attribute.String("mcp.tool.name", "decrypt"),
		attribute.String("mcp.tool.package", "crypto"),
		attribute.String("dapr.component.name", args.ComponentName),
	)

	cipherStream := strings.NewReader(args.CipherText)

	decryptOpts := dapr.DecryptOptions{
		ComponentName: args.ComponentName,
		KeyName:       "rsa-private-key.pem",
	}

	plainStream, err := cryptoClient.Decrypt(ctx, cipherStream, decryptOpts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.ComponentName)
		}
		log.Printf("Dapr Decrypt failed: %v", err)
		toolErrorMessage := fmt.Errorf("dapr Decrypt failed: %v", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	plainBuf, err := io.ReadAll(plainStream)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		if timer != nil {
			timer.Stop("error", args.ComponentName)
		}
		toolErrorMessage := fmt.Errorf("failed to read decrypted stream: %w", err).Error()
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: toolErrorMessage}},
			IsError: true,
		}, nil, nil
	}

	span.SetStatus(codes.Ok, "")
	if timer != nil {
		timer.Stop("success", args.ComponentName)
	}

	plainText := string(plainBuf)

	successMessage := fmt.Sprintf(
		"Successfully decrypted message using component '%s'. Plain text is returned in the tool result.",
		args.ComponentName,
	)
	log.Println(successMessage)
	structuredResult := map[string]string{
		"plain_text":     plainText,
		"component_name": args.ComponentName,
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: successMessage}},
	}, structuredResult, nil
}

func RegisterTools(server *mcp.Server, client CryptoClient, metrics *telemetry.ToolMetrics) {
	cryptoClient = client
	toolMetrics = metrics

	// Encrypt Annotations
	notIdempotent := false
	isDestructive := true
	notReadOnly := false
	isOpenWorld := true

	// Decrypt Annotations
	isIdempotent := true
	isReadOnly := true
	notDestructive := false

	mcp.AddTool(server, &mcp.Tool{
		Name:  "encrypt_data",
		Title: "Encrypt Sensitive Data for Confidentiality",
		Description: "Encrypts arbitrary plain text data using a Dapr cryptography component. **This is a SIDE-EFFECT action (mutates data form) that is NOT IDEMPOTENT.** Use ONLY when the user explicitly says 'encrypt this' or 'store this encrypted'.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `ComponentName` of the cryptography component.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `ComponentName` and `PlainText`.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**WORKFLOW RULE**: The output from `encrypt_data` is the ciphertext to be used in subsequent storage or publication steps.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &isDestructive,
			ReadOnlyHint:    notReadOnly,
			IdempotentHint:  notIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, encryptTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:  "decrypt_data",
		Title: "Decrypt Sensitive Cipher Text",
		Description: "Decrypts encrypted cipher text data back into its original plain text form. **This is a Data Retrieval operation (Read-Only) that IS IDEMPOTENT.** Use ONLY when the user explicitly requests to read data that was previously encrypted.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `ComponentName` of the cryptography component.\n" +
			"2. Ensure the `ComponentName` matches a valid cryptography component name.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `ComponentName` and `CipherText`.\n" +
			"2. **OPTIONAL INPUTS**: If the required key is not embedded in the ciphertext header, you MUST ask the user for the explicit `KeyName`.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**DEFAULTS:**\n" +
			"- If `KeyName` is not provided, the tool will attempt to use the key embedded in the ciphertext header, if available.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &notDestructive,
			ReadOnlyHint:    isReadOnly,
			IdempotentHint:  isIdempotent,
			OpenWorldHint:   &isOpenWorld,
		},
	}, decryptTool)
}
