// Package biz authn module — provider-neutral authentication usecase.
//
// This usecase orchestrates OAuth/OIDC login URL construction, authorization-
// code exchange, refresh, logout URL creation, token revocation, token
// introspection and current-principal retrieval through a provider-neutral
// AuthnRepo implemented in the data layer.
//
// Hub does not issue local access tokens. All token operations are delegated
// to the configured identity provider (Casdoor by default through
// github.com/aisphereio/kernel/authn/casdoor).
//
// The repo interface returns kernel authn.Principal directly (not a hub-local
// copy) so that the service layer can map it 1:1 onto the proto Principal
// without losing fields. The usecase layer is intentionally thin: it validates
// inputs, records audit events when an audit recorder is wired, and forwards
// to the repo.
package biz

import (
	"context"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

// Authn action / resource constants used for audit recording. These mirror
// the legacy backend's constants but are scoped to "authn.*" to match the
// new service name.
const (
	AuthnActionLoginURL   = "authn.login_url"
	AuthnActionExchange   = "authn.exchange"
	AuthnActionRefresh    = "authn.refresh"
	AuthnActionLogoutURL  = "authn.logout_url"
	AuthnActionRevoke     = "authn.revoke"
	AuthnActionIntrospect = "authn.introspect"
	AuthnActionMe         = "authn.me"
	AuthnResourceSession  = "aisphere:hub:authn:session"
)

// AuthnLoginURLRequest is the biz-side input for LoginURL.
type AuthnLoginURLRequest struct {
	RedirectURI string
	State       string
	Scope       string
	Prompt      string
	OrgID       string
	AppID       string
}

// AuthnExchangeURLRequest is the biz-side input for Exchange.
type AuthnExchangeURLRequest struct {
	Code         string
	RedirectURI  string
	State        string
	CodeVerifier string
	OrgID        string
	AppID        string
}

// AuthnRefreshRequest is the biz-side input for Refresh.
type AuthnRefreshRequest struct {
	RefreshToken string
	Scope        string
	OrgID        string
	AppID        string
}

// AuthnLogoutURLRequest is the biz-side input for LogoutURL.
type AuthnLogoutURLRequest struct {
	PostLogoutRedirectURI string
	IDTokenHint           string
	State                 string
	// AccessToken identifies the token to revoke locally before returning the
	// IdP logout URL. When empty, the usecase will try to read it from the
	// request context (set by the service layer from the Authorization header).
	AccessToken string
	OrgID       string
	AppID       string
}

// AuthnRevokeRequest is the biz-side input for Revoke.
type AuthnRevokeRequest struct {
	Token     string
	TokenType string
	OrgID     string
	AppID     string
}

// AuthnIntrospectRequest is the biz-side input for Introspect.
type AuthnIntrospectRequest struct {
	Token     string
	TokenType string
	Issuer    string
	Audience  []string
	OrgID     string
	AppID     string
}

// AuthnToken mirrors kernel authn.TokenSet minus Raw AttributeSet. We drop
// Raw because it carries provider-specific extras that have no business in
// the biz layer; the service layer can pull them from the kernel TokenSet
// when needed (currently only access_token round-trips, and that is already
// in AccessToken).
type AuthnToken struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	Scope        string
	ExpiresAt    time.Time
}

// AuthnRevokeResult is the biz-side output for Revoke. It distinguishes
// "IdP-side revoked" from "only local blacklist updated" because the latter
// is best-effort and the caller may need to know the token is still valid
// against other Resource Servers.
type AuthnRevokeResult struct {
	Revoked    bool
	IDPRevoked bool
	Message    string
}

// AuthnIntrospectResult is the biz-side output for Introspect.
type AuthnIntrospectResult struct {
	Active    bool
	Principal authn.Principal
	Scope     string
	ExpiresAt time.Time
	IssuedAt  time.Time
}

