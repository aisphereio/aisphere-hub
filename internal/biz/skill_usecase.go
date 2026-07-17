package biz

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aisphereio/kernel/authn"
)

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type SkillGitEngine interface {
	CreateRepository(context.Context, string) error
	DeleteRepository(context.Context, string) error
	ResolveRef(context.Context, string, string) (string, error)
	Merge(context.Context, string, string, string, string) (string, error)
	ListReleases(context.Context, string) ([]SkillRelease, error)
}

type SkillRelationships interface {
	GrantOwner(context.Context, AuthzObjectRef, AuthzSubjectRef) error
	GrantRole(context.Context, AuthzObjectRef, string, AuthzSubjectRef) error
	RevokeAll(context.Context, AuthzObjectRef, AuthzSubjectRef) error
	RevokeResource(context.Context, AuthzObjectRef) error
	ReadRelationships(context.Context, AuthzRelationshipFilter, int, string) ([]AuthzRelationship, string, error)
}

type SkillUsecase struct {
	skills GitSkillRepository
	pulls  PullRequestRepository
	git    SkillGitEngine
	rels   SkillRelationships
}

func NewSkillUsecase(skills GitSkillRepository, pulls PullRequestRepository, git SkillGitEngine, rels SkillRelationships) *SkillUsecase {
	return &SkillUsecase{skills: skills, pulls: pulls, git: git, rels: rels}
}

func (uc *SkillUsecase) CreateSkill(ctx context.Context, principal authn.Principal, in *GitSkill) (*GitSkill, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	if in == nil || !skillNamePattern.MatchString(strings.TrimSpace(in.Name)) {
		return nil, ErrSkillInvalidArgument
	}
	if uc.skills == nil || uc.git == nil || uc.rels == nil {
		return nil, ErrSkillDependencyFailed
	}
	item := *in
	item.Name = strings.TrimSpace(item.Name)
	item.OwnerID = principal.SubjectID
	item.OrgID = principal.OrgID
	item.DefaultBranch = SkillDefaultBranch
	item.Status = SkillStatusProvisioning
	if item.Visibility == "" {
		item.Visibility = SkillVisibilityPrivate
	}
	created, err := uc.skills.CreateSkill(ctx, &item)
	if err != nil {
		return nil, err
	}
	if err := uc.git.CreateRepository(ctx, item.Name); err != nil {
		_ = uc.skills.DeleteSkill(context.WithoutCancel(ctx), item.Name)
		return nil, fmt.Errorf("%w: create repository: %v", ErrSkillDependencyFailed, err)
	}
	resource := AuthzObjectRef{Type: "skill", ID: item.Name}
	if err := uc.rels.GrantOwner(ctx, resource, principalSubject(principal)); err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.git.DeleteRepository(compensateCtx, item.Name)
		_ = uc.skills.DeleteSkill(compensateCtx, item.Name)
		return nil, fmt.Errorf("%w: project owner: %v", ErrSkillDependencyFailed, err)
	}
	active, err := uc.skills.UpdateSkillStatus(ctx, created.Name, SkillStatusProvisioning, SkillStatusActive)
	if err != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = uc.rels.RevokeResource(compensateCtx, resource)
		_ = uc.git.DeleteRepository(compensateCtx, item.Name)
		_ = uc.skills.DeleteSkill(compensateCtx, item.Name)
		return nil, err
	}
	return active, nil
}

func (uc *SkillUsecase) GetSkill(ctx context.Context, name string) (*GitSkill, error) {
	return uc.skills.GetSkill(ctx, strings.TrimSpace(name))
}

func (uc *SkillUsecase) ListSkills(ctx context.Context, opts GitSkillListOptions) (*GitSkillListResult, error) {
	return uc.skills.ListSkills(ctx, opts)
}

func (uc *SkillUsecase) UpdateSkill(ctx context.Context, in *GitSkill) (*GitSkill, error) {
	if in == nil || !skillNamePattern.MatchString(strings.TrimSpace(in.Name)) {
		return nil, ErrSkillInvalidArgument
	}
	return uc.skills.UpdateSkill(ctx, in)
}

