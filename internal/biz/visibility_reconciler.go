package biz

import (
	"context"
	"fmt"
	"time"

	"github.com/aisphereio/kernel/logx"
)

// VisibilityReconciler converges namespaces whose visibility_sync_status is
// not SYNCED toward their desired visibility (design §7.5.5). It is the
// safety net behind the synchronous UpdateNamespaceVisibility path: when the
// SpiceDB projection fails mid-switch the row lands in PUBLISHING/REVOKING/
// SYNC_FAILED, and this reconciler retries until it converges or exhausts.
//
// Convergence is one-direction: the DB desired visibility is the source of
// truth; the reconciler projects it into SpiceDB (write wildcard for PUBLIC,
// delete wildcard for PRIVATE) and stamps SYNCED on success. It never flips
// the DB desired value — that is the caller's job.
//
// Run is a taskx.Handler (func(context.Context) error) registered with the
// taskx.Scheduler (design §7.5.5 / decision 4). Cross-replica singleton via
// RedisLocker lease so only one Hub instance runs reconcile per tick.
type VisibilityReconciler struct {
	namespaces NamespaceRepository
	rels       NamespaceRelationships
	outbox     OutboxClaimer // optional: V1 reconciler does direct projection, outbox is for credential cleanup
	log        logx.Logger
	batchSize  int
}

// OutboxClaimer is the optional outbox surface the reconciler uses for
// credential_ref_cleanup and namespace_cleanup events. nil disables outbox
// processing (V1 visibility reconcile is direct).
type OutboxClaimer interface {
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]OutboxEvent, error)
	Ack(ctx context.Context, id int64) error
	Nak(ctx context.Context, id int64, maxRetries int, baseBackoff time.Duration) error
}

// OutboxEvent mirrors data.OutboxEvent (kept in biz so the reconciler doesn't
// import data). The data layer converts at the boundary.
type OutboxEvent struct {
	ID            int64
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       map[string]any
	Status        string
	RetryCount    int
	NextRetryAt   time.Time
}

// NewVisibilityReconciler builds the reconciler. batchSize defaults to 100.
// outbox may be nil (V1 visibility reconcile is direct; outbox claimer is for
// future credential cleanup integration).
func NewVisibilityReconciler(
	namespaces NamespaceRepository,
	rels NamespaceRelationships,
	outbox OutboxClaimer,
	log logx.Logger,
	batchSize int,
) *VisibilityReconciler {
	if batchSize <= 0 {
		batchSize = 100
	}
	if log == nil {
		log = logx.Noop()
	}
	return &VisibilityReconciler{
		namespaces: namespaces,
		rels:       rels,
		outbox:     outbox,
		log:        log.Named("biz.k8s.reconciler"),
		batchSize:  batchSize,
	}
}

// Run is the taskx.Handler entry point. It scans namespaces in
// PUBLISHING/REVOKING/SYNC_FAILED and converges each toward desired. Returns
// nil on completion (partial progress is fine — the next tick resumes).
func (r *VisibilityReconciler) Run(ctx context.Context) error {
	converged, failed := 0, 0
	for _, status := range []string{VisibilitySyncPublishing, VisibilitySyncRevoking, VisibilitySyncFailed} {
		ns, err := r.namespaces.ListNamespacesBySyncStatus(ctx, status, r.batchSize)
		if err != nil {
			r.log.WithContext(ctx).Error("reconciler: list namespaces failed",
				logx.String("sync_status", status), logx.Err(err))
			return err
		}
		for _, n := range ns {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := r.convergeOne(ctx, n); err != nil {
				failed++
				r.log.WithContext(ctx).Warn("reconciler: converge failed",
					logx.String("namespace_id", n.ID),
					logx.String("desired", n.Visibility),
					logx.Err(err))
				continue
			}
			converged++
		}
	}
	if converged > 0 || failed > 0 {
		r.log.WithContext(ctx).Info("reconciler: visibility pass complete",
			logx.Int("converged", converged), logx.Int("failed", failed))
	}
	return nil
}

// convergeOne projects the namespace's desired visibility into SpiceDB and
// stamps SYNCED on success (design §7.5.5 single-direction converge).
func (r *VisibilityReconciler) convergeOne(ctx context.Context, ns *Namespace) error {
	resource := namespaceResource(ns.ID)
	wildcard := AuthzSubjectRef{Type: "user", Relation: "..."}
	if ns.Visibility == NamespaceVisibilityPublic {
		// Ensure wildcard viewer exists (idempotent TOUCH).
		if _, err := r.rels.WriteRelationships(ctx, AuthzRelationship{
			Resource: resource, Relation: "viewer", Subject: wildcard,
		}); err != nil {
			return fmt.Errorf("project PUBLIC wildcard: %w", err)
		}
	} else {
		// Ensure no wildcard viewer (idempotent delete).
		if _, err := r.rels.DeleteRelationships(ctx, AuthzRelationshipFilter{
			ResourceType:    "k8s_namespace",
			ResourceID:      ns.ID,
			Relation:        "viewer",
			SubjectType:     "user",
			SubjectRelation: "...",
		}); err != nil {
			return fmt.Errorf("project PRIVATE (revoke wildcard): %w", err)
		}
	}
	// Stamp SYNCED. Use CAS with current revision; if a concurrent writer
	// changed the row, the next tick picks it up.
	_, err := r.namespaces.UpdateNamespaceVisibility(ctx, ns.ID, ns.Revision, ns.Visibility, VisibilitySyncSynced)
	if err != nil {
		return fmt.Errorf("stamp SYNCED: %w", err)
	}
	return nil
}
