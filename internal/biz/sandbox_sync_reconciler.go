package biz

import (
	"context"

	"github.com/aisphereio/kernel/logx"
)

// SandboxSyncReconciler periodically converges sandbox/warm-pool/claim state
// for all READY namespaces. It is the safety net behind the manual Sync RPCs:
// when a user never clicks "sync", or a create's best-effort status backfill
// missed (Pod still starting), this reconciler eventually picks up the real
// runtime state from the cluster and stamps it in the DB.
//
// It mirrors VisibilityReconciler: list candidate namespaces → converge each.
// Unlike the visibility reconciler it does not use an outbox; sync is purely
// idempotent reconciliation (import/update/remove) with no side effects beyond
// DB writes + best-effort SpiceDB projection (already handled inside the Sync
// methods).
type SandboxSyncReconciler struct {
	sandboxes   *SandboxUsecase
	namespaces  NamespaceRepository
	log         logx.Logger
	batchSize   int
}

// NewSandboxSyncReconciler wires the reconciler. batchSize bounds the number of
// namespaces processed per tick (default 100).
func NewSandboxSyncReconciler(
	sandboxes *SandboxUsecase,
	namespaces NamespaceRepository,
	log logx.Logger,
	batchSize int,
) *SandboxSyncReconciler {
	if batchSize <= 0 {
		batchSize = 100
	}
	if log == nil {
		log = logx.Noop()
	}
	return &SandboxSyncReconciler{
		sandboxes:  sandboxes,
		namespaces: namespaces,
		log:        log.Named("biz.k8s.sandbox_sync_reconciler"),
		batchSize:  batchSize,
	}
}

// Run is the scheduler entry point. It lists all READY namespaces and calls
// ReconcileNamespaceSync for each. Errors on individual namespaces are logged
// and non-fatal — the reconciler continues to the next namespace.
func (r *SandboxSyncReconciler) Run(ctx context.Context) error {
	candidates, err := r.namespaces.ListReadyNamespaces(ctx, r.batchSize)
	if err != nil {
		r.log.WithContext(ctx).Error("reconciler: list ready namespaces failed", logx.Err(err))
		return err
	}
	if len(candidates) == 0 {
		return nil
	}

	converged, failed := 0, 0
	var totSb, totWp, totCl int
	for _, ns := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sbImp, sbUpd, _, wpImp, wpUpd, _, clImp, clUpd, _, err := r.sandboxes.ReconcileNamespaceSync(ctx, ns.ID)
		if err != nil {
			failed++
			r.log.WithContext(ctx).Warn("reconciler: namespace sync failed",
				logx.String("namespace_id", ns.ID),
				logx.String("kube_name", ns.KubeName),
				logx.Err(err))
			continue
		}
		converged++
		totSb += sbImp + sbUpd
		totWp += wpImp + wpUpd
		totCl += clImp + clUpd
	}

	if converged > 0 || failed > 0 {
		r.log.WithContext(ctx).Info("reconciler: sandbox sync pass complete",
			logx.Int("namespaces", len(candidates)),
			logx.Int("converged", converged),
			logx.Int("failed", failed),
			logx.Int("sandbox_changes", totSb),
			logx.Int("warm_pool_changes", totWp),
			logx.Int("claim_changes", totCl))
	}
	return nil
}
