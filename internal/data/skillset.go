package data

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/dbx"
	"gorm.io/gorm"
)

type skillSetModel struct {
	ID          int64          `gorm:"primaryKey;autoIncrement;column:id"`
	Name        string         `gorm:"column:name;size:128;uniqueIndex;not null"`
	DisplayName string         `gorm:"column:display_name;size:256;not null;default:''"`
	Description string         `gorm:"column:description;type:text;not null;default:''"`
	Visibility  string         `gorm:"column:visibility;size:32;index;not null;default:'private'"`
	OwnerID     string         `gorm:"column:owner_id;size:128;index;not null;default:''"`
	OrgID       string         `gorm:"column:org_id;size:128;index;not null;default:''"`
	LabelsJSON  []byte         `gorm:"column:labels;type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAt   time.Time      `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index"`
	Members     []skillSetMemberModel `gorm:"foreignKey:SkillSetID;references:ID"`
}

type skillSetMemberModel struct {
	ID         int64     `gorm:"primaryKey;autoIncrement;column:id"`
	SkillSetID int64     `gorm:"column:skill_set_id;index;not null"`
	SkillName  string    `gorm:"column:skill_name;size:128;index;not null"`
	SortOrder  int       `gorm:"column:sort_order;not null;default:0"`
	CreatedAt  time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt  time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (skillSetModel) TableName() string       { return "aihub_skill_sets" }
func (skillSetMemberModel) TableName() string { return "aihub_skill_set_items" }

type skillSetRepo struct {
	resources *Resources
}

func NewSkillSetRepo(resources *Resources) biz.SkillSetRepo {
	return &skillSetRepo{resources: resources}
}

func (r *skillSetRepo) db(ctx context.Context) *gorm.DB {
	if r == nil || r.resources == nil || r.resources.DB == nil {
		return nil
	}
	return r.resources.DB.GORM(ctx)
}

func (r *skillSetRepo) CreateSkillSet(ctx context.Context, set *biz.SkillSet) (*biz.SkillSet, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	row, err := skillSetToRow(set)
	if err != nil {
		return nil, err
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := validateSkillSetMembers(tx, set.Members); err != nil {
			return err
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return createSkillSetMembers(tx, row.ID, set.Members)
	}); err != nil {
		if isDuplicateError(err) {
			return nil, biz.ErrSkillSetAlreadyExists
		}
		return nil, err
	}
	return r.GetSkillSet(ctx, row.Name)
}

func (r *skillSetRepo) UpdateSkillSet(ctx context.Context, name string, patch biz.SkillSetPatch) (*biz.SkillSet, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var row skillSetModel
		if err := tx.Where("name = ?", name).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return biz.ErrSkillSetNotFound
			}
			return err
		}
		updates := map[string]any{"updated_at": time.Now()}
		if patch.DisplayName != nil {
			updates["display_name"] = *patch.DisplayName
		}
		if patch.Description != nil {
			updates["description"] = *patch.Description
		}
		if patch.Visibility != nil {
			updates["visibility"] = *patch.Visibility
		}
		if patch.Labels != nil {
			encoded, err := json.Marshal(*patch.Labels)
			if err != nil {
				return biz.ErrSkillSetInvalidArgument
			}
			updates["labels"] = encoded
		}
		if err := tx.Model(&skillSetModel{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return err
		}
		if patch.Members != nil {
			if err := validateSkillSetMembers(tx, *patch.Members); err != nil {
				return err
			}
			if err := tx.Where("skill_set_id = ?", row.ID).Delete(&skillSetMemberModel{}).Error; err != nil {
				return err
			}
			if err := createSkillSetMembers(tx, row.ID, *patch.Members); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return r.GetSkillSet(ctx, name)
}

func (r *skillSetRepo) ListSkillSets(ctx context.Context, opts biz.SkillSetListOptions) (*biz.SkillSetListResult, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	query := db.Model(&skillSetModel{})
	if q := strings.TrimSpace(opts.Query); q != "" {
		like := "%" + q + "%"
		query = query.Where("name ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
	}
	visibilityClauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	visibilityClauses = append(visibilityClauses, "visibility = 'public'")
	if opts.OwnerID != "" {
		visibilityClauses = append(visibilityClauses, "owner_id = ?")
		args = append(args, opts.OwnerID)
	}
	if opts.OrgID != "" {
		visibilityClauses = append(visibilityClauses, "(visibility = 'internal' AND org_id = ?)")
		args = append(args, opts.OrgID)
	}
	if len(opts.VisibleNames) > 0 {
		visibilityClauses = append(visibilityClauses, "name IN ?")
		args = append(args, opts.VisibleNames)
	}
	query = query.Where("("+strings.Join(visibilityClauses, " OR ")+")", args...)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []skillSetModel
	err := query.
		Preload("Members", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("sort_order ASC, skill_name ASC")
		}).
		Order("updated_at DESC, name ASC").
		Limit(opts.Limit).
		Offset(opts.Offset).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]*biz.SkillSet, 0, len(rows))
	for i := range rows {
		items = append(items, skillSetRowToDomain(&rows[i]))
	}
	return &biz.SkillSetListResult{Items: items, Total: total}, nil
}

