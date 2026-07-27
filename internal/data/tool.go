package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"gorm.io/gorm"
)

// aihubToolModel maps to aihub_tools (migration 202607270001).
type aihubToolModel struct {
	ID            int64           `gorm:"primaryKey;column:id;autoIncrement"`
	ToolID        string          `gorm:"column:tool_id;size:128;uniqueIndex;not null"`
	DisplayName   string          `gorm:"column:display_name;size:256;not null;default:''"`
	Description   string          `gorm:"column:description;type:text;not null;default:''"`
	Status        string          `gorm:"column:status;size:32;not null;default:'active'"`
	Scope         string          `gorm:"column:scope;size:32;not null;default:'project'"`
	OwnerType     string          `gorm:"column:owner_type;size:32;not null;default:'user'"`
	OwnerID       string          `gorm:"column:owner_id;size:128;not null;default:''"`
	OwnerName     string          `gorm:"column:owner_name;size:256;not null;default:''"`
	OrgID         string          `gorm:"column:org_id;size:128;not null;default:''"`
	ProjectID     string          `gorm:"column:project_id;size:128;not null;default:''"`
	LatestVersion string          `gorm:"column:latest_version;size:64;not null;default:''"`
	LabelsJSON    json.RawMessage `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	ObjectRef     string          `gorm:"column:object_ref;size:256;not null;default:''"`
	CreatedAt     time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt     time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt     *time.Time      `gorm:"column:deleted_at"`
}

func (aihubToolModel) TableName() string { return "aihub_tools" }

// aihubToolVersionModel maps to aihub_tool_versions.
type aihubToolVersionModel struct {
	ID            int64     `gorm:"primaryKey;column:id;autoIncrement"`
	ToolID        string    `gorm:"column:tool_id;size:128;not null;index:idx_aihub_tool_versions_tool"`
	Version       string    `gorm:"column:version;size:64;not null"`
	Revision      string    `gorm:"column:revision;size:128;not null;default:''"`
	SHA256        string    `gorm:"column:sha256;size:64;not null;default:''"`
	Author        string    `gorm:"column:author;size:128;not null;default:''"`
	CommitMsg     string    `gorm:"column:commit_msg;type:text;not null;default:''"`
	DefinitionJSON json.RawMessage `gorm:"column:definition_json;type:jsonb;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (aihubToolVersionModel) TableName() string { return "aihub_tool_versions" }

// toolRepo implements biz.ToolRepository backed by Postgres via GORM.
type toolRepo struct {
	db func(context.Context) *gorm.DB
}

// NewToolRepo builds a biz.ToolRepository from Resources.
func NewToolRepo(resources *Resources) biz.ToolRepository {
	return &toolRepo{
		db: func(ctx context.Context) *gorm.DB {
			if resources == nil || resources.DB == nil {
				return nil
			}
			return resources.DB.GORM(ctx)
		},
	}
}

func newToolRepoForDB(db *gorm.DB) *toolRepo {
	return &toolRepo{db: func(ctx context.Context) *gorm.DB { return db.WithContext(ctx) }}
}

func (r *toolRepo) List(ctx context.Context, opts biz.ToolListOptions) ([]*biz.Tool, string, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, "", errors.New("tool repository database is not configured")
	}
	q := database.Model(&aihubToolModel{}).Where("deleted_at IS NULL")
	if v := strings.TrimSpace(opts.Query); v != "" {
		like := "%" + v + "%"
		q = q.Where("tool_id LIKE ? OR display_name LIKE ? OR description LIKE ?", like, like, like)
	}
	if opts.Scope != "" {
		q = q.Where("scope = ?", opts.Scope)
	}
	if opts.Status != "" {
		q = q.Where("status = ?", opts.Status)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var rows []aihubToolModel
	if err := q.Order("tool_id ASC").Offset(opts.Offset).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	// Load versions for each tool in one pass per tool (small N).
	out := make([]*biz.Tool, 0, len(rows))
	for i := range rows {
		t, err := r.loadVersions(database, &rows[i], "")
		if err != nil {
			return nil, "", err
		}
		out = append(out, t)
	}
	next := ""
	if hasMore {
		next = fmt.Sprintf("%d", opts.Offset+limit)
	}
	return out, next, nil
}

func (r *toolRepo) Get(ctx context.Context, id string, version string) (*biz.Tool, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("tool repository database is not configured")
	}
	var row aihubToolModel
	err := database.Where("tool_id = ? AND deleted_at IS NULL", strings.TrimSpace(id)).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrToolNotFound
		}
		return nil, err
	}
	return r.loadVersions(database, &row, version)
}

