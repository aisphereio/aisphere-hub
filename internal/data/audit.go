// Package data audit module — adapter from biz.AuditRepo to kernel auditx.Store.
//
// When Resources.AuditStore is nil (audit disabled or store does not
// implement Queryer), every method returns ErrAuditNotConfigured so
// the biz layer can surface a clear 503 to the client.

package data

import (
	"context"

	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/errorx"
)

type auditRepo struct {
	resources *Resources
}

// NewAuditRepo creates a new biz.AuditRepo backed by kernel auditx.Store.
//
// resources may be nil (e.g. in unit tests); in that case Query returns
// ErrAuditNotConfigured. Production code paths always pass a non-nil
// resources with AuditStore set when audit.enabled=true.
func NewAuditRepo(resources *Resources) biz.AuditRepo {
	return &auditRepo{resources: resources}
}

func (r *auditRepo) Query(ctx context.Context, filter biz.AuditQueryFilter) ([]biz.AuditRecord, error) {
	if r == nil || r.resources == nil || r.resources.AuditStore == nil {
		return nil, errorx.Unavailable(errorx.Code("AUDIT_NOT_CONFIGURED"),
			"audit store is not configured; set audit.enabled=true in config")
	}
	records, err := r.resources.AuditStore.Query(ctx, biz.AuditQueryFilterToKernel(filter))
	if err != nil {
		return nil, err
	}
	out := make([]biz.AuditRecord, 0, len(records))
	for _, rec := range records {
		out = append(out, biz.AuditRecordFromKernel(rec))
	}
	return out, nil
}

// compile-time reference to auditx to keep the import even if the file
// is later refactored to not use auditx directly.
var _ auditx.Recorder
