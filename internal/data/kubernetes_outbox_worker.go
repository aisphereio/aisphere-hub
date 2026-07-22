package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/logx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const credentialCleanupEventType = "credential_ref_cleanup"

// CredentialCleanupWorker consumes only credential_ref_cleanup rows. Filtering
// at claim time is critical: visibility/share events are converged by their own
// reconcilers and must never be acknowledged by this worker.
type CredentialCleanupWorker struct {
	outbox    credentialCleanupOutbox
	creds     biz.ClusterCredentialStore
	workerID  string
	batchSize int
	lease     time.Duration
	log       logx.Logger
}

type credentialCleanupOutbox interface {
	ClaimEventTypes(ctx context.Context, workerID string, eventTypes []string, limit int, lease time.Duration) ([]OutboxEvent, error)
	RequeueStuck(ctx context.Context) (int, error)
	Ack(ctx context.Context, id int64) error
	Nak(ctx context.Context, id int64, maxRetries int, baseBackoff time.Duration) error
}

func NewCredentialCleanupWorker(outbox OutboxRepo, creds biz.ClusterCredentialStore, log logx.Logger, batchSize int, lease time.Duration) (*CredentialCleanupWorker, error) {
	typed, ok := outbox.(credentialCleanupOutbox)
	if !ok {
		return nil, errors.New("kubernetes credential cleanup: outbox does not support typed claims")
	}
	if creds == nil {
		return nil, errors.New("kubernetes credential cleanup: credential store is required")
	}
	if batchSize <= 0 {
		batchSize = 32
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	if log == nil {
		log = logx.Noop()
	}
	host, _ := os.Hostname()
	if strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	return &CredentialCleanupWorker{
		outbox:    typed,
		creds:     creds,
		workerID:  "aisphere-hub/" + host,
		batchSize: batchSize,
		lease:     lease,
		log:       log.Named("data.k8s.credential_cleanup"),
	}, nil
}

func (w *CredentialCleanupWorker) Run(ctx context.Context) error {
	if requeued, err := w.outbox.RequeueStuck(ctx); err != nil {
		return fmt.Errorf("credential cleanup: requeue stuck rows: %w", err)
	} else if requeued > 0 {
		w.log.WithContext(ctx).Warn("credential cleanup: recovered expired claims", logx.Int("rows", requeued))
	}

	events, err := w.outbox.ClaimEventTypes(ctx, w.workerID, []string{credentialCleanupEventType}, w.batchSize, w.lease)
	if err != nil {
		return fmt.Errorf("credential cleanup: claim: %w", err)
	}

	var runErr error
	for _, event := range events {
		ref, _ := event.Payload["old_credential_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			err := errors.New("credential cleanup payload missing old_credential_ref")
			_ = w.outbox.Nak(ctx, event.ID, 5, 5*time.Second)
			runErr = errors.Join(runErr, err)
			continue
		}
		if err := w.creds.Delete(ctx, ref); err != nil {
			_ = w.outbox.Nak(ctx, event.ID, 5, 5*time.Second)
			runErr = errors.Join(runErr, fmt.Errorf("delete credential ref %s: %w", ref, err))
			continue
		}
		if err := w.outbox.Ack(ctx, event.ID); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("ack credential cleanup %d: %w", event.ID, err))
			continue
		}
		w.log.WithContext(ctx).Info("credential cleanup completed",
			logx.String("cluster_id", event.AggregateID),
			logx.String("credential_ref", ref),
		)
	}
	return runErr
}

// ClaimEventTypes is the event-filtered variant used by specialized workers.
// It uses the same SKIP LOCKED transaction as Claim, but constrains event_type
// before rows are marked in_progress.
func (r *outboxRepo) ClaimEventTypes(ctx context.Context, workerID string, eventTypes []string, limit int, lease time.Duration) ([]OutboxEvent, error) {
	_ = workerID // reserved for future claimed_by observability column
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("outbox: database not configured")
	}
	if len(eventTypes) == 0 {
		return nil, errors.New("outbox: at least one event type is required")
	}
	if limit <= 0 {
		limit = 16
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	now := time.Now()
	leaseUntil := now.Add(lease)

	var claimed []k8sOutboxModel
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND next_retry_at <= ?", "pending", now).
			Where("event_type IN ?", eventTypes).
			Order("id ASC").
			Limit(limit)
		if err := query.Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]int64, len(claimed))
		for i, row := range claimed {
			ids[i] = row.ID
		}
		return tx.Model(&k8sOutboxModel{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":        "in_progress",
				"next_retry_at": leaseUntil,
				"updated_at":    now,
			}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: claim event types: %w", err)
	}
	out := make([]OutboxEvent, len(claimed))
	for i, row := range claimed {
		out[i] = outboxModelToEvent(row)
	}
	return out, nil
}
