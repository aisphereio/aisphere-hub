package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
)

type cleanupTestOutbox struct {
	events   []OutboxEvent
	acked    []int64
	nacked   []int64
	requeued bool
}

func (o *cleanupTestOutbox) Enqueue(context.Context, string, string, string, map[string]any) error { return nil }
func (o *cleanupTestOutbox) Claim(context.Context, string, int, time.Duration) ([]OutboxEvent, error) {
	return o.events, nil
}
func (o *cleanupTestOutbox) Ack(_ context.Context, id int64) error {
	o.acked = append(o.acked, id)
	return nil
}
func (o *cleanupTestOutbox) Nak(_ context.Context, id int64, _ int, _ time.Duration) error {
	o.nacked = append(o.nacked, id)
	return nil
}
func (o *cleanupTestOutbox) ListPending(context.Context, time.Duration, int) ([]OutboxEvent, error) {
	return nil, nil
}
func (o *cleanupTestOutbox) RequeueStuck(context.Context) (int, error) {
	o.requeued = true
	return 1, nil
}

type cleanupTestCredentialStore struct {
	deleted []string
	err     error
}

func (*cleanupTestCredentialStore) NewCredentialRef() (string, error) { return "ref", nil }
func (*cleanupTestCredentialStore) Put(context.Context, string, int64, kubernetesx.Credential) (biz.CredentialLocator, error) {
	return biz.CredentialLocator{}, nil
}
func (*cleanupTestCredentialStore) PutWithRef(context.Context, string, string, int64, kubernetesx.Credential) (biz.CredentialLocator, error) {
	return biz.CredentialLocator{}, nil
}
func (*cleanupTestCredentialStore) Get(context.Context, biz.CredentialLocator) (kubernetesx.Credential, error) {
	return kubernetesx.Credential{}, nil
}
func (s *cleanupTestCredentialStore) Delete(_ context.Context, ref string) error {
	if s.err != nil {
		return s.err
	}
	s.deleted = append(s.deleted, ref)
	return nil
}
func (*cleanupTestCredentialStore) RotateKey(context.Context, string, string) (int, error) { return 0, nil }

func TestCredentialCleanupWorkerProcessesCleanupAndConfirmation(t *testing.T) {
	outbox := &cleanupTestOutbox{events: []OutboxEvent{
		{ID: 1, EventType: "credential_ref_cleanup", Payload: map[string]any{"old_credential_ref": "old-ref"}},
		{ID: 2, EventType: "visibility_sync", Payload: map[string]any{"namespace_id": "ns-1"}},
	}}
	store := &cleanupTestCredentialStore{}
	worker := NewCredentialCleanupWorker(outbox, store, logx.Noop())

	if err := worker.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !outbox.requeued {
		t.Fatal("expected stuck-event recovery before claim")
	}
	if len(store.deleted) != 1 || store.deleted[0] != "old-ref" {
		t.Fatalf("deleted refs = %#v", store.deleted)
	}
	if len(outbox.acked) != 2 || len(outbox.nacked) != 0 {
		t.Fatalf("acked=%v nacked=%v", outbox.acked, outbox.nacked)
	}
}

func TestCredentialCleanupWorkerNaksDeleteFailure(t *testing.T) {
	outbox := &cleanupTestOutbox{events: []OutboxEvent{
		{ID: 7, EventType: "credential_ref_cleanup", Payload: map[string]any{"old_credential_ref": "old-ref"}},
	}}
	store := &cleanupTestCredentialStore{err: errors.New("database unavailable")}
	worker := NewCredentialCleanupWorker(outbox, store, logx.Noop())

	if err := worker.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(outbox.acked) != 0 || len(outbox.nacked) != 1 || outbox.nacked[0] != 7 {
		t.Fatalf("acked=%v nacked=%v", outbox.acked, outbox.nacked)
	}
}
