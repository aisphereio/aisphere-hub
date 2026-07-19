package data

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type skillPullRequestModel struct {
	ID          string     `gorm:"primaryKey;column:id;size:36"`
	SkillName   string     `gorm:"column:skill_name;size:128;not null;index"`
	SourceRef   string     `gorm:"column:source_ref;size:512;not null"`
	TargetRef   string     `gorm:"column:target_ref;size:512;not null"`
	SourceSHA   string     `gorm:"column:source_sha;size:64;not null"`
	TargetSHA   string     `gorm:"column:target_sha;size:64;not null"`
	Title       string     `gorm:"column:title;size:512;not null"`
	Description string     `gorm:"column:description;type:text;not null;default:''"`
	State       string     `gorm:"column:state;size:32;not null;index"`
	AuthorID    string     `gorm:"column:author_id;size:128;not null"`
	MergedBy    string     `gorm:"column:merged_by;size:128;not null;default:''"`
	MergedSHA   string     `gorm:"column:merged_sha;size:64;not null;default:''"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
	MergedAt    *time.Time `gorm:"column:merged_at"`
}

func (skillPullRequestModel) TableName() string { return "skill_pull_requests" }

type skillPullRequestReviewModel struct {
	ID            string    `gorm:"primaryKey;column:id;size:36"`
	PullRequestID string    `gorm:"column:pull_request_id;size:36;not null;uniqueIndex:idx_pr_reviewer"`
	ReviewerID    string    `gorm:"column:reviewer_id;size:128;not null;uniqueIndex:idx_pr_reviewer"`
	Verdict       string    `gorm:"column:verdict;size:32;not null"`
	Comment       string    `gorm:"column:comment;type:text;not null;default:''"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (skillPullRequestReviewModel) TableName() string { return "skill_pull_request_reviews" }

type pullRequestRepo struct {
	db    func(context.Context) *gorm.DB
	newID func() string
}

func NewPullRequestRepo(resources *Resources) biz.PullRequestRepository {
	return &pullRequestRepo{
		db: func(ctx context.Context) *gorm.DB {
			if resources == nil || resources.DB == nil {
				return nil
			}
			return resources.DB.GORM(ctx)
		},
		newID: func() string { return uuid.NewString() },
	}
}

func newPullRequestRepoForDB(db *gorm.DB) *pullRequestRepo {
	return &pullRequestRepo{
		db:    func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) },
		newID: func() string { return uuid.NewString() },
	}
}

func (r *pullRequestRepo) CreatePullRequest(ctx context.Context, pr *biz.SkillPullRequest) (*biz.SkillPullRequest, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	row := pullRequestModelFromBiz(pr)
	if row.ID == "" {
		row.ID = r.newID()
	}
	if row.TargetRef == "" {
		row.TargetRef = "refs/heads/" + biz.SkillDefaultBranch
	}
	if row.State == "" {
		row.State = biz.PullRequestStateOpen
	}
	if err := db.Create(&row).Error; err != nil {
		return nil, err
	}
	return row.toBiz(), nil
}

func (r *pullRequestRepo) GetPullRequest(ctx context.Context, skillName, id string) (*biz.SkillPullRequest, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	var row skillPullRequestModel
	if err := db.Where("skill_name = ? AND id = ?", skillName, id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrPullRequestNotFound
		}
		return nil, err
	}
	return row.toBiz(), nil
}

func (r *pullRequestRepo) ListPullRequests(ctx context.Context, skillName string, opts biz.PullRequestListOptions) (*biz.PullRequestListResult, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := db.Where("skill_name = ?", skillName)
	if opts.State != "" {
		query = query.Where("state = ?", opts.State)
	}
	var rows []skillPullRequestModel
	if err := query.Order("created_at DESC").Offset(opts.Offset).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*biz.SkillPullRequest, 0, len(rows))
	for i := range rows {
		items = append(items, rows[i].toBiz())
	}
	return &biz.PullRequestListResult{Items: items, NextOffset: opts.Offset + len(items), HasMore: hasMore}, nil
}

func (r *pullRequestRepo) CreateReview(ctx context.Context, review *biz.SkillPullRequestReview) (*biz.SkillPullRequestReview, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	row := pullRequestReviewModelFromBiz(review)
	if row.ID == "" {
		row.ID = r.newID()
	}
	if err := db.Create(&row).Error; err != nil {
		if isDuplicateError(err) {
			return nil, biz.ErrPullRequestReviewExists
		}
		return nil, err
	}
	return row.toBiz(), nil
}

