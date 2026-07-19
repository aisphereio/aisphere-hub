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
	var guard accessx.Guard
	if resources == nil {
		guard = accessx.NewGuard(authz.DenyAll(), auditRecorder(resources))
	} else {
		guard = accessx.NewGuard(resources.Authz, auditRecorder(resources))
	}
	catalog := HubCatalog()
	return serverx.ServerMiddlewareFromProviders(context.Background(), serverx.RuntimeProviders{
		Security:            securityRuntime,
		AccessGuard:         &guard,
		AccessResolver:      hubAccessResolver(catalog),
		RequestInfoResolver: catalog.RequestInfoResolver,
	})
}

func hubAccessResolver(catalog serverx.ServiceCatalog) mwaccess.Resolver {
	return func(ctx context.Context, operation string, request any) (accessx.Check, bool, error) {
		// Create operations bootstrap a new resource that does not yet exist
		// in the authorization graph. Skip the SpiceDB check — the biz layer
		// will write the owner relationship after the row is persisted.
		if strings.HasSuffix(operation, "/CreateSkill") {
			return accessx.Check{
				SkipPolicy:  accessx.SkipAuthz,
				AuditAction: "hub.skill.create",
			}, true, nil
		}
		return catalog.AccessResolver(ctx, operation, request)
	}
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

func auditRecorder(resources *data.Resources) auditx.Recorder {
	if resources == nil {
		return auditx.Noop()
	}
	return resources.Audit
}
