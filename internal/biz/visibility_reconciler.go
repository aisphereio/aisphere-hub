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
type VisibilityReconciler struct {
	namespaces NamespaceRepository
	rels       NamespaceRelationships
	outbox     OutboxClaimer
	log        logx.Logger
	batchSize  int
}

type OutboxClaimer interface {
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]OutboxEvent, error)
	Ack(ctx context.Context, id int64) error
	Nak(ctx context.Context, id int64, maxRetries int, baseBackoff time.Duration) error
}

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

func (r *VisibilityReconciler) convergeOne(ctx context.Context, ns *Namespace) error {
	resource := namespaceResource(ns.ID)
	wildcard := AuthzSubjectRef{Type: "user", ID: "*"}
	if ns.Visibility == NamespaceVisibilityPublic {
		if _, err := r.rels.WriteRelationships(ctx, AuthzRelationship{
			Resource: resource, Relation: "viewer", Subject: wildcard,
		}); err != nil {
			return fmt.Errorf("project PUBLIC wildcard: %w", err)
		}
	} else {
		if _, err := r.rels.DeleteRelationships(ctx, AuthzRelationshipFilter{
			ResourceType: "k8s_namespace",
			ResourceID:   ns.ID,
			Relation:     "viewer",
			SubjectType:  "user",
			SubjectID:    "*",
		}); err != nil {
			return fmt.Errorf("project PRIVATE (revoke wildcard): %w", err)
		}
	}

	updated, err := r.namespaces.UpdateNamespaceVisibility(ctx, ns.ID, ns.Revision, ns.Visibility, VisibilitySyncSynced)
	if err != nil {
		return fmt.Errorf("stamp SYNCED: %w", err)
	}

	// A visibility projection failure used to leave lifecycle=FAILED forever,
	// even after the reconciler successfully repaired SpiceDB. Recover only when
	// the remote namespace is known to exist (UID populated); a genuine remote
	// creation failure has no UID and remains FAILED for operator action.
	if updated.Lifecycle == NamespaceLifecycleFailed && updated.KubernetesUID != "" {
		if _, err := r.namespaces.UpdateNamespaceStatus(ctx, updated.ID, NamespaceLifecycleFailed, NamespaceLifecycleReady, map[string]any{
			"last_error_code":    "",
			"last_error_message": "",
			"last_sync_at":       time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("recover namespace lifecycle: %w", err)
		}
	}
	return nil
}
