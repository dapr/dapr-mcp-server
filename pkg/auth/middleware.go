// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

const (
	bearerScheme = "Bearer"
	// tokenPreviewLength is how many leading token characters debug logs may show.
	tokenPreviewLength = 20
	redactedValue      = "[REDACTED]"
)

// sensitiveHeaders are request headers whose values are never logged.
// Keys are canonical header names.
var sensitiveHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Cookie":              {},
	"Set-Cookie":          {},
	"X-Api-Key":           {},
}

// Middleware provides HTTP authentication middleware.
type Middleware struct {
	authenticators []Authenticator
	config         Config
	logger         *slog.Logger
}

// NewMiddleware creates a new authentication middleware.
func NewMiddleware(cfg Config, authenticators []Authenticator, logger *slog.Logger) *Middleware {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Middleware{
		authenticators: authenticators,
		config:         cfg,
		logger:         logger,
	}
}

// Handler returns an HTTP middleware that authenticates requests.
//
// Skip paths are matched against r.URL.Path exactly as received.
// Paths that are not in clean form (for example "//livez" or "/livez/../mcp") never match a skip path,
// so they are authenticated; http.ServeMux then redirects them to their clean form.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.logRequest(r)

		if m.shouldSkip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if !m.config.Enabled || m.config.Mode == ModeDisabled {
			next.ServeHTTP(w, r)
			return
		}

		token, headerUsed := m.extractTokenWithSource(r)
		if token == "" {
			m.logger.Debug("no authentication token found", "path", r.URL.Path)
			http.Error(w, "Unauthorized: no token provided", http.StatusUnauthorized)
			return
		}

		m.logger.Debug("token extracted",
			"header_used", headerUsed,
			"token_length", len(token),
			"token_prefix", safeTokenPrefix(token),
		)

		var identity *Identity
		var lastErr error
		for _, auth := range m.authenticators {
			id, err := auth.Authenticate(r.Context(), token)
			if err == nil {
				identity = id
				break
			}
			m.logger.Debug("authenticator rejected token", "mode", auth.Mode(), "error", err)
			lastErr = err
		}

		if identity == nil {
			m.logger.Debug("all authenticators failed", "path", r.URL.Path, "last_error", lastErr)
			http.Error(w, "Unauthorized: invalid token", http.StatusUnauthorized)
			return
		}

		m.logger.Debug("authentication successful",
			"path", r.URL.Path,
			"subject", identity.Subject,
			"method", identity.AuthMethod,
		)

		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
	})
}

// logRequest logs the request line and headers at debug level, redacting credential headers.
func (m *Middleware) logRequest(r *http.Request) {
	if !m.logger.Enabled(r.Context(), slog.LevelDebug) {
		return
	}
	headers := make(map[string]any, len(r.Header))
	for name, values := range r.Header {
		if m.isSensitiveHeader(name) {
			headers[name] = redactedValue
			continue
		}
		headers[name] = values
	}
	m.logger.Debug("incoming request",
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
		"headers", headers,
	)
}

// isSensitiveHeader reports whether a header may carry credentials, case-insensitively.
func (m *Middleware) isSensitiveHeader(name string) bool {
	if _, ok := sensitiveHeaders[http.CanonicalHeaderKey(name)]; ok {
		return true
	}
	custom := m.config.DaprSentry.TokenHeader
	return custom != "" && strings.EqualFold(name, custom)
}

// shouldSkip returns true if the path should skip authentication.
// A skip path ending in "*" matches any path with that prefix.
func (m *Middleware) shouldSkip(p string) bool {
	if !isCleanPath(p) {
		return false
	}
	for _, skipPath := range m.config.SkipPaths {
		if p == skipPath {
			return true
		}
		if prefix, ok := strings.CutSuffix(skipPath, skipPathWildcard); ok && strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// isCleanPath reports whether p is already in the form path.Clean produces,
// allowing a single trailing slash.
func isCleanPath(p string) bool {
	if p == "" {
		return false
	}
	c := path.Clean(p)
	return p == c || (c != "/" && p == c+"/")
}

// extractTokenWithSource extracts the token and returns which header it came from.
//
// The configured DaprSentry.TokenHeader is checked first for every auth mode,
// and holds a raw token. When it is absent or empty the Authorization header is used,
// as "Bearer <token>" or as a raw token.
func (m *Middleware) extractTokenWithSource(r *http.Request) (token string, header string) {
	custom := m.config.DaprSentry.TokenHeader
	if custom != "" && !strings.EqualFold(custom, authorizationHeader) {
		if v := strings.TrimSpace(r.Header.Get(custom)); v != "" {
			if strings.ContainsAny(v, " \t") {
				return "", ""
			}
			return v, custom
		}
	}

	return parseAuthorization(r.Header.Get(authorizationHeader))
}

// parseAuthorization parses an Authorization header value as "Bearer <token>" or a raw token.
// It returns an empty token for an empty bearer token, another scheme, or embedded whitespace.
func parseAuthorization(value string) (token string, header string) {
	if value == "" {
		return "", ""
	}
	scheme, rest, found := strings.Cut(value, " ")
	if !found {
		if strings.EqualFold(scheme, bearerScheme) || strings.ContainsRune(scheme, '\t') {
			return "", ""
		}
		return scheme, authorizationHeader + " (raw)"
	}
	if !strings.EqualFold(scheme, bearerScheme) || rest == "" || strings.ContainsAny(rest, " \t") {
		return "", ""
	}
	return rest, authorizationHeader + " (" + bearerScheme + ")"
}

// safeTokenPrefix returns the first few characters of a token for debugging
// without exposing the full token.
func safeTokenPrefix(token string) string {
	if len(token) <= tokenPreviewLength {
		return "[token too short to preview safely]"
	}
	return token[:tokenPreviewLength] + "..."
}

// NoopMiddleware returns a middleware that does nothing (for disabled auth).
func NoopMiddleware(next http.Handler) http.Handler {
	return next
}
