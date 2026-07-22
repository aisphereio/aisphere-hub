// Command git-credential-aisphere is the git credential helper invoked by Git
// during clone/fetch/push/LFS against the AISphere git endpoint. Git maps
// `helper = aisphere` to this binary.
//
// It implements the four helper subcommands over stdin/stdout:
//
//	capability — advertise authtype support (Git >= 2.43)
//	get        — return a Bearer access token (refreshing first if stale)
//	store      — no-op (we self-manage the session)
//	erase      — clear an invalid access token, keep the refresh token
//
// The token comes from ~/.aisphere/credentials.json (written by
// `git aisphere login`). If the session is missing/expired the helper prints a
// clear `git aisphere login` instruction to stderr and returns no credential,
// so Git surfaces the message.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aisphereio/aisphere-git-cli/internal/config"
	"github.com/aisphereio/aisphere-git-cli/internal/credential"
	"github.com/aisphereio/aisphere-git-cli/internal/oauth"
	"github.com/aisphereio/aisphere-git-cli/internal/store"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "git-credential-aisphere: missing operation (get|store|erase|capability)")
		os.Exit(2)
	}
	cfg := config.FromEnv()
	st := store.New(cfg.StoreDir)
	flow := oauth.New(cfg)

	helper := credential.New(cfg, st, refreshFn(flow, cfg))

	switch os.Args[1] {
	case "capability":
		helper.Capability(os.Stdout)
	case "get":
		if err := helper.Get(os.Stdin, os.Stdout); err != nil {
			// A missing/expired session is the common failure: tell the user
			// how to recover on stderr (Git shows stderr to the user).
			fmt.Fprintf(os.Stderr, "aisphere: %v\n", err)
			os.Exit(0) // exit 0 so Git does not abort other helpers
		}
	case "store":
		helper.Store(os.Stdin, os.Stdout)
	case "erase":
		helper.Erase(os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "git-credential-aisphere: unknown operation %q\n", os.Args[1])
		os.Exit(2)
	}
}

// refreshFn adapts oauth.Flow.Refresh into the store.Credentials-refresh
// signature expected by the credential helper. It preserves issuer/client/
// subject metadata and only rotates the access/refresh tokens.
func refreshFn(flow *oauth.Flow, cfg config.Config) func(store.Credentials) (store.Credentials, error) {
	return func(c store.Credentials) (store.Credentials, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tok, err := flow.Refresh(ctx, c.RefreshToken)
		if err != nil {
			// Treat invalid_grant / revoked refresh as a logged-out session.
			if isInvalidGrant(err) {
				return store.Credentials{}, store.ErrNoSession
			}
			return store.Credentials{}, fmt.Errorf("refresh token: %w", err)
		}
		refreshToken := tok.RefreshToken
		if refreshToken == "" {
			refreshToken = c.RefreshToken // Casdoor may not rotate it
		}
		claims, _ := oauth.ParseTokenClaims(tok.AccessToken)
		out := c
		out.AccessToken = tok.AccessToken
		out.AccessTokenExp = tok.Expiry
		out.RefreshToken = refreshToken
		out.RefreshedAt = time.Now()
		if claims.SubjectID() != "" {
			out.SubjectID = claims.SubjectID()
		}
		if claims.Name != "" {
			out.SubjectName = claims.Name
		}
		return out, nil
	}
}

// isInvalidGrant reports whether err looks like an OAuth invalid_grant
// (expired/revoked refresh token). We match on the error string because the
// oauth2 package wraps provider errors opaquely.
func isInvalidGrant(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, needle := range []string{"invalid_grant", "invalid refresh", "expired", "revoked"} {
		if containsFold(msg, needle) {
			return true
		}
	}
	return false
}

func containsFold(s, substr string) bool {
	return indexOfFold(s, substr) >= 0
}

func indexOfFold(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(s) < len(substr) {
		return -1
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
