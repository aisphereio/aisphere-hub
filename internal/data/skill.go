package data

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"gorm.io/gorm"
)

type skillModel struct {
	Name          string    `gorm:"primaryKey;column:name;size:128"`
	DisplayName   string    `gorm:"column:display_name;size:256;not null;default:''"`
	Description   string    `gorm:"column:description;type:text;not null;default:''"`
	Visibility    string    `gorm:"column:visibility;size:32;not null;default:'private';index"`
	OwnerID       string    `gorm:"column:owner_id;size:128;not null;index"`
	OrgID         string    `gorm:"column:org_id;size:128;not null;default:'';index"`
	ProjectID     string    `gorm:"column:project_id;size:128;not null;default:'';index"`
	DefaultBranch string    `gorm:"column:default_branch;size:128;not null;default:'main'"`
	Status        string    `gorm:"column:status;size:32;not null;default:'provisioning';index"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt     time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (skillModel) TableName() string { return "skills" }

type skillRepo struct {
	db func(context.Context) *gorm.DB
}

func NewSkillRepo(resources *Resources) biz.GitSkillRepository {
	return &skillRepo{db: func(ctx context.Context) *gorm.DB {
		if resources == nil || resources.DB == nil {
			return nil
		}
		return resources.DB.GORM(ctx)
	}}
}

func newSkillRepoForDB(db *gorm.DB) *skillRepo {
	return &skillRepo{db: func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) }}
}

func (r *skillRepo) CreateSkill(ctx context.Context, skill *biz.GitSkill) (*biz.GitSkill, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	row := skillModelFromBiz(skill)
	if row.DefaultBranch == "" {
		row.DefaultBranch = biz.SkillDefaultBranch
	}
	if row.Visibility == "" {
		row.Visibility = biz.SkillVisibilityPrivate
	}
	if row.Status == "" {
		row.Status = biz.SkillStatusProvisioning
	}
	if err := db.Create(&row).Error; err != nil {
		if isDuplicateError(err) {
			return nil, biz.ErrSkillAlreadyExists
		}
		return nil, err
	}
	return row.toBiz(), nil
}

func (r *skillRepo) GetSkill(ctx context.Context, name string) (*biz.GitSkill, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	var row skillModel
	if err := db.Where("name = ?", strings.TrimSpace(name)).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrSkillNotFound
		}
		return nil, err
	}
	return row.toBiz(), nil
}

func (r *skillRepo) ListSkills(ctx context.Context, opts biz.GitSkillListOptions) (*biz.GitSkillListResult, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := db.Model(&skillModel{})
	if value := strings.TrimSpace(opts.Query); value != "" {
		like := "%" + value + "%"
		query = query.Where("name LIKE ? OR display_name LIKE ?", like, like)
	}
	if opts.Visibility != "" {
		query = query.Where("visibility = ?", opts.Visibility)
	}
	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}
	var rows []skillModel
	if err := query.Order("name ASC").Offset(opts.Offset).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*biz.GitSkill, 0, len(rows))
	for i := range rows {
		items = append(items, rows[i].toBiz())
	}
	return &biz.GitSkillListResult{Items: items, NextOffset: opts.Offset + len(items), HasMore: hasMore}, nil
}

func (r *skillRepo) UpdateSkill(ctx context.Context, skill *biz.GitSkill) (*biz.GitSkill, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	result := db.Model(&skillModel{}).Where("name = ?", strings.TrimSpace(skill.Name)).Updates(map[string]any{
		"display_name": skill.DisplayName,
		"description":  skill.Description,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, skill.Name)
}

func (r *skillRepo) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*biz.GitSkill, error) {
	return r.updateOne(ctx, name, map[string]any{"visibility": visibility})
}

func (r *skillRepo) UpdateSkillStatus(ctx context.Context, name, expected, next string) (*biz.GitSkill, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	query := db.Model(&skillModel{}).Where("name = ?", strings.TrimSpace(name))
	if expected != "" {
		query = query.Where("status = ?", expected)
	}
	result := query.Update("status", next)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, name)
}

func (r *skillRepo) DeleteSkill(ctx context.Context, name string) error {
	db := r.database(ctx)
	if db == nil {
		return errors.New("skill repository database is not configured")
	}
	result := db.Where("name = ?", strings.TrimSpace(name)).Delete(&skillModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return biz.ErrSkillNotFound
	}
	return nil
}

func (r *skillRepo) updateOne(ctx context.Context, name string, values map[string]any) (*biz.GitSkill, error) {
	db := r.database(ctx)
	if db == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	result := db.Model(&skillModel{}).Where("name = ?", strings.TrimSpace(name)).Updates(values)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, name)
}

func (r *skillRepo) database(ctx context.Context) *gorm.DB {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db(ctx)
}

func skillModelFromBiz(skill *biz.GitSkill) skillModel {
	if skill == nil {
		return skillModel{}
	}
	return skillModel{
		Name:          strings.TrimSpace(skill.Name),
		DisplayName:   skill.DisplayName,
		Description:   skill.Description,
		Visibility:    skill.Visibility,
		OwnerID:       skill.OwnerID,
		OrgID:         skill.OrgID,
		ProjectID:     skill.ProjectID,
		DefaultBranch: skill.DefaultBranch,
		Status:        skill.Status,
		CreatedAt:     skill.CreateTime,
		UpdatedAt:     skill.UpdateTime,
	}
}

func (m skillModel) toBiz() *biz.GitSkill {
	return &biz.GitSkill{
		Name:          m.Name,
		DisplayName:   m.DisplayName,
		Description:   m.Description,
		Visibility:    m.Visibility,
		OwnerID:       m.OwnerID,
		OrgID:         m.OrgID,
		ProjectID:     m.ProjectID,
		DefaultBranch: m.DefaultBranch,
		Status:        m.Status,
		CreateTime:    m.CreatedAt,
		UpdateTime:    m.UpdatedAt,
	}
}

func isDuplicateError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") || strings.Contains(message, "unique constraint")
}
