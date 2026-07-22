package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claims models the Casdoor access-token JWT payload. Casdoor's `sub` claim
// is the username while `id` is the stable user UUID — SpiceDB relationships
// key on the UUID, so SubjectID below prefers `id` (mirroring
// kernel/authn/casdoor.principalFromClaims).
type Claims struct {
	Issuer        string   `json:"iss"`
	Audience      []string `json:"aud"`
	Subject       string   `json:"sub"`        // username
	ID            string   `json:"id"`         // stable UUID
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName"`
	Email         string   `json:"email"`
	Owner         string   `json:"owner"`
	PreferredName string   `json:"preferred_username"`
	IssuedAt      int64    `json:"iat"`
	ExpiresAt     int64    `json:"exp"`
}

// SubjectID returns the stable principal identifier (UUID), preferring id
// over sub, matching kernel/authn/casdoor.principalFromClaims.
func (c Claims) SubjectID() string {
	if c.ID != "" {
		return c.ID
	}
	if c.Subject != "" {
		return c.Subject
	}
	return ""
}

// ParseTokenClaims decodes the JWT payload without signature verification.
// Signature is verified by the Gateway at request time; the CLI only needs
// the claims to populate the stored session and display status. It still
// rejects structurally invalid tokens.
func ParseTokenClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("not a jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// fall back to padded standard base64
		payload, err = base64.StdEncoding.DecodeString(padBase64(parts[1]))
		if err != nil {
			return Claims{}, fmt.Errorf("decode payload: %w", err)
		}
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, fmt.Errorf("parse claims: %w", err)
	}
	return c, nil
}

// IsExpired reports whether the token is past expiry with the given leeway.
func (c Claims) IsExpired(leeway time.Duration) bool {
	if c.ExpiresAt == 0 {
		return false // unknown, assume valid
	}
	return time.Now().Add(leeway).After(time.Unix(c.ExpiresAt, 0))
}

// VerifyAudience reports whether the expected audience is present.
func (c Claims) VerifyAudience(expected string) bool {
	for _, a := range c.Audience {
		if a == expected {
			return true
		}
	}
	return false
}

// ExchangeResult is the outcome of a code exchange.
type ExchangeResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Claims       Claims
}

// noopContext is a default context for helpers that do not receive one.
func noopContext() context.Context { return context.Background() }

func padBase64(s string) string {
	if mod := len(s) % 4; mod != 0 {
		s += strings.Repeat("=", 4-mod)
	}
	return s
}
