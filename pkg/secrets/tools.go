// Package secrets exposes the Dapr secrets building block as MCP tools.
package secrets

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "secrets"

	toolGetSecret      = "get_secret"
	toolGetBulkSecrets = "get_bulk_secrets"

	attrSecretName = "dapr.secrets.key" //nolint:gosec // span attribute name, not a credential
)

// SecretsClient defines the interface for secrets operations.
type SecretsClient interface {
	GetSecret(ctx context.Context, storeName, key string, meta map[string]string) (map[string]string, error)
	GetBulkSecret(ctx context.Context, storeName string, meta map[string]string) (map[string]map[string]string, error)
}

// GetSecretArgs are the arguments of the get_secret tool.
type GetSecretArgs struct {
	StoreName  string            `json:"storeName" jsonschema:"The name of the configured Dapr secret store component (e.g., 'vault')."`
	SecretName string            `json:"secretName" jsonschema:"The specific name of the secret to retrieve (e.g., 'db-credentials')."`
	Metadata   map[string]string `json:"metadata,omitempty" jsonschema:"Optional per-request metadata (e.g., 'version_id'). Check the secret store documentation for supported fields."`
}

// GetBulkSecretArgs are the arguments of the get_bulk_secrets tool.
type GetBulkSecretArgs struct {
	StoreName string            `json:"storeName" jsonschema:"The name of the configured Dapr secret store component (e.g., 'vault')."`
	Metadata  map[string]string `json:"metadata,omitempty" jsonschema:"Optional per-request metadata for the bulk retrieval operation."`
}

type handler struct {
	client SecretsClient
	inst   toolkit.Instrumentation
}

func (h *handler) getSecret(ctx context.Context, _ *mcp.CallToolRequest, args GetSecretArgs) (*mcp.CallToolResult, map[string]string, error) {
	ctx, call := h.inst.Start(ctx, toolGetSecret, packageName,
		attribute.String(toolkit.AttrComponentName, args.StoreName),
		attribute.String(attrSecretName, args.SecretName),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "storeName", Value: args.StoreName},
		toolkit.Field{Name: "secretName", Value: args.SecretName},
	); res != nil {
		return res, nil, nil
	}

	secret, err := h.client.GetSecret(ctx, args.StoreName, args.SecretName, args.Metadata)
	if err != nil {
		return call.Fail(fmt.Errorf("get secret %q from store %q: %w", args.SecretName, args.StoreName, err)), nil, nil
	}

	secretJSON, err := toolkit.MarshalIndent(secret)
	if err != nil {
		return call.Fail(err), nil, nil
	}

	keys := slices.Sorted(maps.Keys(secret))
	call.Succeed("store", args.StoreName, "secret", args.SecretName, "keys", len(keys))

	text := fmt.Sprintf(
		"Successfully retrieved secret '%s' from store '%s'. It contains the following key(s): %s.\n\nJSON Value:\n%s",
		args.SecretName, args.StoreName, strings.Join(keys, ", "), secretJSON,
	)
	return toolkit.TextResult(text), secret, nil
}

func (h *handler) getBulkSecrets(ctx context.Context, _ *mcp.CallToolRequest, args GetBulkSecretArgs) (*mcp.CallToolResult, map[string]map[string]string, error) {
	ctx, call := h.inst.Start(ctx, toolGetBulkSecrets, packageName,
		attribute.String(toolkit.AttrComponentName, args.StoreName),
	)
	defer call.End()

	if res := call.Require(toolkit.Field{Name: "storeName", Value: args.StoreName}); res != nil {
		return res, nil, nil
	}

	secrets, err := h.client.GetBulkSecret(ctx, args.StoreName, args.Metadata)
	if err != nil {
		return call.Fail(fmt.Errorf("get bulk secrets from store %q: %w", args.StoreName, err)), nil, nil
	}

	secretsJSON, err := toolkit.MarshalIndent(secrets)
	if err != nil {
		return call.Fail(err), nil, nil
	}

	names := slices.Sorted(maps.Keys(secrets))
	call.Succeed("store", args.StoreName, "secrets", len(names))

	text := fmt.Sprintf(
		"Successfully retrieved %d secret(s) in bulk from store '%s'. Names retrieved: %s.\n\nJSON Value:\n%s",
		len(names), args.StoreName, strings.Join(names, ", "), secretsJSON,
	)
	return toolkit.TextResult(text), secrets, nil
}

// RegisterTools registers the get_secret and get_bulk_secrets tools on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client SecretsClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolGetSecret,
		Title: "Retrieve Single Authorized Secret",
		Description: "Retrieves a single, whitelisted secret (e.g., API key, credential) from a configured Dapr secret store. **This is a highly SENSITIVE, READ-ONLY and IDEMPOTENT Data Retrieval operation.** Use ONLY when the user explicitly requests a specific secret.\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the secret store.\n" +
			"2. Ensure `secretName` is explicitly provided by the user.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `storeName` and `secretName`.\n" +
			"2. **NEVER INVENT**: You must NOT invent `secretName` or `storeName` names; they must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**SECURITY WARNING**: This tool provides access to critical credentials. NEVER guess secret names, and NEVER store retrieved secrets without explicit authorization.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.getSecret)
	mcp.AddTool(server, &mcp.Tool{
		Name:  toolGetBulkSecrets,
		Title: "Retrieve All Secrets (HIGHLY RESTRICTED)",
		Description: "Attempts to retrieve ALL secrets the application has access to from a specific Dapr secret store. **This operation is READ-ONLY and IDEMPOTENT but HIGHLY RESTRICTED and extremely high-risk.**\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `storeName` of the secret store.\n" +
			"2. Ensure the user explicitly requests bulk retrieval and understands the risks.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide a non-empty value for `storeName`.\n" +
			"2. **RISK WARNING**: Avoid this tool unless the user explicitly requests enumeration of all accessible secrets, as it provides a broad view of the system's credentials.",
		Annotations: toolkit.ReadOnly.Annotations(true),
	}, h.getBulkSecrets)
}

// ToolNames returns the names of the tools RegisterTools adds.
func ToolNames() []string {
	return []string{toolGetSecret, toolGetBulkSecrets}
}
