package biz

import (
	"context"
	"errors"
	"time"
)

const (
	SkillDefaultBranch      = "main"
	SkillStatusProvisioning = "provisioning"
	SkillStatusDeleting     = "deleting"

	PullRequestStateOpen   = "open"
	PullRequestStateClosed = "closed"
	PullRequestStateMerged = "merged"

	ReviewVerdictApprove        = "approve"
	ReviewVerdictRequestChanges = "request_changes"
)

var (
	ErrPullRequestNotFound     = errors.New("pull request not found")
	ErrPullRequestNotOpen      = errors.New("pull request is not open")
	ErrPullRequestStale        = errors.New("pull request target changed")
	ErrPullRequestReviewExists = errors.New("pull request review already exists")
)

// GitSkill contains only management metadata. Git refs and objects are owned
// by the embedded Git engine and are never duplicated in PostgreSQL.
type GitSkill struct {
	Name          string
	DisplayName   string
	Description   string
	Visibility    string
	OwnerID       string
	OrgID         string
	ProjectID     string
	DefaultBranch string
	Status        string
	CreateTime    time.Time
	UpdateTime    time.Time
}

type GitSkillListOptions struct {
	Limit      int
	Offset     int
	Query      string
	Visibility string
	Status     string
}

type GitSkillListResult struct {
	Items      []*GitSkill
	NextOffset int
	HasMore    bool
}

type GitSkillRepository interface {
	CreateSkill(context.Context, *GitSkill) (*GitSkill, error)
	GetSkill(context.Context, string) (*GitSkill, error)
	ListSkills(context.Context, GitSkillListOptions) (*GitSkillListResult, error)
	UpdateSkill(context.Context, *GitSkill) (*GitSkill, error)
	UpdateSkillVisibility(context.Context, string, string) (*GitSkill, error)
	UpdateSkillStatus(context.Context, string, string, string) (*GitSkill, error)
	DeleteSkill(context.Context, string) error
}

type SkillPullRequest struct {
	ID          string
	SkillName   string
	SourceRef   string
	TargetRef   string
	SourceSHA   string
	TargetSHA   string
	Title       string
	Description string
	State       string
	AuthorID    string
	MergedBy    string
	MergedSHA   string
	CreateTime  time.Time
	UpdateTime  time.Time
	MergedTime  time.Time
}

type SkillPullRequestReview struct {
	ID            string
	PullRequestID string
	ReviewerID    string
	Verdict       string
	Comment       string
	CreateTime    time.Time
}

type PullRequestListOptions struct {
	State  string
	Limit  int
	Offset int
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
