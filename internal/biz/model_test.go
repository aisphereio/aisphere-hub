package biz

import (
	"context"
	"strings"
	"testing"

	"github.com/aisphereio/kernel/authn"
)

// --- fakes ---

type fakeModelProfileRepo struct {
	profiles map[string]*ModelProfile
}

func newFakeModelProfileRepo() *fakeModelProfileRepo {
	return &fakeModelProfileRepo{profiles: map[string]*ModelProfile{}}
}

func (r *fakeModelProfileRepo) List(_ context.Context, opts ModelProfileListOptions) ([]*ModelProfile, string, error) {
	out := make([]*ModelProfile, 0, len(r.profiles))
	for _, p := range r.profiles {
		out = append(out, p)
	}
	return out, "", nil
}
func (r *fakeModelProfileRepo) Get(_ context.Context, id, version string) (*ModelProfile, error) {
	p, ok := r.profiles[id]
	if !ok {
		return nil, ErrModelProfileNotFound
	}
	if version != "" {
		if _, ok := p.Revisions[version]; !ok {
			return nil, ErrModelProfileNotFound
		}
	}
	return p, nil
}
func (r *fakeModelProfileRepo) Create(_ context.Context, p *ModelProfile) (*ModelProfile, error) {
	if _, exists := r.profiles[p.ID]; exists {
		return nil, ErrModelProfileAlreadyExists
	}
	cp := *p
	r.profiles[p.ID] = &cp
	return &cp, nil
}
func (r *fakeModelProfileRepo) Update(_ context.Context, p *ModelProfile) (*ModelProfile, error) {
	if _, ok := r.profiles[p.ID]; !ok {
		return nil, ErrModelProfileNotFound
	}
	cp := *p
	r.profiles[p.ID] = &cp
	return &cp, nil
}
func (r *fakeModelProfileRepo) Delete(_ context.Context, id string) error {
	delete(r.profiles, id)
	return nil
}

// fakeModelProfileRels records grants and answers Check via a per-
// (resource,permission) allow map. Default-deny.
type fakeModelProfileRels struct {
	ownerGrants  []AuthzObjectRef
	parentGrants []AuthzObjectRef
	revokes      []AuthzObjectRef
	allow        map[string]bool // key = "{type}:{id}|{permission}"
	grantOwnerErr error
}

func (r *fakeModelProfileRels) allowKey(res AuthzObjectRef, perm string) string {
	return res.Type + ":" + res.ID + "|" + perm
}

func (r *fakeModelProfileRels) Check(_ context.Context, req AuthzCheckRequest) (AuthzDecision, error) {
	allowed := r.allow[r.allowKey(req.Resource, req.Permission)]
	return AuthzDecision{Allowed: allowed, Effect: allowEffect(allowed)}, nil
}

func allowEffect(allowed bool) string {
	if allowed {
		return "allow"
	}
	return "deny"
}

func (r *fakeModelProfileRels) BatchCheck(_ context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error) {
	decisions := make([]AuthzDecision, 0, len(req.Checks))
	for _, c := range req.Checks {
		allowed := r.allow[r.allowKey(c.Resource, c.Permission)]
		decisions = append(decisions, AuthzDecision{Allowed: allowed, Effect: allowEffect(allowed)})
	}
	return AuthzBatchCheckResult{Decisions: decisions}, nil
}

func (r *fakeModelProfileRels) GrantOwner(_ context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error {
	r.ownerGrants = append(r.ownerGrants, resource)
	return r.grantOwnerErr
}
func (r *fakeModelProfileRels) GrantParent(_ context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error {
	r.parentGrants = append(r.parentGrants, resource)
	return nil
}
func (r *fakeModelProfileRels) RevokeResource(_ context.Context, resource AuthzObjectRef) error {
	r.revokes = append(r.revokes, resource)
	return nil
}

// --- helpers ---

func modelPrincipal() authn.Principal {
	return authn.Principal{
		SubjectID:   "u1",
		SubjectType: "user",
		Name:        "Alice",
		OrgID:       "org-1",
		ProjectID:   "proj-1",
	}
}

func validModelProfileInput() *ModelProfile {
	return &ModelProfile{
		ID:            "coding-default",
		Provider:      "openai",
		APIFormat:     "openai_responses",
		Endpoint:      "https://api.openai.com",
		UpstreamModel: "gpt-4o",
		ProjectID:     "proj-1",
	}
}

// --- tests ---

func TestValidModelProfileID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"coding-default", true},
		{"openai-prod", true},
		{"a", true},
		{"", false},
		{"-leading", false},
		{"trailing-", false},
		{"has space", false},
		{"has.dot", false},
		{"UPPER", false},
		{"has/slash", false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := validModelProfileID(c.id); got != c.want {
				t.Errorf("validModelProfileID(%q): got %v, want %v", c.id, got, c.want)
			}
		})
	}
}

