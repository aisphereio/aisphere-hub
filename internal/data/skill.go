package data

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"gorm.io/gorm"
)

const skillSelectColumns = `
	r.id AS repository_id,
	r.name,
	p.display_name,
	r.description,
	p.visibility,
	p.created_by_id AS owner_id,
	p.created_by_type AS owner_type,
	p.created_by_name AS owner_name,
	p.org_id,
	p.project_id,
	p.default_branch,
	p.lifecycle_status AS status,
	r.created_at,
	r.updated_at`

// softRepoModel is intentionally only a compatibility model for data-layer
// tests and joins. Soft Serve owns the real repos migration and lifecycle.
type softRepoModel struct {
	ID          int64     `gorm:"primaryKey;column:id;autoIncrement"`
	Name        string    `gorm:"column:name;size:255;uniqueIndex;not null"`
	Description string    `gorm:"column:description;type:text;not null;default:''"`
	Private     bool      `gorm:"column:private;not null;default:true"`
	Mirror      bool      `gorm:"column:mirror;not null;default:false"`
	Hidden      bool      `gorm:"column:hidden;not null;default:false"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (softRepoModel) TableName() string { return "repos" }

type skillProfileModel struct {
	RepositoryID   int64     `gorm:"primaryKey;column:repository_id"`
	DisplayName    string    `gorm:"column:display_name;size:256;not null;default:''"`
	OrgID          string    `gorm:"column:org_id;size:128;not null;index"`
	ProjectID      string    `gorm:"column:project_id;size:128;not null;default:'';index"`
	CreatedByType  string    `gorm:"column:created_by_type;size:32;not null;default:'user'"`
	CreatedByID    string    `gorm:"column:created_by_id;size:128;not null;index"`
	CreatedByName  string    `gorm:"column:created_by_name;size:256;not null;default:''"`
	Visibility     string    `gorm:"column:visibility;size:32;not null;default:'private';index"`
	Status         string    `gorm:"column:lifecycle_status;size:32;not null;default:'provisioning';index"`
	DefaultBranch  string    `gorm:"column:default_branch;size:128;not null;default:'main'"`
	ProvisionError string    `gorm:"column:provision_error;type:text;not null;default:''"`
	CreatedAt      time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (skillProfileModel) TableName() string { return "hub_skill_profiles" }

type skillRecord struct {
	RepositoryID  int64     `gorm:"column:repository_id"`
	Name          string    `gorm:"column:name"`
	DisplayName   string    `gorm:"column:display_name"`
	Description   string    `gorm:"column:description"`
	Visibility    string    `gorm:"column:visibility"`
	OwnerID       string    `gorm:"column:owner_id"`
	OwnerType     string    `gorm:"column:owner_type"`
	OwnerName     string    `gorm:"column:owner_name"`
	OrgID         string    `gorm:"column:org_id"`
	ProjectID     string    `gorm:"column:project_id"`
	DefaultBranch string    `gorm:"column:default_branch"`
	Status        string    `gorm:"column:status"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

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

func (r *skillRepo) GetSkill(ctx context.Context, name string) (*biz.GitSkill, error) {
	database := r.database(ctx)
	if database == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	var row skillRecord
	result := skillQuery(database).
		Where("r.name = ?", strings.TrimSpace(name)).
		Limit(1).
		Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return row.toBiz(), nil
}

func (r *skillRepo) ListSkills(ctx context.Context, opts biz.GitSkillListOptions) (*biz.GitSkillListResult, error) {
	database := r.database(ctx)
	if database == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	query := skillQuery(database)
	if value := strings.TrimSpace(opts.Query); value != "" {
		like := "%" + value + "%"
		query = query.Where("r.name LIKE ? OR p.display_name LIKE ?", like, like)
	}
	if opts.Visibility != "" {
		query = query.Where("p.visibility = ?", opts.Visibility)
	}
	if opts.Status != "" {
		query = query.Where("p.lifecycle_status = ?", opts.Status)
	}
	var rows []skillRecord
	if err := query.Order("r.name ASC").Offset(opts.Offset).Limit(limit + 1).Scan(&rows).Error; err != nil {
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
	if skill == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	database := r.database(ctx)
	if database == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	name := strings.TrimSpace(skill.Name)
	err := database.Transaction(func(tx *gorm.DB) error {
		profile := tx.Table("hub_skill_profiles").
			Where("repository_id = (SELECT id FROM repos WHERE name = ?)", name).
			Update("display_name", skill.DisplayName)
		if profile.Error != nil {
			return profile.Error
		}
		if profile.RowsAffected == 0 {
			return biz.ErrSkillNotFound
		}
		return tx.Table("repos").Where("name = ?", name).Update("description", skill.Description).Error
	})
	if err != nil {
		return nil, err
	}
	return r.GetSkill(ctx, name)
}

func (r *skillRepo) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*biz.GitSkill, error) {
	database := r.database(ctx)
	if database == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	name = strings.TrimSpace(name)
	err := database.Transaction(func(tx *gorm.DB) error {
		profile := tx.Table("hub_skill_profiles").
			Where("repository_id = (SELECT id FROM repos WHERE name = ?)", name).
			Update("visibility", visibility)
		if profile.Error != nil {
			return profile.Error
		}
		if profile.RowsAffected == 0 {
			return biz.ErrSkillNotFound
		}
		// Soft Serve's private flag is only a coarse protocol projection. The
		// authoritative authorization decision remains in SpiceDB.
		return tx.Table("repos").Where("name = ?", name).Update("private", visibility != biz.SkillVisibilityPublic).Error
	})
	if err != nil {
		return nil, err
	}
	return r.GetSkill(ctx, name)
}

func (r *skillRepo) UpdateSkillStatus(ctx context.Context, name, expected, next string) (*biz.GitSkill, error) {
	database := r.database(ctx)
	if database == nil {
		return nil, errors.New("skill repository database is not configured")
	}
	query := database.Table("hub_skill_profiles").
		Where("repository_id = (SELECT id FROM repos WHERE name = ?)", strings.TrimSpace(name))
	if expected != "" {
		query = query.Where("lifecycle_status = ?", expected)
	}
	result := query.Update("lifecycle_status", next)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, name)
}

func skillQuery(db *gorm.DB) *gorm.DB {
	return db.Table("repos AS r").
		Select(skillSelectColumns).
		Joins("JOIN hub_skill_profiles AS p ON p.repository_id = r.id")
}

func (r *skillRepo) database(ctx context.Context) *gorm.DB {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db(ctx)
}

func (m skillRecord) toBiz() *biz.GitSkill {
	return &biz.GitSkill{
		RepositoryID:  m.RepositoryID,
		Name:          m.Name,
		DisplayName:   m.DisplayName,
		Description:   m.Description,
		Visibility:    m.Visibility,
		OwnerID:       m.OwnerID,
		OwnerType:     m.OwnerType,
		OwnerName:     m.OwnerName,
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
