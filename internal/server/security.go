package server

import (
	"context"

	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/accessx"
	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/middleware"
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
		AccessResolver:      catalog.AccessResolver,
		RequestInfoResolver: catalog.RequestInfoResolver,
	})
}

func mustHubSecurityRuntime(cfg conf.SecurityConfig) *securityx.Runtime {
	runtime, err := securityx.NewRuntime(context.Background(), securityx.Config{
		Authn: securityx.AuthnBoundaryConfig{
			Enabled:        cfg.Authn.Enabled,
			Mode:           cfg.Authn.Mode,
			Provider:       cfg.Authn.Provider,
			OIDC:           cfg.Authn.OIDC,
			PrincipalJWT:   cfg.Authn.PrincipalJWT,
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