func TestCreateModelProfile_RequiresProjectEdit(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{}} // no edit granted
	uc := NewModelProfileUsecase(repo, rels)
	p := validModelProfileInput()

	_, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), p)
	if err == nil {
		t.Fatal("expected forbidden error when project edit is not granted")
	}
	if !strings.Contains(err.Error(), "forbidden") && !strings.Contains(err.Error(), "PERMISSION_DENIED") {
		t.Errorf("expected permission-denied error, got: %v", err)
	}
	if len(repo.profiles) != 0 {
		t.Errorf("expected no profile persisted on authz denial, got %d", len(repo.profiles))
	}
	if len(rels.ownerGrants) != 0 {
		t.Errorf("expected no owner grant on authz denial, got %d", len(rels.ownerGrants))
	}
}

func TestCreateModelProfile_PersistsAndGrantsOwnerParent(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills": true, // creator can edit the parent project
	}}
	uc := NewModelProfileUsecase(repo, rels)
	p := validModelProfileInput()

	out, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.Version != "v1" {
		t.Errorf("version: got %q, want v1", out.Version)
	}
	if out.Object != "model_profile:coding-default" {
		t.Errorf("object: got %q", out.Object)
	}
	if out.Model != "aisphere://coding-default" {
		t.Errorf("logical model: got %q", out.Model)
	}
	if out.Status != "active" {
		t.Errorf("status: got %q, want active", out.Status)
	}
	if out.OwnerID != "u1" || out.OwnerType != "user" {
		t.Errorf("owner: got %s:%s, want user:u1", out.OwnerType, out.OwnerID)
	}
	if out.ProjectID != "proj-1" || out.OrgID != "org-1" {
		t.Errorf("scope: project=%q org=%q", out.ProjectID, out.OrgID)
	}
	if len(rels.ownerGrants) != 1 || rels.ownerGrants[0].ID != "coding-default" {
		t.Errorf("owner grant: got %+v", rels.ownerGrants)
	}
	if len(rels.parentGrants) != 1 || rels.parentGrants[0].ID != "coding-default" {
		t.Errorf("parent grant: got %+v", rels.parentGrants)
	}
	// revision immutable record + sha256 populated
	rev, ok := out.Revisions["v1"]
	if !ok {
		t.Fatal("v1 revision not recorded")
	}
	if rev.SHA256 == "" {
		t.Error("revision sha256 is empty")
	}
	if rev.Author != "Alice" {
		t.Errorf("revision author: got %q, want Alice", rev.Author)
	}
}

func TestCreateModelProfile_OrgIDFallbackWhenNoProject(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills": true, // edit via org-id fallback
	}}
	uc := NewModelProfileUsecase(repo, rels)
	p := validModelProfileInput()
	p.ProjectID = "" // no explicit project; principal has ProjectID though

	principal := modelPrincipal()
	principal.ProjectID = "" // force org-id fallback
	out, err := uc.CreateModelProfile(context.Background(), principal, p)
	if err != nil {
		t.Fatalf("create with org fallback: %v", err)
	}
	if out.ProjectID != "org-1" {
		t.Errorf("expected project_id fallback to org-1, got %q", out.ProjectID)
	}
}

func TestCreateModelProfile_RollbackOwnerGrantFailure(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{
		allow:         map[string]bool{"zone:org-1|manage_skills": true},
		grantOwnerErr: errFakeGrant,
	}
	uc := NewModelProfileUsecase(repo, rels)
	_, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), validModelProfileInput())
	if err == nil {
		t.Fatal("expected error when GrantOwner fails")
	}
	if len(repo.profiles) != 0 {
		t.Errorf("expected row soft-deleted on grant failure, got %d profiles", len(repo.profiles))
	}
}

func TestResolveModelProfile_SnapshotNoPlaintextCredential(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills":            true,
		"model_profile:openai-prod|view": true,
		"model_profile:openai-prod|execute": true,
	}}
	uc := NewModelProfileUsecase(repo, rels)
	created, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), &ModelProfile{
		ID:            "openai-prod",
		Provider:      "openai",
		APIFormat:     "openai_chat_completions",
		Endpoint:      "https://api.openai.com",
		UpstreamModel: "gpt-4o",
		SecretRef:     "secret://model/openai-prod",
		AllowedTools:  []string{"workspace.read"},
		Limits:        ModelProfileLimits{MaxInputTokens: 128000, MaxOutputTokens: 16384},
		ProjectID:     "proj-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	snap, err := uc.ResolveModelProfile(context.Background(), modelPrincipal(), created.ID, "", "rt-1", "sess-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// logical_name format: aisphere://{id}@{revision}
	want := "aisphere://openai-prod@v1"
	if snap.LogicalName != want {
		t.Errorf("logical_name: got %q, want %q", snap.LogicalName, want)
	}
	// credential_ref is a ref, never a plain-text key
	if snap.CredentialRef != "secret://model/openai-prod" {
		t.Errorf("credential_ref: got %q", snap.CredentialRef)
	}
	if strings.Contains(snap.CredentialRef, "sk-") {
		t.Error("snapshot leaked a plain-text key")
	}
	if snap.Protocol != "openai_chat_completions" {
		t.Errorf("protocol: got %q", snap.Protocol)
	}
	if snap.BaseURL != "https://api.openai.com" {
		t.Errorf("base_url: got %q", snap.BaseURL)
	}
	if snap.Limits.ContextWindow != 128000 || snap.Limits.MaxOutputTokens != 16384 {
		t.Errorf("limits: got %+v", snap.Limits)
	}
	if !snap.Capabilities.Tools {
		t.Error("capabilities.tools should be true when allowed_tools non-empty")
	}
	if !snap.Capabilities.Streaming {
		t.Error("capabilities.streaming should default true")
	}
	if snap.SHA256 == "" {
		t.Error("snapshot sha256 is empty")
	}
}