func (uc *SkillUsecase) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*GitSkill, error) {
	if visibility != SkillVisibilityPrivate && visibility != SkillVisibilityInternal && visibility != SkillVisibilityPublic {
		return nil, ErrSkillInvalidArgument
	}
	current, err := uc.skills.GetSkill(ctx, name)
	if err != nil || current.Visibility == visibility {
		return current, err
	}
	resource := AuthzObjectRef{Type: "skill", ID: name}
	wildcards := []AuthzSubjectRef{{Type: "user", ID: "*"}, {Type: "service", ID: "*"}, {Type: "service_account", ID: "*"}}
	if visibility == SkillVisibilityPublic {
		for i, subject := range wildcards {
			if err := uc.rels.GrantRole(ctx, resource, "viewer", subject); err != nil {
				for _, granted := range wildcards[:i] {
					_ = uc.rels.RevokeAll(context.WithoutCancel(ctx), resource, granted)
				}
				return nil, err
			}
		}
	} else if current.Visibility == SkillVisibilityPublic {
		for _, subject := range wildcards {
			if err := uc.rels.RevokeAll(ctx, resource, subject); err != nil {
				return nil, err
			}
		}
	}
	updated, err := uc.skills.UpdateSkillVisibility(ctx, name, visibility)
	if err != nil && visibility == SkillVisibilityPublic {
		for _, subject := range wildcards {
			_ = uc.rels.RevokeAll(context.WithoutCancel(ctx), resource, subject)
		}
	}
	return updated, err
}

func (uc *SkillUsecase) DeleteSkill(ctx context.Context, name string) error {
	if _, err := uc.skills.UpdateSkillStatus(ctx, name, SkillStatusActive, SkillStatusDeleting); err != nil {
		return err
	}
	if err := uc.git.DeleteRepository(ctx, name); err != nil {
		return err
	}
	if err := uc.rels.RevokeResource(ctx, AuthzObjectRef{Type: "skill", ID: name}); err != nil {
		return err
	}
	return uc.skills.DeleteSkill(ctx, name)
}

func (uc *SkillUsecase) ListSkillShares(ctx context.Context, name string) ([]SkillShare, error) {
	rels, _, err := uc.rels.ReadRelationships(ctx, AuthzRelationshipFilter{ResourceType: "skill", ResourceID: name}, 1000, "")
	if err != nil {
		return nil, err
	}
	out := make([]SkillShare, 0, len(rels))
	for _, rel := range rels {
		out = append(out, SkillShare{SkillName: name, Relation: rel.Relation, SubjectType: rel.Subject.Type, SubjectID: rel.Subject.ID, SubjectRelation: rel.Subject.Relation})
	}
	return out, nil
}

func (uc *SkillUsecase) CreateSkillShare(ctx context.Context, share SkillShare) (*SkillShare, error) {
	if !validSkillRelation(share.Relation) || strings.TrimSpace(share.SubjectType) == "" || strings.TrimSpace(share.SubjectID) == "" {
		return nil, ErrSkillInvalidArgument
	}
	subject := AuthzSubjectRef{Type: share.SubjectType, ID: share.SubjectID, Relation: share.SubjectRelation}
	if err := uc.rels.GrantRole(ctx, AuthzObjectRef{Type: "skill", ID: share.SkillName}, share.Relation, subject); err != nil {
		return nil, err
	}
	copy := share
	return &copy, nil
}

func (uc *SkillUsecase) DeleteSkillShare(ctx context.Context, share SkillShare) error {
	return uc.rels.RevokeAll(ctx, AuthzObjectRef{Type: "skill", ID: share.SkillName}, AuthzSubjectRef{Type: share.SubjectType, ID: share.SubjectID, Relation: share.SubjectRelation})
}

func (uc *SkillUsecase) CreatePullRequest(ctx context.Context, principal authn.Principal, in *SkillPullRequest) (*SkillPullRequest, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	if in == nil || strings.TrimSpace(in.SkillName) == "" || strings.TrimSpace(in.SourceRef) == "" || strings.TrimSpace(in.Title) == "" {
		return nil, ErrSkillInvalidArgument
	}
	item := *in
	item.SourceRef = normalizeBranchRef(item.SourceRef)
	item.TargetRef = "refs/heads/" + SkillDefaultBranch
	var err error
	item.SourceSHA, err = uc.git.ResolveRef(ctx, item.SkillName, item.SourceRef)
	if err != nil || item.SourceSHA == "" {
		return nil, ErrSkillInvalidArgument
	}
	item.TargetSHA, err = uc.git.ResolveRef(ctx, item.SkillName, item.TargetRef)
	if err != nil || item.TargetSHA == "" {
		return nil, ErrSkillInvalidArgument
	}
	item.AuthorID = principal.SubjectID
	item.State = PullRequestStateOpen
	return uc.pulls.CreatePullRequest(ctx, &item)
}

