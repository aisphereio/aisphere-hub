package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	authnv1 "github.com/aisphereio/aisphere-hub/api/authn/v1"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/service"

	"github.com/aisphereio/kernel/logx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// NewHTTPServer builds the HTTP server and registers all enabled services.
//
// Authz strategy: per-RPC, in the biz layer. We intentionally do NOT
// mount a blanket authz middleware on /v1/skills/* because:
//
//  1. The biz layer already calls authz.Require / authz.Can on every
//     skill operation (see internal/biz/skill.go requireSkillPermission
//     / requireSkillRead).
//
//  2. A middleware would need to parse the resource ID from the URL
//     path (e.g. /v1/skills/{name}), which duplicates the proto
//     route binding and breaks for nested resources like
//     /v1/skills/{name}/versions/{version}/files.
//
//  3. Read operations need ownership + public-visibility fallbacks
//     (see canReadSkill) that a middleware cannot express without
//     loading the skill row — which the handler is about to do anyway.
//
//  4. A second authz round-trip per request would double SpiceDB load
//     for no security gain.
//
// Authn strategy: when security.authn is enabled, the transport-level
// authn filter verifies Bearer tokens on non-public routes and injects
// authn.Principal into ctx. When authn is disabled, no filter is mounted
// and principalFromContext returns Anonymous so dev mode remains usable.
func NewHTTPServer(cfg conf.ServerConfig, accessLog logx.AccessLogConfig, resources *data.Resources, authnSvc *service.AuthnService, authzSvc *service.AuthzService, auditSvc *service.AuditService, skillSvc *service.SkillService) *khttp.Server {
	addr := cfg.HTTP.Addr
	if addr == "" {
		addr = "0.0.0.0:8000"
	}
	timeout := cfg.HTTP.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	opts := []khttp.ServerOption{khttp.Address(addr), khttp.Timeout(timeout)}
	if cfg.HTTP.CORS.Enabled {
		opts = append(opts, khttp.CORS(khttp.CORSConfig{
			Enabled:          true,
			AllowedOrigins:   cfg.HTTP.CORS.AllowedOrigins,
			AllowedMethods:   cfg.HTTP.CORS.AllowedMethods,
			AllowedHeaders:   cfg.HTTP.CORS.AllowedHeaders,
			ExposedHeaders:   cfg.HTTP.CORS.ExposedHeaders,
			AllowCredentials: cfg.HTTP.CORS.AllowCredentials,
			MaxAge:           cfg.HTTP.CORS.MaxAge,
		}))
	}
	if resources != nil {
		if resources.Logger != nil {
			opts = append(opts, khttp.Logger(resources.Logger.Named("http")))
		}
		opts = append(opts, khttp.Metrics(resources.Metrics))
	}
	opts = append(opts, khttp.AccessLog(accessLog))
	// Register the authn filter BEFORE any routes are mounted. The filter
	// verifies Bearer tokens on all non-public paths and injects
	// authn.Principal into the request context, so service handlers can
	// call authn.PrincipalFromContext(ctx) instead of each handler
	// re-implementing Authorization header parsing.
	//
	// When authn is disabled (resources.Authn == nil), newAuthnFilter
	// returns nil and no filter is registered — dev mode stays usable
	// without a Casdoor backend.
	if authnFilter := newAuthnFilter(resources); authnFilter != nil {
		opts = append(opts, khttp.Filter(authnFilter))
	}
	srv := khttp.NewServer(opts...)
	// Register each service that is enabled. Services may be nil when
	// their corresponding feature is disabled in config; in that case
	// only the enabled services are mounted, plus /healthz and /readyz.
	if authnSvc != nil {
		authnSvc.RegisterHTTPServer(srv)
	}
	if authzSvc != nil {
		authzSvc.RegisterHTTPServer(srv)
	}
	if auditSvc != nil {
		auditSvc.RegisterHTTPServer(srv)
	}
	if skillSvc != nil {
		skillSvc.RegisterHTTPServer(srv)
	}
	if resources != nil && resources.DTM != nil {
		dtmSkill := data.NewSkillDTMBranchHandler(resources)
		srv.HandleFunc("/internal/dtm/skill/package/promote", dtmSkill.PromotePackage)
		srv.HandleFunc("/internal/dtm/skill/package/promote_compensate", dtmSkill.CompensatePackage)
		srv.HandleFunc("/internal/dtm/skill/metadata/upsert", dtmSkill.UpsertMetadata)
		srv.HandleFunc("/internal/dtm/skill/metadata/upsert_compensate", dtmSkill.CompensateMetadata)
		srv.HandleFunc("/internal/dtm/skill/draft/object/promote", dtmSkill.PromoteDraftObject)
		srv.HandleFunc("/internal/dtm/skill/draft/object/promote_compensate", dtmSkill.CompensateDraftObject)
		srv.HandleFunc("/internal/dtm/skill/draft/metadata/upsert", dtmSkill.UpsertDraftMetadata)
		srv.HandleFunc("/internal/dtm/skill/draft/metadata/upsert_compensate", dtmSkill.CompensateDraftMetadata)
	}
	srv.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	srv.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if resources == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": map[string]string{"resources": "nil"}})
			return
		}
		checks := map[string]string{}
		allReady := true

		// DB check (when enabled).
		if resources.DB != nil {
			if err := resources.DB.PingContext(r.Context()); err != nil {
				checks["database"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["database"] = "ok"
			}
		}

		// Cache check (when enabled).
		if resources.Cache != nil {
			if err := resources.Cache.Ping(r.Context()); err != nil {
				checks["cache"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["cache"] = "ok"
			}
		}

		// SpiceDB check (when authz enabled). We call ReadSchema as the
		// liveness probe — it's a lightweight gRPC call that exercises
		// the full authz stack (connection, auth, schema service). A
		// failure here means either SpiceDB is down or the configured
		// token is wrong; either way the hub cannot serve authz-protected
		// requests.
		if resources.AuthzService != nil {
			if _, err := resources.AuthzService.ReadSchema(r.Context()); err != nil {
				checks["spicedb"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["spicedb"] = "ok"
			}
		}

		// Object store check is intentionally skipped — it's only used
		// by skill upload/download, and an outage there should not make
		// the whole hub report not-ready. Skill RPCs will surface the
		// error themselves.

		if !allReady {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
	})
	return srv
}

// isAuthnPublicPath reports whether the given path should skip authn
// middleware. Public authn paths are the login/logout/exchange/refresh
// endpoints (both the JSON RPCs and the 302 redirect routes), plus the
// health/readiness probes.
//
// Used by the authn middleware selector in app.NewApp; kept in the server
// package so the path list lives next to the route registration.
func isAuthnPublicPath(path string) bool {
	switch {
	case path == "/healthz" || path == "/readyz":
		return true
	case strings.HasPrefix(path, "/internal/dtm/"):
		return true
	case strings.HasPrefix(path, "/v1/authn/login"),
		strings.HasPrefix(path, "/v1/authn/logout"),
		strings.HasPrefix(path, "/v1/authn/exchange"),
		strings.HasPrefix(path, "/v1/authn/refresh"),
		strings.HasPrefix(path, "/v1/authn/revoke"),
		strings.HasPrefix(path, "/v1/authn/introspect"),
		strings.HasPrefix(path, "/v1/authn/login-url"),
		strings.HasPrefix(path, "/v1/authn/logout-url"):
		return true
	}
	return false
}

// Compile-time reference to ensure authnv1 stays imported even if the
// generated HTTP server registration moves into a separate file later.
var _ = authnv1.RegisterAuthnServiceHTTPServer

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
