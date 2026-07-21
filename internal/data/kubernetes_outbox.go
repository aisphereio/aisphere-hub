package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// k8s_outbox data adapter (design §8.5 / §7.5.3). The outbox drives reliable
// SpiceDB projection for visibility switches and delayed credential_ref
// cleanup. Multiple Hub replicas claim rows safely via SELECT FOR UPDATE SKIP
// LOCKED so the same event is processed exactly once across replicas.
//
// Enqueue is called *inside* the caller's DB transaction (design §7.5.3 step
// 1) so the outbox row commits atomically with the state change it describes.
// Claim/Ack/Nak are called by the reconcile worker (taskx.Scheduler job,
// design §7.5.5) on its own transaction.
//
// Status lifecycle: pending → in_progress (Claim) → done (Ack) or back to
// pending with retry_count++ (Nak). A row that exhausts retries is set to
// 'failed' and surfaces in operator metrics; the reconciler keeps it for
// inspection rather than silently dropping it.

// OutboxEvent is the biz-facing view of a queued event. Payload is opaque to
// the outbox layer; the reconciler interprets it per event_type.
type OutboxEvent struct {
	ID            int64
	AggregateType string // "cluster" | "namespace" | "namespace_share"
	AggregateID   string // cluster_id / namespace_id / share_id
	EventType     string // "credential_ref_cleanup" | "visibility_sync" | "share_sync" | "namespace_delete"
	Payload       map[string]any
	Status        string
	RetryCount    int
	NextRetryAt   time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OutboxRepo is the interface the reconciler depends on. Kept in data (not
// biz) because it is a pure persistence concern; the biz reconciler receives
// the repo via construction. biz.OutboxRepository in cluster.go would create a
// circular import (biz already imports nothing from data), so we define the
// interface here and biz accepts it as a constructor param.
type OutboxRepo interface {
	// Enqueue writes a pending row. MUST be called within the caller's open
	// *gorm.DB transaction so the outbox commit is atomic with the state
	// change (design §7.5.3 step 1). The caller passes the transactional DB
	// via the repo's db closure (resources.DB.GORM(ctx) already routes
	// through any active Tx).
	Enqueue(ctx context.Context, aggregateType, aggregateID, eventType string, payload map[string]any) error

	// Claim reserves up to limit pending rows for this worker, marking them
	// in_progress with next_retry_at pushed into the future so a crashed
	// worker's rows become re-claimable after the lease. Returns the claimed
	// rows in ID order. SELECT FOR UPDATE SKIP LOCKED makes this safe across
	// replicas (design §8.5).
	Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]OutboxEvent, error)

	// Ack marks a row done. Idempotent: a second Ack on a done row is a no-op.
	Ack(ctx context.Context, id int64) error

	// Nak returns a row to pending, increments retry_count, and sets
	// next_retry_at with exponential backoff. After maxRetries the row is set
	// to 'failed' instead of requeued (design §7.5.5).
	Nak(ctx context.Context, id int64, maxRetries int, baseBackoff time.Duration) error

	// ListPending returns pending rows older than a threshold, for monitoring
	// / operator inspection. Read-only; does not claim.
	ListPending(ctx context.Context, olderThan time.Duration, limit int) ([]OutboxEvent, error)
}

// outboxRepo implements OutboxRepo against the k8s_outbox table.
type outboxRepo struct {
	db func(context.Context) *gorm.DB
}

// NewOutboxRepo builds an OutboxRepo from the Resources DB closure.
func NewOutboxRepo(db func(context.Context) *gorm.DB) OutboxRepo {
	return &outboxRepo{db: db}
}