func (uc *SkillUsecase) GetPullRequest(ctx context.Context, skill, id string) (*SkillPullRequest, []*SkillPullRequestReview, error) {
	pr, err := uc.pulls.GetPullRequest(ctx, skill, id)
	if err != nil {
		return nil, nil, err
	}
	reviews, err := uc.pulls.ListReviews(ctx, id)
	return pr, reviews, err
}

func (uc *SkillUsecase) ListPullRequests(ctx context.Context, skill string, opts PullRequestListOptions) (*PullRequestListResult, error) {
	return uc.pulls.ListPullRequests(ctx, skill, opts)
}

func (uc *SkillUsecase) ReviewPullRequest(ctx context.Context, principal authn.Principal, in *SkillPullRequestReview) (*SkillPullRequestReview, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	if in == nil || (in.Verdict != ReviewVerdictApprove && in.Verdict != ReviewVerdictRequestChanges) {
		return nil, ErrSkillInvalidArgument
	}
	copy := *in
	copy.ReviewerID = principal.SubjectID
	return uc.pulls.CreateReview(ctx, &copy)
}

func (uc *SkillUsecase) ClosePullRequest(ctx context.Context, skill, id string) (*SkillPullRequest, error) {
	return uc.pulls.ClosePullRequest(ctx, skill, id)
}

func (uc *SkillUsecase) MergePullRequest(ctx context.Context, principal authn.Principal, skill, id, expectedTargetSHA string) (*SkillPullRequest, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	pr, err := uc.pulls.GetPullRequest(ctx, skill, id)
	if err != nil {
		return nil, err
	}
	if pr.State != PullRequestStateOpen {
		return nil, ErrPullRequestNotOpen
	}
	reviews, err := uc.pulls.ListReviews(ctx, id)
	if err != nil {
		return nil, err
	}
	approved := false
	for _, review := range reviews {
		if review.Verdict == ReviewVerdictRequestChanges {
			return nil, ErrPullRequestNotApproved
		}
		approved = approved || review.Verdict == ReviewVerdictApprove
	}
	if !approved {
		return nil, ErrPullRequestNotApproved
	}
	currentTarget, err := uc.git.ResolveRef(ctx, skill, pr.TargetRef)
	if err != nil {
		return nil, err
	}
	if currentTarget != pr.TargetSHA || currentTarget != expectedTargetSHA {
		return nil, ErrPullRequestStale
	}
	currentSource, err := uc.git.ResolveRef(ctx, skill, pr.SourceRef)
	if err != nil {
		return nil, err
	}
	if currentSource != pr.SourceSHA {
		return nil, ErrPullRequestStale
	}
	mergedSHA, err := uc.git.Merge(ctx, skill, pr.SourceRef, pr.TargetRef, expectedTargetSHA)
	if err != nil {
		return nil, err
	}
	return uc.pulls.MergePullRequest(ctx, skill, id, expectedTargetSHA, mergedSHA, principal.SubjectID)
}

func (uc *SkillUsecase) ListReleases(ctx context.Context, skill string) ([]SkillRelease, error) {
	return uc.git.ListReleases(ctx, skill)
}

func requirePrincipal(principal authn.Principal) error {
	if !principal.IsAuthenticated() {
		return authn.ErrUnauthenticated("authenticated principal is required")
	}
	return nil
}

func principalSubject(principal authn.Principal) AuthzSubjectRef {
	subjectType := principal.SubjectType
	if subjectType == "" {
		subjectType = "user"
	}
	return AuthzSubjectRef{Type: subjectType, ID: principal.SubjectID}
}

func normalizeBranchRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

func validSkillRelation(relation string) bool {
	switch relation {
	case "editor", "reviewer", "publisher", "viewer":
		return true
	default:
		return false
	}
}
