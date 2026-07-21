package biz

import (
	"context"
	"time"

	"github.com/aisphereio/kernel/errorx"
)

const (
	SkillDefaultBranch      = "main"
	SkillVisibilityPrivate  = "private"
	SkillVisibilityInternal = "internal"
	SkillVisibilityPublic   = "public"
	SkillStatusProvisioning = "provisioning"
	SkillStatusActive       = "active"
	SkillStatusDeleting     = "deleting"

	PullRequestStateOpen   = "open"
	PullRequestStateClosed = "closed"
	PullRequestStateMerged = "merged"

	ReviewVerdictApprove        = "approve"
	ReviewVerdictRequestChanges = "request_changes"
)

var (
	ErrSkillAlreadyExists      = errorx.Conflict(errorx.Code("SKILL_ALREADY_EXISTS"), "skill already exists")
	ErrSkillNotFound           = errorx.NotFound(errorx.Code("SKILL_NOT_FOUND"), "skill not found")
	ErrSkillInvalidArgument    = errorx.BadRequest(errorx.Code("SKILL_INVALID_ARGUMENT"), "invalid skill argument")
	ErrSkillDependencyFailed   = errorx.Unavailable(errorx.Code("SKILL_DEPENDENCY_FAILED"), "skill dependency failed")
	ErrPullRequestNotFound     = errorx.NotFound(errorx.Code("PULL_REQUEST_NOT_FOUND"), "pull request not found")
	ErrPullRequestNotOpen      = errorx.Conflict(errorx.Code("PULL_REQUEST_NOT_OPEN"), "pull request is not open")
	ErrPullRequestStale        = errorx.Conflict(errorx.Code("PULL_REQUEST_STALE"), "pull request target changed")
	ErrPullRequestNotApproved  = errorx.Conflict(errorx.Code("PULL_REQUEST_NOT_APPROVED"), "pull request is not approved")
	ErrPullRequestReviewExists = errorx.Conflict(errorx.Code("PULL_REQUEST_REVIEW_EXISTS"), "pull request review already exists")
)

type GitSkill struct {
	RepositoryID                               int64
	Name, DisplayName, Description, Visibility string
	OwnerID, OwnerType, OrgID, ProjectID       string
	DefaultBranch, Status                      string
	CreateTime, UpdateTime                     time.Time
}

type GitSkillListOptions struct {
	Limit, Offset             int
	Query, Visibility, Status string
}
type GitSkillListResult struct {
	Items      []*GitSkill
	NextOffset int
	HasMore    bool
}

type GitSkillRepository interface {
	GetSkill(context.Context, string) (*GitSkill, error)
	ListSkills(context.Context, GitSkillListOptions) (*GitSkillListResult, error)
	UpdateSkill(context.Context, *GitSkill) (*GitSkill, error)
	UpdateSkillVisibility(context.Context, string, string) (*GitSkill, error)
	UpdateSkillStatus(context.Context, string, string, string) (*GitSkill, error)
}

type SkillPullRequest struct {
	ID, SkillName, SourceRef, TargetRef, SourceSHA, TargetSHA string
	Title, Description, State, AuthorID, MergedBy, MergedSHA  string
	CreateTime, UpdateTime, MergedTime                        time.Time
}

type SkillPullRequestReview struct {
	ID, PullRequestID, ReviewerID, Verdict, Comment string
	CreateTime                                      time.Time
}

type PullRequestListOptions struct {
	State         string
	Limit, Offset int
}
type PullRequestListResult struct {
	Items      []*SkillPullRequest
	NextOffset int
	HasMore    bool
}

type PullRequestRepository interface {
	CreatePullRequest(context.Context, *SkillPullRequest) (*SkillPullRequest, error)
	GetPullRequest(context.Context, string, string) (*SkillPullRequest, error)
	ListPullRequests(context.Context, string, PullRequestListOptions) (*PullRequestListResult, error)
	CreateReview(context.Context, *SkillPullRequestReview) (*SkillPullRequestReview, error)
	ListReviews(context.Context, string) ([]*SkillPullRequestReview, error)
	ClosePullRequest(context.Context, string, string) (*SkillPullRequest, error)
	MergePullRequest(context.Context, string, string, string, string, string) (*SkillPullRequest, error)
}

type SkillRelease struct {
	Tag, CommitSHA, ManifestSHA256 string
	CreateTime                     time.Time
}
type SkillShare struct{ SkillName, Relation, SubjectType, SubjectID, SubjectRelation string }
