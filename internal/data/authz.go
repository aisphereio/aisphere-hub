// Package data authz module — adapter from biz.AuthzRepo to kernel authz.Service.
//
// This file is the only place that imports kernel authz types directly.
// It converts between biz domain types (AuthzObjectRef, AuthzSubjectRef,
// AuthzRelationship, AuthzDecision, etc.) and kernel authz types
// (ObjectRef, SubjectRef, Relationship, Decision, etc.), then delegates
// to Resources.AuthzService.
//
// When Resources.AuthzService is nil (authz disabled or provider does not
// implement the full Service interface), every method returns
// ErrAuthzUnsupported so the biz layer can surface a clear 400 to the
// client. The biz layer NEVER sees a nil pointer dereference.

package data

import (
	"context"
	"errors"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

type authzRepo struct {
	resources *Resources
}

// NewAuthzRepo creates a new biz.AuthzRepo backed by kernel authz.Service.
//
// resources may be nil (e.g. in unit tests); in that case every method
// returns ErrAuthzUnsupported. Production code paths always pass a
// non-nil resources with AuthzService set when security.authz.enabled=true.
func NewAuthzRepo(resources *Resources) biz.AuthzRepo {
	return &authzRepo{resources: resources}
}

func (r *authzRepo) logger() logx.Logger {
	if r != nil && r.resources != nil && r.resources.Logger != nil {
		return r.resources.Logger.Named("authz.repo")
	}
	return logx.DefaultLogger().Named("authz.repo")
}

func (r *authzRepo) metrics() metricsx.Manager {
	if r != nil && r.resources != nil {
		return metricsx.Ensure(r.resources.Metrics)
	}
	return metricsx.Noop()
}

// service returns the kernel authz.Service, or an error if it is not
// configured. Callers MUST handle the error before using the returned
// service.
func (r *authzRepo) service() (authz.Service, error) {
	if r == nil || r.resources == nil || r.resources.AuthzService == nil {
		return nil, errorx.BadRequest(errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY"),
			"authz service is not configured; set security.authz.enabled=true and provider=spicedb in config")
	}
	return r.resources.AuthzService, nil
}

// --- Check ---

func (r *authzRepo) Check(ctx context.Context, req biz.AuthzCheckRequest) (out biz.AuthzDecision, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "check", logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "check", started, err, logx.Bool("allowed", out.Allowed))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzDecision{}, err
	}
	decision, err := svc.Check(ctx, authz.CheckRequest{
		Subject:          subjectRefToKernel(req.Subject),
		Resource:         objectRefToKernel(req.Resource),
		Permission:       req.Permission,
		TenantID:         req.TenantID,
		OrgID:            req.OrgID,
		ProjectID:        req.ProjectID,
		SubjectAttrs:     authz.AttributeSet(req.SubjectAttrs),
		ResourceAttrs:    authz.AttributeSet(req.ResourceAttrs),
		EnvironmentAttrs: authz.AttributeSet(req.EnvironmentAttrs),
		Consistency:      consistencyToKernel(req.Consistency),
	})
	if err != nil {
		return biz.AuthzDecision{}, err
	}
	return decisionFromKernel(decision), nil
}

// --- BatchCheck ---

func (r *authzRepo) BatchCheck(ctx context.Context, req biz.AuthzBatchCheckRequest) (out biz.AuthzBatchCheckResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "batch_check", logx.Int("checks", len(req.Checks)))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "batch_check", started, err, logx.Int("decisions", len(out.Decisions)))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzBatchCheckResult{}, err
	}
	checks := make([]authz.CheckRequest, 0, len(req.Checks))
	for _, c := range req.Checks {
		checks = append(checks, authz.CheckRequest{
			Subject:          subjectRefToKernel(c.Subject),
			Resource:         objectRefToKernel(c.Resource),
			Permission:       c.Permission,
			TenantID:         c.TenantID,
			OrgID:            c.OrgID,
			ProjectID:        c.ProjectID,
			SubjectAttrs:     authz.AttributeSet(c.SubjectAttrs),
			ResourceAttrs:    authz.AttributeSet(c.ResourceAttrs),
			EnvironmentAttrs: authz.AttributeSet(c.EnvironmentAttrs),
			Consistency:      consistencyToKernel(c.Consistency),
		})
	}
	result, err := svc.BatchCheck(ctx, authz.BatchCheckRequest{
		Checks:      checks,
		Consistency: consistencyToKernel(req.Consistency),
	})
	if err != nil {
		return biz.AuthzBatchCheckResult{}, err
	}
	decisions := make([]biz.AuthzDecision, 0, len(result.Decisions))
	for _, d := range result.Decisions {
		decisions = append(decisions, decisionFromKernel(d))
	}
	return biz.AuthzBatchCheckResult{Decisions: decisions}, nil
}

// --- Write / Delete / Read relationships ---