// AuthnRepo abstracts the identity-provider-facing operations.
//
// The interface mirrors kernel authn.Authenticator + authn.TokenService plus
// two extra methods (LogoutURL, RevokeLocal) that kernel does not provide
// directly:
//   - LogoutURL is not part of kernel authn because Casdoor's logout URL is
//     just a templated URL the adapter can build without SDK calls. The data
//     layer constructs it from cfg.Endpoint + client_id + post_logout_redirect_uri.
//   - RevokeLocal updates the hub-local session blacklist. kernel's
//     casdoor.RevokeToken returns UNIMPLEMENTED today; the data layer falls
//     back to this method so hub can still reject revoked tokens at the next
//     API call.
type AuthnRepo interface {
	// LoginURL returns the IdP hosted login URL.
	LoginURL(ctx context.Context, req AuthnLoginURLRequest) (string, error)
	// Exchange exchanges an OAuth authorization code for tokens.
	Exchange(ctx context.Context, req AuthnExchangeURLRequest) (*AuthnToken, error)
	// Refresh refreshes an access token.
	Refresh(ctx context.Context, req AuthnRefreshRequest) (*AuthnToken, error)
	// LogoutURL returns the IdP logout URL.
	LogoutURL(ctx context.Context, req AuthnLogoutURLRequest) (string, error)
	// Revoke revokes a token at the IdP. Implementations should treat
	// "IdP does not support revocation" as a soft success and surface it via
	// RevokeResult.IDPRevoked=false rather than returning an error.
	Revoke(ctx context.Context, req AuthnRevokeRequest) (*AuthnRevokeResult, error)
	// RevokeLocal updates the local session blacklist so subsequent calls to
	// IsRevoked return true for this token until it expires.
	RevokeLocal(ctx context.Context, token string) error
	// IsRevoked checks whether a token is in the local session blacklist.
	// The usecase calls this from Introspect and from Me to reject tokens that
	// were revoked locally even when the IdP itself has not yet expired them.
	IsRevoked(ctx context.Context, token string) (bool, error)
	// Verify validates a token at the IdP and returns the principal.
	// Used by Introspect.
	Verify(ctx context.Context, req AuthnIntrospectRequest) (authn.Principal, error)
}

// AuthnUsecase orchestrates authentication flows.
type AuthnUsecase struct {
	repo    AuthnRepo
	log     logx.Logger
	metrics metricsx.Manager
}

// AuthnUsecaseOption customizes AuthnUsecase without breaking older tests that
// call NewAuthnUsecase(repo).
type AuthnUsecaseOption func(*AuthnUsecase)

func AuthnUsecaseLogger(logger logx.Logger) AuthnUsecaseOption {
	return func(uc *AuthnUsecase) { uc.log = logger }
}

func AuthnUsecaseMetrics(manager metricsx.Manager) AuthnUsecaseOption {
	return func(uc *AuthnUsecase) { uc.metrics = manager }
}

// NewAuthnUsecase creates a new AuthnUsecase.
func NewAuthnUsecase(repo AuthnRepo, opts ...AuthnUsecaseOption) *AuthnUsecase {
	uc := &AuthnUsecase{repo: repo, log: logx.Noop(), metrics: metricsx.Noop()}
	for _, opt := range opts {
		if opt != nil {
			opt(uc)
		}
	}
	if uc.log == nil {
		uc.log = logx.Noop()
	}
	uc.log = uc.log.Named("authn")
	uc.metrics = metricsx.Ensure(uc.metrics)
	observability.RegisterMetrics(uc.metrics)
	return uc
}

// LoginURL returns the IdP hosted login URL.
func (uc *AuthnUsecase) LoginURL(ctx context.Context, req AuthnLoginURLRequest) (out string, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "login_url",
		logx.Bool("has_state", req.State != ""),
		logx.Bool("has_scope", req.Scope != ""),
	)
	defer func() { observability.End(ctx, logger, uc.metrics, "authn", "login_url", started, err) }()
	if strings.TrimSpace(req.RedirectURI) == "" {
		return "", errInvalidArgument("redirect_uri is required")
	}
	return uc.repo.LoginURL(ctx, req)
}

