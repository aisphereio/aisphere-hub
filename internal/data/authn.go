// Package data authn module — Casdoor OAuth adapter through kernel authn.
//
// This file implements biz.AuthnRepo. It bridges hub's authn usecase to the
// kernel authn contracts (Authenticator, LoginService, LogoutService,
// TokenService) provided by github.com/aisphereio/kernel/authn/casdoor.
//
// One kernel-side/runtime gap is handled here so the biz layer stays clean:
//
//  1. Session blacklist: kernel authn does not maintain a hub-local token
//     blacklist. We use cachex.Cache when available; otherwise we fall back
//     to an in-memory sync.Map. The blacklist stores the SHA-256 hash of the
//     token (not the token itself) with TTL = token exp - now, so the entry
//     auto-expires when the token would have expired anyway.

package data

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/cachex"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

type authnRepo struct {
	resources *Resources
	cfg       conf.AuthnConfig
	// blacklist is the in-memory fallback used when Cache is nil.
	blacklist sync.Map // key = sha256(token) -> expiry time.Time
}

const defaultLoginScope = "openid profile email"

// NewAuthnRepo creates a new biz.AuthnRepo backed by kernel authn + Casdoor.
//
// cfg is the hub authn config; it is used to look up the configured Casdoor
// endpoint. Pass resources so the repo can reach the kernel Casdoor client
// and the cache for the local blacklist.
func NewAuthnRepo(resources *Resources, cfg conf.AuthnConfig) biz.AuthnRepo {
	return &authnRepo{resources: resources, cfg: cfg}
}

func (r *authnRepo) logger() logx.Logger {
	if r != nil && r.resources != nil && r.resources.Logger != nil {
		return r.resources.Logger.Named("authn.repo")
	}
	return logx.DefaultLogger().Named("authn.repo")
}

func (r *authnRepo) metrics() metricsx.Manager {
	if r != nil && r.resources != nil {
		return metricsx.Ensure(r.resources.Metrics)
	}
	return metricsx.Noop()
}

// LoginURL builds the IdP hosted login URL through kernel authn.LoginService.
//
// Always uses the raw casdoor.Client (Resources.LoginService), never the
// cached wrapper — login URL construction is cheap and includes a per-request
// state parameter that should NOT be cached.
func (r *authnRepo) LoginURL(ctx context.Context, req biz.AuthnLoginURLRequest) (out string, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "login_url", logx.Bool("has_state", req.State != ""))
	defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "login_url", started, err) }()
	svc, err := r.loginService()
	if err != nil {
		return "", err
	}
	loginURL, err := svc.BuildLoginURL(ctx, authn.LoginURLRequest{
		RedirectURI: req.RedirectURI,
		State:       req.State,
		Scope:       loginScope(req.Scope),
		OrgID:       req.OrgID,
		AppID:       req.AppID,
	})
	if err != nil {
		return "", err
	}
	return loginURL.URL, nil
}

// Exchange exchanges an OAuth authorization code for IdP tokens through
// kernel authn.TokenService.
//
// Uses Resources.TokenService (which may be a CachedClient). CachedClient
// delegates ExchangeCode to the inner provider without caching, which is
// correct — code exchange is one-shot and the returned tokens are unique
// per call.
//
// NOTE: kernel/authn/casdoor/token.go:ExchangeCode does not currently forward
// CodeVerifier to the Casdoor SDK GetOAuthToken call. We pass it through
// AuthnExchangeURLRequest so the biz/proto surface is forward-compatible,
// but PKCE enforcement will not kick in until the kernel adapter is updated.
func (r *authnRepo) Exchange(ctx context.Context, req biz.AuthnExchangeURLRequest) (out *biz.AuthnToken, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "exchange", logx.Bool("has_code", strings.TrimSpace(req.Code) != ""))
	defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "exchange", started, err) }()
	svc, err := r.tokenService()
	if err != nil {
		return nil, err
	}
	tokenSet, _, err := svc.ExchangeCode(ctx, authn.AuthCodeExchangeRequest{
		Code:         req.Code,
		State:        req.State,
		RedirectURI:  req.RedirectURI,
		CodeVerifier: req.CodeVerifier,
		OrgID:        req.OrgID,
		AppID:        req.AppID,
	})
	if err != nil {
		return nil, err
	}
	return authTokenFromKernel(tokenSet), nil
}

