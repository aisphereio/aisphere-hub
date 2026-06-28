// Package service authz module — HTTP/gRPC handlers for AuthzService.
//
// The service layer is intentionally thin: it converts proto DTOs to biz
// domain objects, calls the corresponding usecase method, and converts
// the result back. All business logic (validation, error mapping) lives
// in biz.
//
// Authz on authz: all RPCs require a Bearer token (kernel authn
// middleware enforces this). Whether the caller is allowed to perform
// the authz operation itself (e.g. "can user X write relationships on
// resource Y?") is a policy decision that lives in the SpiceDB schema —
// we do NOT enforce it here. Operators who want to gate WriteRelationships
// by an additional permission check should add an authz.Require call in
// the biz layer with the appropriate resource/permission.

package service

import (
	"context"

	authzv1 "github.com/aisphereio/aisphere-hub/api/authz/v1"
	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/authn"
	khttp "github.com/aisphereio/kernel/transportx/http"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuthzService implements v1.AuthzServiceHTTPServer and v1.AuthzServiceServer.
type AuthzService struct {
	authzv1.UnimplementedAuthzServiceServer

	uc *biz.AuthzUsecase
}

// NewAuthzService creates a new AuthzService.
func NewAuthzService(uc *biz.AuthzUsecase) *AuthzService {
	return &AuthzService{uc: uc}
}

// RegisterHTTPServer registers the proto-generated HTTP routes.
func (s *AuthzService) RegisterHTTPServer(srv *khttp.Server) {
	authzv1.RegisterAuthzServiceHTTPServer(srv, s)
}

// --- Check ---

func (s *AuthzService) CheckPermission(ctx context.Context, req *authzv1.CheckPermissionRequest) (*authzv1.CheckPermissionResponse, error) {
	decision, err := s.uc.Check(ctx, biz.AuthzCheckRequest{
		Subject:          subjectRefFromDTO(req.GetSubject()),
		Resource:         objectRefFromDTO(req.GetResource()),
		Permission:       req.GetPermission(),
		TenantID:         req.GetTenantId(),
		OrgID:            req.GetOrgId(),
		ProjectID:        req.GetProjectId(),
		SubjectAttrs:     structToMap(req.GetSubjectAttrs()),
		ResourceAttrs:    structToMap(req.GetResourceAttrs()),
		EnvironmentAttrs: structToMap(req.GetEnvironmentAttrs()),
		Consistency: biz.AuthzConsistency{
			FullyConsistent: req.GetFullyConsistent(),
			Token:           req.GetConsistencyToken(),
		},
	})
	if err != nil {
		return nil, err
	}
	return decisionToDTO(decision), nil
}

// --- BatchCheck ---

func (s *AuthzService) BatchCheckPermissions(ctx context.Context, req *authzv1.BatchCheckPermissionsRequest) (*authzv1.BatchCheckPermissionsResponse, error) {
	checks := make([]biz.AuthzCheckRequest, 0, len(req.GetChecks()))
	for _, c := range req.GetChecks() {
		checks = append(checks, biz.AuthzCheckRequest{
			Subject:          subjectRefFromDTO(c.GetSubject()),
			Resource:         objectRefFromDTO(c.GetResource()),
			Permission:       c.GetPermission(),
			TenantID:         c.GetTenantId(),
			OrgID:            c.GetOrgId(),
			ProjectID:        c.GetProjectId(),
			SubjectAttrs:     structToMap(c.GetSubjectAttrs()),
			ResourceAttrs:    structToMap(c.GetResourceAttrs()),
			EnvironmentAttrs: structToMap(c.GetEnvironmentAttrs()),
			Consistency: biz.AuthzConsistency{
				FullyConsistent: c.GetFullyConsistent(),
				Token:           c.GetConsistencyToken(),
			},
		})
	}
	result, err := s.uc.BatchCheck(ctx, biz.AuthzBatchCheckRequest{
		Checks: checks,
		Consistency: biz.AuthzConsistency{
			FullyConsistent: req.GetFullyConsistent(),
			Token:           req.GetConsistencyToken(),
		},
	})
	if err != nil {
		return nil, err
	}
	decisions := make([]*authzv1.CheckPermissionResponse, 0, len(result.Decisions))
	for _, d := range result.Decisions {
		decisions = append(decisions, decisionToDTO(d))
	}
	return &authzv1.BatchCheckPermissionsResponse{
		Decisions:        decisions,
		ConsistencyToken: "", // biz.AuthzBatchCheckResult does not surface a single token today
	}, nil
}

// --- Write / Delete / Read relationships ---

func (s *AuthzService) WriteRelationships(ctx context.Context, req *authzv1.WriteRelationshipsRequest) (*authzv1.WriteRelationshipsResponse, error) {
	rels := make([]biz.AuthzRelationship, 0, len(req.GetRelationships()))
	for _, rel := range req.GetRelationships() {
		rels = append(rels, relationshipFromDTO(rel))
	}
	result, err := s.uc.WriteRelationships(ctx, rels...)
	if err != nil {
		return nil, err
	}
	return &authzv1.WriteRelationshipsResponse{
		ConsistencyToken: result.ConsistencyToken,
		Written:          int32(result.Written),
	}, nil
}

func (s *AuthzService) DeleteRelationships(ctx context.Context, req *authzv1.DeleteRelationshipsRequest) (*authzv1.DeleteRelationshipsResponse, error) {
	result, err := s.uc.DeleteRelationships(ctx, filterFromDTO(req.GetFilter()))
	if err != nil {
		return nil, err
	}
	return &authzv1.DeleteRelationshipsResponse{
		ConsistencyToken: result.ConsistencyToken,
		Deleted:          int32(result.Deleted),
	}, nil
}

func (s *AuthzService) ReadRelationships(ctx context.Context, req *authzv1.ReadRelationshipsRequest) (*authzv1.ReadRelationshipsResponse, error) {
	rels, nextCursor, err := s.uc.ReadRelationships(ctx, filterFromDTO(req.GetFilter()), int(req.GetLimit()), req.GetCursor())
	if err != nil {
		return nil, err
	}
	out := &authzv1.ReadRelationshipsResponse{
		Relationships: make([]*authzv1.Relationship, 0, len(rels)),
		NextCursor:    nextCursor,
	}
	for _, rel := range rels {
		out.Relationships = append(out.Relationships, relationshipToDTO(rel))
	}
	return out, nil
}

// --- Lookup ---

func (s *AuthzService) LookupResources(ctx context.Context, req *authzv1.LookupResourcesRequest) (*authzv1.LookupResourcesResponse, error) {
	result, err := s.uc.LookupResources(ctx, biz.AuthzLookupResourcesRequest{
		Subject:          subjectRefFromDTO(req.GetSubject()),
		ResourceType:     req.GetResourceType(),
		Permission:       req.GetPermission(),
		TenantID:         req.GetTenantId(),
		OrgID:            req.GetOrgId(),
		ProjectID:        req.GetProjectId(),
		SubjectAttrs:     structToMap(req.GetSubjectAttrs()),
		EnvironmentAttrs: structToMap(req.GetEnvironmentAttrs()),
		Consistency: biz.AuthzConsistency{
			FullyConsistent: req.GetFullyConsistent(),
			Token:           req.GetConsistencyToken(),
		},
		Limit:  int(req.GetLimit()),
		Cursor: req.GetCursor(),
	})
	if err != nil {
		return nil, err
	}
	resources := make([]*authzv1.ObjectRef, 0, len(result.Resources))
	for _, res := range result.Resources {
		resources = append(resources, objectRefToDTO(res))
	}
	return &authzv1.LookupResourcesResponse{
		Resources:        resources,
		NextCursor:       result.NextCursor,
		ConsistencyToken: result.ConsistencyToken,
	}, nil
}

func (s *AuthzService) LookupSubjects(ctx context.Context, req *authzv1.LookupSubjectsRequest) (*authzv1.LookupSubjectsResponse, error) {
	result, err := s.uc.LookupSubjects(ctx, biz.AuthzLookupSubjectsRequest{
		Resource:         objectRefFromDTO(req.GetResource()),
		Permission:       req.GetPermission(),
		SubjectType:      req.GetSubjectType(),
		TenantID:         req.GetTenantId(),
		OrgID:            req.GetOrgId(),
		ProjectID:        req.GetProjectId(),
		ResourceAttrs:    structToMap(req.GetResourceAttrs()),
		EnvironmentAttrs: structToMap(req.GetEnvironmentAttrs()),
		Consistency: biz.AuthzConsistency{
			FullyConsistent: req.GetFullyConsistent(),
			Token:           req.GetConsistencyToken(),
		},
		Limit:  int(req.GetLimit()),
		Cursor: req.GetCursor(),
	})
	if err != nil {
		return nil, err
	}
	subjects := make([]*authzv1.SubjectRef, 0, len(result.Subjects))
	for _, sub := range result.Subjects {
		subjects = append(subjects, subjectRefToDTO(sub))
	}
	return &authzv1.LookupSubjectsResponse{
		Subjects:         subjects,
		NextCursor:       result.NextCursor,
		ConsistencyToken: result.ConsistencyToken,
	}, nil
}

// --- Schema ---

func (s *AuthzService) ReadSchema(ctx context.Context, req *authzv1.ReadSchemaRequest) (*authzv1.ReadSchemaResponse, error) {
	schema, err := s.uc.ReadSchema(ctx)
	if err != nil {
		return nil, err
	}
	return &authzv1.ReadSchemaResponse{SchemaText: schema.Text}, nil
}

func (s *AuthzService) WriteSchema(ctx context.Context, req *authzv1.WriteSchemaRequest) (*emptypb.Empty, error) {
	if err := s.uc.WriteSchema(ctx, biz.AuthzSchema{Text: req.GetSchemaText()}); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// --- DTO conversion helpers ---

func objectRefFromDTO(ref *authzv1.ObjectRef) biz.AuthzObjectRef {
	if ref == nil {
		return biz.AuthzObjectRef{}
	}
	return biz.AuthzObjectRef{Type: ref.GetType(), ID: ref.GetId()}
}

func objectRefToDTO(ref biz.AuthzObjectRef) *authzv1.ObjectRef {
	return &authzv1.ObjectRef{Type: ref.Type, Id: ref.ID}
}

func subjectRefFromDTO(ref *authzv1.SubjectRef) biz.AuthzSubjectRef {
	if ref == nil {
		return biz.AuthzSubjectRef{}
	}
	return biz.AuthzSubjectRef{
		Type:     ref.GetType(),
		ID:       ref.GetId(),
		Relation: ref.GetRelation(),
	}
}

func subjectRefToDTO(ref biz.AuthzSubjectRef) *authzv1.SubjectRef {
	return &authzv1.SubjectRef{
		Type:     ref.Type,
		Id:       ref.ID,
		Relation: ref.Relation,
	}
}

func relationshipFromDTO(rel *authzv1.Relationship) biz.AuthzRelationship {
	if rel == nil {
		return biz.AuthzRelationship{}
	}
	out := biz.AuthzRelationship{
		Resource:      objectRefFromDTO(rel.GetResource()),
		Relation:      rel.GetRelation(),
		Subject:       subjectRefFromDTO(rel.GetSubject()),
		CaveatName:    rel.GetCaveatName(),
		CaveatContext: structToMap(rel.GetCaveatContext()),
	}
	if rel.GetExpiresAt() != nil {
		out.ExpiresAt = rel.GetExpiresAt().AsTime()
	}
	return out
}

func relationshipToDTO(rel biz.AuthzRelationship) *authzv1.Relationship {
	out := &authzv1.Relationship{
		Resource:      objectRefToDTO(rel.Resource),
		Relation:      rel.Relation,
		Subject:       subjectRefToDTO(rel.Subject),
		CaveatName:    rel.CaveatName,
		CaveatContext: mapToStruct(rel.CaveatContext),
	}
	if !rel.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(rel.ExpiresAt)
	}
	return out
}

func filterFromDTO(filter *authzv1.RelationshipFilter) biz.AuthzRelationshipFilter {
	if filter == nil {
		return biz.AuthzRelationshipFilter{}
	}
	return biz.AuthzRelationshipFilter{
		ResourceType:    filter.GetResourceType(),
		ResourceID:      filter.GetResourceId(),
		Relation:        filter.GetRelation(),
		SubjectType:     filter.GetSubjectType(),
		SubjectID:       filter.GetSubjectId(),
		SubjectRelation: filter.GetSubjectRelation(),
	}
}

func decisionToDTO(d biz.AuthzDecision) *authzv1.CheckPermissionResponse {
	return &authzv1.CheckPermissionResponse{
		Effect:           d.Effect,
		Allowed:          d.Allowed,
		Reason:           d.Reason,
		ConsistencyToken: d.ConsistencyToken,
		MissingContext:   append([]string(nil), d.MissingContext...),
	}
}

// structToMap converts a proto Struct to a map[string]any. Returns nil
// when the input is nil or empty. Used to pass ABAC context to biz.
func structToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	m := s.AsMap()
	if len(m) == 0 {
		return nil
	}
	return m
}

// mapToStruct converts a map[string]any to a proto Struct. Returns nil
// when the input is nil or empty.
func mapToStruct(m map[string]any) *structpb.Struct {
	if len(m) == 0 {
		return nil
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return st
}

// --- compile-time guard ---
//
// Ensure we satisfy both HTTP and gRPC interfaces at compile time. The
// gRPC interface requires mustEmbedUnimplementedAuthzServiceServer,
// which we embed above; the HTTP interface requires only the methods.
var _ authzv1.AuthzServiceServer = (*AuthzService)(nil)

// _authnPackageReference keeps the authn import referenced even when no
// method uses it directly today. We keep authn imported because future
// middleware-driven principal extraction will need it, and removing the
// import now would force a re-import cycle on the next change.
var _ authn.Principal
