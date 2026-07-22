package oauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aisphereio/aisphere-git-cli/internal/config"
	"golang.org/x/oauth2"
)

// Flow runs the Casdoor Authorization Code + PKCE flow for the CLI.
type Flow struct {
	cfg config.Config
}

// New returns a Flow bound to the given config.
func New(cfg config.Config) *Flow {
	return &Flow{cfg: cfg}
}

// LoginResult is what `git aisphere login` stores.
type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Claims       Claims
}

// Login performs the full interactive login:
//  1. generate PKCE + state + nonce
//  2. start a loopback callback server
//  3. open the system browser at the authorize URL
//  4. receive the callback, validate state
//  5. exchange the code (with code_verifier, no client_secret) for tokens
//  6. decode the access-token claims
//
// It blocks until the callback arrives or the context is cancelled.
func (f *Flow) Login(ctx context.Context, openBrowser func(string) error) (LoginResult, error) {
	pkce, err := NewPKCE()
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate pkce: %w", err)
	}
	state, err := newState()
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate state: %w", err)
	}

	authorizeURL := f.buildAuthorizeURL(pkce.Challenge, state)

	// callback coordination
	var (
		mu        sync.Mutex
		cbCode    string
		cbState   string
		cbErr     string
		received  = make(chan struct{}, 1)
		consumed  bool
	)
	srv := &http.Server{
		Addr: f.cfg.CallbackHostPort(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			mu.Lock()
			already := consumed
			consumed = true
			mu.Unlock()
			if already {
				http.Error(w, "callback already consumed", http.StatusBadRequest)
				return
			}
			cbCode = q.Get("code")
			cbState = q.Get("state")
			cbErr = q.Get("error")
			// Respond to the browser with a simple page; the CLI exits after.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if cbErr != "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<h1>Login failed</h1><p>%s</p><p>You may close this tab.</p>", htmlEscape(cbErr))
			} else {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "<h1>Login successful</h1><p>You may close this tab and return to the terminal.</p>")
			}
			select {
			case received <- struct{}{}:
			default:
			}
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", f.cfg.CallbackHostPort())
	if err != nil {
		return LoginResult{}, fmt.Errorf("listen on %s: %w (is another login in progress or the port in use?)", f.cfg.CallbackHostPort(), err)
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()

	// Open the browser AFTER the listener is up so the redirect lands.
	// Always print the authorize URL too: rundll32 may "succeed" without
	// actually showing a browser (headless/sandboxed sessions), and the URL
	// must be opened verbatim because it carries this run's PKCE challenge
	// and state — a hand-written URL would break the code exchange.
	fmt.Fprintf(stderrWriter(), "Open this URL to sign in (browser launch attempted, open manually if it did not appear):\n%s\n", authorizeURL)
	if openBrowser != nil {
		if err := openBrowser(authorizeURL); err != nil {
			fmt.Fprintf(stderrWriter(), "Browser launch error: %v\n", err)
		}
	}

	select {
	case <-received:
	case <-ctx.Done():
		return LoginResult{}, ctx.Err()
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return LoginResult{}, fmt.Errorf("callback server: %w", err)
		}
		return LoginResult{}, errors.New("callback server stopped before login completed")
	}

	if cbErr != "" {
		return LoginResult{}, fmt.Errorf("login rejected by provider: %s", cbErr)
	}
	if cbState == "" || cbState != state {
		return LoginResult{}, errors.New("oauth state mismatch (possible CSRF); aborting")
	}
	if cbCode == "" {
		return LoginResult{}, errors.New("no authorization code in callback")
	}

	tokens, err := f.exchangeCode(ctx, cbCode, pkce.Verifier)
	if err != nil {
		return LoginResult{}, err
	}
	claims, err := ParseTokenClaims(tokens.AccessToken)
	if err != nil {
		return LoginResult{}, fmt.Errorf("parse access token: %w", err)
	}
	return LoginResult{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    tokens.Expiry,
		Claims:       claims,
	}, nil
}

// buildAuthorizeURL constructs the Casdoor authorize endpoint URL with the
// PKCE challenge, state, nonce and the configured redirect_uri. We build it
// manually rather than via the SDK's GetSigninUrl because that helper does
// not append code_challenge.
func (f *Flow) buildAuthorizeURL(challenge, state string) string {
	endpoint := strings.TrimRight(f.cfg.Issuer, "/")
	q := url.Values{}
	q.Set("client_id", f.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", f.cfg.CallbackURI())
	q.Set("scope", "openid profile email")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	// Casdoor serves the browser-facing authorize UI at /login/oauth/authorize
	// (no /api prefix); only the token endpoint lives under /api/. Verified
	// against the OIDC discovery `authorization_endpoint`.
	return endpoint + "/login/oauth/authorize?" + q.Encode()
}

// exchangeCode swaps the authorization code for tokens. Per RFC 8252 a native
// public client must NOT send client_secret; we only send client_id and the
// code_verifier.
func (f *Flow) exchangeCode(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	endpoint := strings.TrimRight(f.cfg.Issuer, "/")
	oauthCfg := &oauth2.Config{
		ClientID:    f.cfg.ClientID,
		RedirectURL: f.cfg.CallbackURI(),
		Endpoint: oauth2.Endpoint{
			AuthURL:   endpoint + "/login/oauth/authorize",
			TokenURL:  endpoint + "/api/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	return oauthCfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
}

// Refresh exchanges a refresh token for a new access token. As with the code
// exchange, no client_secret is sent (public client). If Casdoor returns
// invalid_grant the caller should treat the session as expired.
func (f *Flow) Refresh(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	endpoint := strings.TrimRight(f.cfg.Issuer, "/")
	oauthCfg := &oauth2.Config{
		ClientID:    f.cfg.ClientID,
		RedirectURL: f.cfg.CallbackURI(),
		Endpoint: oauth2.Endpoint{
			TokenURL:  endpoint + "/api/login/oauth/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	src := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return src.Token()
}

// htmlEscape escapes a string for embedding in the minimal HTML response the
// callback server returns to the browser.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&#34;")
	return s
}

// stderrWriter returns os.Stderr (extracted so the package does not import os
// in multiple files; the helper lives next to its only caller).
func stderrWriter() *os.File { return os.Stderr }
