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

	// File-content API errors. These mirror the GitLab/Gitea repository-files
	// REST shape; the underlying store is still the bare git repo, so the codes
	// align with what the in-browser editor surfaces to the user.
	ErrFileNotFound       = errorx.NotFound(errorx.Code("SKILL_FILE_NOT_FOUND"), "skill file not found")
	ErrFileAlreadyExists  = errorx.Conflict(errorx.Code("SKILL_FILE_ALREADY_EXISTS"), "skill file already exists")
	ErrFilePathInvalid    = errorx.BadRequest(errorx.Code("SKILL_FILE_PATH_INVALID"), "invalid skill file path")
	ErrBranchNotFound     = errorx.NotFound(errorx.Code("SKILL_BRANCH_NOT_FOUND"), "skill branch not found")
	ErrGitOperationFailed = errorx.Internal(errorx.Code("SKILL_GIT_OPERATION_FAILED"), "skill git operation failed")
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

// FileInfo describes a single entry (file or directory) inside a skill
// repository tree, in the GitLab/Gitea repository-contents REST shape.
type FileInfo struct {
	Name         string
	Path         string
	Type         string // "file" | "dir" | "symlink" | "commit"
	Size         int64
	Mode         string
	SHA          string
	LastModified time.Time
}

// FileContent is the full content of a single file plus the commit metadata
// needed for optimistic-concurrency writes (SHA must be echoed back on update).
type FileContent struct {
	Name          string
	Path          string
	SHA           string
	Size          int64
	Content       string
	Encoding      string // always "base64"
	Ref           string
	CommitSHA     string
	CommitMessage string
	LastModified  time.Time
}
