package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/aisphere-hub/internal/service"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/taskx"
)

type kubernetesRuntime struct {
	clusterService   *service.ClusterService
	namespaceService *service.NamespaceService
	sandboxService   *service.SandboxService
	scheduler        *taskx.Scheduler
	close            func() error
}

func wireKubernetesRuntime(
	ctx context.Context,
	bc conf.Bootstrap,
	resources *data.Resources,
	authzUsecase *biz.AuthzUsecase,
	logger logx.Logger,
) (*kubernetesRuntime, error) {
	runtime := &kubernetesRuntime{close: func() error { return nil }}
	if !bc.Kubernetes.Enabled {
		return runtime, nil
	}
	if resources.DB == nil {
		return nil, errors.New("kubernetes runtime requires database")
	}

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
	runtime.clusterService = service.NewClusterService(clusterUC)
	runtime.namespaceService = service.NewNamespaceService(namespaceUC)

	// Sandbox usecase (design §11). Reuses the same KubernetesProvider pool
	// (which now also implements biz.SandboxProvider) and the same outbox for
	// async cleanup. The concrete k8sClientPool implements both interfaces; if
	// K8s is disabled (noClientPool), the sandbox provider is nil and the
	// sandbox service is not registered.
	sandboxRepo := data.NewSandboxRepo(resources)
	var sandboxProvider biz.SandboxProvider
	if sp, ok := resources.KubernetesClientPool.(biz.SandboxProvider); ok {
		sandboxProvider = sp
	}
	if sandboxProvider != nil {
		sandboxUC := biz.NewSandboxUsecase(
			sandboxRepo,
			namespaceRepo,
			clusterRepo,
			sandboxProvider,
			outboxRepo,
			authzUsecase,
			logger,
			biz.ClusterUsecaseOptions{
				MaxScan:          bc.Kubernetes.Reconcile.MaxScan,
				MaxHydrateRounds: bc.Kubernetes.Reconcile.MaxHydrateRounds,
			},
		)
		runtime.sandboxService = service.NewSandboxService(sandboxUC)
	}

	interval := bc.Kubernetes.Reconcile.Interval
	if interval > 0 {
		schedulerOptions := make([]taskx.Option, 0, 1)
		closeLocker := func() error { return nil }
		leaseTTL := bc.Kubernetes.Reconcile.LeaseTTL
		leaseEnabled := leaseTTL > 0
		if leaseEnabled {
			if !bc.Data.Cache.Enabled {
				return nil, errors.New("kubernetes reconcile lease requires data.cache.enabled=true")
			}
			locker, closeFn, err := data.NewKubernetesTaskLocker(ctx, bc.Data.Cache.Config)
			if err != nil {
				return nil, err
			}
			closeLocker = closeFn
			schedulerOptions = append(schedulerOptions, taskx.WithLocker(locker))
		}

		scheduler := taskx.NewScheduler(schedulerOptions...)
		lease := taskx.LeaseOptions{Enabled: leaseEnabled, TTL: leaseTTL}
		retry := taskx.RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: 5 * time.Second,
			MaxBackoff:     time.Minute,
			Multiplier:     2,
		}

		visibility := biz.NewVisibilityReconciler(namespaceRepo, authzUsecase, nil, logger, 100)
		if err := scheduler.Register(taskx.Job{
			Name:       "k8s-visibility-reconciler",
			Schedule:   taskx.Every(interval),
			Handler:    visibility.Run,
			RunOnStart: true,
			Timeout:    2 * time.Minute,
			Retry:      retry,
			Lease:      lease,
		}); err != nil {
			_ = closeLocker()
			return nil, fmt.Errorf("register visibility reconciler: %w", err)
		}

		cleanupWorker, err := data.NewCredentialCleanupWorker(
			outboxRepo,
			resources.KubernetesCredStore,
			logger,
			32,
			leaseTTL,
		)
		if err != nil {
			_ = closeLocker()
			return nil, err
		}
		if err := scheduler.Register(taskx.Job{
			Name:       "k8s-credential-cleanup",
			Schedule:   taskx.Every(interval),
			Handler:    cleanupWorker.Run,
			RunOnStart: true,
			Timeout:    2 * time.Minute,
			Retry:      retry,
			Lease: taskx.LeaseOptions{
				Enabled: leaseEnabled,
				Key:     "k8s-credential-cleanup",
				TTL:     leaseTTL,
			},
		}); err != nil {
			_ = closeLocker()
			return nil, fmt.Errorf("register credential cleanup: %w", err)
		}

		runtime.scheduler = scheduler
		runtime.close = func() error {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := scheduler.Shutdown(shutdownCtx); err != nil && !errors.Is(err, taskx.ErrNotStarted) {
				logger.Warn("kubernetes scheduler shutdown failed", logx.Err(err))
			}
			return closeLocker()
		}
	}

	if err := biz.BootstrapClusterRelationships(ctx, clusterRepo, authzUsecase, logger); err != nil {
		logger.Warn("k8s cluster authz bootstrap failed; historical cluster permissions may be incomplete", logx.Err(err))
	}
	return runtime, nil
}