// k8sOutboxModel maps to the k8s_outbox table (migration §8.5). BIGSERIAL id
// so IDs are monotonic across replicas; payload_json is JSONB.
type k8sOutboxModel struct {
	ID            int64           `gorm:"primaryKey;column:id;autoIncrement"`
	AggregateType string          `gorm:"column:aggregate_type;size:64;not null"`
	AggregateID   string          `gorm:"column:aggregate_id;size:128;not null"`
	EventType     string          `gorm:"column:event_type;size:64;not null"`
	PayloadJSON   json.RawMessage `gorm:"column:payload_json;type:jsonb;not null;default:'{}'::jsonb"`
	Status        string          `gorm:"column:status;size:32;not null;default:pending"`
	RetryCount    int             `gorm:"column:retry_count;not null;default:0"`
	NextRetryAt   time.Time       `gorm:"column:next_retry_at;not null"`
	CreatedAt     time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt     time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (k8sOutboxModel) TableName() string { return "k8s_outbox" }

// Enqueue writes a pending row. The caller is responsible for wrapping this in
// a transaction with the state change it describes (design §7.5.3 step 1).
func (r *outboxRepo) Enqueue(ctx context.Context, aggregateType, aggregateID, eventType string, payload map[string]any) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("outbox: database not configured")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}
	row := k8sOutboxModel{
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		PayloadJSON:   payloadBytes,
		Status:        "pending",
		NextRetryAt:   time.Now(),
	}
	if err := db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("outbox: enqueue %s for %s/%s: %w", eventType, aggregateType, aggregateID, err)
	}
	return nil
}

// Claim reserves pending rows for this worker using SELECT FOR UPDATE SKIP
// LOCKED (design §8.5). Rows are marked in_progress and their next_retry_at
// is pushed forward by the lease so a crashed worker's rows become
// re-claimable. The FOR UPDATE lock is held until the caller commits/rolls
// back the transaction — Claim opens its own transaction so the lock is
// released when Claim returns (the row is already flipped to in_progress,
// which is what prevents re-claim by another worker).
func (r *outboxRepo) Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]OutboxEvent, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("outbox: database not configured")
	}
	if limit <= 0 {
		limit = 16
	}
	now := time.Now()
	leaseUntil := now.Add(lease)

	// SELECT ... FOR UPDATE SKIP LOCKED inside a tx, then UPDATE the claimed
	// rows to in_progress. We do it in one tx so the lock and the status flip
	// commit together.
	var claimed []k8sOutboxModel
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND next_retry_at <= ?", "pending", now).
			Order("id ASC").
			Limit(limit).
			Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		ids := make([]int64, len(claimed))
		for i, c := range claimed {
			ids[i] = c.ID
		}
		// Flip to in_progress and push next_retry_at forward as the lease. A
		// crashed worker leaves the row in_progress; after leaseUntil passes
		// another worker can re-claim it because the Claim filter only
		// selects status='pending'. To recover stuck in_progress rows we
		// also requeue them in a separate sweep (RequeueStuck, below) — but
		// for V1 the lease-on-next_retry_at approach plus a periodic
		// RequeueStuck covers it.
		return tx.Model(&k8sOutboxModel{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"status":        "in_progress",
				"next_retry_at": leaseUntil,
				"updated_at":    now,
			}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	out := make([]OutboxEvent, len(claimed))
	for i, c := range claimed {
		out[i] = outboxModelToEvent(c)
	}
	return out, nil
}

// RequeueStuck flips in_progress rows whose lease has expired back to pending
// so they can be re-claimed. Called by the reconciler at the start of each
// tick. Not part of the OutboxRepo interface (internal housekeeping) but
// exposed so the reconciler can call it.
func (r *outboxRepo) RequeueStuck(ctx context.Context) (int, error) {
	db := r.db(ctx)
	if db == nil {
		return 0, errors.New("outbox: database not configured")
	}
	now := time.Now()
	res := db.WithContext(ctx).Model(&k8sOutboxModel{}).
		Where("status = ? AND next_retry_at <= ?", "in_progress", now).
		Update("status", "pending")
	if res.Error != nil {
		return 0, fmt.Errorf("outbox: requeue stuck: %w", res.Error)
	}
	return int(res.RowsAffected), nil
}

