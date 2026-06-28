// Package service audit module — HTTP handler for AuditService.

package service

import (
	"context"

	auditv1 "github.com/aisphereio/aisphere-hub/api/audit/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"

	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuditService implements v1.AuditServiceHTTPServer and v1.AuditServiceServer.
type AuditService struct {
	auditv1.UnimplementedAuditServiceServer

	uc *biz.AuditUsecase
}

// NewAuditService creates a new AuditService.
func NewAuditService(uc *biz.AuditUsecase) *AuditService {
	return &AuditService{uc: uc}
}

// RegisterHTTPServer registers the proto-generated HTTP routes.
func (s *AuditService) RegisterHTTPServer(srv *khttp.Server) {
	auditv1.RegisterAuditServiceHTTPServer(srv, s)
}

// QueryAuditRecords returns audit records matching the filter.
func (s *AuditService) QueryAuditRecords(ctx context.Context, req *auditv1.QueryAuditRecordsRequest) (*auditv1.QueryAuditRecordsResponse, error) {
	filter := biz.AuditQueryFilter{
		ActorID:      req.GetActorId(),
		ActorType:    req.GetActorType(),
		ResourceType: req.GetResourceType(),
		ResourceID:   req.GetResourceId(),
		Action:       req.GetAction(),
		Result:       req.GetResult(),
		TenantID:     req.GetTenantId(),
		OrgID:        req.GetOrgId(),
		ProjectID:    req.GetProjectId(),
		Limit:        int(req.GetLimit()),
	}
	if req.GetFrom() != nil {
		filter.From = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		filter.To = req.GetTo().AsTime()
	}
	records, err := s.uc.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := &auditv1.QueryAuditRecordsResponse{
		Records: make([]*auditv1.AuditRecord, 0, len(records)),
		Total:   int32(len(records)),
	}
	for _, r := range records {
		out.Records = append(out.Records, auditRecordToDTO(r))
	}
	return out, nil
}

// --- DTO conversion helpers ---

func auditRecordToDTO(r biz.AuditRecord) *auditv1.AuditRecord {
	out := &auditv1.AuditRecord{
		Id:        r.ID,
		Action:    r.Action,
		Result:    r.Result,
		Severity:  r.Severity,
		Reason:    r.Reason,
		Actor:     auditActorToDTO(r.Actor),
		Resource:  auditResourceToDTO(r.Resource),
		RequestId: r.RequestID,
		TraceId:   r.TraceID,
		ClientIp:  r.ClientIP,
		UserAgent: r.UserAgent,
		Metadata:  mapToStruct(r.Metadata),
	}
	if !r.Time.IsZero() {
		out.Time = timestamppb.New(r.Time)
	}
	return out
}

func auditActorToDTO(a biz.AuditActor) *auditv1.AuditActor {
	return &auditv1.AuditActor{
		SubjectId:   a.SubjectID,
		SubjectType: a.SubjectType,
		TenantId:    a.TenantID,
		OrgId:       a.OrgID,
		ProjectId:   a.ProjectID,
		Name:        a.Name,
		Email:       a.Email,
	}
}

func auditResourceToDTO(r biz.AuditResource) *auditv1.AuditResource {
	return &auditv1.AuditResource{
		Type:      r.Type,
		Id:        r.ID,
		TenantId:  r.TenantID,
		OrgId:     r.OrgID,
		ProjectId: r.ProjectID,
	}
}

// compile-time reference to structpb to keep the import even if no
// method uses it directly today.
var _ *structpb.Struct
