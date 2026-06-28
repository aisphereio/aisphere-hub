// Package biz authz module — provider-neutral authorization usecase.
//
// This usecase wraps kernel authz interfaces (Authorizer, RelationshipStore,
// ResourceLookup, SubjectLookup, SchemaManager) into a single biz.AuthzRepo
// abstraction. The data layer adapts the kernel spicedb.Client into this
// interface.
//
// Layering contract:
//   - biz imports: kernel logx + errorx + authz (types only)
//   - biz MUST NOT import: data, conf, spicedb SDK
//
// The usecase layer is intentionally thin: it validates inputs (non-empty
// subject/resource/permission, valid filter shape), records audit-friendly
// logs via logx, and forwards to the repo. The complex ReBAC / ABAC
// evaluation lives in SpiceDB.

package biz

import (
	"context"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/authz"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

// --- Domain types ---
//
// These mirror kernel authz types but are owned by biz so the data layer
// can be swapped without touching biz code. Conversion happens in data.

// AuthzObjectRef is a typed (resource_type, resource_id) pair.
type AuthzObjectRef struct {
	Type string
	ID   string
}

// AuthzSubjectRef extends AuthzObjectRef with an optional relation, so
// we can express "member of group X" as {Type:"group", ID:"g_1",
// Relation:"member"}.
type AuthzSubjectRef struct {
	Type     string
	ID       string
	Relation string
}

// AuthzRelationship is one ReBAC tuple: resource#relation@subject,
// optionally caveated for ABAC.
type AuthzRelationship struct {
	Resource      AuthzObjectRef
	Relation      string
	Subject       AuthzSubjectRef
	CaveatName    string
	CaveatContext map[string]any
	ExpiresAt     time.Time
}

// AuthzRelationshipFilter selects relationships by any combination of
// resource / relation / subject fields. Empty fields are wildcards.
type AuthzRelationshipFilter struct {
	ResourceType    string
	ResourceID      string
	Relation        string
	SubjectType     string
	SubjectID       string
	SubjectRelation string
}

// AuthzConsistency controls SpiceDB consistency semantics.
type AuthzConsistency struct {
	FullyConsistent bool
	Token           string
}

// AuthzDecision is the result of a permission check.
type AuthzDecision struct {
	Effect           string // "allow" | "deny" | "no_match"
	Allowed          bool
	Reason           string
	ConsistencyToken string
	MissingContext   []string
}

// AuthzCheckRequest is the input for CheckPermission.
type AuthzCheckRequest struct {
	Subject          AuthzSubjectRef
	Resource         AuthzObjectRef
	Permission       string
	TenantID         string
	OrgID            string
	ProjectID        string
	SubjectAttrs     map[string]any
	ResourceAttrs    map[string]any
	EnvironmentAttrs map[string]any
	Consistency      AuthzConsistency
}

// AuthzBatchCheckRequest is the input for BatchCheckPermissions.
type AuthzBatchCheckRequest struct {
	Checks      []AuthzCheckRequest
	Consistency AuthzConsistency
}

// AuthzBatchCheckResult is the result of BatchCheckPermissions.
type AuthzBatchCheckResult struct {
	Decisions []AuthzDecision
}

// AuthzLookupResourcesRequest is the input for LookupResources.
type AuthzLookupResourcesRequest struct {
	Subject          AuthzSubjectRef
	ResourceType     string
	Permission       string
	TenantID         string
	OrgID            string
	ProjectID        string
	SubjectAttrs     map[string]any
	EnvironmentAttrs map[string]any
	Consistency      AuthzConsistency
	Limit            int
	Cursor           string
}

// AuthzLookupResourcesResult is the result of LookupResources.
type AuthzLookupResourcesResult struct {
	Resources        []AuthzObjectRef
	NextCursor       string
	ConsistencyToken string
}

// AuthzLookupSubjectsRequest is the input for LookupSubjects.
type AuthzLookupSubjectsRequest struct {
	Resource         AuthzObjectRef
	Permission       string
	SubjectType      string
	TenantID         string
	OrgID            string
	ProjectID        string
	ResourceAttrs    map[string]any
	EnvironmentAttrs map[string]any
	Consistency      AuthzConsistency
	Limit            int
	Cursor           string
}

// AuthzLookupSubjectsResult is the result of LookupSubjects.
type AuthzLookupSubjectsResult struct {
	Subjects         []AuthzSubjectRef
	NextCursor       string
	ConsistencyToken string
}

// AuthzWriteResult is the result of Write/Delete relationships.
type AuthzWriteResult struct {
	ConsistencyToken string
	Written          int
	Deleted          int
}

// AuthzSchema is the SpiceDB schema text + version.
type AuthzSchema struct {
	Text string
}

// --- Error sentinels ---

var (
	ErrAuthzInvalidRequest = errorx.BadRequest(
		errorx.Code("AUTHZ_INVALID_REQUEST"),
		"invalid authorization request",
	)
	ErrAuthzPermissionDenied = errorx.Forbidden(
		errorx.Code("AUTHZ_PERMISSION_DENIED"),
		"permission denied",
	)
	ErrAuthzBackendFailed = errorx.Unavailable(
		errorx.Code("AUTHZ_BACKEND_FAILED"),
		"authorization backend failed",
	)
	ErrAuthzUnsupported = errorx.BadRequest(
		errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY"),
		"authorization capability is unsupported",
	)
)

// --- Repo interface ---
//
// biz.AuthzRepo abstracts the kernel authz.Service so biz can be unit-
// tested with a fake. The data layer adapts spicedb.Client (or any future
// ReBAC engine) into this interface.

type AuthzRepo interface {
	Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error)
	BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error)
	WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error)
	DeleteRelationships(ctx context.Context, filter AuthzRelationshipFilter) (AuthzWriteResult, error)
	ReadRelationships(ctx context.Context, filter AuthzRelationshipFilter, limit int, cursor string) ([]AuthzRelationship, string, error)
	LookupResources(ctx context.Context, req AuthzLookupResourcesRequest) (AuthzLookupResourcesResult, error)
	LookupSubjects(ctx context.Context, req AuthzLookupSubjectsRequest) (AuthzLookupSubjectsResult, error)
	ReadSchema(ctx context.Context) (AuthzSchema, error)
	WriteSchema(ctx context.Context, schema AuthzSchema) error
}

