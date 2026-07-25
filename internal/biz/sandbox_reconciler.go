package biz

import (
	"context"
	"time"

	"github.com/aisphereio/kernel/logx"
)

// sandboxSyncer isolates the three principal-free sync cores so the reconciler
// can be unit-tested with a stub instead of a fully wired SandboxUsecase.
// *SandboxUsecase satisfies this interface (same package, unexported methods).
type sandboxSyncer interface {
	syncSandboxClaimsCore(ctx context.Context, namespaceID string) (updated, removed, sandboxesLinked int, err error)
	syncWarmPoolsCore(ctx context.Context, namespaceID string) (updated, removed int, err error)
	syncSandboxesCore(ctx context.Context, namespaceID string) (imported, updated, removed int, err error)
}

// SandboxReconciler is the background worker behind the manual Sync buttons
// (design §11 / plan PR8). Every Interval it scans the least-recently-synced
// namespaces and converges SandboxClaim → WarmPool → Sandbox state from the
// observed CRD status, so a freshly created WarmPool/Claim reaches READY
// without an operator clicking "sync".
//
// Like VisibilityReconciler it runs with system-level trust and does NOT
// re-run authz: the namespace owner already authorized state creation on the
// synchronous path, and the reconciler only mirrors observed remote state.
type SandboxReconciler struct {
	syncer     sandboxSyncer
	namespaces NamespaceRepository
	log        logx.Logger
	batchSize  int
}

// NewSandboxReconciler builds a reconciler. batchSize bounds the per-pass scan
// (maps to ReconcileConfig.MaxScan); defaults to 100 when <= 0.
func NewSandboxReconciler(syncer sandboxSyncer, namespaces NamespaceRepository, log logx.Logger, batchSize int) *SandboxReconciler {
	if batchSize <= 0 {
		batchSize = 100
	}
	if log == nil {
		log = logx.Noop()
	}
	return &SandboxReconciler{
		syncer:     syncer,
		namespaces: namespaces,
		log:        log.Named("biz.k8s.sandbox_reconciler"),
		batchSize:  batchSize,
	}
}

// Run is the taskx.Scheduler handler. It is periodic (Every(Interval)) and must
// return nil on infra-level errors only; per-namespace failures are logged and
// counted so one bad namespace never aborts the whole pass.
func (r *SandboxReconciler) Run(ctx context.Context) error {
	namespaces, err := r.namespaces.ListNamespacesForReconcile(ctx, r.batchSize)
	if err != nil {
		r.log.WithContext(ctx).Error("sandbox reconciler: list namespaces failed", logx.Err(err))
		return err
	}
	if len(namespaces) == 0 {
		return nil
	}

	converged, failed := 0, 0
	now := time.Now().UTC()
	for _, ns := range namespaces {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.convergeOne(ctx, ns.ID, ns.KubeName); err != nil {
			failed++
			r.log.WithContext(ctx).Warn("sandbox reconciler: converge failed",
				logx.String("namespace_id", ns.ID),
				logx.String("kube_name", ns.KubeName),
				logx.Err(err))
		} else {
			converged++
		}
		// Stamp progress unconditionally so a failing namespace does not
		// monopolize the next pass (fair round-robin via last_sync_at ASC).
		if terr := r.namespaces.TouchNamespaceSync(ctx, ns.ID, now); terr != nil {
			r.log.WithContext(ctx).Warn("sandbox reconciler: touch sync failed",
				logx.String("namespace_id", ns.ID), logx.Err(terr))
		}
	}
	r.log.WithContext(ctx).Info("sandbox reconciler: pass complete",
		logx.Int("scanned", len(namespaces)),
		logx.Int("converged", converged),
		logx.Int("failed", failed))
	return nil
}

// convergeOne runs the three sync cores in dependency order for a single
// namespace:
//  1. Claims — so a claim-allocated sandbox gets its Hub row (owner = claim
//     owner) before SyncSandboxes would otherwise import it as ns-owner.
//  2. WarmPools — drive READY from observed readyReplicas.
//  3. Sandboxes — import/update/remove; byKubeName now includes any row the
//     claims step just created, so those are updated rather than re-imported.
func (r *SandboxReconciler) convergeOne(ctx context.Context, namespaceID, kubeName string) error {
	if _, _, _, err := r.syncer.syncSandboxClaimsCore(ctx, namespaceID); err != nil {
		return err
	}
	if _, _, err := r.syncer.syncWarmPoolsCore(ctx, namespaceID); err != nil {
		return err
	}
	if _, _, _, err := r.syncer.syncSandboxesCore(ctx, namespaceID); err != nil {
		return err
	}
	return nil
}