// Refresh refreshes an IdP access token through kernel authn.TokenService.
//
// Uses Resources.TokenService (which may be a CachedClient). CachedClient
// delegates RefreshToken to the inner provider without caching, which is
// correct — refresh issues a brand-new access token that should not be
// served from any cache.
func (r *authnRepo) Refresh(ctx context.Context, req biz.AuthnRefreshRequest) (out *biz.AuthnToken, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "refresh", logx.Bool("has_refresh_token", strings.TrimSpace(req.RefreshToken) != ""))
	defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "refresh", started, err) }()
	svc, err := r.tokenService()
	if err != nil {
		return nil, err
	}
	tokenSet, err := svc.RefreshToken(ctx, authn.RefreshTokenRequest{
		RefreshToken: req.RefreshToken,
		Scope:        req.Scope,
		OrgID:        req.OrgID,
		AppID:        req.AppID,
	})
	if err != nil {
		return nil, err
	}
	return authTokenFromKernel(tokenSet), nil
}

// LogoutURL builds the IdP logout (end-session) URL through kernel
	// authn.LogoutService.
	//
	// Always uses the raw casdoor.Client (Resources.LogoutService), never the
	// cached wrapper — logout URL construction is cheap and includes per-request
	// state that should NOT be cached.
	func (r *authnRepo) LogoutURL(ctx context.Context, req biz.AuthnLogoutURLRequest) (out string, err error) {
		ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "logout_url", logx.Bool("has_state", req.State != ""), logx.Bool("has_id_token_hint", req.IDTokenHint != ""))
		defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "logout_url", started, err) }()
		svc, err := r.logoutService()
		if err != nil {
			return "", err
		}
		logoutURL, err := svc.BuildLogoutURL(ctx, authn.LogoutURLRequest{
			PostLogoutRedirectURI: req.PostLogoutRedirectURI,
			IDTokenHint:           req.IDTokenHint,
			State:                 req.State,
			OrgID:                 req.OrgID,
			AppID:                 req.AppID,
		})
		if err != nil {
			return "", err
		}
		return logoutURL.URL, nil
	}

// Revoke attempts to revoke a token at the IdP through kernel authn.TokenService.
//
// kernel/authn/casdoor/token.go:RevokeToken currently returns
// ErrIdentityBackendFailed because the Casdoor SDK does not expose a token
// revocation helper. We trap that specific error and return IDPRevoked=false
// instead of propagating it; the biz layer's Revoke method always also calls
// RevokeLocal, so the token is still rejected at hub via the local blacklist.
//
// Cache interaction: when TokenService is a *authn.CachedClient, a successful
// RevokeToken call already invalidates the cache (see CachedClient.RevokeToken).
// When RevokeToken fails (e.g. UNIMPLEMENTED for Casdoor), we ALSO call
// Invalidate explicitly so the next VerifyToken does not return the cached
// Principal. This is important because hub's local blacklist (checked in
// biz.Me and biz.Introspect) catches revoked tokens, but middleware-driven
// Authenticate calls go straight to the kernel cache without checking the
// hub blacklist — so the cache MUST be invalidated or revoked tokens would
// keep working through middleware for up to TTL.
func (r *authnRepo) Revoke(ctx context.Context, req biz.AuthnRevokeRequest) (result *biz.AuthnRevokeResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "revoke", logx.Bool("has_token", strings.TrimSpace(req.Token) != ""), logx.String("token_type", req.TokenType))
	defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "revoke", started, err) }()
	svc, err := r.tokenService()
	if err != nil {
		return nil, err
	}
	err = svc.RevokeToken(ctx, authn.RevokeTokenRequest{
		Token:     req.Token,
		TokenType: req.TokenType,
		OrgID:     req.OrgID,
		AppID:     req.AppID,
	})
	if err == nil {
		// CachedClient.RevokeToken already invalidated the cache.
		return &biz.AuthnRevokeResult{Revoked: true, IDPRevoked: true}, nil
	}
	// IdP revocation failed (e.g. Casdoor adapter UNIMPLEMENTED). Force-
	// invalidate the cache so middleware-driven Authenticate calls do not
	// keep serving the cached Principal for the revoked token.
	if r.resources != nil && r.resources.CachedTokenService != nil {
		_ = r.resources.CachedTokenService.Invalidate(ctx, req.Token)
	}
	return &biz.AuthnRevokeResult{
		Revoked:    false,
		IDPRevoked: false,
		Message:    fmt.Sprintf("idp revocation not supported: %v", err),
	}, nil
}

