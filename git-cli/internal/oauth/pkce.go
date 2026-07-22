// Package oauth implements the Casdoor Authorization Code + PKCE flow for the
// AISphere Git CLI: it builds the authorize URL, runs the loopback callback
// server, exchanges the code for tokens, refreshes them, and verifies the
// resulting JWT. It reuses kernel/authn/casdoor's claims model so the resolved
// subject (Casdoor user UUID) matches what SpiceDB keys on.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE is a Proof Key for Code Exchange pair (RFC 7636, method S256).
type PKCE struct {
	Verifier  string // 43-128 char URL-safe random; sent at token exchange
	Challenge string // base64url(sha256(verifier)); sent in authorize request
}

// NewPKCE generates a fresh S256 PKCE pair. The verifier is 64 raw bytes
// base64url-encoded (86 chars), well within the 43-128 spec range.
func NewPKCE() (PKCE, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	challenge := sha256Challenge(verifier)
	return PKCE{Verifier: verifier, Challenge: challenge}, nil
}

func sha256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newState returns a high-entropy base64url state parameter for CSRF defense.
func newState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