func (r *authzRepo) WriteRelationships(ctx context.Context, rels ...biz.AuthzRelationship) (out biz.AuthzWriteResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "write_relationships", logx.Int("relationships", len(rels)))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "write_relationships", started, err, logx.Int("written", out.Written))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzWriteResult{}, err
	}
	kernelRels := make([]authz.Relationship, 0, len(rels))
	for _, rel := range rels {
		kernelRels = append(kernelRels, relationshipToKernel(rel))
	}
	result, err := svc.WriteRelationships(ctx, kernelRels...)
	if err != nil {
		return biz.AuthzWriteResult{}, err
	}
	return biz.AuthzWriteResult{
		ConsistencyToken: result.ConsistencyToken,
		Written:          result.Written,
	}, nil
}

func (r *authzRepo) DeleteRelationships(ctx context.Context, filter biz.AuthzRelationshipFilter) (out biz.AuthzWriteResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "delete_relationships", logx.String("resource_type", filter.ResourceType))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "delete_relationships", started, err, logx.Int("deleted", out.Deleted))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzWriteResult{}, err
	}
	result, err := svc.DeleteRelationships(ctx, filterToKernel(filter))
	if err != nil {
		return biz.AuthzWriteResult{}, err
	}
	return biz.AuthzWriteResult{
		ConsistencyToken: result.ConsistencyToken,
		Deleted:          result.Deleted,
	}, nil
}

func (r *authzRepo) ReadRelationships(ctx context.Context, filter biz.AuthzRelationshipFilter, limit int, cursor string) (out []biz.AuthzRelationship, nextCursor string, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "read_relationships", logx.String("resource_type", filter.ResourceType), logx.Int("limit", limit))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "read_relationships", started, err, logx.Int("relationships", len(out)))
	}()
	svc, err := r.service()
	if err != nil {
		return nil, "", err
	}
	rels, err := svc.ReadRelationships(ctx, filterToKernel(filter))
	if err != nil {
		return nil, "", err
	}
	out = make([]biz.AuthzRelationship, 0, len(rels))
	for _, rel := range rels {
		out = append(out, relationshipFromKernel(rel))
	}
	// SpiceDB's ReadRelationships streams all matches; kernel adapter
	// already drains the stream into a slice. Cursor-based pagination is
	// not exposed by the kernel interface today — return empty next cursor
	// so callers know they got everything in one shot. When the kernel
	// adds cursor support, we will thread limit/cursor through.
	return out, "", nil
}

// --- Lookup ---

func (r *authzRepo) LookupResources(ctx context.Context, req biz.AuthzLookupResourcesRequest) (out biz.AuthzLookupResourcesResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "lookup_resources", logx.String("resource_type", req.ResourceType), logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "lookup_resources", started, err, logx.Int("resources", len(out.Resources)))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzLookupResourcesResult{}, err
	}
	result, err := svc.LookupResources(ctx, authz.LookupResourcesRequest{
		Subject:          subjectRefToKernel(req.Subject),
		ResourceType:     req.ResourceType,
		Permission:       req.Permission,
		TenantID:         req.TenantID,
		OrgID:            req.OrgID,
		ProjectID:        req.ProjectID,
		SubjectAttrs:     authz.AttributeSet(req.SubjectAttrs),
		EnvironmentAttrs: authz.AttributeSet(req.EnvironmentAttrs),
		Consistency:      consistencyToKernel(req.Consistency),
		Limit:            req.Limit,
		Cursor:           req.Cursor,
	})
	if err != nil {
		return biz.AuthzLookupResourcesResult{}, err
	}
	resources := make([]biz.AuthzObjectRef, 0, len(result.Resources))
	for _, res := range result.Resources {
		resources = append(resources, objectRefFromKernel(res))
	}
	return biz.AuthzLookupResourcesResult{
		Resources:        resources,
		NextCursor:       result.NextCursor,
		ConsistencyToken: result.ConsistencyToken,
	}, nil
}

func (r *authzRepo) LookupSubjects(ctx context.Context, req biz.AuthzLookupSubjectsRequest) (out biz.AuthzLookupSubjectsResult, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "lookup_subjects", logx.String("subject_type", req.SubjectType), logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "lookup_subjects", started, err, logx.Int("subjects", len(out.Subjects)))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzLookupSubjectsResult{}, err
	}
	result, err := svc.LookupSubjects(ctx, authz.LookupSubjectsRequest{
		Resource:         objectRefToKernel(req.Resource),
		Permission:       req.Permission,
		SubjectType:      req.SubjectType,
		TenantID:         req.TenantID,
		OrgID:            req.OrgID,
		ProjectID:        req.ProjectID,
		ResourceAttrs:    authz.AttributeSet(req.ResourceAttrs),
		EnvironmentAttrs: authz.AttributeSet(req.EnvironmentAttrs),
		Consistency:      consistencyToKernel(req.Consistency),
		Limit:            req.Limit,
		Cursor:           req.Cursor,
	})
	if err != nil {
		return biz.AuthzLookupSubjectsResult{}, err
	}
	subjects := make([]biz.AuthzSubjectRef, 0, len(result.Subjects))
	for _, sub := range result.Subjects {
		subjects = append(subjects, subjectRefFromKernel(sub))
	}
	return biz.AuthzLookupSubjectsResult{
		Subjects:         subjects,
		NextCursor:       result.NextCursor,
		ConsistencyToken: result.ConsistencyToken,
	}, nil
}