func (r *skillSetRepo) GetSkillSet(ctx context.Context, name string) (*biz.SkillSet, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var row skillSetModel
	err := db.
		Preload("Members", func(tx *gorm.DB) *gorm.DB {
			return tx.Order("sort_order ASC, skill_name ASC")
		}).
		Where("name = ?", name).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrSkillSetNotFound
	}
	if err != nil {
		return nil, err
	}
	return skillSetRowToDomain(&row), nil
}

func (r *skillSetRepo) DeleteSkillSet(ctx context.Context, name string) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var row skillSetModel
		if err := tx.Where("name = ?", name).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return biz.ErrSkillSetNotFound
			}
			return err
		}
		if err := tx.Where("skill_set_id = ?", row.ID).Delete(&skillSetMemberModel{}).Error; err != nil {
			return err
		}
		return tx.Delete(&row).Error
	})
}

func (r *skillSetRepo) ListSkillSetNamesBySkill(ctx context.Context, skillName string) ([]string, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var names []string
	err := db.Table("aihub_skill_set_items AS item").
		Select("skill_set.name").
		Joins("JOIN aihub_skill_sets AS skill_set ON skill_set.id = item.skill_set_id AND skill_set.deleted_at IS NULL").
		Where("item.skill_name = ?", skillName).
		Order("skill_set.name ASC").
		Pluck("skill_set.name", &names).Error
	return names, err
}

func validateSkillSetMembers(tx *gorm.DB, members []biz.SkillSetMember) error {
	if len(members) == 0 {
		return nil
	}
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.SkillName)
	}
	var count int64
	if err := tx.Model(&skillModel{}).Where("name IN ?", names).Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(names)) {
		return biz.ErrSkillSetSkillNotFound
	}
	return nil
}

func createSkillSetMembers(tx *gorm.DB, skillSetID int64, members []biz.SkillSetMember) error {
	if len(members) == 0 {
		return nil
	}
	rows := make([]skillSetMemberModel, 0, len(members))
	for _, member := range members {
		rows = append(rows, skillSetMemberModel{
			SkillSetID: skillSetID,
			SkillName:  member.SkillName,
			SortOrder:  member.Order,
		})
	}
	return tx.Create(&rows).Error
}

func skillSetToRow(set *biz.SkillSet) (*skillSetModel, error) {
	labels, err := json.Marshal(set.Labels)
	if err != nil {
		return nil, biz.ErrSkillSetInvalidArgument
	}
	return &skillSetModel{
		ID:          set.ID,
		Name:        set.Name,
		DisplayName: set.DisplayName,
		Description: set.Description,
		Visibility:  set.Visibility,
		OwnerID:     set.OwnerID,
		OrgID:       set.OrgID,
		LabelsJSON:  labels,
	}, nil
}

func skillSetRowToDomain(row *skillSetModel) *biz.SkillSet {
	if row == nil {
		return nil
	}
	labels := map[string]string{}
	_ = json.Unmarshal(row.LabelsJSON, &labels)
	members := make([]biz.SkillSetMember, 0, len(row.Members))
	for _, member := range row.Members {
		members = append(members, biz.SkillSetMember{SkillName: member.SkillName, Order: member.SortOrder})
	}
	return &biz.SkillSet{
		ID:          row.ID,
		Name:        row.Name,
		DisplayName: row.DisplayName,
		Description: row.Description,
		Visibility:  row.Visibility,
		OwnerID:     row.OwnerID,
		OrgID:       row.OrgID,
		Labels:      labels,
		Members:     members,
		CreateTime:  row.CreatedAt,
		UpdateTime:  row.UpdatedAt,
	}
}

func isDuplicateError(err error) bool {
	return errors.Is(err, dbx.ErrDuplicateKey) || errors.Is(err, gorm.ErrDuplicatedKey)
}
