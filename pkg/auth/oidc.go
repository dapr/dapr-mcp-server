// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCAuthenticator authenticates requests using OIDC tokens.
type OIDCAuthenticator struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	config   OIDCConfig
}

// NewOIDCAuthenticator creates a new OIDC authenticator that logs to slog.Default.
func NewOIDCAuthenticator(ctx context.Context, cfg OIDCConfig) (*OIDCAuthenticator, error) {
	return NewOIDCAuthenticatorWithLogger(ctx, cfg, nil)
}

// NewOIDCAuthenticatorWithLogger creates a new OIDC authenticator with a custom logger.
// A nil logger means slog.Default.
func NewOIDCAuthenticatorWithLogger(ctx context.Context, cfg OIDCConfig, logger *slog.Logger) (*OIDCAuthenticator, error) {
	if logger == nil {
		logger = slog.Default()
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("create OIDC provider: %w", err)
	}

	if cfg.SkipIssuerCheck {
		logger.Warn("OIDC issuer check is disabled; tokens from any issuer signed by the provider's keys are accepted. Use only for development.",
			"issuer_url", cfg.IssuerURL)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID:             cfg.ClientID,
		SkipIssuerCheck:      cfg.SkipIssuerCheck,
		SupportedSigningAlgs: cfg.AllowedAlgorithms,
	})

	return &OIDCAuthenticator{
		provider: provider,
		verifier: verifier,
		config:   cfg,
	}, nil
}

// Authenticate validates an OIDC token and returns the identity.
// Identity.Email is set only when the provider marks the address as verified.
func (a *OIDCAuthenticator) Authenticate(ctx context.Context, token string) (*Identity, error) {
	idToken, err := a.verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: parse token claims: %w", ErrInvalidToken, err)
	}

	var allClaims map[string]interface{}
	if err := idToken.Claims(&allClaims); err != nil {
		return nil, fmt.Errorf("%w: parse token claims: %w", ErrInvalidToken, err)
	}

	identity := &Identity{
		Subject:    idToken.Subject,
		Issuer:     idToken.Issuer,
		Audience:   idToken.Audience,
		Name:       claims.Name,
		Claims:     allClaims,
		AuthMethod: ModeOIDC,
	}
	if claims.EmailVerified {
		identity.Email = claims.Email
	}
	return identity, nil
}

// Mode returns the authentication mode.
func (a *OIDCAuthenticator) Mode() AuthMode {
	return ModeOIDC
}
