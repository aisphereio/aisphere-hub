package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"gorm.io/gorm"
)

// aihubToolModel maps to aihub_tools (migration 202607270001).
type aihubToolModel struct {
	ID            int64           `gorm:"primaryKey;column:id;autoIncrement"`
	ToolID        string          `gorm:"column:tool_id;size:128;not null"`
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
	if rt := strings.TrimSpace(opts.RuntimeType); rt != "" {
		// runtime.type lives inside the latest version's definition_json
		// ({"Runtime":{"Type":...}} — Go struct marshal, capitalized keys).
		q = q.Where(`EXISTS (
			SELECT 1 FROM aihub_tool_versions v
			WHERE v.tool_id = aihub_tools.tool_id
			  AND v.version = aihub_tools.latest_version
			  AND v.definition_json->'Runtime'->>'Type' = ?
		)`, rt)
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
		return writeVersionRows(tx, t, versionConflictStrict)
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
		return writeVersionRows(tx, t, versionConflictStrict)
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

// UpsertBuiltin idempotently upserts a builtin tool (matched by tool_id).
// Builtin versions are content-addressed ("builtin-<sha256[:12]>"), so
// reseeding an unchanged definition is a no-op and a changed definition
// inserts a NEW version row (existing rows are never overwritten). Used by
// biz.SeedBuiltinTools at startup.
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
		return writeVersionRows(tx, t, versionConflictSkip)
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

// versionConflictMode controls how writeVersionRows treats an already-existing
// (tool_id, version) row.
type versionConflictMode int

const (
	// versionConflictStrict keeps versions immutable for user-driven writes:
	// identical content is adopted as a no-op (e.g. re-creating a previously
	// soft-deleted tool whose old version rows are still present), while
	// different content is rejected with ErrToolVersionConflict.
	versionConflictStrict versionConflictMode = iota
	// versionConflictSkip silently skips existing rows. Used by content-
	// addressed builtin seeding, where the version label already derives from
	// the definition, so "exists" always means "same content".
	versionConflictSkip
)

// writeVersionRows inserts version rows for t. It NEVER updates an existing
// row's definition in place: versions are immutable. New rows get sha256 /
// revision backfilled from the definition when unset.
func writeVersionRows(tx *gorm.DB, t *biz.Tool, mode versionConflictMode) error {
	for ver, v := range t.Versions {
		defJSON, err := json.Marshal(v.Definition)
		if err != nil {
			return fmt.Errorf("tool repo: marshal definition for version %q: %w", ver, err)
		}
		sha := v.SHA256
		if sha == "" {
			sha = toolSha256Hex(defJSON)
		}
		rev := v.Revision
		if rev == "" {
			rev = sha[:12]
		}
		row := aihubToolVersionModel{
			ToolID:         t.ID,
			Version:        v.Version,
			Revision:       rev,
			SHA256:         sha,
			Author:         v.Author,
			CommitMsg:      v.CommitMsg,
			DefinitionJSON: defJSON,
			CreatedAt:      v.CreateTime,
		}
		var existing aihubToolVersionModel
		findErr := tx.Where("tool_id = ? AND version = ?", row.ToolID, row.Version).First(&existing).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		case findErr != nil:
			return findErr
		default:
			if mode == versionConflictSkip {
				continue
			}
			// Strict mode: identical content is a no-op (adopt), different
			// content violates version immutability.
			if !jsonSemanticEqual(existing.DefinitionJSON, defJSON) {
				return fmt.Errorf("%w: tool %q version %q", biz.ErrToolVersionConflict, t.ID, ver)
			}
			if existing.SHA256 == "" || existing.Revision == "" {
				if err := tx.Model(&aihubToolVersionModel{}).
					Where("tool_id = ? AND version = ?", row.ToolID, row.Version).
					Updates(map[string]any{"sha256": sha, "revision": rev}).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// toolSha256Hex returns the hex sha256 of b (used for version records).
func toolSha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// jsonSemanticEqual compares two JSON documents structurally. jsonb storage
// reorders object keys, so a byte comparison against a freshly marshaled
// definition would always report a difference.
func jsonSemanticEqual(a, b []byte) bool {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
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
