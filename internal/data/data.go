package data

import (
	"context"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/observability"

	iamauthz "github.com/aisphereio/aisphere-iam/client/authzgrpc"
	"github.com/aisphereio/kernel/accessx"
	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authn/casdoor"
	"github.com/aisphereio/kernel/authn/oidcx"
	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/cachex"
	_ "github.com/aisphereio/kernel/cachex/redis"
	"github.com/aisphereio/kernel/dbx"
	_ "github.com/aisphereio/kernel/dbx/postgres"
	"github.com/aisphereio/kernel/dtmx"
	_ "github.com/aisphereio/kernel/dtmx/dtm"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
	"github.com/aisphereio/kernel/migrationx"
	"github.com/aisphereio/kernel/objectstorex"
	_ "github.com/aisphereio/kernel/objectstorex/minio"
)

type Resources struct {
	Logger  logx.Logger
	Metrics metricsx.Manager

	DB          dbx.DB
	Cache       cachex.Cache
	ObjectStore objectstorex.Client
	DTM         dtmx.Manager
	DTMConfig   dtmx.Config
	SkillConfig conf.SkillConfig
	// Audit is the audit recorder used by business modules to write
	// security audit records (skill.create / skill.delete / skill.share.*
	// etc.). May be auditx.Noop() when audit is disabled.
	Audit auditx.Recorder
	// AuditStore exposes the Queryer interface when the configured audit
	// store supports querying (e.g. MemoryStore, postgres store). May be
	// nil when audit is disabled or the configured store does not support
	// querying (e.g. a write-only log shipper). Business layers MUST
	// nil-check before use.
	AuditStore auditx.Store
	Authn      authn.Authenticator
	Authz      authz.Authorizer
	Access     accessx.Guard

	// AuthzService exposes the full authz.Service surface (Check,
	// BatchCheck, WriteRelationships, ReadRelationships,
	// LookupResources, LookupSubjects, ReadSchema, WriteSchema) when the
	// configured authorizer implements it. May be nil when authz is
	// disabled or the configured provider only implements Authorizer
	// (e.g. memory / noop). Business layers MUST nil-check before use.
	AuthzService runtimeAuthzService

	// LoginService builds IdP login URLs. Always the raw casdoor.Client —
	// login URL construction is cheap and includes a per-request state
	// parameter that should NOT be cached.
	LoginService authn.LoginService
	// LogoutService builds IdP logout (end-session) URLs. Always the raw
	// casdoor.Client — logout URL construction is cheap and includes
	// per-request state that should NOT be cached.
	LogoutService authn.LogoutService
	// TokenService exchanges codes, refreshes tokens, verifies tokens, and
	// revokes tokens. When Cache is configured this is wrapped by
	// authn.CachedClient so VerifyToken hits the cache; ExchangeCode and
	// RefreshToken are one-shot and bypass the cache; RevokeToken
	// invalidates the cache entry on success.
	TokenService authn.TokenService
	// CachedTokenService is the *CachedClient wrapping TokenService when
	// cache is enabled, or nil when cache is disabled. Use it to call
	// Invalidate explicitly from revocation flows that bypass RevokeToken
	// (e.g. hub's local-blacklist fallback when the IdP's RevokeToken is
	// UNIMPLEMENTED). Always nil-check before use.
	CachedTokenService *authn.CachedClient
	// Casdoor exposes the underlying Casdoor client when the authn provider
	// is "casdoor". May be nil when authn is disabled or another provider is
	// used. Business layers should type-check (authn.LoginService,
	// authn.TokenService) before using it.
	Casdoor *casdoor.Client

	closers []func() error
}

type Data struct {
	Resources *Resources
}

