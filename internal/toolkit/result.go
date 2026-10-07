package toolkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Content types chosen for outgoing payloads.
const (
	ContentTypeJSON = "application/json"
	ContentTypeText = "text/plain"
)

const jsonIndent = "  "

// ErrMissingArgument is wrapped by the error ValidateRequired returns.
var ErrMissingArgument = errors.New("missing required argument")

// TextResult returns a successful result carrying text.
func TextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// ErrorResult returns a result carrying text with IsError set.
func ErrorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}
}

// Field names a tool argument and the value supplied for it.
type Field struct {
	Name  string
	Value string
}

// ValidateRequired returns an error wrapping ErrMissingArgument that names
// every field whose value is empty or only whitespace, or nil if none are.
func ValidateRequired(fields ...Field) error {
	var missing []string
	for _, f := range fields {
		if strings.TrimSpace(f.Value) == "" {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingArgument, strings.Join(missing, ", "))
}

// ContentTypeFor returns ContentTypeJSON when payload is valid JSON and
// ContentTypeText otherwise.
func ContentTypeFor(payload []byte) string {
	if json.Valid(payload) {
		return ContentTypeJSON
	}
	return ContentTypeText
}

// IndentJSON returns data pretty-printed when it is valid JSON, and the raw
// bytes as a string otherwise.
func IndentJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", jsonIndent); err != nil {
		return string(data)
	}
	return buf.String()
}

// MarshalIndent pretty-prints v as JSON.
func MarshalIndent(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", jsonIndent)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}