func (r *toolRepo) Create(ctx context.Context, t *biz.Tool) (*biz.Tool, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("tool repository database is not configured")
	}
	model := toolBizToModel(t)
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			return err
		}
		return writeVersionRows(tx, t)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, t.ID, "")
}

func (r *toolRepo) Update(ctx context.Context, t *biz.Tool) (*biz.Tool, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("tool repository database is not configured")
	}
	err := database.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"display_name":    t.DisplayName,
			"description":     t.Description,
			"status":          t.Status,
			"scope":           t.Scope,
			"latest_version":  t.LatestVersion,
			"labels_json":     labelsToJSON(t.Labels),
			"updated_at":      time.Now().UTC(),
		}
		res := tx.Model(&aihubToolModel{}).
			Where("tool_id = ? AND deleted_at IS NULL", t.ID).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return biz.ErrToolNotFound
		}
		return writeVersionRows(tx, t)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, t.ID, "")
}

func (r *toolRepo) Delete(ctx context.Context, id string) error {
	database := r.db(ctx)
	if database == nil {
		return errors.New("tool repository database is not configured")
	}
	now := time.Now().UTC()
	res := database.Model(&aihubToolModel{}).
		Where("tool_id = ? AND deleted_at IS NULL", strings.TrimSpace(id)).
		Update("deleted_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrToolNotFound
	}
	return nil
}

// UpsertBuiltin idempotently upserts a builtin tool (matched by tool_id). It
// always overwrites the builtin-v1 version row so seed definitions stay in
// sync with the code. Used by biz.SeedBuiltinTools at startup.
func (r *toolRepo) UpsertBuiltin(ctx context.Context, t *biz.Tool) (*biz.Tool, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("tool repository database is not configured")
	}
	model := toolBizToModel(t)
	err := database.Transaction(func(tx *gorm.DB) error {
		// Upsert the tool row. On conflict (tool_id), update mutable fields but
		// preserve any user-set owner/org (builtin seeds always reassert these).
		var existing aihubToolModel
		findErr := tx.Where("tool_id = ?", t.ID).First(&existing).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model).Error; err != nil {
				return err
			}
		} else if findErr != nil {
			return findErr
		} else {
			updates := map[string]any{
				"display_name":   t.DisplayName,
				"description":    t.Description,
				"status":         t.Status,
				"scope":          t.Scope,
				"owner_type":     t.OwnerType,
				"owner_id":       t.OwnerID,
				"owner_name":     t.OwnerName,
				"latest_version": t.LatestVersion,
				"updated_at":     time.Now().UTC(),
			}
			if err := tx.Model(&aihubToolModel{}).Where("tool_id = ?", t.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return writeVersionRows(tx, t)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, t.ID, "")
}