// --- Usecase ---

// AuthzUsecase orchestrates authorization checks and relationship
// management. It is intentionally thin — the heavy lifting (ReBAC graph
// traversal, ABAC caveat evaluation) happens in SpiceDB.
type AuthzUsecase struct {
	repo    AuthzRepo
	log     logx.Logger
	metrics metricsx.Manager
}

// NewAuthzUsecase creates a new AuthzUsecase. log may be nil.
func NewAuthzUsecase(repo AuthzRepo, log logx.Logger, managers ...metricsx.Manager) *AuthzUsecase {
	if log == nil {
		log = logx.Noop()
	}
	manager := metricsx.Noop()
	if len(managers) > 0 {
		manager = metricsx.Ensure(managers[0])
	}
	observability.RegisterMetrics(manager)
	return &AuthzUsecase{repo: repo, log: log.Named("authz"), metrics: manager}
}

// --- Check ---

// Check evaluates whether subject has permission on resource.
// Returns AuthzDecision with Effect="allow"/"deny"/"no_match".
// On backend failure, returns ErrAuthzBackendFailed.
func (uc *AuthzUsecase) Check(ctx context.Context, req AuthzCheckRequest) (decision AuthzDecision, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "check", logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "check", started, err, logx.Bool("allowed", decision.Allowed))
	}()
	if err := validateCheckRequest(req); err != nil {
		return AuthzDecision{}, err
	}
	decision, err = uc.repo.Check(ctx, req)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz check failed",
			logx.String("subject", req.Subject.Type+":"+req.Subject.ID),
			logx.String("resource", req.Resource.Type+":"+req.Resource.ID),
			logx.String("permission", req.Permission),
			logx.Err(err),
		)
		return AuthzDecision{}, mapAuthzError(err)
	}
	uc.log.WithContext(ctx).Debug("authz check",
		logx.String("subject", req.Subject.Type+":"+req.Subject.ID),
		logx.String("resource", req.Resource.Type+":"+req.Resource.ID),
		logx.String("permission", req.Permission),
		logx.String("effect", decision.Effect),
		logx.Bool("allowed", decision.Allowed),
	)
	return decision, nil
}