// RevokeLocal adds the token's SHA-256 hash to the local session blacklist
// with TTL = token exp - now. When Cache is configured, we use it; otherwise
// we fall back to an in-memory sync.Map.
//
// We hash the token rather than storing it raw so that cache dumps (e.g.
// Redis MONITOR) do not leak live bearer tokens.
//
// Note: the local blacklist and the kernel CachedClient share the same
// cachex instance but use different key prefixes ("authn:revoked:" vs
// "authn:token:"), so they do not collide.
func (r *authnRepo) RevokeLocal(ctx context.Context, token string) (err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "revoke_local", logx.Bool("has_token", strings.TrimSpace(token) != ""))
	defer func() { observability.End(ctx, logger, r.metrics(), "authn.repo", "revoke_local", started, err) }()
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	ttl := bearerTokenTTL(token)
	if ttl <= 0 {
		ttl = 24 * time.Hour // fallback for malformed tokens
	}
	key := tokenBlacklistKey(token)
	expiry := time.Now().Add(ttl)
	if r.resources != nil && r.resources.Cache != nil {
		if err := r.resources.Cache.Set(ctx, key, expiry.Unix(), ttl); err != nil {
			return err
		}
	} else {
		r.blacklist.Store(key, expiry)
	}
	// Also invalidate the kernel authn cache so middleware-driven requests
	// with the revoked token stop returning the cached Principal. Without
	// this, a revoked token would keep working through middleware for up to
	// the cache TTL even though hub's Me/Introspect correctly reject it.
	if r.resources != nil && r.resources.CachedTokenService != nil {
		_ = r.resources.CachedTokenService.Invalidate(ctx, token)
	}
	return nil
}

// IsRevoked checks whether a token is in the local session blacklist.
func (r *authnRepo) IsRevoked(ctx context.Context, token string) (revoked bool, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "is_revoked", logx.Bool("has_token", strings.TrimSpace(token) != ""))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authn.repo", "is_revoked", started, err, logx.Bool("revoked", revoked))
	}()
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	key := tokenBlacklistKey(token)
	if r.resources != nil && r.resources.Cache != nil {
		var expiry int64
		err := r.resources.Cache.Get(ctx, key, &expiry)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, cachex.ErrNotFound) {
			return false, err
		}
		return false, nil
	}
	v, ok := r.blacklist.Load(key)
	if !ok {
		return false, nil
	}
	expiry, _ := v.(time.Time)
	if time.Now().After(expiry) {
		r.blacklist.Delete(key)
		return false, nil
	}
	return true, nil
}

