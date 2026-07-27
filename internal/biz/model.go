package biz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

// ModelProfile catalog errors.
var (
	ErrModelProfileNotFound        = errorx.NotFound(errorx.Code("MODEL_PROFILE_NOT_FOUND"), "model profile not found")
	ErrModelProfileInvalidArgument = errorx.BadRequest(errorx.Code("INVALID_ARGUMENT"), "invalid model profile argument")
	ErrModelProfileAlreadyExists   = errorx.Conflict(errorx.Code("MODEL_PROFILE_ALREADY_EXISTS"), "model profile already exists")
)

// validModelProfileID constrains profile identifiers: lowercase DNS-1123-ish
// (allows hyphens), e.g. "coding-default", "openai-prod". Dots are not allowed
// (unlike tool IDs) so the logical name aisphere://{id}@{rev} stays unambiguous.
func validModelProfileID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			continue
		case r == '-':
			if i == 0 {
				return false
			}
			continue
		default:
			return false
		}
	}
	last := id[len(id)-1]
	return last != '-'
}

// --- Domain types ---

// ModelProfile is the flat catalog record (matches the frontend ModelProfile
// type and the proto ModelProfile message). The versioned, immutable detail
// lives in ModelProfileRevision; Revisions holds the per-revision records
// internally (not exposed in the flat proto surface).
type ModelProfile struct {
	ID            string
	Version       string // latest revision label
	Status        string // active | disabled
	DisplayName   string
	Description   string
	Provider      string // openai | vllm | vertex | custom
	APIFormat     string // openai_responses | openai_chat_completions | gemini
	Endpoint      string
	Model         string // logical model name (aisphere://{id})
	UpstreamModel string
	UpstreamPath  string
	SecretRef     string // credential ref, e.g. secret://model/openai-prod
	AllowedTools  []string
	Limits        ModelProfileLimits
	Reasoning     string // arbitrary JSON (string)
	Labels        map[string]string
	Metadata      string // arbitrary JSON (string)
	Object        string // "model_profile:{id}" SpiceDB object ref
	OwnerType     string
	OwnerID       string
	OwnerName     string
	OrgID         string
	ProjectID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Revisions holds the immutable per-revision records. The data layer
	// populates this on read; Create/Update populate the new entry on write.
	// The service layer does NOT expose it in the flat proto ModelProfile.
	Revisions map[string]ModelProfileRevision
}

// ModelProfileLimits caps token usage for a profile.
type ModelProfileLimits struct {
	MaxInputTokens  int32
	MaxOutputTokens int32
}

// ModelProfileRevision is an immutable snapshot of a profile at a revision.
type ModelProfileRevision struct {
	Revision          string
	Provider          string
	APIFormat         string
	Endpoint          string
	Model             string
	UpstreamModel     string
	UpstreamPath      string
	SecretRef         string
	AllowedTools      []string
	Limits            ModelProfileLimits
	Reasoning         string // JSON (string)
	DefaultParameters string // JSON (string)
	Metadata          string // JSON (string)
	SHA256            string
	Author            string
	CommitMsg         string
	CreateTime        time.Time
}

// ModelProfileCapabilities is the resolved capability flags Runtime uses to
// decide tool calling / streaming / reasoning / multimodal.
type ModelProfileCapabilities struct {
	Tools      bool
	Streaming  bool
	Reasoning  bool
	Multimodal bool
}

// ModelProfileSnapshotLimits is the limits shape Runtime consumes.
type ModelProfileSnapshotLimits struct {
	ContextWindow   int32 // = MaxInputTokens
	MaxOutputTokens int32
}

// ModelProfileSnapshot is the immutable runtime resolution result. It never
// carries a plain-text credential — only CredentialRef the Runtime
// CredentialProvider resolves. LogicalName is what ADK sees.
type ModelProfileSnapshot struct {
	ProfileID         string
	Revision          string
	LogicalName       string // aisphere://{id}@{revision}
	Provider          string
	Protocol          string // = APIFormat
	BaseURL           string // = Endpoint
	UpstreamModel     string
	UpstreamPath      string
	CredentialRef     string
	Capabilities      ModelProfileCapabilities
	Limits            ModelProfileSnapshotLimits
	SHA256            string
	DefaultParameters string
	GeneratedAt       time.Time
}

