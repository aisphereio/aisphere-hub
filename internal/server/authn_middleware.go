// Package server authn_middleware.go — kernel authn middleware for HTTP server.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// authnMiddleware is the kernel HTTP FilterFunc that verifies Bearer tokens
// and injects authn.Principal into the request context.
type authnMiddleware struct {
	resources *data.Resources
}

// newAuthnFilter returns a khttp.FilterFunc that verifies Bearer tokens. When
// resources.Authn is nil, returns nil (no filter registered).
func newAuthnFilter(resources *data.Resources) khttp.FilterFunc {
	if resources == nil || resources.Authn == nil {
		return nil
	}
	am := &authnMiddleware{resources: resources}
	return am.filter
}

// filter is the actual http middleware. It runs before the router.
func (m *authnMiddleware) filter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		logger := logx.FromContextOr(r.Context(), m.logger()).Named("authn.middleware").With(
			logx.String("method", r.Method),
			logx.String("path", r.URL.Path),
		)
		ctx := logx.Inject(r.Context(), logger)
		ctx = metricsx.Inject(ctx, m.metrics())
		r = r.WithContext(ctx)

		if isAuthnPublicPath(r.URL.Path) {
			r = r.WithContext(authn.ContextWithPrincipal(ctx, authn.Anonymous()))
			observability.MiddlewareDecision(ctx, m.metrics(), "http", "public", start, nil)
			logger.Debug("authn middleware skipped public path")
			next.ServeHTTP(w, r)
			return
		}

		token := bearerTokenFromHeader(r.Header.Get("Authorization"))
		if token == "" {
			err := authn.ErrUnauthenticated("missing or malformed Authorization header")
			observability.MiddlewareDecision(ctx, m.metrics(), "http", "missing_token", start, err)
			logger.Warn("authn middleware rejected request", logx.String("reason", "missing_token"), logx.Err(err))
			writeAuthnFilterError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		principal, err := m.resources.Authn.Authenticate(ctx, authn.Credential{
			Scheme: authn.CredentialBearer,
			Token:  token,
		})
		if err != nil {
			status := http.StatusUnauthorized
			if isAuthnBackendError(err) {
				status = http.StatusServiceUnavailable
			}
			observability.MiddlewareDecision(ctx, m.metrics(), "http", "rejected", start, err)
			logger.Warn("authn middleware rejected request",
				logx.Int("status", status),
				logx.String("error_code", observability.ErrorCode(err)),
				logx.Err(err),
			)
			writeAuthnFilterError(w, status, err.Error())
			return
		}

		principal = principal.Normalize()
		ctx = authn.ContextWithPrincipal(ctx, principal)
		observability.MiddlewareDecision(ctx, m.metrics(), "http", "accepted", start, nil)
		logger.Debug("authn middleware accepted request",
			logx.String("subject_id", principal.SubjectID),
			logx.String("org_id", principal.OrgID),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *authnMiddleware) logger() logx.Logger {
	if m != nil && m.resources != nil && m.resources.Logger != nil {
		return m.resources.Logger
	}
	return logx.DefaultLogger()
}

func (m *authnMiddleware) metrics() metricsx.Manager {
	if m != nil && m.resources != nil {
		return metricsx.Ensure(m.resources.Metrics)
	}
	return metricsx.Noop()
}

// bearerTokenFromHeader extracts the Bearer token from an Authorization header
// value. Returns "" when the header is absent or malformed.
func bearerTokenFromHeader(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// isAuthnBackendError returns true when err indicates the identity backend
// (Casdoor) is unavailable, not that the token is invalid.
func isAuthnBackendError(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := errorx.As(err); ok {
		return e.Code() == authn.CodeIdentityBackendFailed
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "identity backend failed") ||
		strings.Contains(lower, "casdoor") && strings.Contains(lower, "unavailable")
}

// writeAuthnFilterError writes a JSON error response consistent with the authn
// service layer's writeAuthnError format.
func writeAuthnFilterError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    status,
		"message": message,
	})
}

var _ context.Context