// loadVersions attaches version records to a tool model. If version is non-
// empty, only that version is loaded (and must exist); otherwise all versions
// are loaded.
func (r *toolRepo) loadVersions(db *gorm.DB, row *aihubToolModel, version string) (*biz.Tool, error) {
	t := toolModelToBiz(row)
	q := db.Model(&aihubToolVersionModel{}).Where("tool_id = ?", row.ToolID)
	if version != "" {
		q = q.Where("version = ?", version)
	}
	var vrows []aihubToolVersionModel
	if err := q.Order("created_at DESC").Find(&vrows).Error; err != nil {
		return nil, err
	}
	if version != "" && len(vrows) == 0 {
		return nil, fmt.Errorf("%w: version %q not found", biz.ErrToolNotFound, version)
	}
	t.Versions = make(map[string]biz.ToolVersion, len(vrows))
	for _, v := range vrows {
		t.Versions[v.Version] = toolVersionModelToBiz(v)
	}
	return t, nil
}

// writeVersionRows inserts version rows, skipping ones that already exist
// (ON CONFLICT do nothing) so upserts are idempotent.
func writeVersionRows(tx *gorm.DB, t *biz.Tool) error {
	for ver, v := range t.Versions {
		defJSON, err := json.Marshal(v.Definition)
		if err != nil {
			return fmt.Errorf("tool repo: marshal definition for version %q: %w", ver, err)
		}
		row := aihubToolVersionModel{
			ToolID:         t.ID,
			Version:        v.Version,
			Revision:       v.Revision,
			SHA256:         v.SHA256,
			Author:         v.Author,
			CommitMsg:      v.CommitMsg,
			DefinitionJSON: defJSON,
			CreatedAt:      v.CreateTime,
		}
		// Idempotent: skip if this (tool_id, version) already exists.
		var count int64
		if err := tx.Model(&aihubToolVersionModel{}).
			Where("tool_id = ? AND version = ?", row.ToolID, row.Version).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			// Update the definition in place so seed definitions stay current.
			if err := tx.Model(&aihubToolVersionModel{}).
				Where("tool_id = ? AND version = ?", row.ToolID, row.Version).
				Updates(map[string]any{
					"definition_json": defJSON,
					"author":          row.Author,
					"commit_msg":      row.CommitMsg,
				}).Error; err != nil {
				return err
			}
			continue
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// --- model <-> biz conversion ---

func toolBizToModel(t *biz.Tool) aihubToolModel {
	return aihubToolModel{
		ToolID:        t.ID,
		DisplayName:   t.DisplayName,
		Description:   t.Description,
		Status:        t.Status,
		Scope:         t.Scope,
		OwnerType:     t.OwnerType,
		OwnerID:       t.OwnerID,
		OwnerName:     t.OwnerName,
		OrgID:         t.OrgID,
		ProjectID:     t.ProjectID,
		LatestVersion: t.LatestVersion,
		LabelsJSON:    labelsToJSON(t.Labels),
		ObjectRef:     t.Object,
	}
}

func toolModelToBiz(m *aihubToolModel) *biz.Tool {
	return &biz.Tool{
		ID:            m.ToolID,
		DisplayName:   m.DisplayName,
		Description:   m.Description,
		Status:        m.Status,
		Scope:         m.Scope,
		Labels:        labelsFromJSON(m.LabelsJSON),
		Object:        m.ObjectRef,
		OwnerType:     m.OwnerType,
		OwnerID:       m.OwnerID,
		OwnerName:     m.OwnerName,
		OrgID:         m.OrgID,
		ProjectID:     m.ProjectID,
		LatestVersion: m.LatestVersion,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func toolVersionModelToBiz(m aihubToolVersionModel) biz.ToolVersion {
	var def biz.ToolDefinition
	if len(m.DefinitionJSON) > 0 {
		_ = json.Unmarshal(m.DefinitionJSON, &def)
	}
	return biz.ToolVersion{
		Version:    m.Version,
		Revision:   m.Revision,
		SHA256:     m.SHA256,
		Author:     m.Author,
		CommitMsg:  m.CommitMsg,
		CreateTime: m.CreatedAt,
		Definition: def,
	}
}

func labelsToJSON(labels map[string]string) json.RawMessage {
	if len(labels) == 0 {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func labelsFromJSON(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}
