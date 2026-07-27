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

// aihubModelProfileModel maps to aihub_model_profiles (migration 202607280001).
type aihubModelProfileModel struct {
	ID            int64           `gorm:"primaryKey;column:id;autoIncrement"`
	ProfileID     string          `gorm:"column:profile_id;size:128;uniqueIndex;not null"`
	DisplayName   string          `gorm:"column:display_name;size:256;not null;default:''"`
	Description   string          `gorm:"column:description;type:text;not null;default:''"`
	Status        string          `gorm:"column:status;size:32;not null;default:'active'"`
	Provider      string          `gorm:"column:provider;size:64;not null;default:''"`
	APIFormat     string          `gorm:"column:api_format;size:64;not null;default:'openai_responses'"`
	Endpoint      string          `gorm:"column:endpoint;size:256;not null;default:''"`
	ModelName     string          `gorm:"column:model_name;size:128;not null;default:''"`
	UpstreamModel string          `gorm:"column:upstream_model;size:128;not null;default:''"`
	UpstreamPath  string          `gorm:"column:upstream_path;size:256;not null;default:''"`
	SecretRef     string          `gorm:"column:secret_ref;size:256;not null;default:''"`
	LatestRevision string         `gorm:"column:latest_revision;size:64;not null;default:''"`
	OwnerType     string          `gorm:"column:owner_type;size:32;not null;default:'user'"`
	OwnerID       string          `gorm:"column:owner_id;size:128;not null;default:''"`
	OwnerName     string          `gorm:"column:owner_name;size:256;not null;default:''"`
	OrgID         string          `gorm:"column:org_id;size:128;not null;default:''"`
	ProjectID     string          `gorm:"column:project_id;size:128;not null;default:''"`
	LabelsJSON    json.RawMessage `gorm:"column:labels_json;type:jsonb;not null;default:'{}'::jsonb"`
	ObjectRef     string          `gorm:"column:object_ref;size:256;not null;default:''"`
	CreatedAt     time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt     time.Time       `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt     *time.Time      `gorm:"column:deleted_at"`
}

func (aihubModelProfileModel) TableName() string { return "aihub_model_profiles" }

// aihubModelProfileRevisionModel maps to aihub_model_profile_revisions.
type aihubModelProfileRevisionModel struct {
	ID                    int64           `gorm:"primaryKey;column:id;autoIncrement"`
	ProfileID             string          `gorm:"column:profile_id;size:128;not null;index:idx_aihub_model_profile_revisions_profile"`
	Revision              string          `gorm:"column:revision;size:64;not null"`
	Provider              string          `gorm:"column:provider;size:64;not null;default:''"`
	APIFormat             string          `gorm:"column:api_format;size:64;not null;default:''"`
	Endpoint              string          `gorm:"column:endpoint;size:256;not null;default:''"`
	ModelName             string          `gorm:"column:model_name;size:128;not null;default:''"`
	UpstreamModel         string          `gorm:"column:upstream_model;size:128;not null;default:''"`
	UpstreamPath          string          `gorm:"column:upstream_path;size:256;not null;default:''"`
	SecretRef             string          `gorm:"column:secret_ref;size:256;not null;default:''"`
	AllowedToolsJSON      json.RawMessage `gorm:"column:allowed_tools_json;type:jsonb;not null;default:'[]'::jsonb"`
	LimitsJSON            json.RawMessage `gorm:"column:limits_json;type:jsonb;not null;default:'{}'::jsonb"`
	ReasoningJSON         json.RawMessage `gorm:"column:reasoning_json;type:jsonb;not null;default:'{}'::jsonb"`
	DefaultParametersJSON json.RawMessage `gorm:"column:default_parameters_json;type:jsonb;not null;default:'{}'::jsonb"`
	MetadataJSON          json.RawMessage `gorm:"column:metadata_json;type:jsonb;not null;default:'{}'::jsonb"`
	SHA256                string          `gorm:"column:sha256;size:64;not null;default:''"`
	Author                string          `gorm:"column:author;size:128;not null;default:''"`
	CommitMsg             string          `gorm:"column:commit_msg;type:text;not null;default:''"`
	CreatedAt             time.Time       `gorm:"column:created_at;not null;autoCreateTime"`
}

func (aihubModelProfileRevisionModel) TableName() string { return "aihub_model_profile_revisions" }

// modelProfileRepo implements biz.ModelProfileRepository backed by Postgres.
type modelProfileRepo struct {
	db func(context.Context) *gorm.DB
}

// NewModelProfileRepo builds a biz.ModelProfileRepository from Resources.
func NewModelProfileRepo(resources *Resources) biz.ModelProfileRepository {
	return &modelProfileRepo{
		db: func(ctx context.Context) *gorm.DB {
			if resources == nil || resources.DB == nil {
				return nil
			}
			return resources.DB.GORM(ctx)
		},
	}
}

func (r *modelProfileRepo) List(ctx context.Context, opts biz.ModelProfileListOptions) ([]*biz.ModelProfile, string, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, "", errors.New("model profile repository database is not configured")
	}
	q := database.Model(&aihubModelProfileModel{}).Where("deleted_at IS NULL")
	if v := strings.TrimSpace(opts.OrgID); v != "" {
		q = q.Where("org_id = ?", v)
	}
	if v := strings.TrimSpace(opts.Query); v != "" {
		like := "%" + v + "%"
		q = q.Where("profile_id LIKE ? OR display_name LIKE ? OR description LIKE ?", like, like, like)
	}
	if opts.Status != "" {
		q = q.Where("status = ?", opts.Status)
	}
	if opts.Provider != "" {
		q = q.Where("provider = ?", opts.Provider)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var rows []aihubModelProfileModel
	if err := q.Order("profile_id ASC").Offset(opts.Offset).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]*biz.ModelProfile, 0, len(rows))
	for i := range rows {
		p, err := r.loadRevisions(database, &rows[i])
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	next := ""
	if hasMore {
		next = fmt.Sprintf("%d", opts.Offset+limit)
	}
	return out, next, nil
}

func (r *modelProfileRepo) Get(ctx context.Context, id string, version string) (*biz.ModelProfile, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("model profile repository database is not configured")
	}
	var row aihubModelProfileModel
	err := database.Where("profile_id = ? AND deleted_at IS NULL", strings.TrimSpace(id)).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrModelProfileNotFound
		}
		return nil, err
	}
	p, err := r.loadRevisions(database, &row)
	if err != nil {
		return nil, err
	}
	// If a specific version was requested, verify it exists.
	if version != "" {
		if _, ok := p.Revisions[version]; !ok {
			return nil, fmt.Errorf("%w: version %q not found", biz.ErrModelProfileNotFound, version)
		}
	}
	return p, nil
}

func (r *modelProfileRepo) Create(ctx context.Context, p *biz.ModelProfile) (*biz.ModelProfile, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("model profile repository database is not configured")
	}
	model := modelProfileBizToModel(p)
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model).Error; err != nil {
			return err
		}
		return writeModelProfileRevisionRows(tx, p)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, p.ID, "")
}

func (r *modelProfileRepo) Update(ctx context.Context, p *biz.ModelProfile) (*biz.ModelProfile, error) {
	database := r.db(ctx)
	if database == nil {
		return nil, errors.New("model profile repository database is not configured")
	}
	err := database.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"display_name":    p.DisplayName,
			"description":     p.Description,
			"status":          p.Status,
			"provider":        p.Provider,
			"api_format":      p.APIFormat,
			"endpoint":        p.Endpoint,
			"model_name":      p.Model,
			"upstream_model":  p.UpstreamModel,
			"upstream_path":   p.UpstreamPath,
			"secret_ref":      p.SecretRef,
			"latest_revision": p.Version,
			"labels_json":     labelsToJSON(p.Labels),
			"updated_at":      time.Now().UTC(),
		}
		res := tx.Model(&aihubModelProfileModel{}).
			Where("profile_id = ? AND deleted_at IS NULL", p.ID).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return biz.ErrModelProfileNotFound
		}
		return writeModelProfileRevisionRows(tx, p)
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, p.ID, "")
}

func (r *modelProfileRepo) Delete(ctx context.Context, id string) error {
	database := r.db(ctx)
	if database == nil {
		return errors.New("model profile repository database is not configured")
	}
	now := time.Now().UTC()
	res := database.Model(&aihubModelProfileModel{}).
		Where("profile_id = ? AND deleted_at IS NULL", strings.TrimSpace(id)).
		Update("deleted_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return biz.ErrModelProfileNotFound
	}
	return nil
}

// loadRevisions attaches revision records to a profile model. JSON-only
// fields (limits/allowed_tools/reasoning/default_parameters/metadata) live
// exclusively in the revision rows, so the latest revision's values are
// merged back into the flat profile the proto surface exposes inline.
func (r *modelProfileRepo) loadRevisions(db *gorm.DB, row *aihubModelProfileModel) (*biz.ModelProfile, error) {
	p := modelProfileModelToBiz(row)
	var vrows []aihubModelProfileRevisionModel
	if err := db.Model(&aihubModelProfileRevisionModel{}).
		Where("profile_id = ?", row.ProfileID).
		Order("created_at DESC").
		Find(&vrows).Error; err != nil {
		return nil, err
	}
	p.Revisions = make(map[string]biz.ModelProfileRevision, len(vrows))
	for _, v := range vrows {
		p.Revisions[v.Revision] = modelProfileRevisionModelToBiz(v)
	}
	if latest, ok := p.Revisions[row.LatestRevision]; ok {
		p.Limits = latest.Limits
		p.AllowedTools = latest.AllowedTools
		p.Reasoning = latest.Reasoning
		p.DefaultParameters = latest.DefaultParameters
		p.Metadata = latest.Metadata
	}
	return p, nil
}

// writeModelProfileRevisionRows inserts revision rows that do not yet exist
// (ON CONFLICT do nothing via count-then-insert) so updates are idempotent.
func writeModelProfileRevisionRows(tx *gorm.DB, p *biz.ModelProfile) error {
	for ver, v := range p.Revisions {
		var count int64
		if err := tx.Model(&aihubModelProfileRevisionModel{}).
			Where("profile_id = ? AND revision = ?", p.ID, ver).
			Count(&count).Error; err != nil {
			return err
		}
		row := modelProfileRevisionBizToModel(p.ID, v)
		if count > 0 {
			// Existing revisions are immutable: do not overwrite. (Create
			// re-runs with the same version label are no-ops.)
			continue
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// --- biz <-> model conversion ---

func modelProfileBizToModel(p *biz.ModelProfile) aihubModelProfileModel {
	return aihubModelProfileModel{
		ProfileID:      p.ID,
		DisplayName:    p.DisplayName,
		Description:    p.Description,
		Status:         p.Status,
		Provider:       p.Provider,
		APIFormat:      p.APIFormat,
		Endpoint:       p.Endpoint,
		ModelName:      p.Model,
		UpstreamModel:  p.UpstreamModel,
		UpstreamPath:   p.UpstreamPath,
		SecretRef:      p.SecretRef,
		LatestRevision: p.Version,
		OwnerType:      p.OwnerType,
		OwnerID:        p.OwnerID,
		OwnerName:      p.OwnerName,
		OrgID:          p.OrgID,
		ProjectID:      p.ProjectID,
		LabelsJSON:     labelsToJSON(p.Labels),
		ObjectRef:      p.Object,
	}
}

func modelProfileModelToBiz(m *aihubModelProfileModel) *biz.ModelProfile {
	return &biz.ModelProfile{
		ID:            m.ProfileID,
		Version:       m.LatestRevision,
		Status:        m.Status,
		DisplayName:   m.DisplayName,
		Description:   m.Description,
		Provider:      m.Provider,
		APIFormat:     m.APIFormat,
		Endpoint:      m.Endpoint,
		Model:         m.ModelName,
		UpstreamModel: m.UpstreamModel,
		UpstreamPath:  m.UpstreamPath,
		SecretRef:     m.SecretRef,
		Labels:        labelsFromJSON(m.LabelsJSON),
		Object:        m.ObjectRef,
		OwnerType:     m.OwnerType,
		OwnerID:       m.OwnerID,
		OwnerName:     m.OwnerName,
		OrgID:         m.OrgID,
		ProjectID:     m.ProjectID,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func modelProfileRevisionBizToModel(profileID string, v biz.ModelProfileRevision) aihubModelProfileRevisionModel {
	return aihubModelProfileRevisionModel{
		ProfileID:             profileID,
		Revision:              v.Revision,
		Provider:              v.Provider,
		APIFormat:             v.APIFormat,
		Endpoint:              v.Endpoint,
		ModelName:             v.Model,
		UpstreamModel:         v.UpstreamModel,
		UpstreamPath:          v.UpstreamPath,
		SecretRef:             v.SecretRef,
		AllowedToolsJSON:      stringSliceToJSON(v.AllowedTools),
		LimitsJSON:            limitsToJSON(v.Limits),
		ReasoningJSON:         stringToJSONRaw(v.Reasoning),
		DefaultParametersJSON: stringToJSONRaw(v.DefaultParameters),
		MetadataJSON:          stringToJSONRaw(v.Metadata),
		SHA256:                v.SHA256,
		Author:                v.Author,
		CommitMsg:             v.CommitMsg,
		CreatedAt:             v.CreateTime,
	}
}

func modelProfileRevisionModelToBiz(m aihubModelProfileRevisionModel) biz.ModelProfileRevision {
	return biz.ModelProfileRevision{
		Revision:          m.Revision,
		Provider:          m.Provider,
		APIFormat:         m.APIFormat,
		Endpoint:          m.Endpoint,
		Model:             m.ModelName,
		UpstreamModel:     m.UpstreamModel,
		UpstreamPath:      m.UpstreamPath,
		SecretRef:         m.SecretRef,
		AllowedTools:      stringSliceFromJSON(m.AllowedToolsJSON),
		Limits:            limitsFromJSON(m.LimitsJSON),
		Reasoning:         jsonRawToString(m.ReasoningJSON),
		DefaultParameters: jsonRawToString(m.DefaultParametersJSON),
		Metadata:          jsonRawToString(m.MetadataJSON),
		SHA256:            m.SHA256,
		Author:            m.Author,
		CommitMsg:          m.CommitMsg,
		CreateTime:        m.CreatedAt,
	}
}

// --- json helpers ---

func stringSliceToJSON(ss []string) json.RawMessage {
	if len(ss) == 0 {
		return json.RawMessage("[]")
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

func stringSliceFromJSON(raw json.RawMessage) []string {
	out := []string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func limitsToJSON(l biz.ModelProfileLimits) json.RawMessage {
	b, err := json.Marshal(l)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func limitsFromJSON(raw json.RawMessage) biz.ModelProfileLimits {
	var l biz.ModelProfileLimits
	if len(raw) == 0 {
		return l
	}
	_ = json.Unmarshal(raw, &l)
	return l
}

// stringToJSONRaw converts a JSON string to json.RawMessage. Empty/invalid →
// "{}" so the NOT NULL DEFAULT column invariant holds.
func stringToJSONRaw(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// jsonRawToString converts a json.RawMessage back to a string. Empty → "".
func jsonRawToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}
