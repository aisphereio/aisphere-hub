// Package biz audit module — read-only audit log query usecase.
//
// This usecase wraps kernel auditx.Queryer into a biz.AuditRepo
// abstraction. The data layer adapts Resources.AuditStore (which is
// auditx.MemoryStore today, but could be a postgres-backed Store
// tomorrow) into this interface.
//
// Layering contract:
//   - biz imports: kernel auditx (types only) + errorx + logx
//   - biz MUST NOT import: data, conf, or any storage driver

package biz

import (
	"context"
	"time"

	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
)

// --- Domain types ---

// AuditRecord mirrors kernel auditx.Record but is owned by biz.
type AuditRecord struct {
	ID        string
	Time      time.Time
	Action    string
	Result    string
	Severity  string
	Reason    string
	Actor     AuditActor
	Resource  AuditResource
	RequestID string
	TraceID   string
	ClientIP  string
	UserAgent string
	Metadata  map[string]any
}

// AuditActor is the actor sub-record.
type AuditActor struct {
	SubjectID   string
	SubjectType string
	TenantID    string
	OrgID       string
	ProjectID   string
	Name        string
	Email       string
}

// AuditResource is the resource sub-record.
type AuditResource struct {
	Type      string
	ID        string
	TenantID  string
	OrgID     string
	ProjectID string
}

// AuditQueryFilter selects audit records by any combination of fields.
// Empty fields are wildcards.
type AuditQueryFilter struct {
	ActorID      string
	ActorType    string
	ResourceType string
	ResourceID   string
	Action       string
	Result       string
	TenantID     string
	OrgID        string
	ProjectID    string
	From         time.Time
	To           time.Time
	Limit        int
}

// --- Error sentinels ---

var (
	ErrAuditNotConfigured = errorx.Unavailable(
		errorx.Code("AUDIT_NOT_CONFIGURED"),
		"audit store is not configured; set audit.enabled=true in config",
	)
	ErrAuditInvalidArgument = errorx.BadRequest(
		errorx.Code("AUDIT_INVALID_ARGUMENT"),
		"invalid audit query",
	)
)

// --- Repo interface ---

type AuditRepo interface {
	Query(ctx context.Context, filter AuditQueryFilter) ([]AuditRecord, error)
}

// --- Usecase ---

// AuditUsecase is the read-only audit query usecase.
type AuditUsecase struct {
	repo AuditRepo
	log  logx.Logger
}

// NewAuditUsecase creates a new AuditUsecase. log may be nil.
func NewAuditUsecase(repo AuditRepo, log logx.Logger) *AuditUsecase {
	if log == nil {
		log = logx.Noop()
	}
	return &AuditUsecase{repo: repo, log: log.Named("audit")}
}

// Query returns audit records matching the filter, sorted by time
// descending (newest first). Limit defaults to 100, capped at 1000.
func (uc *AuditUsecase) Query(ctx context.Context, filter AuditQueryFilter) ([]AuditRecord, error) {
	if uc.repo == nil {
		return nil, ErrAuditNotConfigured
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}
	records, err := uc.repo.Query(ctx, filter)
	if err != nil {
		uc.log.WithContext(ctx).Warn("audit query failed",
			logx.String("action", filter.Action),
			logx.String("resource", filter.ResourceType+":"+filter.ResourceID),
			logx.Err(err),
		)
		return nil, err
	}
	return records, nil
}

// --- conversion helpers (used by data layer) ---

// AuditRecordFromKernel converts a kernel auditx.Record to biz.AuditRecord.
// Exported so the data layer can use it without re-implementing the mapping.
func AuditRecordFromKernel(r auditx.Record) AuditRecord {
	return AuditRecord{
		ID:        r.ID,
		Time:      r.Time,
		Action:    r.Action,
		Result:    r.Result,
		Severity:  r.Severity,
		Reason:    r.Reason,
		Actor:     AuditActorFromKernel(r.Actor),
		Resource:  AuditResourceFromKernel(r.Resource),
		RequestID: r.RequestID,
		TraceID:   r.TraceID,
		ClientIP:  r.ClientIP,
		UserAgent: r.UserAgent,
		Metadata:  map[string]any(r.Metadata),
	}
}

func AuditActorFromKernel(a auditx.Actor) AuditActor {
	return AuditActor{
		SubjectID:   a.SubjectID,
		SubjectType: a.SubjectType,
		TenantID:    a.TenantID,
		OrgID:       a.OrgID,
		ProjectID:   a.ProjectID,
		Name:        a.Name,
		Email:       a.Email,
	}
}

func AuditResourceFromKernel(r auditx.Resource) AuditResource {
	return AuditResource{
		Type:      r.Type,
		ID:        r.ID,
		TenantID:  r.TenantID,
		OrgID:     r.OrgID,
		ProjectID: r.ProjectID,
	}
}

// AuditQueryFilterToKernel converts a biz.AuditQueryFilter to kernel
// auditx.QueryFilter. Exported so the data layer can use it.
func AuditQueryFilterToKernel(f AuditQueryFilter) auditx.QueryFilter {
	return auditx.QueryFilter{
		ActorID:      f.ActorID,
		ActorType:    f.ActorType,
		ResourceType: f.ResourceType,
		ResourceID:   f.ResourceID,
		Action:       f.Action,
		Result:       f.Result,
		TenantID:     f.TenantID,
		OrgID:        f.OrgID,
		ProjectID:    f.ProjectID,
		From:         f.From,
		To:           f.To,
		Limit:        f.Limit,
	}
}
