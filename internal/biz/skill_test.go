package biz

import (
	"context"
	"errors"
	"testing"

	"github.com/aisphereio/kernel/authn"
)

func TestSkillUsecaseCreateActivatesRepositoryAndOwner(t *testing.T) {
	skills := newMemoryGitSkillRepo()
	git := &fakeSkillGitEngine{}
	rels := &fakeSkillRelationships{}
	uc := NewSkillUsecase(skills, newMemoryPullRequestRepo(), git, rels)
	principal := authn.Principal{SubjectID: "owner-1", SubjectType: authn.SubjectTypeUser, OrgID: "org-1"}

	created, err := uc.CreateSkill(context.Background(), principal, &GitSkill{Name: "search", OrgID: "org-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != SkillStatusActive || created.DefaultBranch != SkillDefaultBranch {
		t.Fatalf("created skill = status %q branch %q", created.Status, created.DefaultBranch)
	}
	if !git.created["search"] {
		t.Fatal("repository was not created")
	}
	if rels.ownerResource.ID != "search" || rels.ownerSubject.ID != "owner-1" {
		t.Fatalf("owner projection = resource %+v subject %+v", rels.ownerResource, rels.ownerSubject)
	}
}

func TestSkillUsecaseCreateCompensatesAfterOwnerProjectionFailure(t *testing.T) {
	skills := newMemoryGitSkillRepo()
	git := &fakeSkillGitEngine{}
	rels := &fakeSkillRelationships{grantOwnerErr: errors.New("iam unavailable")}
	uc := NewSkillUsecase(skills, newMemoryPullRequestRepo(), git, rels)

	_, err := uc.CreateSkill(context.Background(), authn.Principal{SubjectID: "owner-1", SubjectType: authn.SubjectTypeUser, OrgID: "org-1"}, &GitSkill{Name: "search", OrgID: "org-1", ProjectID: "project-1"})
	if err == nil {
		t.Fatal("expected IAM failure")
	}
	if !git.deleted["search"] {
		t.Fatal("repository compensation was not executed")
	}
	if _, ok := skills.items["search"]; ok {
		t.Fatal("metadata compensation was not executed")
	}
}

func TestSkillUsecaseCreateRequiresProject(t *testing.T) {
	uc := NewSkillUsecase(newMemoryGitSkillRepo(), newMemoryPullRequestRepo(), &fakeSkillGitEngine{}, &fakeSkillRelationships{})
	_, err := uc.CreateSkill(context.Background(), authn.Principal{SubjectID: "owner-1", SubjectType: authn.SubjectTypeUser}, &GitSkill{Name: "search"})
	if !errors.Is(err, ErrSkillInvalidArgument) {
		t.Fatalf("CreateSkill() error = %v, want ErrSkillInvalidArgument", err)
	}
}

func TestSkillUsecaseCreateRejectsPrincipalOrgMismatch(t *testing.T) {
	uc := NewSkillUsecase(newMemoryGitSkillRepo(), newMemoryPullRequestRepo(), &fakeSkillGitEngine{}, &fakeSkillRelationships{})
	_, err := uc.CreateSkill(context.Background(), authn.Principal{SubjectID: "owner-1", SubjectType: authn.SubjectTypeUser, OrgID: "org-1"}, &GitSkill{Name: "search", OrgID: "org-2", ProjectID: "project-1"})
	if !errors.Is(err, ErrSkillInvalidArgument) {
		t.Fatalf("CreateSkill() error = %v, want ErrSkillInvalidArgument", err)
	}
}

func TestPullRequestMergeRequiresApprovalAndFreshTarget(t *testing.T) {
	skills := newMemoryGitSkillRepo()
	_, _ = skills.CreateSkill(context.Background(), &GitSkill{Name: "search", Status: SkillStatusActive})
	pulls := newMemoryPullRequestRepo()
	git := &fakeSkillGitEngine{refs: map[string]string{
		"search:refs/heads/feature": "source-1",
		"search:refs/heads/main":    "main-1",
	}}
	uc := NewSkillUsecase(skills, pulls, git, &fakeSkillRelationships{})
	editor := authn.Principal{SubjectID: "editor-1", SubjectType: authn.SubjectTypeUser}

	pr, err := uc.CreatePullRequest(context.Background(), editor, &SkillPullRequest{SkillName: "search", SourceRef: "feature", Title: "Improve search"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.SourceSHA != "source-1" || pr.TargetSHA != "main-1" || pr.TargetRef != "refs/heads/main" {
		t.Fatalf("PR snapshot = %+v", pr)
	}
	if _, err := uc.MergePullRequest(context.Background(), editor, "search", pr.ID, "main-1"); !errors.Is(err, ErrPullRequestNotApproved) {
		t.Fatalf("unapproved merge error = %v", err)
	}
	if _, err := uc.ReviewPullRequest(context.Background(), authn.Principal{SubjectID: "reviewer-1", SubjectType: authn.SubjectTypeUser}, &SkillPullRequestReview{PullRequestID: pr.ID, Verdict: ReviewVerdictApprove}); err != nil {
		t.Fatal(err)
	}

	git.refs["search:refs/heads/main"] = "main-2"
	if _, err := uc.MergePullRequest(context.Background(), editor, "search", pr.ID, "main-1"); !errors.Is(err, ErrPullRequestStale) {
		t.Fatalf("stale merge error = %v", err)
	}
	git.refs["search:refs/heads/main"] = "main-1"
	merged, err := uc.MergePullRequest(context.Background(), editor, "search", pr.ID, "main-1")
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != PullRequestStateMerged || merged.MergedSHA != "merge-1" {
		t.Fatalf("merged PR = %+v", merged)
	}
}

type memoryGitSkillRepo struct{ items map[string]*GitSkill }

func newMemoryGitSkillRepo() *memoryGitSkillRepo {
	return &memoryGitSkillRepo{items: map[string]*GitSkill{}}
}
func (r *memoryGitSkillRepo) CreateSkill(_ context.Context, in *GitSkill) (*GitSkill, error) {
	if _, ok := r.items[in.Name]; ok {
		return nil, ErrSkillAlreadyExists
	}
	out := *in
	if out.DefaultBranch == "" {
		out.DefaultBranch = SkillDefaultBranch
	}
	if out.Status == "" {
		out.Status = SkillStatusProvisioning
	}
	r.items[out.Name] = &out
	copy := out
	return &copy, nil
}
func (r *memoryGitSkillRepo) GetSkill(_ context.Context, name string) (*GitSkill, error) {
	in, ok := r.items[name]
	if !ok {
		return nil, ErrSkillNotFound
	}
	out := *in
	return &out, nil
}
func (r *memoryGitSkillRepo) ListSkills(context.Context, GitSkillListOptions) (*GitSkillListResult, error) {
	return &GitSkillListResult{}, nil
}
func (r *memoryGitSkillRepo) UpdateSkill(_ context.Context, in *GitSkill) (*GitSkill, error) {
	r.items[in.Name] = in
	return in, nil
}
func (r *memoryGitSkillRepo) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*GitSkill, error) {
	item, err := r.GetSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	item.Visibility = visibility
	r.items[name] = item
	return item, nil
}
func (r *memoryGitSkillRepo) UpdateSkillStatus(ctx context.Context, name, expected, next string) (*GitSkill, error) {
	item, err := r.GetSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	if expected != "" && item.Status != expected {
		return nil, ErrSkillNotFound
	}
	item.Status = next
	r.items[name] = item
	return item, nil
}
func (r *memoryGitSkillRepo) DeleteSkill(_ context.Context, name string) error {
	delete(r.items, name)
	return nil
}

type memoryPullRequestRepo struct {
	items   map[string]*SkillPullRequest
	reviews map[string][]*SkillPullRequestReview
	next    int
}

func newMemoryPullRequestRepo() *memoryPullRequestRepo {
	return &memoryPullRequestRepo{items: map[string]*SkillPullRequest{}, reviews: map[string][]*SkillPullRequestReview{}}
}
func (r *memoryPullRequestRepo) CreatePullRequest(_ context.Context, in *SkillPullRequest) (*SkillPullRequest, error) {
	r.next++
	out := *in
	out.ID = string(rune('0' + r.next))
	if out.State == "" {
		out.State = PullRequestStateOpen
	}
	r.items[out.ID] = &out
	return &out, nil
}
func (r *memoryPullRequestRepo) GetPullRequest(_ context.Context, skill, id string) (*SkillPullRequest, error) {
	in, ok := r.items[id]
	if !ok || in.SkillName != skill {
		return nil, ErrPullRequestNotFound
	}
	out := *in
	return &out, nil
}
func (r *memoryPullRequestRepo) ListPullRequests(context.Context, string, PullRequestListOptions) (*PullRequestListResult, error) {
	return &PullRequestListResult{}, nil
}
func (r *memoryPullRequestRepo) CreateReview(_ context.Context, in *SkillPullRequestReview) (*SkillPullRequestReview, error) {
	out := *in
	out.ID = "review-1"
	r.reviews[in.PullRequestID] = append(r.reviews[in.PullRequestID], &out)
	return &out, nil
}
func (r *memoryPullRequestRepo) ListReviews(_ context.Context, id string) ([]*SkillPullRequestReview, error) {
	return r.reviews[id], nil
}
func (r *memoryPullRequestRepo) ClosePullRequest(_ context.Context, skill, id string) (*SkillPullRequest, error) {
	item, err := r.GetPullRequest(context.Background(), skill, id)
	if err != nil {
		return nil, err
	}
	item.State = PullRequestStateClosed
	r.items[id] = item
	return item, nil
}
func (r *memoryPullRequestRepo) MergePullRequest(_ context.Context, skill, id, expected, merged, actor string) (*SkillPullRequest, error) {
	item, err := r.GetPullRequest(context.Background(), skill, id)
	if err != nil {
		return nil, err
	}
	if item.State != PullRequestStateOpen {
		return nil, ErrPullRequestNotOpen
	}
	if item.TargetSHA != expected {
		return nil, ErrPullRequestStale
	}
	item.State = PullRequestStateMerged
	item.MergedSHA = merged
	item.MergedBy = actor
	r.items[id] = item
	return item, nil
}

type fakeSkillGitEngine struct {
	created map[string]bool
	deleted map[string]bool
	refs    map[string]string
}

func (e *fakeSkillGitEngine) CreateRepository(_ context.Context, name string) error {
	if e.created == nil {
		e.created = map[string]bool{}
	}
	e.created[name] = true
	return nil
}
func (e *fakeSkillGitEngine) DeleteRepository(_ context.Context, name string) error {
	if e.deleted == nil {
		e.deleted = map[string]bool{}
	}
	e.deleted[name] = true
	return nil
}
func (e *fakeSkillGitEngine) ResolveRef(_ context.Context, skill, ref string) (string, error) {
	return e.refs[skill+":"+ref], nil
}
func (e *fakeSkillGitEngine) Merge(_ context.Context, skill, source, target, expected string) (string, error) {
	return "merge-1", nil
}
func (e *fakeSkillGitEngine) ListReleases(context.Context, string) ([]SkillRelease, error) {
	return nil, nil
}

type fakeSkillRelationships struct {
	ownerResource AuthzObjectRef
	ownerSubject  AuthzSubjectRef
	grantOwnerErr error
}

func (r *fakeSkillRelationships) GrantOwner(_ context.Context, resource AuthzObjectRef, subject AuthzSubjectRef) error {
	r.ownerResource, r.ownerSubject = resource, subject
	return r.grantOwnerErr
}
func (r *fakeSkillRelationships) GrantRole(context.Context, AuthzObjectRef, string, AuthzSubjectRef) error {
	return nil
}
func (r *fakeSkillRelationships) RevokeAll(context.Context, AuthzObjectRef, AuthzSubjectRef) error {
	return nil
}
func (r *fakeSkillRelationships) RevokeResource(context.Context, AuthzObjectRef) error { return nil }
func (r *fakeSkillRelationships) ReadRelationships(context.Context, AuthzRelationshipFilter, int, string) ([]AuthzRelationship, string, error) {
	return nil, "", nil
}