func (r *pullRequestRepo) ListReviews(ctx context.Context, pullRequestID string) ([]*biz.SkillPullRequestReview, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	var rows []skillPullRequestReviewModel
	if err := db.Where("pull_request_id = ?", pullRequestID).Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.SkillPullRequestReview, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].toBiz())
	}
	return out, nil
}

func (r *pullRequestRepo) ClosePullRequest(ctx context.Context, skillName, id string) (*biz.SkillPullRequest, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	result := db.Model(&skillPullRequestModel{}).
		Where("skill_name = ? AND id = ? AND state = ?", skillName, id, biz.PullRequestStateOpen).
		Update("state", biz.PullRequestStateClosed)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		if _, err := r.GetPullRequest(ctx, skillName, id); err != nil {
			return nil, err
		}
		return nil, biz.ErrPullRequestNotOpen
	}
	return r.GetPullRequest(ctx, skillName, id)
}

func (r *pullRequestRepo) MergePullRequest(ctx context.Context, skillName, id, expectedTargetSHA, mergedSHA, mergedBy string) (*biz.SkillPullRequest, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("pull request repository database is not configured")
	}
	var merged *biz.SkillPullRequest
	err := db.Transaction(func(tx *gorm.DB) error {
		var row skillPullRequestModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("skill_name = ? AND id = ?", skillName, id).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return biz.ErrPullRequestNotFound
			}
			return err
		}
		if row.State != biz.PullRequestStateOpen {
			return biz.ErrPullRequestNotOpen
		}
		if strings.TrimSpace(row.TargetSHA) != strings.TrimSpace(expectedTargetSHA) {
			return biz.ErrPullRequestStale
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"state":      biz.PullRequestStateMerged,
			"merged_sha": mergedSHA,
			"merged_by":  mergedBy,
			"merged_at":  now,
		}
		result := tx.Model(&skillPullRequestModel{}).
			Where("id = ? AND state = ? AND target_sha = ?", id, biz.PullRequestStateOpen, expectedTargetSHA).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return biz.ErrPullRequestStale
		}
		if err := tx.Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		merged = row.toBiz()
		return nil
	})
	return merged, err
}

func (r *pullRequestRepo) database(ctx context.Context) *gorm.DB {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db(ctx)
}

func pullRequestModelFromBiz(pr *biz.SkillPullRequest) skillPullRequestModel {
	if pr == nil {
		return skillPullRequestModel{}
	}
	return skillPullRequestModel{
		ID: pr.ID, SkillName: pr.SkillName, SourceRef: pr.SourceRef, TargetRef: pr.TargetRef,
		SourceSHA: pr.SourceSHA, TargetSHA: pr.TargetSHA, Title: pr.Title, Description: pr.Description,
		State: pr.State, AuthorID: pr.AuthorID, MergedBy: pr.MergedBy, MergedSHA: pr.MergedSHA,
		CreatedAt: pr.CreateTime, UpdatedAt: pr.UpdateTime,
	}
}

func (m skillPullRequestModel) toBiz() *biz.SkillPullRequest {
	out := &biz.SkillPullRequest{
		ID: m.ID, SkillName: m.SkillName, SourceRef: m.SourceRef, TargetRef: m.TargetRef,
		SourceSHA: m.SourceSHA, TargetSHA: m.TargetSHA, Title: m.Title, Description: m.Description,
		State: m.State, AuthorID: m.AuthorID, MergedBy: m.MergedBy, MergedSHA: m.MergedSHA,
		CreateTime: m.CreatedAt, UpdateTime: m.UpdatedAt,
	}
	if m.MergedAt != nil {
		out.MergedTime = *m.MergedAt
	}
	return out
}

func pullRequestReviewModelFromBiz(review *biz.SkillPullRequestReview) skillPullRequestReviewModel {
	if review == nil {
		return skillPullRequestReviewModel{}
	}
	return skillPullRequestReviewModel{
		ID: review.ID, PullRequestID: review.PullRequestID, ReviewerID: review.ReviewerID,
		Verdict: review.Verdict, Comment: review.Comment, CreatedAt: review.CreateTime,
	}
}

func (m skillPullRequestReviewModel) toBiz() *biz.SkillPullRequestReview {
	return &biz.SkillPullRequestReview{
		ID: m.ID, PullRequestID: m.PullRequestID, ReviewerID: m.ReviewerID,
		Verdict: m.Verdict, Comment: m.Comment, CreateTime: m.CreatedAt,
	}
}
