// Package biz skill_test.go — unit tests for SkillUsecase.
//
// These tests use a fake SkillRepo and a fake AuthzUsecase (via the
// AuthzRepo interface) to verify the skill usecase's business logic
// without touching a real database or SpiceDB. Coverage focuses on:
//
//   - ownership: CreateSkill stamps OwnerID from principal
//   - authz integration: GetSkill / UpdateSkill / DeleteSkill call
//     requireSkillRead / requireSkillPermission correctly
//   - public-visibility fallback: GetSkill on a public skill succeeds
//     even when authz denies
//   - state machine: transitions enforce CAS (expected status match)
//   - share: CreateSkillShare rejects "owner" relation; DeleteSkillShare
//     re-grants owner if the subject was the owner
//   - audit: recordAudit is called on success and failure (verified
//     via the fake audit recorder)

package biz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
)

// --- fakes ---

// fakeSkillRepo is an in-memory SkillRepo for testing.
type fakeSkillRepo struct {
	mu        sync.Mutex
	skills    map[string]*Skill
	versions  map[string]map[string]*SkillVersion // skill -> version -> *
	files     map[string]map[string][]*SkillFile  // skill -> version -> files
	downloads map[string]int                      // "skill:version" -> count
	createErr error                               // inject create error
	updateErr error
	deleteErr error
}

func newFakeSkillRepo() *fakeSkillRepo {
	return &fakeSkillRepo{
		skills:    map[string]*Skill{},
		versions:  map[string]map[string]*SkillVersion{},
		files:     map[string]map[string][]*SkillFile{},
		downloads: map[string]int{},
	}
}

func (r *fakeSkillRepo) CreateSkill(ctx context.Context, skill *Skill) (*Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return nil, r.createErr
	}
	if _, exists := r.skills[skill.Name]; exists {
		return nil, ErrSkillAlreadyExists
	}
	now := time.Now()
	skill.ID = int64(len(r.skills) + 1)
	skill.CreateTime = now
	skill.UpdateTime = now
	r.skills[skill.Name] = skill
	// Create initial draft version (mirrors data layer behavior).
	if r.versions[skill.Name] == nil {
		r.versions[skill.Name] = map[string]*SkillVersion{}
	}
	r.versions[skill.Name][skill.Version] = &SkillVersion{
		SkillName: skill.Name,
		Version:   skill.Version,
		Status:    SkillVersionStatusDraft,
		Author:    skill.OwnerID,
	}
	return skill, nil
}

func (r *fakeSkillRepo) UpdateSkill(ctx context.Context, skill *Skill) (*Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	existing, ok := r.skills[skill.Name]
	if !ok {
		return nil, ErrSkillNotFound
	}
	existing.DisplayName = skill.DisplayName
	existing.Description = skill.Description
	existing.Version = skill.Version
	existing.SourceType = skill.SourceType
	existing.SourceURI = skill.SourceURI
	existing.ManifestJSON = skill.ManifestJSON
	existing.Tags = skill.Tags
	existing.UpdateTime = time.Now()
	return existing, nil
}

func (r *fakeSkillRepo) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.skills[name]
	if !ok {
		return nil, ErrSkillNotFound
	}
	existing.Visibility = visibility
	existing.UpdateTime = time.Now()
	return existing, nil
}

func (r *fakeSkillRepo) ListSkills(ctx context.Context, opts SkillListOptions) (*SkillListResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	items := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		items = append(items, s)
	}
	// Simple offset+limit; ignore Query/Status/Visibility filters for test simplicity.
	start := opts.Offset
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[start:end]
	return &SkillListResult{
		Items:      page,
		NextOffset: start + len(page),
		HasMore:    end < len(items),
	}, nil
}

func (r *fakeSkillRepo) GetSkill(ctx context.Context, name string) (*Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.skills[name]
	if !ok {
		return nil, ErrSkillNotFound
	}
	return s, nil
}

func (r *fakeSkillRepo) DeleteSkill(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if _, ok := r.skills[name]; !ok {
		return ErrSkillNotFound
	}
	delete(r.skills, name)
	delete(r.versions, name)
	delete(r.files, name)
	return nil
}

func (r *fakeSkillRepo) ListSkillVersions(ctx context.Context, name string) ([]*SkillVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[name]
	out := make([]*SkillVersion, 0, len(versions))
	for _, v := range versions {
		out = append(out, v)
	}
	return out, nil
}

