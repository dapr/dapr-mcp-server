//go:build ignore

// This is a standalone test server that simulates Dapr Sentry's JWKS endpoint.
// Run with: go run mock_sentry_server.go
//
// It will:
// 1. Start a JWKS server on :8200
// 2. Print a valid JWT token you can use to test authentication
//
// Then test with:
// curl -H "X-My-Auth: <token>" http://localhost:8080/sse

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func main() {
	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}

	// Create JWKS
	jwk := jose.JSONWebKey{
		Key:       &privateKey.PublicKey,
		KeyID:     "sentry-key-1",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}
	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}

	// Create a test JWT
	token := createToken(privateKey, "sentry-key-1")

	// Print configuration
	fmt.Println("=== Mock Dapr Sentry JWKS Server ===")
	fmt.Println()
	fmt.Println("JWKS Endpoint: http://localhost:8200/.well-known/jwks.json")
	fmt.Println()
	fmt.Println("Environment variables for dapr-mcp-server:")
	fmt.Println("  export AUTH_MODE=dapr-sentry")
	fmt.Println("  export DAPR_SENTRY_ENABLED=true")
	fmt.Println("  export DAPR_SENTRY_JWKS_URL=http://localhost:8200/.well-known/jwks.json")
	fmt.Println("  export DAPR_SENTRY_TRUST_DOMAIN=public")
	fmt.Println("  export DAPR_SENTRY_AUDIENCE=public")
	fmt.Println("  export DAPR_SENTRY_TOKEN_HEADER=X-My-Auth")
	fmt.Println()
	fmt.Println("Test JWT Token (valid for 1 hour):")
	fmt.Println(token)
	fmt.Println()
	fmt.Println("Test command:")
	fmt.Printf("  curl -H \"X-My-Auth: %s\" http://localhost:8080/sse\n", token)
	fmt.Println()
	fmt.Println("Starting server on :8200...")

	// Serve JWKS
	http.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("JWKS request from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	})

	http.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("JWKS request from %s", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	})

	log.Fatal(http.ListenAndServe(":8200", nil))
}

func createToken(privateKey *rsa.PrivateKey, keyID string) string {
	signerOpts := jose.SignerOptions{}
	signerOpts.WithType("JWT")
	signerOpts.WithHeader("kid", keyID)

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: jose.RS256,
		Key:       privateKey,
	}, &signerOpts)
	if err != nil {
		log.Fatal(err)
	}

	claims := struct {
		jwt.Claims
		Use string `json:"use,omitempty"`
	}{
		Claims: jwt.Claims{
			Subject:   "spiffe://public/ns/default/17c9f2b4-859d-4249-8701-7e846540e704",
			Audience:  jwt.Audience{"public"},
			Expiry:    jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-1 * time.Minute)),
			ID:        "test-jwt-id-12345",
		},
		Use: "sig",
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		log.Fatal(err)
	}

	return token
}
