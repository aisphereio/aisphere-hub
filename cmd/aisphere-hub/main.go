package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/gitengine"
	"github.com/aisphereio/aisphere-hub/internal/observability"
	"github.com/aisphereio/aisphere-hub/internal/server"
	"github.com/aisphereio/aisphere-hub/internal/service"

	kernel "github.com/aisphereio/kernel"
	"github.com/aisphereio/kernel/configx"
	configenv "github.com/aisphereio/kernel/configx/env"
	"github.com/aisphereio/kernel/configx/file"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
	"github.com/aisphereio/kernel/taskx"
	"github.com/redis/go-redis/v9"
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
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		os.Exit(gitengine.RunHook(context.Background(), os.Args[2:], os.Stdin, os.Stderr))
	}
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

	// The shared database requires deterministic migration ordering. Preserve
	// Hub's migration config, prevent NewResources from applying it early, then
	// run Soft Serve migrations before Kernel migrationx below.
	hubMigration := bc.Data.Database.Migration
	bc.Data.Database.Migration.Enabled = false

	resources, cleanup, err := data.NewResources(bootstrapCtx, bc)
	if err != nil {
		logger.Error("resource initialization failed", logx.Err(err))
		panic(err)
	}
	defer cleanup()
	if err := data.ApplyStorageMigrations(bootstrapCtx, resources.DB, bc.Skill.Git.DataPath, hubMigration); err != nil {
		logger.Error("storage migration failed", logx.Err(err))
		panic(err)
	}

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

	// Wire the skill module. Kernel/dbx owns the PostgreSQL connection pool;
	// the embedded Git engine reuses the same pool for Soft Serve metadata.
	if resources.DB == nil {
		logger.Error("embedded git engine requires the Kernel database")
		panic("embedded git engine requires database")
	}
	gitEngine, err := gitengine.New(bootstrapCtx, gitengine.Config{
		DataPath:      bc.Skill.Git.DataPath,
		IAMEndpoint:   bc.Security.Authz.IAMGRPC.Endpoint,
		IAMInsecure:   bc.Security.Authz.IAMGRPC.Insecure,
		IAMCaller:     bc.Security.Authz.IAMGRPC.CallerService,
		DefaultBranch: biz.SkillDefaultBranch,
	}, resources.DB.GORM(bootstrapCtx))
	if err != nil {
		logger.Error("embedded git engine initialization failed", logx.Err(err))
		panic(err)
	}
	defer gitEngine.Close()

	skillRepo := data.NewSkillRepo(resources)
	pullRequestRepo := data.NewPullRequestRepo(resources)
	skillUsecase := biz.NewSkillUsecase(skillRepo, pullRequestRepo, gitEngine, authzUsecase)

	// Attach optional project validator when authz is enabled.
	if bc.Security.Authz.Enabled && !bc.Security.Authz.DevAllowAll {
		projectValidator, err := data.NewProjectValidator(
			bc.Security.Authz.IAMGRPC.Endpoint,
			bc.Security.Authz.IAMGRPC.CallerService,
			bc.Security.Authz.IAMGRPC.Insecure,
		)
		if err != nil {
			logger.Error("project validator initialization failed", logx.Err(err))
			panic(err)
		}
		defer projectValidator.Close()
		skillUsecase.WithProjectValidator(projectValidator)
	}
	skillService := service.NewSkillService(skillUsecase)
	// File-content API sits alongside the skill service as a convenience
	// layer over the same bare git repo. Authz is enforced inside the
	// usecase (writes bypass the receive-pack update hook).
	fileUsecase := biz.NewFileUsecase(gitEngine, authzUsecase)
	fileService := service.NewFileService(fileUsecase)

	// Repair durable owner relationships through IAM's runtime authorization API.
	if err := data.BootstrapAuthzRelationships(bootstrapCtx, resources, logger); err != nil {
		logger.Warn("authz relationship bootstrap failed; historical skill permissions may be incomplete", logx.Err(err))
	}

	// Wire the Kubernetes cluster management plane (design §5/§6/§7.5.5).
	var clusterService *service.ClusterService
	var namespaceService *service.NamespaceService
	var k8sScheduler *taskx.Scheduler
	var k8sLeaseClient redis.UniversalClient
	if bc.Kubernetes.Enabled {
		clusterRepo := data.NewClusterRepo(resources)
		namespaceRepo := data.NewNamespaceRepo(resources)
		outboxRepo := data.NewOutboxRepo(resources.DB.GORM)

		clusterUC := biz.NewClusterUsecase(
			clusterRepo,
			resources.KubernetesCredStore,
			resources.KubernetesEndpointPolicy,
			resources.KubernetesClientPool,
			outboxRepo,
			authzUsecase,
			logger,
			biz.ClusterUsecaseOptions{
				MaxScan:          bc.Kubernetes.Reconcile.MaxScan,
				MaxHydrateRounds: bc.Kubernetes.Reconcile.MaxHydrateRounds,
				ProbeTimeout:     30 * time.Second,
			},
		)
		namespaceUC := biz.NewNamespaceUsecase(
			namespaceRepo,
			clusterRepo,
			resources.KubernetesClientPool,
			outboxRepo,
			authzUsecase,
			logger,
			biz.ClusterUsecaseOptions{
				MaxScan:          bc.Kubernetes.Reconcile.MaxScan,
				MaxHydrateRounds: bc.Kubernetes.Reconcile.MaxHydrateRounds,
			},
		)
		clusterService = service.NewClusterService(clusterUC)
		namespaceService = service.NewNamespaceService(namespaceUC)

		if bc.Kubernetes.Reconcile.Interval > 0 {
			schedulerOptions := make([]taskx.Option, 0, 1)
			leaseEnabled := false
			if bc.Data.Cache.Enabled && len(bc.Data.Cache.Config.Addrs) > 0 {
				cacheCfg := bc.Data.Cache.Config
				redisOptions := &redis.UniversalOptions{
					Addrs:        cacheCfg.Addrs,
					Username:     cacheCfg.Username,
					Password:     cacheCfg.Password,
					DB:           cacheCfg.DB,
					MasterName:   cacheCfg.MasterName,
					PoolSize:     cacheCfg.PoolSize,
					MinIdleConns: cacheCfg.MinIdleConns,
					DialTimeout:  cacheCfg.DialTimeout,
					ReadTimeout:  cacheCfg.ReadTimeout,
					WriteTimeout: cacheCfg.WriteTimeout,
				}
				if cacheCfg.TLSEnabled {
					redisOptions.TLSConfig = &tls.Config{ // #nosec G402 -- operator-controlled compatibility flag
						MinVersion:         tls.VersionTLS12,
						InsecureSkipVerify: cacheCfg.TLSSkipVerify,
					}
				}
				k8sLeaseClient = redis.NewUniversalClient(redisOptions)
				pingCtx, cancel := context.WithTimeout(bootstrapCtx, 5*time.Second)
				if err := k8sLeaseClient.Ping(pingCtx).Err(); err != nil {
					cancel()
					logger.Error("k8s task lease redis unavailable", logx.Err(err))
					panic(err)
				}
				cancel()
				defer k8sLeaseClient.Close()
				prefix := cacheCfg.KeyPrefix
				if prefix != "" {
					prefix += ":"
				}
				schedulerOptions = append(schedulerOptions, taskx.WithLocker(taskx.NewRedisLocker(k8sLeaseClient, prefix+"taskx:lease:")))
				leaseEnabled = true
			} else {
				logger.Warn("k8s reconciler distributed lease disabled because Redis cache is not configured")
			}

			k8sScheduler = taskx.NewScheduler(schedulerOptions...)
			leaseTTL := bc.Kubernetes.Reconcile.LeaseTTL
			if leaseTTL < 3*time.Minute {
				leaseTTL = 3 * time.Minute
			}
			lease := taskx.LeaseOptions{Enabled: leaseEnabled, TTL: leaseTTL, RenewInterval: leaseTTL / 3}

			reconciler := biz.NewVisibilityReconciler(namespaceRepo, authzUsecase, nil, logger, 100)
			if err := k8sScheduler.Register(taskx.Job{
				Name:       "k8s-visibility-reconciler",
				Schedule:   taskx.Every(bc.Kubernetes.Reconcile.Interval),
				Handler:    reconciler.Run,
				RunOnStart: true,
				Timeout:    2 * time.Minute,
				Lease:      lease,
				Retry: taskx.RetryPolicy{
					MaxAttempts:    3,
					InitialBackoff: 5 * time.Second,
					MaxBackoff:     1 * time.Minute,
					Multiplier:     2,
				},
			}); err != nil {
				logger.Error("k8s reconciler registration failed", logx.Err(err))
				panic(err)
			}

			cleanupWorker := data.NewCredentialCleanupWorker(outboxRepo, resources.KubernetesCredStore, logger)
			if err := k8sScheduler.Register(taskx.Job{
				Name:       "k8s-outbox-cleanup",
				Schedule:   taskx.Every(bc.Kubernetes.Reconcile.Interval),
				Handler:    cleanupWorker.Run,
				RunOnStart: true,
				Timeout:    2 * time.Minute,
				Lease: taskx.LeaseOptions{
					Enabled:       leaseEnabled,
					Key:           "k8s-outbox-cleanup",
					TTL:           leaseTTL,
					RenewInterval: leaseTTL / 3,
				},
				Retry: taskx.RetryPolicy{
					MaxAttempts:    3,
					InitialBackoff: 5 * time.Second,
					MaxBackoff:     1 * time.Minute,
					Multiplier:     2,
				},
			}); err != nil {
				logger.Error("k8s outbox worker registration failed", logx.Err(err))
				panic(err)
			}
		}

		// Bootstrap k8s SpiceDB relationships (warn, not fatal — design §7.6.6).
		if err := biz.BootstrapClusterRelationships(bootstrapCtx, clusterRepo, authzUsecase, logger); err != nil {
			logger.Warn("k8s cluster authz bootstrap failed; historical cluster permissions may be incomplete", logx.Err(err))
		}
	}

	httpServer := server.NewHTTPServer(bc.Server, bc.Log.AccessLog, resources, bc.Security, gitEngine, authnService, authzService, auditService, skillService, clusterService, namespaceService, fileService)
	grpcServer := server.NewGRPCServer(bc.Server, bc.Log.AccessLog, resources, bc.Security, authnService, authzService, auditService, skillService, clusterService, namespaceService, fileService)

	opts := []kernel.Option{
		kernel.Name(bc.Service.Name),
		kernel.Version(bc.Service.Version),
		kernel.LogxLogger(logger),
		kernel.Logger(slogLogger),
		kernel.Metrics(metrics),
		kernel.Server(httpServer, grpcServer),
		kernel.StopTimeout(10 * time.Second),
	}
	if k8sScheduler != nil {
		opts = append(opts, kernel.AfterStart(func(ctx context.Context) error {
			if err := k8sScheduler.Start(ctx); err != nil {
				logger.Error("k8s scheduler start failed", logx.Err(err))
				return err
			}
			return nil
		}))
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
