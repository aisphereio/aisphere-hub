package server

import (
		"context"
		"strings"

		skillv1 "github.com/aisphereio/aisphere-hub/api/skill/v1"
		"github.com/aisphereio/aisphere-hub/internal/conf"
		"github.com/aisphereio/aisphere-hub/internal/data"
		"github.com/aisphereio/aisphere-hub/internal/gitengine"
		"github.com/aisphereio/kernel/accessx"
		"github.com/aisphereio/kernel/auditx"
		"github.com/aisphereio/kernel/authz"
		"github.com/aisphereio/kernel/errorx"
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
			if strings.HasPrefix(operation, "git.") {
				return gitengine.ResolveProtocolAccess(ctx, operation, request)
			}
// Validate required authz parameters before delegating to the
				// generated resolver. Missing org_id would produce a confusing
				// SpiceDB 403 instead of a clear 400. project_id is optional.
				if strings.HasSuffix(operation, "/CreateSkill") {
					if req, ok := request.(*skillv1.CreateSkillRequest); ok {
						if strings.TrimSpace(req.GetOrgId()) == "" {
							return accessx.Check{}, false, errorx.BadRequest("ORG_ID_REQUIRED", "org_id is required for skill creation")
						}
					}
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