// Exchange exchanges an OAuth authorization code for IdP tokens.
func (uc *AuthnUsecase) Exchange(ctx context.Context, req AuthnExchangeURLRequest) (out *AuthnToken, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "exchange",
		logx.Bool("has_code", strings.TrimSpace(req.Code) != ""),
		logx.Bool("has_verifier", strings.TrimSpace(req.CodeVerifier) != ""),
	)
	defer func() { observability.End(ctx, logger, uc.metrics, "authn", "exchange", started, err) }()
	if strings.TrimSpace(req.Code) == "" {
		return nil, errInvalidArgument("code is required")
	}
	if strings.TrimSpace(req.RedirectURI) == "" {
		return nil, errInvalidArgument("redirect_uri is required")
	}
	return uc.repo.Exchange(ctx, req)
}

// Refresh refreshes an IdP access token.
func (uc *AuthnUsecase) Refresh(ctx context.Context, req AuthnRefreshRequest) (out *AuthnToken, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "refresh",
		logx.Bool("has_refresh_token", strings.TrimSpace(req.RefreshToken) != ""),
		logx.Bool("has_scope", req.Scope != ""),
	)
	defer func() { observability.End(ctx, logger, uc.metrics, "authn", "refresh", started, err) }()
	if strings.TrimSpace(req.RefreshToken) == "" {
		return nil, errInvalidArgument("refresh_token is required")
	}
	return uc.repo.Refresh(ctx, req)
}

// LogoutURL returns the IdP logout URL. As a side effect, when req.AccessToken
// is non-empty, the access token is added to the local session blacklist
// before the URL is returned so subsequent API calls with the same token are
// rejected even before the IdP session expires.
//
// The side effect is best-effort: if RevokeLocal fails, the logout URL is
// still returned and the error is logged but not propagated, because the
// user's intent (log out of the IdP) should not be blocked by a local
// blacklist write failure.
func (uc *AuthnUsecase) LogoutURL(ctx context.Context, req AuthnLogoutURLRequest) (out string, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "logout_url",
		logx.Bool("has_access_token", strings.TrimSpace(req.AccessToken) != ""),
		logx.Bool("has_state", req.State != ""),
	)
	defer func() { observability.End(ctx, logger, uc.metrics, "authn", "logout_url", started, err) }()
	url, err := uc.repo.LogoutURL(ctx, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(req.AccessToken) != "" {
		// Best-effort local revoke. We do not surface the error to the caller:
		// see method comment.
		if revokeErr := uc.repo.RevokeLocal(ctx, req.AccessToken); revokeErr != nil {
			logger.Warn("authn logout local revoke failed", logx.Err(revokeErr))
		}
	}
	return url, nil
}

// Revoke explicitly revokes a token at the IdP and updates the local
// session blacklist.
func (uc *AuthnUsecase) Revoke(ctx context.Context, req AuthnRevokeRequest) (result *AuthnRevokeResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "revoke",
		logx.Bool("has_token", strings.TrimSpace(req.Token) != ""),
		logx.String("token_type", req.TokenType),
	)
	defer func() { observability.End(ctx, logger, uc.metrics, "authn", "revoke", started, err) }()
	if strings.TrimSpace(req.Token) == "" {
		return nil, errInvalidArgument("token is required")
	}
	result, err = uc.repo.Revoke(ctx, req)
	if err != nil {
		return nil, err
	}
	// Always update the local blacklist regardless of IdP revocation outcome
	// so that subsequent API calls with the same token are rejected at hub.
	if revokeErr := uc.repo.RevokeLocal(ctx, req.Token); revokeErr != nil {
		logger.Warn("authn revoke local blacklist failed", logx.Err(revokeErr))
	}
	if result != nil {
		result.Revoked = true
	} else {
		result = &AuthnRevokeResult{Revoked: true}
	}
	return result, nil
}

