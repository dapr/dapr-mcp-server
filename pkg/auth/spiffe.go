// Package auth provides authentication and authorization for the MCP server.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// SPIFFEConnectionTimeout is the maximum time to wait for SPIRE agent connection.
const SPIFFEConnectionTimeout = 10 * time.Second

// ErrSPIFFEClientNotAllowed is returned when a valid SPIFFE ID is not in the configured allowlist.
var ErrSPIFFEClientNotAllowed = errors.New("SPIFFE client not in allowlist")

// SPIFFEAuthenticator authenticates requests using SPIFFE JWT-SVIDs.
type SPIFFEAuthenticator struct {
	config   SPIFFEConfig
	bundles  jwtbundle.Source
	closer   io.Closer
	serverID spiffeid.ID
	policy   spiffePolicy
}

// spiffePolicy decides which SPIFFE IDs may call the server.
type spiffePolicy struct {
	trustDomain spiffeid.TrustDomain
	// allowed is empty when every workload in the trust domain is accepted.
	allowed map[spiffeid.ID]struct{}
}

// newSPIFFEPolicy parses the trust domain and allowlist, rejecting malformed entries
// and allowlisted IDs outside the trust domain.
func newSPIFFEPolicy(cfg SPIFFEConfig) (spiffePolicy, error) {
	td, err := spiffeid.TrustDomainFromString(cfg.TrustDomain)
	if err != nil {
		return spiffePolicy{}, fmt.Errorf("parse SPIFFE trust domain %q: %w", cfg.TrustDomain, err)
	}

	allowed := make(map[spiffeid.ID]struct{}, len(cfg.AllowedClients))
	for _, raw := range cfg.AllowedClients {
		id, err := spiffeid.FromString(raw)
		if err != nil {
			return spiffePolicy{}, fmt.Errorf("parse allowed SPIFFE client %q: %w", raw, err)
		}
		if !id.MemberOf(td) {
			return spiffePolicy{}, fmt.Errorf("allowed SPIFFE client %q is not in trust domain %q", raw, td.Name())
		}
		allowed[id] = struct{}{}
	}
	return spiffePolicy{trustDomain: td, allowed: allowed}, nil
}

// authorize checks that id belongs to the trust domain and, when an allowlist is set, is on it.
func (p spiffePolicy) authorize(id spiffeid.ID) error {
	if !id.MemberOf(p.trustDomain) {
		return fmt.Errorf("%w: trust domain mismatch", ErrInvalidIssuer)
	}
	if len(p.allowed) == 0 {
		return nil
	}
	if _, ok := p.allowed[id]; !ok {
		return fmt.Errorf("%w: %w", ErrInvalidToken, ErrSPIFFEClientNotAllowed)
	}
	return nil
}

// NewSPIFFEAuthenticator creates a new SPIFFE authenticator.
// The configuration is validated before connecting to the Workload API.
func NewSPIFFEAuthenticator(ctx context.Context, cfg SPIFFEConfig) (*SPIFFEAuthenticator, error) {
	serverID, err := spiffeid.FromString(cfg.ServerID)
	if err != nil {
		return nil, fmt.Errorf("parse server SPIFFE ID: %w", err)
	}
	policy, err := newSPIFFEPolicy(cfg)
	if err != nil {
		return nil, err
	}

	var opts []workloadapi.JWTSourceOption
	if cfg.EndpointSocket != "" {
		opts = append(opts, workloadapi.WithClientOptions(
			workloadapi.WithAddr(cfg.EndpointSocket),
		))
	}

	connectCtx, cancel := context.WithTimeout(ctx, SPIFFEConnectionTimeout)
	defer cancel()

	source, err := workloadapi.NewJWTSource(connectCtx, opts...)
	if err != nil {
		if errors.Is(connectCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("connect to SPIRE agent at %s (waited %v): %w", cfg.EndpointSocket, SPIFFEConnectionTimeout, err)
		}
		return nil, fmt.Errorf("create JWT source: %w", err)
	}

	return &SPIFFEAuthenticator{
		config:   cfg,
		bundles:  source,
		closer:   source,
		serverID: serverID,
		policy:   policy,
	}, nil
}

// Authenticate validates a SPIFFE JWT-SVID and returns the identity.
func (a *SPIFFEAuthenticator) Authenticate(_ context.Context, token string) (*Identity, error) {
	svid, err := jwtsvid.ParseAndValidate(token, a.bundles, []string{a.serverID.String()})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if err := a.policy.authorize(svid.ID); err != nil {
		return nil, err
	}

	return &Identity{
		Subject:    svid.ID.String(),
		Issuer:     svid.ID.TrustDomain().String(),
		Audience:   svid.Audience,
		Claims:     svid.Claims,
		AuthMethod: ModeSPIFFE,
	}, nil
}

// Mode returns the authentication mode.
func (a *SPIFFEAuthenticator) Mode() AuthMode {
	return ModeSPIFFE
}

// Close closes the Workload API connection. It is safe to call on a partially built authenticator.
func (a *SPIFFEAuthenticator) Close() error {
	if a == nil || a.closer == nil {
		return nil
	}
	return a.closer.Close()
}
