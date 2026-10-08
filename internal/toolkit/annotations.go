package toolkit

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Safety classifies a tool's side effects.
// The values mirror the safety table in AGENTS.md.
type Safety int

const (
	// ReadOnly tools only read data and are idempotent.
	ReadOnly Safety = iota
	// IdempotentWrite tools have side effects, but repeating them is harmless.
	IdempotentWrite
	// DestructiveIdempotent tools remove data, and repeating them is harmless.
	DestructiveIdempotent
	// DestructiveWrite tools may destroy data and are not idempotent.
	DestructiveWrite
	// AdditiveWrite tools have side effects, never destroy data, and are not
	// idempotent.
	AdditiveWrite
)

// Annotations returns fresh MCP annotations for the safety class.
// openWorld reports whether the tool reaches systems beyond the Dapr sidecar.
func (s Safety) Annotations(openWorld bool) *mcp.ToolAnnotations {
	destructive := s == DestructiveIdempotent || s == DestructiveWrite
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    s == ReadOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  s == ReadOnly || s == IdempotentWrite || s == DestructiveIdempotent,
		OpenWorldHint:   &openWorld,
	}
}