// Introspect validates a token and returns the principal it represents.
// Mirrors RFC 7662 token introspection semantics: active=false when the
// token is invalid, expired, revoked (locally or IdP-side), or fails
// signature/issuer/audience checks.
func (uc *AuthnUsecase) Introspect(ctx context.Context, req AuthnIntrospectRequest) (out *AuthnIntrospectResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "introspect",
		logx.Bool("has_token", strings.TrimSpace(req.Token) != ""),
		logx.String("token_type", req.TokenType),
	)
	defer func() {
		active := false
		if out != nil {
			active = out.Active
		}
		observability.End(ctx, logger, uc.metrics, "authn", "introspect", started, err, logx.Bool("active", active))
	}()
	if strings.TrimSpace(req.Token) == "" {
		return &AuthnIntrospectResult{Active: false}, nil
	}
	// Check local blacklist first — cheap and authoritative for hub.
	revoked, checkErr := uc.repo.IsRevoked(ctx, req.Token)
	if checkErr != nil {
		logger.Warn("authn introspect local revoke check failed", logx.Err(checkErr))
	}
	if checkErr == nil && revoked {
		return &AuthnIntrospectResult{Active: false}, nil
	}
	principal, verifyErr := uc.repo.Verify(ctx, req)
	if verifyErr != nil {
		// Any verification failure means the token is not active. The
		// introspection contract returns active=false rather than surfacing
		// invalid-token details to clients.
		logger.Debug("authn introspect verify denied", logx.Err(verifyErr))
		return &AuthnIntrospectResult{Active: false}, nil
	}
	out = &AuthnIntrospectResult{
		Active:    principal.IsAuthenticated(),
		Principal: principal,
		Scope:     joinScopes(principal.Scopes),
		ExpiresAt: principal.ExpiresAt,
		IssuedAt:  principal.IssuedAt,
	}
	return out, nil
}

// Me returns the current principal by verifying the supplied access token.
//
// Transport authn middleware may already have populated authn.Principal in
// ctx for protected routes, but Me still takes the raw access token and
// re-verifies it through the repo. This is correct for an authn endpoint:
// it must also check the hub-local blacklist so tokens revoked via Revoke
// are rejected promptly.
//
// Returns authn.Anonymous() when accessToken is empty, the token is revoked
// locally, or the IdP rejects it. The service layer maps anonymous to a
// 401 response.
func (uc *AuthnUsecase) Me(ctx context.Context, accessToken string) (principal authn.Principal, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authn", "me",
		logx.Bool("has_access_token", strings.TrimSpace(accessToken) != ""),
	)
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authn", "me", started, err,
			logx.Bool("authenticated", principal.IsAuthenticated()),
		)
	}()
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return authn.Anonymous(), errUnauthenticated("access token is required")
	}
	revoked, checkErr := uc.repo.IsRevoked(ctx, accessToken)
	if checkErr != nil {
		logger.Warn("authn me local revoke check failed", logx.Err(checkErr))
	}
	if checkErr == nil && revoked {
		return authn.Anonymous(), errUnauthenticated("token has been revoked")
	}
	principal, err = uc.repo.Verify(ctx, AuthnIntrospectRequest{Token: accessToken})
	if err != nil {
		return authn.Anonymous(), err
	}
	if !principal.IsAuthenticated() {
		return authn.Anonymous(), errUnauthenticated("token is not authenticated")
	}
	return principal.Normalize(), nil
}

// errInvalidArgument is a thin helper to construct a uniform bad-request
// error without importing kernel errorx here. The service layer maps it to
// the appropriate proto error code; biz callers should treat it as
// "user input was wrong" and surface it to the client.
//
// We import errorx via the accessx/authn packages transitively; reusing
// authn.ErrInvalidTokenRequest keeps error codes consistent with kernel.
func errInvalidArgument(msg string) error {
	return authn.ErrInvalidTokenRequest(msg)
}

func errUnauthenticated(msg string) error {
	return authn.ErrUnauthenticated(msg)
}

// joinScopes joins the principal's OAuth scopes with a space separator,
// matching RFC 6749 scope string format.
func joinScopes(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
}
