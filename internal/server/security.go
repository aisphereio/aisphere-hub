package server

import (
	"context"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/accessx"
	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/middleware"
	mwaccess "github.com/aisphereio/kernel/middleware/access"
	"github.com/aisphereio/kernel/securityx"
	"github.com/aisphereio/kernel/serverx"
)

func hubServerMiddlewares(resources *data.Resources, cfg conf.SecurityConfig) []middleware.Middleware {
	securityRuntime := mustHubSecurityRuntime(cfg)
	guard := accessx.NewGuard(authz.AllowAllForDevOnly(), auditRecorder(resources))
	return serverx.ServerMiddlewareFromProviders(context.Background(), serverx.RuntimeProviders{
		Security:       securityRuntime,
		AccessGuard:    &guard,
		AccessResolver: hubAuthenticatedResolver,
	})
}

func mustHubSecurityRuntime(cfg conf.SecurityConfig) *securityx.Runtime {
	runtime, err := securityx.NewRuntime(context.Background(), securityx.Config{
		Authn: securityx.AuthnBoundaryConfig{
			Enabled:        cfg.Authn.Enabled,
			Mode:           cfg.Authn.Mode,
			Provider:       cfg.Authn.Provider,
			OIDC:           cfg.Authn.OIDC,
			InternalCall:   cfg.InternalCall,
			CacheTTL:       cfg.Authn.CacheTTL,
			AllowAnonymous: true,
		},
		InternalCall: cfg.InternalCall,
		Access:       cfg.Access,
	}, nil)
	if err != nil {
		panic(err)
	}
	return runtime
}

func hubAuthenticatedResolver(ctx context.Context, operation string, req any) (accessx.Check, bool, error) {
	_ = ctx
	_ = req
	return accessx.Check{
		SkipPolicy:  accessx.SkipAuthz,
		AuditAction: "hub." + normalizeOperationAction(operation),
	}, true, nil
}

func normalizeOperationAction(operation string) string {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return "unknown"
	}
	if i := strings.LastIndex(operation, "/"); i >= 0 && i+1 < len(operation) {
		operation = operation[i+1:]
	}
	operation = strings.ReplaceAll(operation, ".", "_")
	operation = strings.ReplaceAll(operation, "-", "_")
	return strings.ToLower(operation)
}

func auditRecorder(resources *data.Resources) auditx.Recorder {
	if resources == nil {
		return auditx.Noop()
	}
	return resources.Audit
}

var _ mwaccess.Resolver = hubAuthenticatedResolver