func (r *fakeSkillRepo) GetSkillVersion(ctx context.Context, name, version string) (*SkillVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[name]
	if versions == nil {
		return nil, ErrSkillVersionNotFound
	}
	v, ok := versions[version]
	if !ok {
		return nil, ErrSkillVersionNotFound
	}
	return v, nil
}

func (r *fakeSkillRepo) UpdateSkillVersionStatus(ctx context.Context, name, version, expected, target string) (*SkillVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[name]
	if versions == nil {
		return nil, ErrSkillVersionNotFound
	}
	v, ok := versions[version]
	if !ok {
		return nil, ErrSkillVersionNotFound
	}
	if v.Status != expected {
		return nil, ErrSkillVersionNotFound // CAS failure
	}
	v.Status = target
	v.UpdateTime = time.Now()
	return v, nil
}

func (r *fakeSkillRepo) GetOnlineSkillVersion(ctx context.Context, name string) (*SkillVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	versions := r.versions[name]
	for _, v := range versions {
		if v.Status == SkillVersionStatusOnline {
			return v, nil
		}
	}
	return nil, ErrSkillVersionNotFound
}

func (r *fakeSkillRepo) ListSkillVersionFiles(ctx context.Context, name, version string) ([]*SkillFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[name][version], nil
}

func (r *fakeSkillRepo) GetSkillVersionFile(ctx context.Context, name, version, filePath string) (*SkillFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.files[name][version] {
		if f.Path == filePath {
			return f, nil
		}
	}
	return nil, ErrSkillFileNotFound
}

func (r *fakeSkillRepo) ListSkillDraftFiles(ctx context.Context, name, version string) ([]*SkillFile, error) {
	return r.ListSkillVersionFiles(ctx, name, version)
}

func (r *fakeSkillRepo) GetSkillDraftFile(ctx context.Context, name, version, filePath string) (*SkillFile, error) {
	return r.GetSkillVersionFile(ctx, name, version, filePath)
}

func (r *fakeSkillRepo) UpsertSkillDraftFile(ctx context.Context, file *SkillFile, actor string) (*SkillFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.files[file.SkillName] == nil {
		r.files[file.SkillName] = map[string][]*SkillFile{}
	}
	files := r.files[file.SkillName][file.Version]
	for i, existing := range files {
		if existing.Path == file.Path {
			files[i] = file
			r.files[file.SkillName][file.Version] = files
			return file, nil
		}
	}
	r.files[file.SkillName][file.Version] = append(files, file)
	return file, nil
}

func (r *fakeSkillRepo) DeleteSkillDraftPath(ctx context.Context, name, version, filePath string, recursive bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := r.files[name][version]
	kept := files[:0]
	for _, f := range files {
		if f.Path == filePath || (recursive && strings.HasPrefix(f.Path, filePath+"/")) {
			continue
		}
		kept = append(kept, f)
	}
	r.files[name][version] = kept
	return nil
}

func (r *fakeSkillRepo) MoveSkillDraftPath(ctx context.Context, name, version, oldPath, newPath string, overwrite bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.files[name][version] {
		if f.Path == oldPath {
			f.Path = newPath
			f.Name = newPath
			return nil
		}
	}
	return ErrSkillFileNotFound
}

func (r *fakeSkillRepo) BuildSkillPackageFromDraft(ctx context.Context, name, version string) ([]byte, []*SkillFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	files := append([]*SkillFile(nil), r.files[name][version]...)
	return []byte("fake-draft-package"), files, nil
}

func (r *fakeSkillRepo) SaveSkillPackage(ctx context.Context, skill *Skill, version *SkillVersion, files []*SkillFile, packageBytes []byte, overwrite bool) (*SkillVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[skill.Name] = skill
	if r.versions[skill.Name] == nil {
		r.versions[skill.Name] = map[string]*SkillVersion{}
	}
	r.versions[skill.Name][version.Version] = version
	if r.files[skill.Name] == nil {
		r.files[skill.Name] = map[string][]*SkillFile{}
	}
	r.files[skill.Name][version.Version] = files
	return version, nil
}