func TestResolveModelProfile_DeniesWithoutExecute(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills":               true,
		"model_profile:openai-prod|view":    true,
		"model_profile:openai-prod|execute": false, // explicitly denied
	}}
	uc := NewModelProfileUsecase(repo, rels)
	created, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), &ModelProfile{
		ID: "openai-prod", Provider: "openai", APIFormat: "openai_responses",
		Endpoint: "https://api.openai.com", UpstreamModel: "gpt-4o", ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := uc.ResolveModelProfile(context.Background(), modelPrincipal(), created.ID, "", "rt-1", "sess-1"); err == nil {
		t.Fatal("expected forbidden when execute not granted")
	}
}

func TestUpdateModelProfile_AdvancesRevision(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills":                  true,
		"model_profile:codedef|edit":           true,
		"model_profile:codedef|view":           true,
	}}
	uc := NewModelProfileUsecase(repo, rels)
	created, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), &ModelProfile{
		ID: "codedef", Provider: "openai", APIFormat: "openai_responses",
		Endpoint: "https://api.openai.com", UpstreamModel: "gpt-4o", ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = created

	// Update with a new endpoint; do NOT pass Version → biz bumps v1 → v2.
	updated, err := uc.UpdateModelProfile(context.Background(), modelPrincipal(), &ModelProfile{
		ID: "codedef", Provider: "openai", APIFormat: "openai_responses",
		Endpoint: "https://gateway.example.com", UpstreamModel: "gpt-4o",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != "v2" {
		t.Errorf("version: got %q, want v2", updated.Version)
	}
	if updated.Endpoint != "https://gateway.example.com" {
		t.Errorf("endpoint: got %q", updated.Endpoint)
	}
	// Both revisions present; v2 is the new latest.
	if _, ok := updated.Revisions["v1"]; !ok {
		t.Error("v1 revision lost after update")
	}
	if _, ok := updated.Revisions["v2"]; !ok {
		t.Error("v2 revision not created")
	}
	v2 := updated.Revisions["v2"]
	if v2.SHA256 == "" {
		t.Error("v2 sha256 empty")
	}
	if v2.Endpoint != "https://gateway.example.com" {
		t.Errorf("v2 endpoint: got %q", v2.Endpoint)
	}
	// v1 sha256 must differ from v2 (different endpoint).
	v1 := updated.Revisions["v1"]
	if v1.SHA256 == v2.SHA256 {
		t.Error("v1 and v2 share a sha256 despite different endpoints")
	}
}

func TestNextRevisionLabel(t *testing.T) {
	cases := []struct {
		latest, want string
	}{
		{"", "v1"},
		{"v1", "v2"},
		{"v9", "v10"},
		{"custom-label", "custom-label-r2"},
	}
	for _, c := range cases {
		if got := nextRevisionLabel(c.latest); got != c.want {
			t.Errorf("nextRevisionLabel(%q): got %q, want %q", c.latest, got, c.want)
		}
	}
}

func TestDeleteModelProfile_RevokesResource(t *testing.T) {
	repo := newFakeModelProfileRepo()
	rels := &fakeModelProfileRels{allow: map[string]bool{
		"zone:org-1|manage_skills":                 true,
		"model_profile:codedef|manage":        true,
		"model_profile:codedef|view":          true,
	}}
	uc := NewModelProfileUsecase(repo, rels)
	created, err := uc.CreateModelProfile(context.Background(), modelPrincipal(), &ModelProfile{
		ID: "codedef", Provider: "openai", APIFormat: "openai_responses",
		Endpoint: "https://api.openai.com", UpstreamModel: "gpt-4o", ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := uc.DeleteModelProfile(context.Background(), modelPrincipal(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(rels.revokes) != 1 || rels.revokes[0].ID != "codedef" {
		t.Errorf("expected one revoke on codedef, got %+v", rels.revokes)
	}
}

func TestTestModelProfile_Unavailable(t *testing.T) {
	uc := NewModelProfileUsecase(newFakeModelProfileRepo(), &fakeModelProfileRels{allow: map[string]bool{}})
	_, err := uc.TestModelProfile(context.Background(), modelPrincipal(), "any", "ping")
	if err == nil {
		t.Fatal("expected Unavailable from TestModelProfile stub")
	}
	if !strings.Contains(err.Error(), "not yet available") {
		t.Errorf("expected unavailable message, got: %v", err)
	}
}

// errFakeGrant is a sentinel used to force GrantOwner to fail in rollback tests.
var errFakeGrant = sentinelErr("fake grant failure")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