// --- Schema ---

func (r *authzRepo) ReadSchema(ctx context.Context) (out biz.AuthzSchema, err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "read_schema")
	defer func() {
		observability.End(ctx, logger, r.metrics(), "authz.repo", "read_schema", started, err, logx.Int("schema_size", len(out.Text)))
	}()
	svc, err := r.service()
	if err != nil {
		return biz.AuthzSchema{}, err
	}
	schema, err := svc.ReadSchema(ctx)
	if err != nil {
		return biz.AuthzSchema{}, err
	}
	return biz.AuthzSchema{Text: schema.Text}, nil
}

func (r *authzRepo) WriteSchema(ctx context.Context, schema biz.AuthzSchema) (err error) {
	ctx, logger, started := observability.Begin(ctx, r.logger(), "authz.repo", "write_schema", logx.Int("schema_size", len(schema.Text)))
	defer func() { observability.End(ctx, logger, r.metrics(), "authz.repo", "write_schema", started, err) }()
	svc, err := r.service()
	if err != nil {
		return err
	}
	return svc.WriteSchema(ctx, authz.Schema{Text: schema.Text})
}

// --- conversion helpers ---
//
// These are the ONLY place that knows about both biz and kernel authz
// types. Swapping the kernel engine (spicedb → another ReBAC) only
// requires touching this file.

func objectRefToKernel(ref biz.AuthzObjectRef) authz.ObjectRef {
	return authz.ObjectRef{Type: ref.Type, ID: ref.ID}
}

func objectRefFromKernel(ref authz.ObjectRef) biz.AuthzObjectRef {
	return biz.AuthzObjectRef{Type: ref.Type, ID: ref.ID}
}

func subjectRefToKernel(ref biz.AuthzSubjectRef) authz.SubjectRef {
	return authz.SubjectRef{
		Type:     ref.Type,
		ID:       ref.ID,
		Relation: ref.Relation,
	}
}

func subjectRefFromKernel(ref authz.SubjectRef) biz.AuthzSubjectRef {
	return biz.AuthzSubjectRef{
		Type:     ref.Type,
		ID:       ref.ID,
		Relation: ref.Relation,
	}
}

func relationshipToKernel(rel biz.AuthzRelationship) authz.Relationship {
	out := authz.Relationship{
		Resource:      objectRefToKernel(rel.Resource),
		Relation:      rel.Relation,
		Subject:       subjectRefToKernel(rel.Subject),
		CaveatName:    rel.CaveatName,
		CaveatContext: authz.AttributeSet(rel.CaveatContext),
	}
	if !rel.ExpiresAt.IsZero() {
		out.ExpiresAt = rel.ExpiresAt
	}
	return out
}

func relationshipFromKernel(rel authz.Relationship) biz.AuthzRelationship {
	out := biz.AuthzRelationship{
		Resource:      objectRefFromKernel(rel.Resource),
		Relation:      rel.Relation,
		Subject:       subjectRefFromKernel(rel.Subject),
		CaveatName:    rel.CaveatName,
		CaveatContext: map[string]any(rel.CaveatContext),
	}
	if !rel.ExpiresAt.IsZero() {
		out.ExpiresAt = rel.ExpiresAt
	}
	return out
}

func filterToKernel(filter biz.AuthzRelationshipFilter) authz.RelationshipFilter {
	return authz.RelationshipFilter{
		ResourceType: filter.ResourceType,
		ResourceID:   filter.ResourceID,
		Relation:     filter.Relation,
		SubjectType:  filter.SubjectType,
		SubjectID:    filter.SubjectID,
		SubjectRel:   filter.SubjectRelation,
	}
}

func consistencyToKernel(c biz.AuthzConsistency) authz.Consistency {
	out := authz.Consistency{Token: c.Token}
	if c.FullyConsistent {
		out.Mode = authz.ConsistencyFullyConsistent
	} else if c.Token != "" {
		out.Mode = authz.ConsistencyAtLeastAsFresh
	} else {
		out.Mode = authz.ConsistencyMinimizeLatency
	}
	return out
}

func decisionFromKernel(d authz.Decision) biz.AuthzDecision {
	return biz.AuthzDecision{
		Effect:           string(d.Effect),
		Allowed:          d.Allowed,
		Reason:           d.Reason,
		ConsistencyToken: d.ConsistencyToken,
		MissingContext:   append([]string(nil), d.MissingContext...),
	}
}

// ensure time is referenced even when no method uses it directly (the
// conversion helpers use time.Time via ExpiresAt, but linters may not
// follow the indirection).
var _ = time.Time{}

// ensure errors is referenced for future error-mapping additions.
var _ = errors.Is
