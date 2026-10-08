// Package invoke exposes Dapr service invocation as an MCP tool.
package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/metadata"

	"github.com/dapr/dapr-mcp-server/internal/toolkit"
	"github.com/dapr/dapr-mcp-server/pkg/telemetry"
)

const (
	packageName = "invoke"

	toolInvokeService = "invoke_service"

	attrAppID  = "dapr.invoke.app_id"
	attrMethod = "dapr.invoke.method"
	attrVerb   = "dapr.invoke.verb"

	// DefaultHTTPVerb is used when the caller does not set one.
	DefaultHTTPVerb = http.MethodPost
)

// reservedHeaderPrefixes are metadata key prefixes the Dapr sidecar and gRPC use for themselves,
// such as dapr-api-token and grpc-timeout, so a tool caller must not set them.
var reservedHeaderPrefixes = []string{"dapr-", "grpc-", ":"}

// errInvalidHeader is wrapped by the error validateHeaders returns.
var errInvalidHeader = errors.New("invalid metadata key")

// allowedHTTPVerbs are the verbs Dapr service invocation accepts that make
// sense for a tool call.
var allowedHTTPVerbs = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}

// InvokeClient defines the interface for service invocation operations.
type InvokeClient interface {
	InvokeMethodWithContent(ctx context.Context, appID, methodName, verb string, content *dapr.DataContent) ([]byte, error)
}

// InvokeServiceArgs are the arguments of the invoke_service tool.
type InvokeServiceArgs struct {
	AppID       string            `json:"appID" jsonschema:"The Dapr application ID of the service to call (e.g., 'order-processor')."`
	Method      string            `json:"method" jsonschema:"The method/endpoint on the target service to call (e.g., 'status')."`
	Data        string            `json:"data,omitempty" jsonschema:"The body payload for the request, typically a JSON string."`
	HTTPVerb    string            `json:"httpVerb,omitempty" jsonschema:"The HTTP verb to use: GET, HEAD, POST, PUT, PATCH, DELETE or OPTIONS. Default is 'POST'."`
	ContentType string            `json:"contentType,omitempty" jsonschema:"Optional content type of data. Defaults to application/json when data is valid JSON, text/plain otherwise."`
	Metadata    map[string]string `json:"metadata,omitempty" jsonschema:"Optional key-value pairs to send to the target service as HTTP headers. Keys starting with 'dapr-' or 'grpc-' are reserved."`
}

type handler struct {
	client InvokeClient
	inst   toolkit.Instrumentation
}

func normalizeVerb(verb string) (string, error) {
	verb = strings.ToUpper(strings.TrimSpace(verb))
	if verb == "" {
		return DefaultHTTPVerb, nil
	}
	if !slices.Contains(allowedHTTPVerbs, verb) {
		return "", fmt.Errorf("unsupported httpVerb %q: use one of %s", verb, strings.Join(allowedHTTPVerbs, ", "))
	}
	return verb, nil
}

// validateHeaders rejects empty keys and keys reserved by Dapr or gRPC.
func validateHeaders(headers map[string]string) error {
	for k := range headers {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			return fmt.Errorf("%w: keys must not be empty", errInvalidHeader)
		}
		for _, prefix := range reservedHeaderPrefixes {
			if strings.HasPrefix(key, prefix) {
				return fmt.Errorf("%w %q: keys starting with %q are reserved", errInvalidHeader, k, prefix)
			}
		}
	}
	return nil
}

// withHeaders adds headers as outgoing gRPC metadata.
// The Dapr sidecar forwards that metadata to the target app as HTTP headers.
func withHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	kv := make([]string, 0, 2*len(headers))
	for k, v := range headers {
		kv = append(kv, k, v)
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

func (h *handler) invokeService(ctx context.Context, _ *mcp.CallToolRequest, args InvokeServiceArgs) (*mcp.CallToolResult, any, error) {
	ctx, call := h.inst.Start(ctx, toolInvokeService, packageName,
		attribute.String(attrAppID, args.AppID),
		attribute.String(attrMethod, args.Method),
		attribute.String(attrVerb, args.HTTPVerb),
	)
	defer call.End()

	if res := call.Require(
		toolkit.Field{Name: "appID", Value: args.AppID},
		toolkit.Field{Name: "method", Value: args.Method},
	); res != nil {
		return res, nil, nil
	}
	verb, err := normalizeVerb(args.HTTPVerb)
	if err != nil {
		return call.Fail(err), nil, nil
	}
	if err = validateHeaders(args.Metadata); err != nil {
		return call.Fail(err), nil, nil
	}

	data := []byte(args.Data)
	contentType := args.ContentType
	if contentType == "" {
		contentType = toolkit.ContentTypeFor(data)
	}

	resp, err := h.client.InvokeMethodWithContent(withHeaders(ctx, args.Metadata), args.AppID, args.Method, verb,
		&dapr.DataContent{ContentType: contentType, Data: data})
	if err != nil {
		return call.Fail(fmt.Errorf("invoke method %q on app %q: %w", args.Method, args.AppID, err)), nil, nil
	}

	call.Succeed("app_id", args.AppID, "method", args.Method, "verb", verb, "response_bytes", len(resp))

	text := fmt.Sprintf(
		"Successfully invoked service '%s' method '%s' (%s). The service responded with data/status.\n\nResponse:\n%s",
		args.AppID, args.Method, verb, toolkit.IndentJSON(resp),
	)
	return toolkit.TextResult(text), structuredResponse(args, resp), nil
}

func structuredResponse(args InvokeServiceArgs, resp []byte) map[string]any {
	if len(resp) == 0 {
		return map[string]any{"status": "success_no_content", "app_id": args.AppID, "method": args.Method}
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp, &decoded); err == nil && decoded != nil {
		return decoded
	}
	return map[string]any{"raw_response": string(resp), "app_id": args.AppID, "method": args.Method}
}

// RegisterTools registers the invoke_service tool on server.
// metrics may be nil.
func RegisterTools(server *mcp.Server, client InvokeClient, metrics *telemetry.ToolMetrics) {
	h := &handler{client: client, inst: toolkit.NewInstrumentation(metrics)}

	mcp.AddTool(server, &mcp.Tool{
		Name:  toolInvokeService,
		Title: "Execute Inter-Service Request",
		Description: "Calls a method (endpoint) on another Dapr-enabled service. **This is a DESTRUCTIVE SIDE-EFFECT action that is NOT IDEMPOTENT.** Use this tool to perform transactional business logic (e.g., updating data, creating resources, triggering workflows).\n\n" +
			"**GUIDANCE:**\n" +
			"1. Use `get_components` to find the `appID` of the target service.\n" +
			"2. For `httpVerb`, use 'GET' for read-only status checks, 'POST' for creation, and 'DELETE' for removal. Default is 'POST'.\n" +
			"3. Use `metadata` to send HTTP headers to the target service.\n\n" +
			"**ARGUMENT RULES:**\n" +
			"1. **REQUIRED INPUTS**: You MUST provide non-empty values for `appID` and `method`.\n" +
			"2. **NEVER INVENT**: You must NOT invent `appID` or `method` names; they must be provided by the user or discovered.\n" +
			"3. **CLARIFICATION**: If any required input is missing, you MUST ask the user for clarification.\n\n" +
			"**SECURITY WARNING**: This tool bypasses the standard Resource/Tool abstraction and directly executes service logic. Ensure user intent is clear and the operation is authorized.",
		Annotations: toolkit.DestructiveWrite.Annotations(true),
	}, h.invokeService)
}