// Verify validates a token at the IdP through kernel authn.TokenService or
// the configured Authenticator. We prefer TokenService.VerifyToken because
// it accepts richer VerifyTokenRequest (issuer, audience, leeway), and
// because TokenService is wrapped by CachedClient when cache is available —
// so high-frequency VerifyToken calls (e.g. from biz.Me on every /me
// request) hit the cache instead of re-parsing the JWT every time.
//
// When TokenService is unavailable (e.g. a custom Authenticator that doesn't
// implement TokenService), falls back to Authenticator.Authenticate.
func (r *authnRepo) Verify(ctx context.Context, req biz.AuthnIntrospectRequest) (principal authn.Principal, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authn.repo", "verify", logx.Bool("has_token", strings.TrimSpace(req.Token) != ""), logx.String("token_type", req.TokenType))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authn.repo", "verify", started, err, logx.Bool("authenticated", principal.IsAuthenticated()))
	}()
	if r.resources == nil {
		return authn.Principal{}, authn.ErrIdentityBackendFailed("resources are not configured", nil)
	}
	if svc, err := r.tokenService(); err == nil {
		return svc.VerifyToken(ctx, authn.VerifyTokenRequest{
			Token:     req.Token,
			TokenType: req.TokenType,
			Issuer:    req.Issuer,
			Audience:  req.Audience,
			OrgID:     req.OrgID,
			AppID:     req.AppID,
		})
	}
	if r.resources.Authn == nil {
		return authn.Principal{}, authn.ErrIdentityBackendFailed("authenticator is not configured", nil)
	}
	return r.resources.Authn.Authenticate(ctx, authn.Credential{
		Scheme: authn.CredentialBearer,
		Token:  req.Token,
	})
}

// --- internal helpers ---

func (r *authnRepo) loginService() (authn.LoginService, error) {
	if r.resources == nil || r.resources.LoginService == nil {
		return nil, errorx.Unavailable(errorx.Code("AUTHN_LOGIN_SERVICE_NOT_CONFIGURED"), "casdoor login service is not configured; enable security.authn and set provider=casdoor")
	}
	return r.resources.LoginService, nil
}

func loginScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope != "" {
		return scope
	}
	return defaultLoginScope
}

func (r *authnRepo) tokenService() (authn.TokenService, error) {
	if r.resources == nil || r.resources.TokenService == nil {
		return nil, errorx.Unavailable(errorx.Code("AUTHN_TOKEN_SERVICE_NOT_CONFIGURED"), "casdoor token service is not configured; enable security.authn and set provider=casdoor")
	}
	return r.resources.TokenService, nil
}

func (r *authnRepo) logoutService() (authn.LogoutService, error) {
	if r.resources == nil || r.resources.LogoutService == nil {
		return nil, errorx.Unavailable(errorx.Code("AUTHN_LOGOUT_SERVICE_NOT_CONFIGURED"), "casdoor logout service is not configured; enable security.authn and set provider=casdoor")
	}
	return r.resources.LogoutService, nil
}

func authTokenFromKernel(t authn.TokenSet) *biz.AuthnToken {
	return &biz.AuthnToken{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		IDToken:      t.IDToken,
		TokenType:    t.TokenType,
		Scope:        t.Scope,
		ExpiresAt:    t.ExpiresAt,
	}
}

// tokenBlacklistKey returns the cache key for a token in the local session
// blacklist. We hash with SHA-256 and hex-encode to keep keys URL-safe and
// cache-size-bounded.
func tokenBlacklistKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "authn:revoked:" + hex.EncodeToString(sum[:])
}

// bearerTokenTTL extracts the `exp` claim from a JWT without verifying the
// signature and returns time.Until(exp). This is only used for setting the
// blacklist TTL — the actual token verification still happens through the
// kernel Authenticator at request time. A malformed or non-JWT token falls
// back to 24h.
func bearerTokenTTL(token string) time.Duration {
	const fallback = 24 * time.Hour
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return fallback
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return fallback
		}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return fallback
	}
	ttl := time.Until(time.Unix(int64(claims.Exp), 0))
	if ttl <= 0 {
		return time.Minute
	}
	return ttl
}