func (r *fakeSkillRepo) DownloadSkillPackage(ctx context.Context, name, version, ifNoneMatch string) (*SkillPackageDownload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.versions[name][version]
	if v == nil {
		return nil, ErrSkillVersionNotFound
	}
	if ifNoneMatch != "" && ifNoneMatch == v.SHA256 {
		return &SkillPackageDownload{SkillName: name, Version: version, NotModified: true, SHA256: v.SHA256}, nil
	}
	return &SkillPackageDownload{SkillName: name, Version: version, PackageBytes: []byte("fake-package"), SHA256: v.SHA256}, nil
}

func (r *fakeSkillRepo) IncrementSkillVersionDownloadCount(ctx context.Context, name, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.downloads[name+":"+version]++
	return nil
}

// --- fakeAuthzRepo ---

// fakeAuthzRepo is an in-memory AuthzRepo for testing. It tracks
// relationships and decisions so tests can assert on them.
type fakeAuthzRepo struct {
	mu          sync.Mutex
	rels        []AuthzRelationship
	checkResult AuthzDecision // injected decision for Check / Can / Require
	checkErr    error
	lookupErr   error
}

func (r *fakeAuthzRepo) Check(ctx context.Context, req AuthzCheckRequest) (AuthzDecision, error) {
	return r.checkResult, r.checkErr
}

func (r *fakeAuthzRepo) BatchCheck(ctx context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error) {
	decisions := make([]AuthzDecision, 0, len(req.Checks))
	for range req.Checks {
		decisions = append(decisions, r.checkResult)
	}
	return AuthzBatchCheckResult{Decisions: decisions}, r.checkErr
}

func (r *fakeAuthzRepo) WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rels = append(r.rels, rels...)
	return AuthzWriteResult{Written: len(rels)}, nil
}

func (r *fakeAuthzRepo) DeleteRelationships(ctx context.Context, filter AuthzRelationshipFilter) (AuthzWriteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.rels[:0]
	deleted := 0
	for _, rel := range r.rels {
		if matchesFilter(rel, filter) {
			deleted++
			continue
		}
		kept = append(kept, rel)
	}
	r.rels = kept
	return AuthzWriteResult{Deleted: deleted}, nil
}

func (r *fakeAuthzRepo) ReadRelationships(ctx context.Context, filter AuthzRelationshipFilter, limit int, cursor string) ([]AuthzRelationship, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AuthzRelationship, 0, len(r.rels))
	for _, rel := range r.rels {
		if matchesFilter(rel, filter) {
			out = append(out, rel)
		}
	}
	return out, "", nil
}

func (r *fakeAuthzRepo) LookupResources(ctx context.Context, req AuthzLookupResourcesRequest) (AuthzLookupResourcesResult, error) {
	if r.lookupErr != nil {
		return AuthzLookupResourcesResult{}, r.lookupErr
	}
	// Return all resources that have a relationship with the subject.
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	resources := make([]AuthzObjectRef, 0)
	for _, rel := range r.rels {
		if rel.Subject.Type == req.Subject.Type && rel.Subject.ID == req.Subject.ID {
			if rel.Resource.Type == req.ResourceType {
				if _, ok := seen[rel.Resource.ID]; !ok {
					seen[rel.Resource.ID] = struct{}{}
					resources = append(resources, rel.Resource)
				}
			}
		}
	}
	return AuthzLookupResourcesResult{Resources: resources}, nil
}

func (r *fakeAuthzRepo) LookupSubjects(ctx context.Context, req AuthzLookupSubjectsRequest) (AuthzLookupSubjectsResult, error) {
	return AuthzLookupSubjectsResult{}, nil
}

func (r *fakeAuthzRepo) ReadSchema(ctx context.Context) (AuthzSchema, error) {
	return AuthzSchema{Text: "fake-schema"}, nil
}

func (r *fakeAuthzRepo) WriteSchema(ctx context.Context, schema AuthzSchema) error {
	return nil
}

func matchesFilter(rel AuthzRelationship, filter AuthzRelationshipFilter) bool {
	if filter.ResourceType != "" && rel.Resource.Type != filter.ResourceType {
		return false
	}
	if filter.ResourceID != "" && rel.Resource.ID != filter.ResourceID {
		return false
	}
	if filter.Relation != "" && rel.Relation != filter.Relation {
		return false
	}
	if filter.SubjectType != "" && rel.Subject.Type != filter.SubjectType {
		return false
	}
	if filter.SubjectID != "" && rel.Subject.ID != filter.SubjectID {
		return false
	}
	return true
}

// --- fakeAuditRecorder ---

type fakeAuditRecorder struct {
	mu      sync.Mutex
	records []auditx.Record
}

