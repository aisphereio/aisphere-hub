package data

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/logx"
)

// CredentialCleanupWorker consumes the Kubernetes outbox. The outbox claim
// path uses SELECT FOR UPDATE SKIP LOCKED, so multiple Hub replicas can run the
// worker safely without deleting the same credential twice.
//
// credential_ref_cleanup performs the delayed delete created by
// RotateCredential. visibility_sync is an acknowledgement record emitted only
// after the synchronous SpiceDB projection succeeded; it can be archived here.
type CredentialCleanupWorker struct {
	outbox      OutboxRepo
	credentials biz.ClusterCredentialStore
	log         logx.Logger
	workerID    string
	batchSize   int
	lease       time.Duration
	maxRetries  int
	backoff     time.Duration
}

// NewCredentialCleanupWorker creates the outbox worker. Zero values are
// normalized to conservative production defaults.
func NewCredentialCleanupWorker(outbox OutboxRepo, credentials biz.ClusterCredentialStore, log logx.Logger) *CredentialCleanupWorker {
	if log == nil {
		log = logx.Noop()
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return &CredentialCleanupWorker{
		outbox:      outbox,
		credentials: credentials,
		log:         log.Named("data.k8s.outbox-worker"),
		workerID:    fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		batchSize:   32,
		lease:       2 * time.Minute,
		maxRetries:  8,
		backoff:     5 * time.Second,
	}
}

// Run processes one bounded outbox batch. It is intentionally idempotent:
// credential deletion tolerates an already-removed row and Ack is idempotent.
func (w *CredentialCleanupWorker) Run(ctx context.Context) error {
	if w == nil || w.outbox == nil || w.credentials == nil {
		return nil
	}

	// Recover events left in_progress by a replica that died after Claim.
	if requeuer, ok := w.outbox.(interface {
		RequeueStuck(context.Context) (int, error)
	}); ok {
		requeued, err := requeuer.RequeueStuck(ctx)
		if err != nil {
			return fmt.Errorf("k8s outbox requeue stuck: %w", err)
		}
		if requeued > 0 {
			w.log.WithContext(ctx).Warn("requeued expired kubernetes outbox leases", logx.Int("count", requeued))
		}
	}

	events, err := w.outbox.Claim(ctx, w.workerID, w.batchSize, w.lease)
	if err != nil {
		return fmt.Errorf("claim kubernetes outbox: %w", err)
	}

	processed, failed := 0, 0
	for _, event := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.process(ctx, event); err != nil {
			failed++
			w.log.WithContext(ctx).Warn("kubernetes outbox event failed",
				logx.Int64("event_id", event.ID),
				logx.String("event_type", event.EventType),
				logx.Err(err))
			if nakErr := w.outbox.Nak(context.WithoutCancel(ctx), event.ID, w.maxRetries, w.backoff); nakErr != nil {
				w.log.WithContext(ctx).Error("kubernetes outbox nak failed",
					logx.Int64("event_id", event.ID), logx.Err(nakErr))
			}
			continue
		}
		if err := w.outbox.Ack(context.WithoutCancel(ctx), event.ID); err != nil {
			return fmt.Errorf("ack kubernetes outbox event %d: %w", event.ID, err)
		}
		processed++
	}

	if processed > 0 || failed > 0 {
		w.log.WithContext(ctx).Info("kubernetes outbox pass complete",
			logx.Int("processed", processed), logx.Int("failed", failed))
	}
	return nil
}

func (w *CredentialCleanupWorker) process(ctx context.Context, event OutboxEvent) error {
	switch event.EventType {
	case "credential_ref_cleanup":
		ref, _ := event.Payload["old_credential_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return fmt.Errorf("credential_ref_cleanup payload missing old_credential_ref")
		}
		if err := w.credentials.Delete(ctx, ref); err != nil {
			return fmt.Errorf("delete old credential %s: %w", ref, err)
		}
		return nil
	case "visibility_sync":
		// UpdateNamespaceVisibility enqueues this only after the synchronous
		// SpiceDB projection and DB SYNCED stamp have both succeeded. The durable
		// row is therefore an audit/confirmation record, not pending work.
		return nil
	default:
		return fmt.Errorf("unsupported kubernetes outbox event type %q", event.EventType)
	}
}