// ModelProfileRepository is the data-layer interface for the model catalog.
type ModelProfileRepository interface {
	List(ctx context.Context, opts ModelProfileListOptions) ([]*ModelProfile, string, error)
	Get(ctx context.Context, id string, version string) (*ModelProfile, error)
	Create(ctx context.Context, p *ModelProfile) (*ModelProfile, error)
	Update(ctx context.Context, p *ModelProfile) (*ModelProfile, error)
	Delete(ctx context.Context, id string) error
}

type ModelProfileListOptions struct {
	Limit    int
	Offset   int
	Query    string
	Status   string
	Provider string
	OrgID    string
}

// ModelProfileRelationships is the authz interface. model_profile uses the
// SpiceDB `model_profile` definition (owner/editor/viewer/executor; parent
// project). GrantOwner makes the creator owner; GrantParent links the profile
// to its project so project operators inherit execute.
type ModelProfileRelationships interface {
	Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error)
	BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error)
	GrantOwner(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error
	GrantParent(ctx context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error
	RevokeResource(ctx context.Context, resource AuthzObjectRef) error
}

// ModelProfileUsecase implements the model catalog business logic.
type ModelProfileUsecase struct {
	profiles ModelProfileRepository
	rels     ModelProfileRelationships
}

func NewModelProfileUsecase(profiles ModelProfileRepository, rels ModelProfileRelationships) *ModelProfileUsecase {
	return &ModelProfileUsecase{profiles: profiles, rels: rels}
}

func modelProfileResource(id string) AuthzObjectRef {
	return AuthzObjectRef{Type: "model_profile", ID: id}
}

// currentRevision builds the immutable revision record from the profile's flat
// fields and computes its content sha256. The sha256 covers the revision
// definition only (not author/commit_msg/timestamp) so the same definition
// always yields the same hash.
func (p *ModelProfile) currentRevision(author, commitMsg string) ModelProfileRevision {
	rev := ModelProfileRevision{
		Revision:     p.Version,
		Provider:     p.Provider,
		APIFormat:    p.APIFormat,
		Endpoint:     p.Endpoint,
		Model:        p.Model,
		UpstreamModel: p.UpstreamModel,
		UpstreamPath: p.UpstreamPath,
		SecretRef:    p.SecretRef,
		AllowedTools: append([]string(nil), p.AllowedTools...),
		Limits:       p.Limits,
		Reasoning:    p.Reasoning,
		Metadata:     p.Metadata,
		Author:       author,
		CommitMsg:    commitMsg,
		CreateTime:   time.Now().UTC(),
	}
	rev.SHA256 = computeRevisionSHA256(rev)
	return rev
}

// revisionDefinitionForHash is the canonical struct hashed into sha256. It
// intentionally excludes Revision, SHA256, Author, CommitMsg, CreateTime so
// only the model definition contributes to the hash.
type revisionDefinitionForHash struct {
	Provider          string
	APIFormat         string
	Endpoint          string
	Model             string
	UpstreamModel     string
	UpstreamPath      string
	SecretRef         string
	AllowedTools      []string
	Limits            ModelProfileLimits
	Reasoning         string
	DefaultParameters string
	Metadata          string
}

func computeRevisionSHA256(r ModelProfileRevision) string {
	def := revisionDefinitionForHash{
		Provider: r.Provider, APIFormat: r.APIFormat, Endpoint: r.Endpoint,
		Model: r.Model, UpstreamModel: r.UpstreamModel, UpstreamPath: r.UpstreamPath,
		SecretRef: r.SecretRef, AllowedTools: r.AllowedTools, Limits: r.Limits,
		Reasoning: r.Reasoning, DefaultParameters: r.DefaultParameters, Metadata: r.Metadata,
	}
	b, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// latestRevision returns the revision record for the profile's latest version,
// or the explicitly requested version. Returns ErrModelProfileNotFound when the
// requested version does not exist.
func (p *ModelProfile) latestRevision(version string) (ModelProfileRevision, error) {
	if version == "" {
		version = p.Version
	}
	if r, ok := p.Revisions[version]; ok {
		return r, nil
	}
	return ModelProfileRevision{}, fmt.Errorf("%w: revision %q not found", ErrModelProfileNotFound, version)
}

// nextRevisionLabel bumps a "vN" label to "v(N+1)". Non-conforming labels get
// "-r2" appended so updates always advance the label.
func nextRevisionLabel(latest string) string {
	latest = strings.TrimSpace(latest)
	if latest == "" {
		return "v1"
	}
	if strings.HasPrefix(latest, "v") {
		if n, err := strconv.Atoi(latest[1:]); err == nil {
			return fmt.Sprintf("v%d", n+1)
		}
	}
	return latest + "-r2"
}

// resolveProjectID picks the parent project for a new profile. Priority:
// request > principal.ProjectID > principal.OrgID. The OrgID fallback lets an
// org-scoped catalog work without an explicit project id.
func resolveProjectID(reqProjectID string, principal authn.Principal) (string, error) {
	if v := strings.TrimSpace(reqProjectID); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(principal.ProjectID); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(principal.OrgID); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w: project_id is required (request, principal.project_id, or principal.org_id)", ErrModelProfileInvalidArgument)
}

// CreateModelProfile validates, explicit-checks edit on the parent project
// (correction 4: biz-layer check, no proto authz interpolation), persists, and
// writes SpiceDB owner + parent relationships.
func (uc *ModelProfileUsecase) CreateModelProfile(ctx context.Context, principal authn.Principal, p *ModelProfile) (*ModelProfile, error) {
	if err := validateModelProfileForCreate(p); err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	projectID, err := resolveProjectID(p.ProjectID, principal)
	if err != nil {
		return nil, err
	}
	// Explicit biz-layer edit check on the parent project (correction 4). The
	// proto policy is AUTHENTICATED only; the resource is derived from the
	// principal identity, not a path parameter. Project has no `edit`
	// permission (it has manage/write/operate/read); model_profile.edit
	// inherits project.write via parent->write, so we check `write` here.
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   AuthzObjectRef{Type: "project", ID: projectID},
		Permission: "write",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no edit permission on project")
	}

	p.OwnerType = principal.SubjectType
	p.OwnerID = principal.SubjectID
	p.OwnerName = principal.Name
	p.OrgID = principal.OrgID
	p.ProjectID = projectID
	p.Object = "model_profile:" + p.ID
	if p.Status == "" {
		p.Status = "active"
	}
	if p.APIFormat == "" {
		p.APIFormat = "openai_responses"
	}
	if p.Version == "" {
		p.Version = "v1"
	}
	if p.Model == "" {
		p.Model = "aisphere://" + p.ID
	}
	if p.Revisions == nil {
		p.Revisions = map[string]ModelProfileRevision{}
	}
	p.Revisions[p.Version] = p.currentRevision(principal.Name, "initial revision")

	out, err := uc.profiles.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	// Best-effort SpiceDB relationships; compensate by soft-deleting the row
	// on failure so the catalog does not leak an unowned profile.
	if err := uc.rels.GrantOwner(ctx, modelProfileResource(p.ID), subject); err != nil {
		_ = uc.profiles.Delete(ctx, p.ID)
		return nil, fmt.Errorf("model profile create: grant owner: %w", err)
	}
	if err := uc.rels.GrantParent(ctx, modelProfileResource(p.ID), AuthzSubjectRef{Type: "project", ID: projectID}); err != nil {
		_ = uc.profiles.Delete(ctx, p.ID)
		return nil, fmt.Errorf("model profile create: grant parent: %w", err)
	}
	return out, nil
}

func validateModelProfileForCreate(p *ModelProfile) error {
	if !validModelProfileID(p.ID) {
		return fmt.Errorf("%w: id must match [a-z0-9][a-z0-9-]{0,127} (lowercase, no trailing hyphen)", ErrModelProfileInvalidArgument)
	}
	if strings.TrimSpace(p.Provider) == "" {
		return fmt.Errorf("%w: provider is required", ErrModelProfileInvalidArgument)
	}
	if strings.TrimSpace(p.APIFormat) == "" {
		return fmt.Errorf("%w: api_format is required", ErrModelProfileInvalidArgument)
	}
	if strings.TrimSpace(p.Endpoint) == "" {
		return fmt.Errorf("%w: endpoint is required", ErrModelProfileInvalidArgument)
	}
	if strings.TrimSpace(p.UpstreamModel) == "" {
		return fmt.Errorf("%w: upstream_model is required", ErrModelProfileInvalidArgument)
	}
	return nil
}

// GetModelProfile reads a profile (optionally a specific revision) and checks
// view permission.
func (uc *ModelProfileUsecase) GetModelProfile(ctx context.Context, principal authn.Principal, id, version string) (*ModelProfile, error) {
	p, err := uc.profiles.Get(ctx, id, version)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   modelProfileResource(p.ID),
		Permission: "view",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no view permission on model profile")
	}
	return p, nil
}

