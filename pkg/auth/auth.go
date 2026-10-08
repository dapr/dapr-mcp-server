// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// AuthMode represents the authentication mode.
type AuthMode string

const (
	// ModeDisabled disables authentication (for local development).
	ModeDisabled AuthMode = "disabled"
	// ModeOIDC uses OAuth2.0/OIDC for authentication.
	ModeOIDC AuthMode = "oidc"
	// ModeSPIFFE uses SPIFFE JWT-SVIDs for service-to-service auth.
	ModeSPIFFE AuthMode = "spiffe"
	// ModeHybrid accepts both OIDC and SPIFFE tokens.
	ModeHybrid AuthMode = "hybrid"
	// ModeDaprSentry uses Dapr Sentry JWT tokens for authentication.
	ModeDaprSentry AuthMode = "dapr-sentry"
)

// Authentication errors. Authenticators wrap one of these so callers can classify failures with errors.Is.
var (
	// ErrNoToken is returned when a request carries no token.
	ErrNoToken = errors.New("no authentication token provided")
	// ErrInvalidToken is returned when a token is malformed, badly signed or fails claim checks.
	ErrInvalidToken = errors.New("invalid authentication token")
	// ErrTokenExpired is returned when a token's exp is in the past.
	ErrTokenExpired = errors.New("authentication token expired")
	// ErrInvalidIssuer is returned when a token comes from an unexpected issuer or trust domain.
	ErrInvalidIssuer = errors.New("invalid token issuer")
	// ErrInvalidAudience is returned when a token is not issued for this server.
	ErrInvalidAudience = errors.New("invalid token audience")
	// ErrAuthDisabled is returned when authentication is requested but disabled.
	ErrAuthDisabled = errors.New("authentication is disabled")
	// ErrUnsupportedMethod is returned when the configured auth mode is unknown or its method is not enabled.
	ErrUnsupportedMethod = errors.New("unsupported authentication method")
)

// Identity represents an authenticated identity.
type Identity struct {
	// Subject is the unique identifier for the identity (e.g., user ID or SPIFFE ID).
	Subject string
	// Issuer is the identity provider that issued the token.
	Issuer string
	// Audience is the intended audience of the token.
	Audience []string
	// Email is the email address (for OIDC identities).
	Email string
	// Name is the display name (for OIDC identities).
	Name string
	// Claims contains all claims from the token.
	Claims map[string]interface{}
	// AuthMethod indicates how the identity was authenticated.
	AuthMethod AuthMode
}

// Authenticator is the interface for authentication providers.
type Authenticator interface {
	// Authenticate validates a token and returns the identity.
	Authenticate(ctx context.Context, token string) (*Identity, error)
	// Mode returns the authentication mode.
	Mode() AuthMode
}

// contextKey is a type for context keys.
type contextKey string

const identityKey contextKey = "auth.identity"

// WithIdentity adds an identity to the context.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// GetIdentity retrieves the identity from the context.
func GetIdentity(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityKey).(*Identity)
	return id
}

// IsAuthenticated returns true if the context has an authenticated identity.
func IsAuthenticated(ctx context.Context) bool {
	return GetIdentity(ctx) != nil
}

// CloseAuthenticators closes every authenticator that implements io.Closer,
// such as the SPIFFE authenticator's Workload API connection, and joins any errors.
func CloseAuthenticators(authenticators []Authenticator) error {
	var errs []error
	for _, a := range authenticators {
		if c, ok := a.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close %s authenticator: %w", a.Mode(), err))
			}
		}
	}
	return errors.Join(errs...)
}