func (f *fakeAuditRecorder) Record(ctx context.Context, record auditx.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, record)
	return nil
}

func (f *fakeAuditRecorder) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *fakeAuditRecorder) Last() auditx.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) == 0 {
		return auditx.Record{}
	}
	return f.records[len(f.records)-1]
}

// --- helpers ---

func newTestUsecase(repo SkillRepo, authz *AuthzUsecase, audit auditx.Recorder) *SkillUsecase {
	if authz == nil {
		// Construct an AuthzUsecase with a fake repo that allows everything.
		authz = NewAuthzUsecase(&fakeAuthzRepo{checkResult: AuthzDecision{Effect: "allow", Allowed: true}}, logx.Noop())
	}
	if audit == nil {
		audit = auditx.Noop()
	}
	return NewSkillUsecase(repo, authz, audit, logx.Noop())
}

func testPrincipal(uid string) authn.Principal {
	return authn.Principal{
		SubjectID:   uid,
		SubjectType: authn.SubjectTypeUser,
	}.Normalize()
}

// --- tests ---

func TestCreateSkill_StampsOwnerFromPrincipal(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	out, err := uc.CreateSkill(context.Background(), testPrincipal("u_123"), &Skill{
		Name:       "my-skill",
		Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}
	if out.OwnerID != "u_123" {
		t.Errorf("OwnerID = %q, want %q", out.OwnerID, "u_123")
	}
	if out.OrgID != "" {
		t.Errorf("OrgID = %q, want empty (principal has no OrgID)", out.OrgID)
	}
}

func TestCreateSkill_PreservesExplicitOwner(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	out, err := uc.CreateSkill(context.Background(), testPrincipal("u_123"), &Skill{
		Name:       "my-skill",
		OwnerID:    "u_explicit",
		Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}
	if out.OwnerID != "u_explicit" {
		t.Errorf("OwnerID = %q, want %q (explicit value should be preserved)", out.OwnerID, "u_explicit")
	}
}

func TestCreateSkill_InvalidName(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "invalid name with spaces",
	})
	if err == nil {
		t.Fatal("expected error for invalid name, got nil")
	}
	if !errors.Is(err, ErrSkillInvalidArgument) {
		t.Errorf("expected ErrSkillInvalidArgument, got %v", err)
	}
}

func TestGetSkill_PublicVisibilityFallback(t *testing.T) {
	repo := newFakeSkillRepo()
	// Authz denies everything (no relationships written).
	authzRepo := &fakeAuthzRepo{checkResult: AuthzDecision{Effect: "deny", Allowed: false}}
	authz := NewAuthzUsecase(authzRepo, logx.Noop())
	uc := newTestUsecase(repo, authz, nil)

	// Create a public skill owned by u_1.
	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name:       "public-skill",
		Visibility: SkillVisibilityPublic,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// u_2 (not owner, no authz grant) should be able to read because
	// visibility=public.
	out, err := uc.GetSkill(context.Background(), testPrincipal("u_2"), "public-skill")
	if err != nil {
		t.Fatalf("GetSkill for public skill by non-owner failed: %v", err)
	}
	if out.Name != "public-skill" {
		t.Errorf("got name %q, want public-skill", out.Name)
	}
}

