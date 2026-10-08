package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/bundle/jwtbundle"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	spiffeTestTrustDomain = "cluster.local"
	spiffeTestServerID    = "spiffe://cluster.local/server"
	spiffeTestClient      = "spiffe://cluster.local/ns/default/sa/client"
	spiffeTestKeyID       = "svid-key"
)

func TestNewSPIFFEPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     SPIFFEConfig
		wantErr bool
	}{
		{name: "no allowlist", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain}},
		{name: "valid allowlist", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, AllowedClients: []string{spiffeTestClient}}},
		{name: "invalid trust domain", cfg: SPIFFEConfig{TrustDomain: "Not Valid!"}, wantErr: true},
		{name: "empty trust domain", cfg: SPIFFEConfig{}, wantErr: true},
		{name: "malformed client", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, AllowedClients: []string{"client"}}, wantErr: true},
		{name: "client with userinfo", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, AllowedClients: []string{"spiffe://cluster.local@evil/x"}}, wantErr: true},
		{name: "client outside trust domain", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, AllowedClients: []string{"spiffe://other/x"}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := newSPIFFEPolicy(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSPIFFEPolicyAuthorize(t *testing.T) {
	t.Parallel()
	open, err := newSPIFFEPolicy(SPIFFEConfig{TrustDomain: spiffeTestTrustDomain})
	require.NoError(t, err)
	restricted, err := newSPIFFEPolicy(SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, AllowedClients: []string{spiffeTestClient}})
	require.NoError(t, err)

	tests := []struct {
		name    string
		policy  spiffePolicy
		id      string
		wantErr error
	}{
		{name: "open policy accepts member", policy: open, id: spiffeTestClient},
		{name: "open policy rejects other trust domain", policy: open, id: "spiffe://other/x", wantErr: ErrInvalidIssuer},
		{name: "allowlist accepts listed", policy: restricted, id: spiffeTestClient},
		{name: "allowlist rejects unlisted", policy: restricted, id: "spiffe://cluster.local/ns/default/sa/other", wantErr: ErrSPIFFEClientNotAllowed},
		{name: "allowlist rejects other trust domain", policy: restricted, id: "spiffe://other/ns/default/sa/client", wantErr: ErrInvalidIssuer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.policy.authorize(spiffeid.RequireFromString(tt.id))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNewSPIFFEAuthenticator_ValidatesBeforeConnecting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  SPIFFEConfig
	}{
		{name: "invalid server ID", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, ServerID: "server"}},
		{name: "invalid allowed client", cfg: SPIFFEConfig{TrustDomain: spiffeTestTrustDomain, ServerID: spiffeTestServerID, AllowedClients: []string{"nope"}}},
		{name: "invalid trust domain", cfg: SPIFFEConfig{TrustDomain: "Bad Domain", ServerID: spiffeTestServerID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := NewSPIFFEAuthenticator(context.Background(), tt.cfg)
			require.Error(t, err)
			assert.Nil(t, a)
		})
	}
}

// newBundleSPIFFEAuthenticator builds an authenticator backed by an in-memory JWT bundle.
func newBundleSPIFFEAuthenticator(t *testing.T, allowed []string) (*SPIFFEAuthenticator, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	cfg := SPIFFEConfig{Enabled: true, TrustDomain: spiffeTestTrustDomain, ServerID: spiffeTestServerID, AllowedClients: allowed}
	policy, err := newSPIFFEPolicy(cfg)
	require.NoError(t, err)

	bundle := jwtbundle.New(spiffeid.RequireTrustDomainFromString(spiffeTestTrustDomain))
	require.NoError(t, bundle.AddJWTAuthority(spiffeTestKeyID, &key.PublicKey))

	return &SPIFFEAuthenticator{
		config:   cfg,
		bundles:  bundle,
		serverID: spiffeid.RequireFromString(spiffeTestServerID),
		policy:   policy,
	}, key
}

func signSVID(t *testing.T, key *ecdsa.PrivateKey, subject string, audience string) string {
	t.Helper()
	return signToken(t, jose.ES256, key, spiffeTestKeyID, jwt.Claims{
		Subject:  subject,
		Audience: jwt.Audience{audience},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
}

func TestSPIFFEAuthenticator_Authenticate(t *testing.T) {
	t.Parallel()
	a, key := newBundleSPIFFEAuthenticator(t, []string{spiffeTestClient})
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tests := []struct {
		name    string
		token   string
		wantErr error
	}{
		{name: "allowed client", token: signSVID(t, key, spiffeTestClient, spiffeTestServerID)},
		{name: "unlisted client", token: signSVID(t, key, "spiffe://cluster.local/ns/default/sa/other", spiffeTestServerID), wantErr: ErrSPIFFEClientNotAllowed},
		{name: "wrong audience", token: signSVID(t, key, spiffeTestClient, "spiffe://cluster.local/other-server"), wantErr: ErrInvalidToken},
		{name: "foreign signature", token: signSVID(t, otherKey, spiffeTestClient, spiffeTestServerID), wantErr: ErrInvalidToken},
		{name: "garbage", token: "not-a-jwt", wantErr: ErrInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			identity, err := a.Authenticate(context.Background(), tt.token)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, spiffeTestClient, identity.Subject)
			assert.Equal(t, ModeSPIFFE, identity.AuthMethod)
			assert.Equal(t, spiffeTestTrustDomain, identity.Issuer)
		})
	}
}

type closeRecorder struct {
	stubAuthenticator
	closed bool
	err    error
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return c.err
}

func TestSPIFFEAuthenticatorCloseWithoutSource(t *testing.T) {
	t.Parallel()
	var nilAuth *SPIFFEAuthenticator
	require.NoError(t, nilAuth.Close())
	require.NoError(t, (&SPIFFEAuthenticator{}).Close())

	rec := &closeRecorder{}
	require.NoError(t, (&SPIFFEAuthenticator{closer: rec}).Close())
	assert.True(t, rec.closed)
}

func TestCloseAuthenticators(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	ok := &closeRecorder{}
	failing := &closeRecorder{err: errBoom}

	err := CloseAuthenticators([]Authenticator{ok, stubAuthenticator{}, failing})

	require.ErrorIs(t, err, errBoom)
	assert.True(t, ok.closed)
	assert.True(t, failing.closed)
	require.NoError(t, CloseAuthenticators(nil))
}