func NewResources(ctx context.Context, cfg conf.Bootstrap) (*Resources, func(), error) {
	logger := logx.FromContextOr(ctx, logx.DefaultLogger()).Named("data")
	metrics := metricsx.FromContext(ctx)
	observability.RegisterMetrics(metrics)

	r := &Resources{
		Logger:  logger,
		Metrics: metrics,
		Audit:   auditx.NewMemoryStore(),
		Authz:   authz.DenyAll(),
	}
	// Preserve the typed Store reference so the audit query API can read
	// records back. The default memory store implements both Recorder
	// and Queryer.
	if store, ok := r.Audit.(auditx.Store); ok {
		r.AuditStore = store
	}
	if !cfg.Audit.Enabled {
		r.Audit = auditx.Noop()
		r.AuditStore = nil
	}
	if cfg.Security.Authz.DevAllowAll {
		r.Authz = authz.AllowAllForDevOnly()
		logger.Warn("authz dev_allow_all enabled; permission checks will be allowed and SpiceDB will not be initialized")
	}

	observability.ComponentConfigured(metrics, "db", cfg.Data.Database.Enabled)
	if cfg.Data.Database.Enabled {
		start := time.Now()
		dbCfg := cfg.Data.Database.Config
		dbCfg.Logger = logger.Named("dbx")
		dbCfg.Metrics = metrics
		dbCfg.MetricsEnabled = cfg.Metrics.Enabled || dbCfg.MetricsEnabled
		db, err := dbx.New(dbCfg)
		observability.ComponentInit(ctx, metrics, "db", start, err)
		if err != nil {
			logger.Error("database init failed", logx.Err(err))
			return nil, nil, err
		}
		logger.Info("database initialized", logx.String("driver", db.DriverName()))
		r.DB = db
		r.closers = append(r.closers, db.Close)

		migrationCfg := cfg.Data.Database.Migration.Normalize()
		observability.ComponentConfigured(metrics, "db_migration", migrationCfg.Enabled)
		if migrationCfg.Enabled {
			start = time.Now()
			err = migrationx.Apply(ctx, db, migrationCfg)
			observability.ComponentInit(ctx, metrics, "db_migration", start, err)
			if err != nil {
				logger.Error("database migration failed", logx.Err(err))
				r.Close()
				return nil, nil, err
			}
			logger.Info(
				"database migrations applied",
				logx.String("engine", migrationCfg.Engine),
				logx.String("mode", migrationCfg.Mode),
				logx.String("dir", migrationCfg.Dir),
				logx.String("table", migrationCfg.Table),
			)
		}
	}

	observability.ComponentConfigured(metrics, "cache", cfg.Data.Cache.Enabled)
	if cfg.Data.Cache.Enabled {
		start := time.Now()
		cacheCfg := cfg.Data.Cache.Config
		cacheCfg.Logger = logger.Named("cachex")
		cacheCfg.Metrics = metrics
		cacheCfg.MetricsEnabled = cfg.Metrics.Enabled || cacheCfg.MetricsEnabled
		cache, err := cachex.New(cacheCfg)
		observability.ComponentInit(ctx, metrics, "cache", start, err)
		if err != nil {
			logger.Error("cache init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		logger.Info("cache initialized", logx.String("driver", cache.DriverName()))
		r.Cache = cache
		r.closers = append(r.closers, cache.Close)
	}

	observability.ComponentConfigured(metrics, "object_store", cfg.Data.ObjectStore.Enabled)
	if cfg.Data.ObjectStore.Enabled {
		start := time.Now()
		storeCfg := cfg.Data.ObjectStore.Config
		storeCfg.Logger = logger.Named("objectstorex")
		storeCfg.Metrics = metrics
		storeCfg.MetricsEnabled = cfg.Metrics.Enabled || storeCfg.MetricsEnabled
		store, err := objectstorex.New(storeCfg)
		observability.ComponentInit(ctx, metrics, "object_store", start, err)
		if err != nil {
			logger.Error("object store init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		logger.Info("object store initialized", logx.String("driver", store.DriverName()), logx.String("bucket", store.Bucket()))
		r.ObjectStore = store
		r.closers = append(r.closers, store.Close)
	}

	observability.ComponentConfigured(metrics, "dtm", cfg.DTM.Enabled)
	if cfg.DTM.Enabled {
		start := time.Now()
		dtmCfg := cfg.DTM
		dtmCfg.Logger = logger.Named("dtmx")
		dtmCfg.Metrics = metrics
		dtmCfg.MetricsEnabled = cfg.Metrics.Enabled || dtmCfg.MetricsEnabled
		client, err := dtmx.New(dtmCfg)
		observability.ComponentInit(ctx, metrics, "dtm", start, err)
		if err != nil {
			logger.Error("dtm init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		logger.Info("dtm initialized", logx.String("server", dtmCfg.Server), logx.String("service_base_url", dtmCfg.ServiceBaseURL))
		r.DTM = client
		r.DTMConfig = dtmCfg
		r.closers = append(r.closers, client.Close)
	}
	r.SkillConfig = cfg.Skill

	observability.ComponentConfigured(metrics, "authn", cfg.Security.Authn.Enabled)
	if cfg.Security.Authn.Enabled {
		start := time.Now()
		authnCfg := cfg.Security.Authn
		authnCfg.Casdoor.Logger = logger.Named("authn.casdoor")
		authnCfg.Casdoor.Metrics = metrics
		authnCfg.Casdoor.MetricsEnabled = cfg.Metrics.Enabled || authnCfg.Casdoor.MetricsEnabled

		// Always create the Casdoor client for login/logout/token operations.
		casdoorClient, err := casdoor.New(authnCfg.Casdoor)
		if err != nil {
			logger.Error("authn casdoor client init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		r.Casdoor = casdoorClient
		r.LoginService = casdoorClient
		r.LogoutService = casdoorClient

		// Create the authenticator based on mode.
		authenticator, err := newAuthenticator(authnCfg, logger)
		if err != nil {
			logger.Error("authn authenticator init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		observability.ComponentInit(ctx, metrics, "authn", start, err)
		if err != nil {
			logger.Error("authn init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		logger.Info("authn initialized",
			logx.String("provider", authnCfg.Provider),
			logx.String("mode", authnCfg.Mode),
		)
		r.Authn = authenticator

		// TokenService is wrapped with CachedClient when cache is available.
		if r.Cache != nil {
			cached := authn.NewCachedClient(casdoorClient, casdoorClient, casdoorClient, r.Cache)
			r.TokenService = cached
			r.CachedTokenService = cached
			// Also wrap r.Authn so middleware-driven Authenticate calls go
			// through the same cache as explicit VerifyToken calls.
			r.Authn = cached
		} else {
			r.TokenService = casdoorClient
		}
	}

	observability.ComponentConfigured(metrics, "authz", cfg.Security.Authz.Enabled && !cfg.Security.Authz.DevAllowAll)
	if cfg.Security.Authz.Enabled && !cfg.Security.Authz.DevAllowAll {
		start := time.Now()
		authzCfg := cfg.Security.Authz
		authorizer, closeFn, err := newAuthorizer(authzCfg)
		observability.ComponentInit(ctx, metrics, "authz", start, err)
		if err != nil {
			logger.Error("authz init failed", logx.Err(err))
			r.Close()
			return nil, nil, err
		}
		logger.Info("authz initialized", logx.String("provider", authzCfg.Provider))
		r.Authz = authorizer
		r.AuthzService = authorizer
		if closeFn != nil {
			r.closers = append(r.closers, closeFn)
		}
	}

	r.Access = accessx.New(r.Authn, r.Authz, r.Audit)
	return r, func() { _ = r.Close() }, pingEnabled(ctx, r)
}

func NewData(resources *Resources) *Data {
	return &Data{Resources: resources}
}

func newAuthenticator(cfg conf.AuthnConfig, logger logx.Logger) (authn.Authenticator, error) {
	// Mode-based authenticator selection.
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "principal_jwt":
		return authn.PrincipalJWTAuthenticator{Config: cfg.PrincipalJWT}, nil
	case "gateway_trusted":
		return authn.TrustedHeaderAuthenticator{}, nil
	case "", "casdoor_jwt", "jwt_verify":
		// Use OIDC/JWKS verifier for local JWT validation.
		oidcCfg := oidcConfigFromAuthn(cfg)
		oidcCfg.Logger = logger.Named("authn.oidc")
		return oidcx.New(oidcCfg)
	default:
		// Fallback to provider-based authenticator.
		switch cfg.Provider {
		case "", "casdoor":
			return casdoor.New(cfg.Casdoor)
		default:
			return nil, errorx.BadRequest(errorx.Code("AUTHN_UNSUPPORTED_PROVIDER"), "unsupported authn provider: "+cfg.Provider)
		}
	}
}

// oidcConfigFromAuthn builds an oidcx.Config from the authn config.
func oidcConfigFromAuthn(cfg conf.AuthnConfig) oidcx.Config {
	if cfg.OIDC.DiscoveryURL != "" || cfg.OIDC.JWKSURL != "" || cfg.OIDC.Issuer != "" {
		oidc := cfg.OIDC.Normalized()
		if oidc.Provider == "" || oidc.Provider == "oidc" {
			oidc.Provider = "casdoor"
		}
		return oidc
	}
	c := cfg.Casdoor.Normalized()
	return oidcx.Config{
		Provider:       "casdoor",
		Issuer:         c.Issuer,
		DiscoveryURL:   c.DiscoveryURL,
		JWKSURL:        c.JWKSURL,
		Audience:       c.Audience,
		AllowedAlgs:    c.AllowedAlgs,
		AllowedOwners:  c.AllowedOwners,
		JWKSCacheTTL:   c.JWKSCacheTTL,
		ClockSkew:      60 * time.Second,
		SubjectIDClaim: "sub",
		UsernameClaim:  "name",
		NameClaim:      "displayName",
		EmailClaim:     "email",
		OwnerClaim:     "owner",
		GroupsClaim:    "groups",
		RolesClaim:     "roles",
		ScopesClaim:    "scope",
	}
}

func newAuthorizer(cfg conf.AuthzConfig) (runtimeAuthzService, func() error, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "iam_grpc":
		client, err := iamauthz.New(cfg.IAMGRPC)
		if err != nil {
			return nil, nil, err
		}
		return client, client.Close, nil
	case "spicedb":
		return nil, nil, errorx.BadRequest(
			errorx.Code("AUTHZ_DIRECT_SPICEDB_FORBIDDEN"),
			"Hub must use security.authz.provider=iam_grpc; IAM owns SpiceDB access and schema",
		)
	default:
		return nil, nil, errorx.BadRequest(errorx.Code("AUTHZ_UNSUPPORTED_PROVIDER"), "unsupported authz provider: "+cfg.Provider)
	}
}

func pingEnabled(ctx context.Context, r *Resources) error {
	logger := logx.FromContextOr(ctx, r.Logger).Named("data.ping")
	if r.DB != nil {
		start := time.Now()
		err := r.DB.PingContext(ctx)
		observability.ComponentInit(ctx, r.Metrics, "db_ping", start, err)
		if err != nil {
			logger.Error("database ping failed", logx.Err(err))
			return err
		}
		logger.Debug("database ping ok")
	}
	if r.Cache != nil {
		start := time.Now()
		err := r.Cache.Ping(ctx)
		observability.ComponentInit(ctx, r.Metrics, "cache_ping", start, err)
		if err != nil {
			logger.Error("cache ping failed", logx.Err(err))
			return err
		}
		logger.Debug("cache ping ok")
	}
	return nil
}

func (r *Resources) Close() error {
	var out error
	for i := len(r.closers) - 1; i >= 0; i-- {
		if err := r.closers[i](); err != nil {
			if r.Logger != nil {
				r.Logger.Warn("resource close failed", logx.Int("index", i), logx.Err(err))
			}
			if out == nil {
				out = err
			}
		}
	}
	return out
}