// ListModelProfiles returns profiles visible to the principal via a batch view
// check.
func (uc *ModelProfileUsecase) ListModelProfiles(ctx context.Context, principal authn.Principal, opts ModelProfileListOptions) ([]*ModelProfile, string, error) {
	opts.OrgID = principal.OrgID
	profiles, next, err := uc.profiles.List(ctx, opts)
	if err != nil {
		return nil, "", err
	}
	if len(profiles) == 0 {
		return profiles, next, nil
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, "", err
	}
	checks := make([]AuthzCheckRequest, 0, len(profiles))
	for _, p := range profiles {
		checks = append(checks, AuthzCheckRequest{
			Subject:    subject,
			Resource:   modelProfileResource(p.ID),
			Permission: "view",
			OrgID:      principal.OrgID,
		})
	}
	res, err := uc.rels.BatchCheck(ctx, AuthzBatchCheckRequest{Checks: checks})
	if err != nil {
		return nil, "", err
	}
	out := make([]*ModelProfile, 0, len(profiles))
	for i, p := range profiles {
		if i < len(res.Decisions) && res.Decisions[i].Allowed {
			out = append(out, p)
		}
	}
	return out, next, nil
}

// UpdateModelProfile checks edit permission, persists metadata, and writes a
// new immutable revision (advancing latest_revision).
func (uc *ModelProfileUsecase) UpdateModelProfile(ctx context.Context, principal authn.Principal, p *ModelProfile) (*ModelProfile, error) {
	if !validModelProfileID(p.ID) {
		return nil, fmt.Errorf("%w: invalid id", ErrModelProfileInvalidArgument)
	}
	existing, err := uc.profiles.Get(ctx, p.ID, "")
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   modelProfileResource(p.ID),
		Permission: "edit",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no edit permission on model profile")
	}

	// Preserve identity/ownership from the existing row; the caller cannot
	// reassign owner/org via update.
	p.OwnerType = existing.OwnerType
	p.OwnerID = existing.OwnerID
	p.OwnerName = existing.OwnerName
	p.OrgID = existing.OrgID
	p.ProjectID = existing.ProjectID
	p.Object = existing.Object
	if p.Status == "" {
		p.Status = existing.Status
	}
	if p.APIFormat == "" {
		p.APIFormat = existing.APIFormat
	}
	if p.Model == "" {
		p.Model = existing.Model
	}
	// Advance the revision label: caller-provided version wins; otherwise bump.
	newVersion := strings.TrimSpace(p.Version)
	if newVersion == "" {
		newVersion = nextRevisionLabel(existing.Version)
	}
	p.Version = newVersion
	// Carry forward revisions so the data layer can keep history intact and
	// the new entry is appended.
	if p.Revisions == nil {
		p.Revisions = map[string]ModelProfileRevision{}
	}
	for k, v := range existing.Revisions {
		p.Revisions[k] = v
	}
	p.Revisions[newVersion] = p.currentRevision(principal.Name, "update")

	return uc.profiles.Update(ctx, p)
}

