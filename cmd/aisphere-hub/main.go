package main

import (
	"context"
	"flag"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/observability"
	"github.com/aisphereio/aisphere-hub/internal/server"
	"github.com/aisphereio/aisphere-hub/internal/service"

	kernel "github.com/aisphereio/kernel"
	"github.com/aisphereio/kernel/configx"
	configenv "github.com/aisphereio/kernel/configx/env"
	"github.com/aisphereio/kernel/configx/file"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

var (
	Name     = "app"
	Version  = "dev"
	flagconf string
)

func init() {
	flag.StringVar(&flagconf, "conf", "configs", "config path, eg: -conf configs")
}

func main() {
	flag.Parse()

	cfg := configx.New(configx.WithSource(file.NewSource(flagconf), configenv.NewSource()))
	defer cfg.Close()
	if err := cfg.Load(); err != nil {
		panic(err)
	}

	var bc conf.Bootstrap
	if err := cfg.Scan(&bc); err != nil {
		panic(err)
	}
	applyBuildInfo(&bc)

	logger, err := newLogger(bc.Log)
	if err != nil {
		panic(err)
	}
	slogLogger, err := logx.Slog(logger)
	if err != nil {
		panic(err)
	}
	logx.SetDefault(slogLogger)

	metrics := newMetrics(bc, logger)
	observability.RegisterMetrics(metrics)

	bootstrapCtx := context.Background()
	bootstrapCtx = logx.Inject(bootstrapCtx, logger, logx.String("service", bc.Service.Name), logx.String("version", bc.Service.Version))
	bootstrapCtx = metricsx.Inject(bootstrapCtx, metrics)

	resources, cleanup, err := data.NewResources(bootstrapCtx, bc)
	if err != nil {
		logger.Error("resource initialization failed", logx.Err(err))
		panic(err)
	}
	defer cleanup()

	// Wire the authn module.
	authnRepo := data.NewAuthnRepo(resources, bc.Security.Authn)
	authnUsecase := biz.NewAuthnUsecase(
		authnRepo,
		biz.AuthnUsecaseLogger(logger),
		biz.AuthnUsecaseMetrics(metrics),
	)
	authnService := service.NewAuthnService(authnUsecase)

	// Wire the authz module.
	authzRepo := data.NewAuthzRepo(resources)
	authzUsecase := biz.NewAuthzUsecase(authzRepo, logger, metrics)
	authzService := service.NewAuthzService(authzUsecase)

	// Wire the audit module. Audit persistence remains intentionally lightweight
	// for now; audit hardening is deferred to the next phase.
	auditRepo := data.NewAuditRepo(resources)
	auditUsecase := biz.NewAuditUsecase(auditRepo, logger)
	auditService := service.NewAuditService(auditUsecase)

	// Wire the skill module.
	skillRepo := data.NewSkillRepo(resources)
	skillUsecase := biz.NewSkillUsecase(skillRepo, authzUsecase, resources.Audit, logger, metrics)
	skillService := service.NewSkillService(skillUsecase)

	// Repair durable owner relationships through IAM's runtime authorization API.
	if err := data.BootstrapAuthzRelationships(bootstrapCtx, resources, logger); err != nil {
		logger.Warn("authz relationship bootstrap failed; historical skill permissions may be incomplete", logx.Err(err))
	}

	httpServer := server.NewHTTPServer(bc.Server, bc.Log.AccessLog, resources, bc.Security, authnService, authzService, auditService, skillService)
	grpcServer := server.NewGRPCServer(bc.Server, bc.Log.AccessLog, resources, bc.Security, authnService, authzService, auditService, skillService)

	opts := []kernel.Option{
		kernel.Name(bc.Service.Name),
		kernel.Version(bc.Service.Version),
		kernel.LogxLogger(logger),
		kernel.Logger(slogLogger),
		kernel.Metrics(metrics),
		kernel.Server(httpServer, grpcServer),
		kernel.StopTimeout(10 * time.Second),
	}
	if bc.Metrics.Enabled && bc.Metrics.Addr != "" {
		opts = append(opts, kernel.PrometheusMetrics(bc.Metrics.Addr))
		if bc.Metrics.Path != "" {
			opts = append(opts, kernel.MetricsPath(bc.Metrics.Path))
		}
		opts = append(opts, kernel.MetricsPprof(bc.Metrics.Pprof))
	}

	app := kernel.New(opts...)
	if err := app.Run(); err != nil {
		logger.Error("app run failed", logx.Err(err))
		panic(err)
	}
}

func applyBuildInfo(bc *conf.Bootstrap) {
	if bc.Service.Name == "" {
		bc.Service.Name = Name
	}
	if bc.Service.Version == "" {
		bc.Service.Version = Version
	}
	if bc.Log.ServiceName == "" {
		bc.Log.ServiceName = bc.Service.Name
	}
	if bc.Log.Version == "" {
		bc.Log.Version = bc.Service.Version
	}
}

func newLogger(cfg logx.Config) (logx.Logger, error) {
	if cfg.Output == "" {
		cfg.Output = string(logx.OutputStdout)
	}
	logger, _, err := logx.New(cfg)
	if err != nil {
		return nil, err
	}
	return logger, nil
}

func newMetrics(bc conf.Bootstrap, logger logx.Logger) metricsx.Manager {
	if !bc.Metrics.Enabled {
		return metricsx.Noop()
	}
	manager := metricsx.NewPrometheusManager(bc.Service.Name, bc.Service.Version, logger.Named("metrics"))
	metricsx.RegisterSystemMetrics(manager)
	return manager
}