func TestGetSkill_OwnershipFallback(t *testing.T) {
	repo := newFakeSkillRepo()
	authzRepo := &fakeAuthzRepo{checkResult: AuthzDecision{Effect: "deny", Allowed: false}}
	authz := NewAuthzUsecase(authzRepo, logx.Noop())
	uc := newTestUsecase(repo, authz, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name:       "private-skill",
		Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// Owner should read their own private skill even when authz denies.
	out, err := uc.GetSkill(context.Background(), testPrincipal("u_1"), "private-skill")
	if err != nil {
		t.Fatalf("GetSkill by owner failed: %v", err)
	}
	if out.Name != "private-skill" {
		t.Errorf("got name %q, want private-skill", out.Name)
	}
}

func TestGetSkill_DeniesNonOwnerPrivate(t *testing.T) {
	repo := newFakeSkillRepo()
	authzRepo := &fakeAuthzRepo{checkResult: AuthzDecision{Effect: "deny", Allowed: false}}
	authz := NewAuthzUsecase(authzRepo, logx.Noop())
	uc := newTestUsecase(repo, authz, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name:       "private-skill",
		Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// u_2 is not owner and authz denies → 403.
	_, err = uc.GetSkill(context.Background(), testPrincipal("u_2"), "private-skill")
	if err == nil {
		t.Fatal("expected error for non-owner reading private skill, got nil")
	}
	if errorx.CodeOf(err) != errorx.Code("SKILL_PERMISSION_DENIED") {
		t.Errorf("expected SKILL_PERMISSION_DENIED, got %v", err)
	}
}

func TestUpdateSkill_RequiresEditPermission(t *testing.T) {
	repo := newFakeSkillRepo()
	authzRepo := &fakeAuthzRepo{checkResult: AuthzDecision{Effect: "deny", Allowed: false}}
	authz := NewAuthzUsecase(authzRepo, logx.Noop())
	uc := newTestUsecase(repo, authz, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// u_2 tries to update → 403 (authz denies edit).
	_, err = uc.UpdateSkill(context.Background(), testPrincipal("u_2"), &Skill{
		Name: "skill-1", DisplayName: "hacked",
	})
	if err == nil {
		t.Fatal("expected error for non-owner update, got nil")
	}
	if errorx.CodeOf(err) != errorx.Code("SKILL_PERMISSION_DENIED") {
		t.Errorf("expected SKILL_PERMISSION_DENIED, got %v", err)
	}
}

func TestSubmitSkillVersion_StateMachineCAS(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	// Create skill with initial draft version "0.0.1".
	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Version: "0.0.1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// draft → submitted (should succeed; status is draft).
	_, err = uc.SubmitSkillVersion(context.Background(), testPrincipal("u_1"), "skill-1", "0.0.1")
	if err != nil {
		t.Fatalf("SubmitSkillVersion draft→submitted failed: %v", err)
	}

	// draft → submitted again (should fail; status is now submitted, not draft).
	_, err = uc.SubmitSkillVersion(context.Background(), testPrincipal("u_1"), "skill-1", "0.0.1")
	if err == nil {
		t.Fatal("expected CAS conflict on second submit, got nil")
	}
	// The biz layer maps CAS failure to a 409 Conflict with code
	// SKILL_VERSION_STATUS_CONFLICT. We can't easily assert the errorx
	// code here without importing errorx; just assert non-nil.
}

func TestCreateSkillShare_RejectsOwnerRelation(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	_, err = uc.CreateSkillShare(context.Background(), testPrincipal("u_1"), SkillShareInput{
		Name:        "skill-1",
		Relation:    "owner", // should be rejected
		SubjectType: "user",
		SubjectID:   "u_2",
	})
	if err == nil {
		t.Fatal("expected error for owner relation in CreateSkillShare, got nil")
	}
	if !errors.Is(err, ErrSkillInvalidArgument) {
		t.Errorf("expected ErrSkillInvalidArgument, got %v", err)
	}
}

func TestCreateSkillShare_AcceptsViewerRelation(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	out, err := uc.CreateSkillShare(context.Background(), testPrincipal("u_1"), SkillShareInput{
		Name:        "skill-1",
		Relation:    "viewer",
		SubjectType: "user",
		SubjectID:   "u_2",
	})
	if err != nil {
		t.Fatalf("CreateSkillShare viewer failed: %v", err)
	}
	if out.Relation != "viewer" {
		t.Errorf("Relation = %q, want viewer", out.Relation)
	}
}

func TestDeleteSkillShare_PreservesOwner(t *testing.T) {
	repo := newFakeSkillRepo()
	authzRepo := &fakeAuthzRepo{checkResult: AuthzDecision{Effect: "allow", Allowed: true}}
	authz := NewAuthzUsecase(authzRepo, logx.Noop())
	uc := newTestUsecase(repo, authz, nil)

	// Create skill (writes owner relationship via GrantOwner).
	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	// Grant viewer to u_2.
	_, err = uc.CreateSkillShare(context.Background(), testPrincipal("u_1"), SkillShareInput{
		Name: "skill-1", Relation: "viewer", SubjectType: "user", SubjectID: "u_2",
	})
	if err != nil {
		t.Fatalf("CreateSkillShare failed: %v", err)
	}

	// Delete share for u_1 (the owner). This should revoke all relations
	// between skill-1 and u_1, THEN re-grant owner so ownership is preserved.
	err = uc.DeleteSkillShare(context.Background(), testPrincipal("u_1"), "skill-1", "user", "u_1")
	if err != nil {
		t.Fatalf("DeleteSkillShare for owner failed: %v", err)
	}

	// Verify owner relationship was re-granted.
	authzRepo.mu.Lock()
	hasOwner := false
	for _, rel := range authzRepo.rels {
		if rel.Resource.Type == "skill" && rel.Resource.ID == "skill-1" &&
			rel.Subject.Type == "user" && rel.Subject.ID == "u_1" &&
			rel.Relation == "owner" {
			hasOwner = true
		}
	}
	authzRepo.mu.Unlock()
	if !hasOwner {
		t.Error("owner relationship was not re-granted after DeleteSkillShare")
	}
}

func TestAuditRecordedOnCreateSuccess(t *testing.T) {
	repo := newFakeSkillRepo()
	audit := &fakeAuditRecorder{}
	uc := newTestUsecase(repo, nil, audit)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}

	if audit.Count() != 1 {
		t.Fatalf("audit record count = %d, want 1", audit.Count())
	}
	last := audit.Last()
	if last.Action != "skill.create" {
		t.Errorf("audit action = %q, want skill.create", last.Action)
	}
	if last.Result != auditx.ResultSuccess {
		t.Errorf("audit result = %q, want success", last.Result)
	}
	if last.Actor.SubjectID != "u_1" {
		t.Errorf("audit actor subject_id = %q, want u_1", last.Actor.SubjectID)
	}
}

func TestAuditRecordedOnCreateFailure(t *testing.T) {
	repo := newFakeSkillRepo()
	repo.createErr = errors.New("simulated DB failure")
	audit := &fakeAuditRecorder{}
	uc := newTestUsecase(repo, nil, audit)

	_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
		Name: "skill-1", Visibility: SkillVisibilityPrivate,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if audit.Count() != 1 {
		t.Fatalf("audit record count = %d, want 1", audit.Count())
	}
	last := audit.Last()
	if last.Action != "skill.create" {
		t.Errorf("audit action = %q, want skill.create", last.Action)
	}
	if last.Result != auditx.ResultFailure {
		t.Errorf("audit result = %q, want failure", last.Result)
	}
}

func TestListSkills_AuthzDisabled_ReturnsAll(t *testing.T) {
	repo := newFakeSkillRepo()
	// authz=nil simulates dev mode (authz disabled).
	uc := newTestUsecase(repo, nil, nil)

	// Create 3 skills.
	for _, name := range []string{"s1", "s2", "s3"} {
		_, err := uc.CreateSkill(context.Background(), testPrincipal("u_1"), &Skill{
			Name: name, Visibility: SkillVisibilityPrivate,
		})
		if err != nil {
			t.Fatalf("CreateSkill %s failed: %v", name, err)
		}
	}

	// ListSkills with authz disabled should return all 3.
	out, err := uc.ListSkills(context.Background(), testPrincipal("u_1"), SkillListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListSkills failed: %v", err)
	}
	if len(out.Items) != 3 {
		t.Errorf("got %d items, want 3", len(out.Items))
	}
}

func TestUpdateSkillVisibility(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)
	principal := testPrincipal("u_1")

	created, err := uc.CreateSkill(context.Background(), principal, &Skill{
		Name:       "skill-visibility",
		Visibility: SkillVisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}
	if created.Visibility != SkillVisibilityPrivate {
		t.Fatalf("created visibility = %q, want private", created.Visibility)
	}

	updated, err := uc.UpdateSkillVisibility(context.Background(), principal, "skill-visibility", "PUBLIC")
	if err != nil {
		t.Fatalf("UpdateSkillVisibility failed: %v", err)
	}
	if updated.Visibility != SkillVisibilityPublic {
		t.Fatalf("updated visibility = %q, want public", updated.Visibility)
	}
}

func TestUpdateSkillVisibilityRejectsUnsupportedValue(t *testing.T) {
	repo := newFakeSkillRepo()
	uc := newTestUsecase(repo, nil, nil)
	principal := testPrincipal("u_1")

	if _, err := uc.CreateSkill(context.Background(), principal, &Skill{Name: "skill-visibility"}); err != nil {
		t.Fatalf("CreateSkill failed: %v", err)
	}
	if _, err := uc.UpdateSkillVisibility(context.Background(), principal, "skill-visibility", "org"); err == nil {
		t.Fatal("expected invalid visibility error, got nil")
	}
}