// BatchCheck evaluates multiple checks in one round-trip. Useful for
// list views where each row needs its own check.
func (uc *AuthzUsecase) BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (result AuthzBatchCheckResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "batch_check", logx.Int("checks", len(req.Checks)))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "batch_check", started, err, logx.Int("decisions", len(result.Decisions)))
	}()
	if len(req.Checks) == 0 {
		return AuthzBatchCheckResult{}, nil
	}
	for _, c := range req.Checks {
		if err := validateCheckRequest(c); err != nil {
			return AuthzBatchCheckResult{}, err
		}
	}
	// Apply batch-level consistency to each check that did not set its own.
	for i := range req.Checks {
		if req.Checks[i].Consistency.Token == "" && !req.Checks[i].Consistency.FullyConsistent {
			req.Checks[i].Consistency = req.Consistency
		}
	}
	result, err = uc.repo.BatchCheck(ctx, req)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz batch check failed",
			logx.Int("checks", len(req.Checks)),
			logx.Err(err),
		)
		return AuthzBatchCheckResult{}, mapAuthzError(err)
	}
	return result, nil
}

// --- Relationship CRUD ---

// WriteRelationships creates or updates relationship tuples. Idempotent
// (TOUCH semantics): writing an existing tuple is a no-op.
func (uc *AuthzUsecase) WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (result AuthzWriteResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "write_relationships", logx.Int("relationships", len(rels)))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "write_relationships", started, err, logx.Int("written", result.Written))
	}()
	if len(rels) == 0 {
		return AuthzWriteResult{}, nil
	}
	for _, rel := range rels {
		if err := validateRelationship(rel); err != nil {
			return AuthzWriteResult{}, err
		}
	}
	result, err = uc.repo.WriteRelationships(ctx, rels...)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz write relationships failed",
			logx.Int("count", len(rels)),
			logx.Err(err),
		)
		return AuthzWriteResult{}, mapAuthzError(err)
	}
	uc.log.WithContext(ctx).Info("authz relationships written",
		logx.Int("count", result.Written),
		logx.String("consistency_token", result.ConsistencyToken),
	)
	return result, nil
}

// DeleteRelationships removes relationship tuples matching the filter.
// At least one of resource / subject field must be set.
func (uc *AuthzUsecase) DeleteRelationships(ctx context.Context, filter AuthzRelationshipFilter) (result AuthzWriteResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "delete_relationships", logx.String("resource_type", filter.ResourceType))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "delete_relationships", started, err, logx.Int("deleted", result.Deleted))
	}()
	if err := validateFilter(filter); err != nil {
		return AuthzWriteResult{}, err
	}
	result, err = uc.repo.DeleteRelationships(ctx, filter)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz delete relationships failed",
			logx.String("resource_type", filter.ResourceType),
			logx.String("resource_id", filter.ResourceID),
			logx.String("relation", filter.Relation),
			logx.String("subject_type", filter.SubjectType),
			logx.String("subject_id", filter.SubjectID),
			logx.Err(err),
		)
		return AuthzWriteResult{}, mapAuthzError(err)
	}
	uc.log.WithContext(ctx).Info("authz relationships deleted",
		logx.Int("count", result.Deleted),
	)
	return result, nil
}

// ReadRelationships lists relationship tuples matching the filter.
// Used by share-management UIs.
func (uc *AuthzUsecase) ReadRelationships(ctx context.Context, filter AuthzRelationshipFilter, limit int, cursor string) (rels []AuthzRelationship, nextCursor string, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "read_relationships", logx.String("resource_type", filter.ResourceType), logx.Int("limit", limit))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "read_relationships", started, err, logx.Int("relationships", len(rels)))
	}()
	if err := validateFilter(filter); err != nil {
		return nil, "", err
	}
	if limit < 0 {
		limit = 0
	}
	rels, nextCursor, err = uc.repo.ReadRelationships(ctx, filter, limit, cursor)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz read relationships failed",
			logx.String("resource_type", filter.ResourceType),
			logx.String("resource_id", filter.ResourceID),
			logx.Err(err),
		)
		return nil, "", mapAuthzError(err)
	}
	return rels, nextCursor, nil
}

// --- Lookup ---

// LookupResources returns resource IDs that the subject can access with
// the given permission. Used by "list my skills" views.
func (uc *AuthzUsecase) LookupResources(ctx context.Context, req AuthzLookupResourcesRequest) (result AuthzLookupResourcesResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "lookup_resources", logx.String("resource_type", req.ResourceType), logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "lookup_resources", started, err, logx.Int("resources", len(result.Resources)))
	}()
	if (req.Subject.Type == "" || req.Subject.ID == "") ||
		req.ResourceType == "" || req.Permission == "" {
		return AuthzLookupResourcesResult{}, ErrAuthzInvalidRequest
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	result, err = uc.repo.LookupResources(ctx, req)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz lookup resources failed",
			logx.String("subject", req.Subject.Type+":"+req.Subject.ID),
			logx.String("resource_type", req.ResourceType),
			logx.String("permission", req.Permission),
			logx.Err(err),
		)
		return AuthzLookupResourcesResult{}, mapAuthzError(err)
	}
	return result, nil
}