// Ack marks a row done. Idempotent.
func (r *outboxRepo) Ack(ctx context.Context, id int64) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("outbox: database not configured")
	}
	res := db.WithContext(ctx).Model(&k8sOutboxModel{}).
		Where("id = ? AND status = ?", id, "in_progress").
		Updates(map[string]any{"status": "done", "updated_at": time.Now()})
	if res.Error != nil {
		return fmt.Errorf("outbox: ack %d: %w", id, res.Error)
	}
	// RowsAffected 0 means the row was already done or not in_progress —
	// both are safe to treat as success (idempotent Ack).
	return nil
}

// Nak returns a row to pending with retry_count++ and exponential backoff.
// After maxRetries the row is set to 'failed' instead of requeued.
func (r *outboxRepo) Nak(ctx context.Context, id int64, maxRetries int, baseBackoff time.Duration) error {
	db := r.db(ctx)
	if db == nil {
		return errors.New("outbox: database not configured")
	}
	if maxRetries < 1 {
		maxRetries = 5
	}
	if baseBackoff <= 0 {
		baseBackoff = 5 * time.Second
	}
	now := time.Now()

	// Load the row to read retry_count. We could do this in one UPDATE with a
	// CASE expression, but the two-step is clearer and the row is already
	// locked by the caller's Claim (in_progress).
	var row k8sOutboxModel
	if err := db.WithContext(ctx).First(&row, id).Error; err != nil {
		return fmt.Errorf("outbox: nak load %d: %w", id, err)
	}
	nextRetry := row.RetryCount + 1
	if nextRetry > maxRetries {
		// Exhausted: mark failed. The reconciler surfaces failed rows in
		// metrics; an operator inspects and re-enqueues manually.
		res := db.WithContext(ctx).Model(&k8sOutboxModel{}).
			Where("id = ? AND status = ?", id, "in_progress").
			Updates(map[string]any{"status": "failed", "retry_count": nextRetry, "updated_at": now})
		if res.Error != nil {
			return fmt.Errorf("outbox: nak fail %d: %w", id, res.Error)
		}
		return biz.ErrClusterFailedPrecondition
	}
	// Exponential backoff: base * 2^(retry_count). Cap at ~1h to avoid
	// starving recovery on a poison row.
	backoff := baseBackoff << uint(nextRetry)
	if backoff > time.Hour {
		backoff = time.Hour
	}
	res := db.WithContext(ctx).Model(&k8sOutboxModel{}).
		Where("id = ? AND status = ?", id, "in_progress").
		Updates(map[string]any{
			"status":        "pending",
			"retry_count":   nextRetry,
			"next_retry_at": now.Add(backoff),
			"updated_at":    now,
		})
	if res.Error != nil {
		return fmt.Errorf("outbox: nak %d: %w", id, res.Error)
	}
	return nil
}

// ListPending returns pending rows older than olderThan for monitoring.
func (r *outboxRepo) ListPending(ctx context.Context, olderThan time.Duration, limit int) ([]OutboxEvent, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errors.New("outbox: database not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	threshold := time.Now().Add(-olderThan)
	var rows []k8sOutboxModel
	if err := db.WithContext(ctx).
		Where("status = ? AND next_retry_at <= ?", "pending", threshold).
		Order("next_retry_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("outbox: list pending: %w", err)
	}
	out := make([]OutboxEvent, len(rows))
	for i, row := range rows {
		out[i] = outboxModelToEvent(row)
	}
	return out, nil
}

func outboxModelToEvent(m k8sOutboxModel) OutboxEvent {
	var payload map[string]any
	if len(m.PayloadJSON) > 0 {
		_ = json.Unmarshal(m.PayloadJSON, &payload)
	}
	return OutboxEvent{
		ID:            m.ID,
		AggregateType: m.AggregateType,
		AggregateID:   m.AggregateID,
		EventType:     m.EventType,
		Payload:       payload,
		Status:        m.Status,
		RetryCount:    m.RetryCount,
		NextRetryAt:   m.NextRetryAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}