// DeleteModelProfile checks manage permission, soft-deletes, and revokes all
// SpiceDB relationships on the profile.
func (uc *ModelProfileUsecase) DeleteModelProfile(ctx context.Context, principal authn.Principal, id string) (*ModelProfile, error) {
	existing, err := uc.profiles.Get(ctx, id, "")
	if err != nil {
		return nil, err
	}
	subject, serr := canonicalSubject(principal)
	if serr != nil {
		return nil, serr
	}
	dec, derr := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   modelProfileResource(id),
		Permission: "manage",
		OrgID:      principal.OrgID,
	})
	if derr != nil {
		return nil, derr
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no manage permission on model profile")
	}
	if err := uc.profiles.Delete(ctx, id); err != nil {
		return nil, err
	}
	// Best-effort revoke; a stale relationship does not resurrect a soft-deleted row.
	_ = uc.rels.RevokeResource(ctx, modelProfileResource(id))
	return existing, nil
}

// ResolveModelProfile checks execute permission and returns an immutable
// runtime snapshot. The snapshot carries a credential_ref (never a plain-text
// key) the Runtime CredentialProvider resolves.
func (uc *ModelProfileUsecase) ResolveModelProfile(ctx context.Context, principal authn.Principal, id, version, runtimeID, sessionID string) (*ModelProfileSnapshot, error) {
	p, err := uc.profiles.Get(ctx, id, version)
	if err != nil {
		return nil, err
	}
	subject, err := canonicalSubject(principal)
	if err != nil {
		return nil, err
	}
	dec, err := uc.rels.Check(ctx, AuthzCheckRequest{
		Subject:    subject,
		Resource:   modelProfileResource(p.ID),
		Permission: "execute",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return nil, err
	}
	if !dec.Allowed {
		return nil, errorx.Forbidden(errorx.Code("PERMISSION_DENIED"), "forbidden: no execute permission on model profile")
	}
	rev, err := p.latestRevision(version)
	if err != nil {
		return nil, err
	}
	return buildSnapshot(p, rev), nil
}

// buildSnapshot assembles the immutable runtime snapshot. Capabilities are
// derived (V1 heuristic): tools when allowed_tools is non-empty; streaming
// always on; reasoning when reasoning JSON is non-empty; multimodal off.
func buildSnapshot(p *ModelProfile, rev ModelProfileRevision) *ModelProfileSnapshot {
	return &ModelProfileSnapshot{
		ProfileID:     p.ID,
		Revision:      rev.Revision,
		LogicalName:   fmt.Sprintf("aisphere://%s@%s", p.ID, rev.Revision),
		Provider:      rev.Provider,
		Protocol:      rev.APIFormat,
		BaseURL:       rev.Endpoint,
		UpstreamModel: rev.UpstreamModel,
		UpstreamPath:  rev.UpstreamPath,
		CredentialRef: rev.SecretRef,
		Capabilities: ModelProfileCapabilities{
			Tools:      len(rev.AllowedTools) > 0,
			Streaming:  true,
			Reasoning:  strings.TrimSpace(rev.Reasoning) != "",
			Multimodal: false,
		},
		Limits: ModelProfileSnapshotLimits{
			ContextWindow:   rev.Limits.MaxInputTokens,
			MaxOutputTokens: rev.Limits.MaxOutputTokens,
		},
		SHA256:            rev.SHA256,
		DefaultParameters: rev.DefaultParameters,
		GeneratedAt:       time.Now().UTC(),
	}
}

// TestModelProfile is not yet implemented (depends on a model gateway / Runtime
// dialer). Returns Unavailable. The audit event is recorded by the access
// policy (hub.model.test) even on the stub so test attempts are traceable.
func (uc *ModelProfileUsecase) TestModelProfile(ctx context.Context, principal authn.Principal, id, prompt string) (*ModelProfileTestResult, error) {
	return nil, errorx.Unavailable(errorx.Code("MODEL_TEST_NOT_AVAILABLE"), "model profile test is not yet available; the runtime model dialer is not connected")
}

// ModelProfileTestResult is the (future) test result shape. Returned today by
// no code path — TestModelProfile always errors with Unavailable.
type ModelProfileTestResult struct {
	OK          bool
	Error       string
	LatencyMs   int32
}

// EnsureErrors keeps the errors import referenced when no direct use remains.
var _ = errors.Is