// LookupSubjects returns subjects that can access the given resource with
// the given permission. Used by "who can access this skill?" views.
func (uc *AuthzUsecase) LookupSubjects(ctx context.Context, req AuthzLookupSubjectsRequest) (result AuthzLookupSubjectsResult, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "lookup_subjects", logx.String("subject_type", req.SubjectType), logx.String("permission", req.Permission))
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "lookup_subjects", started, err, logx.Int("subjects", len(result.Subjects)))
	}()
	if (req.Resource.Type == "" || req.Resource.ID == "") ||
		req.SubjectType == "" || req.Permission == "" {
		return AuthzLookupSubjectsResult{}, ErrAuthzInvalidRequest
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	result, err = uc.repo.LookupSubjects(ctx, req)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz lookup subjects failed",
			logx.String("resource", req.Resource.Type+":"+req.Resource.ID),
			logx.String("subject_type", req.SubjectType),
			logx.String("permission", req.Permission),
			logx.Err(err),
		)
		return AuthzLookupSubjectsResult{}, mapAuthzError(err)
	}
	return result, nil
}

// --- Schema ---

// ReadSchema returns the current SpiceDB schema text.
func (uc *AuthzUsecase) ReadSchema(ctx context.Context) (schema AuthzSchema, err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "read_schema")
	defer func() {
		observability.End(ctx, logger, uc.metrics, "authz", "read_schema", started, err, logx.Int("schema_size", len(schema.Text)))
	}()
	schema, err = uc.repo.ReadSchema(ctx)
	if err != nil {
		uc.log.WithContext(ctx).Warn("authz read schema failed", logx.Err(err))
		return AuthzSchema{}, mapAuthzError(err)
	}
	return schema, nil
}

// WriteSchema replaces the SpiceDB schema. Use with care.
func (uc *AuthzUsecase) WriteSchema(ctx context.Context, schema AuthzSchema) (err error) {
	ctx, logger, started := observability.Begin(ctx, uc.log, "authz", "write_schema", logx.Int("schema_size", len(schema.Text)))
	defer func() { observability.End(ctx, logger, uc.metrics, "authz", "write_schema", started, err) }()
	if strings.TrimSpace(schema.Text) == "" {
		return ErrAuthzInvalidRequest
	}
	if err := uc.repo.WriteSchema(ctx, schema); err != nil {
		uc.log.WithContext(ctx).Warn("authz write schema failed", logx.Err(err))
		return mapAuthzError(err)
	}
	uc.log.WithContext(ctx).Info("authz schema written",
		logx.Int("size", len(schema.Text)),
	)
	return nil
}

// --- Business helpers (used by other biz modules like skill) ---

