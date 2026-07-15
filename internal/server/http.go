package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/service"

	"github.com/aisphereio/kernel/authz"
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
// Authn strategy: securityx builds the authn runtime from security.authn and
// serverx/autowire mounts the standard authn middleware before access. Public
// routes are configured through security.access.public_operations instead of a
// Hub-specific authn filter.
func NewHTTPServer(cfg conf.ServerConfig, accessLog logx.AccessLogConfig, resources *data.Resources, securityCfg conf.SecurityConfig, authnSvc *service.AuthnService, authzSvc *service.AuthzService, auditSvc *service.AuditService, skillSvc *service.SkillService) *khttp.Server {
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
	// Register the unified Kernel security chain BEFORE any routes are mounted.
	// securityx builds the authn runtime and access skip policy from config;
	// serverx/autowire owns the actual middleware order.
	if m := hubServerMiddlewares(resources, securityCfg); len(m) > 0 {
		opts = append(opts, khttp.Middleware(m...))
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
	// SkillSet is intentionally a lightweight HTTP resource. It stores only
	// ordered references to canonical Skills; version/release/runtime remain on Skill.
	registerSkillSetHTTP(srv, resources)
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

		// IAM authorization runtime check. Hub deliberately has no schema
		// administration capability, so readiness uses a side-effect-free
		// permission check instead of ReadSchema. A deny decision is healthy:
		// only transport/provider failures return an error. This exercises the
		// complete Hub -> IAM gRPC -> authorization provider path.
		if resources.AuthzService != nil {
			_, err := resources.AuthzService.Check(r.Context(), authz.CheckRequest{
				Subject:    authz.SubjectRef{Type: authz.SubjectTypeService, ID: "aisphere-hub"},
				Resource:   authz.ObjectRef{Type: "iam_authz", ID: "global"},
				Permission: "view_schema",
			})
			if err != nil {
				checks["iam_authz"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["iam_authz"] = "ok"
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