// GrantOwner writes a {resource}#owner@{subject} relationship. Used by
// SkillUsecase.CreateSkill to make the creator the owner.
//
// This is a thin convenience wrapper around WriteRelationships. The
// "owner" relation must be defined in the SpiceDB schema for the
// resource type.
func (uc *AuthzUsecase) GrantOwner(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error {
	_, err := uc.WriteRelationships(ctx, AuthzRelationship{
		Resource: resource,
		Relation: "owner",
		Subject:  subject,
	})
	return err
}

// GrantRole writes a {resource}#{relation}@{subject} relationship. Use
// relation = "viewer" / "editor" / "admin" etc.
func (uc *AuthzUsecase) GrantRole(ctx context.Context, resource AuthzObjectRef, relation string, subject AuthzSubjectRef) error {
	if strings.TrimSpace(relation) == "" {
		return ErrAuthzInvalidRequest
	}
	_, err := uc.WriteRelationships(ctx, AuthzRelationship{
		Resource: resource,
		Relation: relation,
		Subject:  subject,
	})
	return err
}

// RevokeAll removes all relationships between a resource and a subject,
// regardless of relation. Used by SkillUsecase.DeleteSkillShare.
func (uc *AuthzUsecase) RevokeAll(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error {
	_, err := uc.DeleteRelationships(ctx, AuthzRelationshipFilter{
		ResourceType: resource.Type,
		ResourceID:   resource.ID,
		SubjectType:  subject.Type,
		SubjectID:    subject.ID,
	})
	return err
}

// RevokeResource removes ALL relationships on a resource (any subject,
// any relation). Used by SkillUsecase.DeleteSkill to clean up tuples
// before the row is soft-deleted.
func (uc *AuthzUsecase) RevokeResource(ctx context.Context, resource AuthzObjectRef) error {
	_, err := uc.DeleteRelationships(ctx, AuthzRelationshipFilter{
		ResourceType: resource.Type,
		ResourceID:   resource.ID,
	})
	return err
}

// Can is a non-fatal check that returns (true, nil) when subject has
// permission on resource, (false, nil) when denied, and (false, err)
// when the backend fails. Useful for list-view filtering where one
// denied row should not abort the whole list.
func (uc *AuthzUsecase) Can(ctx context.Context, req AuthzCheckRequest) (bool, error) {
	decision, err := uc.Check(ctx, req)
	if err != nil {
		return false, err
	}
	return decision.Allowed, nil
}

// Require is a fatal check that returns ErrAuthzPermissionDenied when
// the subject lacks the permission. Use in handlers that should 403 on
// denial (e.g. write operations).
func (uc *AuthzUsecase) Require(ctx context.Context, req AuthzCheckRequest) error {
	decision, err := uc.Check(ctx, req)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return ErrAuthzPermissionDenied
	}
	return nil
}

// --- helpers ---

func validateCheckRequest(req AuthzCheckRequest) error {
	if req.Subject.Type == "" || req.Subject.ID == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("subject.type and subject.id are required"))
	}
	if req.Resource.Type == "" || req.Resource.ID == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("resource.type and resource.id are required"))
	}
	if strings.TrimSpace(req.Permission) == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("permission is required"))
	}
	return nil
}

func validateRelationship(rel AuthzRelationship) error {
	if rel.Resource.Type == "" || rel.Resource.ID == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("resource.type and resource.id are required"))
	}
	if strings.TrimSpace(rel.Relation) == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("relation is required"))
	}
	if rel.Subject.Type == "" || rel.Subject.ID == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("subject.type and subject.id are required"))
	}
	return nil
}

func validateFilter(filter AuthzRelationshipFilter) error {
	// At least one field must be set — deleting ALL relationships across
	// the entire DB is a footgun we refuse to load.
	if filter.ResourceType == "" && filter.ResourceID == "" &&
		filter.Relation == "" && filter.SubjectType == "" &&
		filter.SubjectID == "" && filter.SubjectRelation == "" {
		return errorx.From(ErrAuthzInvalidRequest, errorx.WithMessage("at least one filter field must be set"))
	}
	return nil
}

// mapAuthzError translates kernel authz errors into biz-level sentinels.
// kernel already maps SpiceDB gRPC errors to authz.ErrBackendFailed /
// authz.ErrInvalidRequest; we just re-wrap them with our own codes so
// the service layer sees consistent error codes regardless of which
// engine is configured.
func mapAuthzError(err error) error {
	if err == nil {
		return nil
	}
	// authz.ErrPermissionDenied is NOT a backend error — it is a check
	// result, so it should never come out of the repo. We only map real
	// backend / request errors here.
	if isAuthzCode(err, authz.CodeBackendFailed) {
		return errorx.Wrap(err, errorx.Code("AUTHZ_BACKEND_FAILED"),
			errorx.WithMessage("authorization backend failed"))
	}
	if isAuthzCode(err, authz.CodeInvalidRequest) {
		return errorx.Wrap(err, errorx.Code("AUTHZ_INVALID_REQUEST"),
			errorx.WithMessage("invalid authorization request"))
	}
	if isAuthzCode(err, authz.CodeUnsupportedCapability) {
		return errorx.Wrap(err, errorx.Code("AUTHZ_UNSUPPORTED_CAPABILITY"),
			errorx.WithMessage("authorization capability is unsupported"))
	}
	return err
}

// isAuthzCode returns true if err is a kernel errorx.Error whose code
// matches the given kernel authz code. Kernel authz codes are defined
// as errorx.Code values (not a separate authz package Code type), so
// we compare via errorx.As + Code().
func isAuthzCode(err error, code errorx.Code) bool {
	if e, ok := errorx.As(err); ok {
		return e.Code() == code
	}
	return false
}
