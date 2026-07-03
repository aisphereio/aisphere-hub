// Package data skill module — PostgreSQL + objectstore implementation of
// biz.SkillRepo.
//
// This file is the only place that imports gorm + objectstorex. It maps
// between biz domain types (Skill, SkillVersion, SkillFile) and GORM
// models (skillModel, skillVersionModel, skillFileModel), and uses kernel
// dbx.DB for transactions + error normalization.
//
// Key design choices:
//
//  1. **dbx.DB.GORM(ctx)** is used everywhere so the same ctx-scoped tx
//     propagates through InjectDB/InjectTx. This lets biz call multiple
//     repo methods in one tx without threading the tx explicitly.
//
//  2. **dbx errors** are auto-normalized by the postgres driver's
//     mapError: 23505 → ErrDuplicateKey, 42P01 → ErrSchemaNotReady,
//     23503 → ErrForeignKeyViolation. We translate those further into
//     biz-level sentinels (ErrSkillAlreadyExists, etc.) so the service
//     layer sees stable codes regardless of the underlying driver.
//
//  3. **logx.FromContext(ctx)** is used for all logging so request-scoped
//     fields (request_id, trace_id, principal) are auto-attached. We use
//     Warn level for expected failures (skill not found, duplicate name)
//     and Error level for unexpected DB errors.
//
//  4. **objectstorex** is optional: when resources.ObjectStore is nil,
//     package bytes are stored in the version row's revision field as a
//     data: URL. This is only suitable for dev/test; production MUST
//     configure MinIO/S3.
//
//  5. **Compensating S3 deletes**: when SaveSkillPackage's DB transaction
//     fails AFTER objects were uploaded, we best-effort delete the
//     uploaded objects so we don't orphan them. The cleanup uses a fresh
//     context (not the request ctx) so client-disconnect does not abort
//     the cleanup halfway through.

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/objectstorex"

	"gorm.io/gorm"
)

// --- GORM models ---
//
// Field tags match the schema in migrations/postgres/000001_create_aihub_skills.sql.
// JSONB columns are exposed as []byte on the model and converted to/from
// string / []string on the biz layer.

type skillModel struct {
	ID           int64          `gorm:"primaryKey;autoIncrement;column:id"`
	Name         string         `gorm:"column:name;size:128;uniqueIndex;not null"`
	DisplayName  string         `gorm:"column:display_name;size:256;not null;default:''"`
	Description  string         `gorm:"column:description;type:text;not null;default:''"`
	Version      string         `gorm:"column:version;size:64;not null;default:''"`
	Status       string         `gorm:"column:status;size:32;index;not null;default:'active'"`
	Visibility   string         `gorm:"column:visibility;size:32;not null;default:'private'"`
	OwnerID      string         `gorm:"column:owner_id;size:128;not null;default:''"`
	OrgID        string         `gorm:"column:org_id;size:128;index;not null;default:''"`
	ProjectID    string         `gorm:"column:project_id;size:128;not null;default:''"`
	SourceType   string         `gorm:"column:source_type;size:32;not null;default:''"`
	SourceURI    string         `gorm:"column:source_uri;type:text;not null;default:''"`
	ManifestJSON []byte         `gorm:"column:manifest_json;type:jsonb;not null;default:'{}'::jsonb"`
	TagsJSON     []byte         `gorm:"column:tags;type:jsonb;not null;default:'[]'::jsonb"`
	CreatedAt    time.Time      `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

type skillVersionModel struct {
	ID                  int64          `gorm:"primaryKey;autoIncrement;column:id"`
	SkillName           string         `gorm:"column:skill_name;size:128;index;not null"`
	Version             string         `gorm:"column:version;size:64;not null"`
	Status              string         `gorm:"column:status;size:32;index;not null;default:'draft'"`
	Author              string         `gorm:"column:author;size:128;not null;default:''"`
	CommitMsg           string         `gorm:"column:commit_msg;type:text;not null;default:''"`
	PublishPipelineInfo string         `gorm:"column:publish_pipeline_info;type:text;not null;default:''"`
	DownloadCount       int64          `gorm:"column:download_count;not null;default:0"`
	MD5                 string         `gorm:"column:md5;size:64;not null;default:''"`
	SHA256              string         `gorm:"column:sha256;size:128;not null;default:''"`
	Revision            string         `gorm:"column:revision;type:text;not null;default:''"`
	SizeBytes           int64          `gorm:"column:size_bytes;not null;default:0"`
	ManifestJSON        []byte         `gorm:"column:manifest_json;type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAt           time.Time      `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt           time.Time      `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt           gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

type skillFileModel struct {
	ID        int64          `gorm:"primaryKey;autoIncrement;column:id"`
	SkillName string         `gorm:"column:skill_name;size:128;index;not null"`
	Version   string         `gorm:"column:version;size:64;index;not null"`
	Path      string         `gorm:"column:path;size:512;not null"`
	Name      string         `gorm:"column:name;size:256;not null;default:''"`
	Type      string         `gorm:"column:type;size:128;not null;default:''"`
	Size      int64          `gorm:"column:size;not null;default:0"`
	Binary    bool           `gorm:"column:binary;not null;default:false"`
	Content   string         `gorm:"column:content;type:text;not null;default:''"`
	CreatedAt time.Time      `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt time.Time      `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (skillModel) TableName() string        { return "aihub_skills" }
func (skillVersionModel) TableName() string { return "aihub_skill_versions" }
func (skillFileModel) TableName() string    { return "aihub_skill_files" }

// --- Repo ---

type skillRepo struct {
	resources *Resources
}

// NewSkillRepo creates a new biz.SkillRepo backed by kernel dbx.DB +
// kernel objectstorex.Client. Both are optional: when resources.DB is
// nil, every method returns ErrSkillInvalidArgument with a clear message;
// when resources.ObjectStore is nil, package bytes are stored inline in
// the version row (dev only).
func NewSkillRepo(resources *Resources) biz.SkillRepo {
	return &skillRepo{resources: resources}
}

// db returns the *gorm.DB for ctx, or nil if the database is not
// configured. Callers MUST nil-check before use.
func (r *skillRepo) db(ctx context.Context) *gorm.DB {
	if r == nil || r.resources == nil || r.resources.DB == nil {
		return nil
	}
	return r.resources.DB.GORM(ctx)
}

func (r *skillRepo) logger(ctx context.Context) logx.Logger {
	fallback := logx.DefaultLogger().Named("skill.repo")
	if r != nil && r.resources != nil && r.resources.Logger != nil {
		fallback = r.resources.Logger.Named("skill.repo")
	}
	return logx.FromContextOr(ctx, fallback)
}

// --- Skill CRUD ---

func (r *skillRepo) CreateSkill(ctx context.Context, skill *biz.Skill) (*biz.Skill, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	row, err := skillToRow(skill)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(row.Version) == "" {
		row.Version = biz.DefaultSkillVersion
	}
	// Create skill row + initial draft version in one transaction.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return mapSkillDBError(err)
		}
		return createInitialSkillDraftInTx(tx, row)
	}); err != nil {
		if errors.Is(err, dbx.ErrDuplicateKey) {
			return nil, biz.ErrSkillAlreadyExists
		}
		r.logger(ctx).Warn("skill create failed",
			logx.String("name", skill.Name),
			logx.Err(err),
		)
		return nil, err
	}
	return rowToSkill(row), nil
}

func (r *skillRepo) UpdateSkill(ctx context.Context, skill *biz.Skill) (*biz.Skill, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	row, err := skillToRow(skill)
	if err != nil {
		return nil, err
	}
	// SECURITY: owner_id / visibility / status are NOT updated here. The
	// biz layer's normalizeSkillForUpdate already zeroed them; the data
	// layer additionally omits them from the update map as defense in
	// depth against a forged request.
	updates := map[string]any{
		"display_name":  row.DisplayName,
		"description":   row.Description,
		"version":       row.Version,
		"source_type":   row.SourceType,
		"source_uri":    row.SourceURI,
		"manifest_json": row.ManifestJSON,
		"tags":          row.TagsJSON,
		"updated_at":    time.Now(),
	}
	res := db.Model(&skillModel{}).Where("name = ?", row.Name).Updates(updates)
	if res.Error != nil {
		return nil, mapSkillDBError(res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, row.Name)
}

func (r *skillRepo) UpdateSkillVisibility(ctx context.Context, name, visibility string) (*biz.Skill, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	res := db.Model(&skillModel{}).Where("name = ?", name).Updates(map[string]any{
		"visibility": visibility,
		"updated_at": time.Now(),
	})
	if res.Error != nil {
		return nil, mapSkillDBError(res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, biz.ErrSkillNotFound
	}
	return r.GetSkill(ctx, name)
}

func (r *skillRepo) ListSkills(ctx context.Context, opts biz.SkillListOptions) (*biz.SkillListResult, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	q := db.Model(&skillModel{})
	if q2 := strings.TrimSpace(opts.Query); q2 != "" {
		like := "%" + q2 + "%"
		q = q.Where("name ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
	}
	if s := strings.TrimSpace(opts.Status); s != "" {
		q = q.Where("status = ?", s)
	}
	if v := strings.TrimSpace(opts.Visibility); v != "" {
		q = q.Where("visibility = ?", v)
	}
	if opts.OnlyOnline {
		q = q.Where(`EXISTS (
			SELECT 1 FROM aihub_skill_versions v
			WHERE v.skill_name = aihub_skills.name
			  AND v.status = ?
			  AND v.deleted_at IS NULL
		)`, biz.SkillVersionStatusOnline)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var rows []skillModel
	if err := q.Order("id ASC").Offset(opts.Offset).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, mapSkillDBError(err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]*biz.Skill, 0, len(rows))
	for i := range rows {
		items = append(items, rowToSkill(&rows[i]))
	}
	return &biz.SkillListResult{
		Items:      items,
		NextOffset: opts.Offset + len(items),
		HasMore:    hasMore,
	}, nil
}

func (r *skillRepo) GetSkill(ctx context.Context, name string) (*biz.Skill, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var row skillModel
	err := db.Where("name = ?", name).First(&row).Error
	if err == nil {
		return rowToSkill(&row), nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrSkillNotFound
	}
	return nil, mapSkillDBError(err)
}

func (r *skillRepo) DeleteSkill(ctx context.Context, name string) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	// Collect S3 keys to purge AFTER the DB transaction commits. We collect
	// them here (before the soft-delete) because the rows would otherwise
	// be filtered out by GORM's implicit deleted_at IS NULL.
	var versionRows []skillVersionModel
	if err := db.Unscoped().Where("skill_name = ?", name).Find(&versionRows).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return mapSkillDBError(err)
	}
	var s3Keys []string
	for i := range versionRows {
		s3Keys = append(s3Keys, collectVersionObjectKeys(&versionRows[i])...)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("skill_name = ?", name).Delete(&skillFileModel{}).Error; err != nil {
			return mapSkillDBError(err)
		}
		if err := tx.Where("skill_name = ?", name).Delete(&skillVersionModel{}).Error; err != nil {
			return mapSkillDBError(err)
		}
		res := tx.Where("name = ?", name).Delete(&skillModel{})
		if res.Error != nil {
			return mapSkillDBError(res.Error)
		}
		if res.RowsAffected == 0 {
			return biz.ErrSkillNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Best-effort S3 cleanup AFTER the DB transaction commits. Failure
	// here does NOT roll back the DB delete (the user already sees the
	// skill as gone); orphaned S3 objects are recoverable via a separate
	// sweep job.
	r.cleanupS3ObjectsAsync(s3Keys)
	return nil
}

// --- SkillVersion ---

func (r *skillRepo) ListSkillVersions(ctx context.Context, name string) ([]*biz.SkillVersion, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var rows []skillVersionModel
	if err := db.Where("skill_name = ?", name).Order("version ASC").Find(&rows).Error; err != nil {
		return nil, mapSkillDBError(err)
	}
	out := make([]*biz.SkillVersion, 0, len(rows))
	for i := range rows {
		out = append(out, rowToSkillVersion(&rows[i]))
	}
	return out, nil
}

func (r *skillRepo) GetSkillVersion(ctx context.Context, name, version string) (*biz.SkillVersion, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var row skillVersionModel
	err := db.Where("skill_name = ? AND version = ?", name, version).First(&row).Error
	if err == nil {
		return rowToSkillVersion(&row), nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrSkillVersionNotFound
	}
	return nil, mapSkillDBError(err)
}

// UpdateSkillVersionStatus atomically transitions a version's status using
// a CAS (compare-and-set) pattern: the UPDATE matches only if the current
// status equals expectedStatus. This closes the TOCTOU window between the
// biz layer's read of the current status and this write — two concurrent
// transition calls on the same version will not both succeed.
//
// When newStatus is "online", any previously-online version of the same
// skill is demoted to "published" within the same transaction.
func (r *skillRepo) UpdateSkillVersionStatus(ctx context.Context, name, version, expectedStatus, newStatus string) (*biz.SkillVersion, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&skillVersionModel{}).
			Where("skill_name = ? AND version = ? AND status = ?", name, version, expectedStatus).
			Updates(map[string]any{
				"status":     newStatus,
				"updated_at": time.Now(),
			})
		if res.Error != nil {
			return mapSkillDBError(res.Error)
		}
		if res.RowsAffected == 0 {
			// Either the row does not exist at all, or it exists but its
			// status no longer matches expectedStatus (concurrent
			// transition). Surface NotFound so the caller (biz layer)
			// maps it to a 409 Conflict.
			return biz.ErrSkillVersionNotFound
		}
		if newStatus == biz.SkillVersionStatusOnline {
			// Demote any other online version of the same skill.
			if err := tx.Model(&skillVersionModel{}).
				Where("skill_name = ? AND version <> ? AND status = ?", name, version, biz.SkillVersionStatusOnline).
				Updates(map[string]any{"status": biz.SkillVersionStatusPublished, "updated_at": time.Now()}).Error; err != nil {
				return mapSkillDBError(err)
			}
			// Update the skill row's current version pointer.
			if err := tx.Model(&skillModel{}).Where("name = ?", name).Updates(map[string]any{
				"version":    version,
				"status":     biz.SkillStatusActive,
				"updated_at": time.Now(),
			}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		if newStatus == biz.SkillVersionStatusOffline {
			if err := tx.Model(&skillModel{}).Where("name = ? AND version = ?", name, version).Updates(map[string]any{
				"status":     biz.SkillStatusArchived,
				"updated_at": time.Now(),
			}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.GetSkillVersion(ctx, name, version)
}

func (r *skillRepo) GetOnlineSkillVersion(ctx context.Context, name string) (*biz.SkillVersion, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var row skillVersionModel
	err := db.Where("skill_name = ? AND status = ?", name, biz.SkillVersionStatusOnline).Order("updated_at DESC").First(&row).Error
	if err == nil {
		return rowToSkillVersion(&row), nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrSkillVersionNotFound
	}
	return nil, mapSkillDBError(err)
}

// --- SkillFile ---

func (r *skillRepo) ListSkillVersionFiles(ctx context.Context, name, version string) ([]*biz.SkillFile, error) {
	if r.resources != nil && r.resources.ObjectStore != nil {
		out, err := r.listSkillVersionFilesFromManifest(ctx, name, version)
		if err == nil || !isSkillManifestMissing(err) {
			return out, err
		}
		// Backward compatibility for rows created before S3-first manifests.
	}
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var rows []skillFileModel
	if err := db.Where("skill_name = ? AND version = ?", name, version).Order("path ASC").Find(&rows).Error; err != nil {
		return nil, mapSkillDBError(err)
	}
	out := make([]*biz.SkillFile, 0, len(rows))
	for i := range rows {
		out = append(out, rowToSkillFile(&rows[i]))
	}
	return out, nil
}

func (r *skillRepo) GetSkillVersionFile(ctx context.Context, name, version, filePath string) (*biz.SkillFile, error) {
	if r.resources != nil && r.resources.ObjectStore != nil {
		out, err := r.getSkillVersionFileFromS3(ctx, name, version, filePath)
		if err == nil || !isSkillManifestMissing(err) {
			return out, err
		}
		// Backward compatibility for rows created before S3-first manifests.
	}
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	var row skillFileModel
	err := db.Where("skill_name = ? AND version = ? AND path = ?", name, version, filePath).First(&row).Error
	if err == nil {
		return rowToSkillFile(&row), nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, biz.ErrSkillFileNotFound
	}
	return nil, mapSkillDBError(err)
}

// --- Package upload / download ---

// downloadSkillPackageMaxBytes caps the in-memory size of a downloaded
// skill package. Matches the upload cap in skillzip.MaxUploadBytes (20MiB)
// plus headroom for the zip container overhead.
const downloadSkillPackageMaxBytes = 32 * 1024 * 1024 // 32 MiB

func (r *skillRepo) SaveSkillPackage(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, files []*biz.SkillFile, packageBytes []byte, overwrite bool) (*biz.SkillVersion, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	log := r.logger(ctx)
	_ = log
	return r.saveSkillPackageS3First(ctx, skill, version, files, packageBytes, overwrite)

	// 1. Upload to object store FIRST (when configured). Remember the key
	// so we can compensating-delete on DB failure.
	var s3Key string
	if r.resources != nil && r.resources.ObjectStore != nil {
		s3Key = skillPackageObjectKey(skill.Name, version.Version)
		if err := r.putObject(ctx, s3Key, packageBytes, "application/zip"); err != nil {
			log.Warn("skill package s3 upload failed",
				logx.String("name", skill.Name),
				logx.String("version", version.Version),
				logx.Err(err),
			)
			return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_UPLOAD_FAILED"),
				errorx.WithMessage("failed to upload skill package to object store"))
		}
		skill.SourceURI = "objectstore://" + s3Key
		version.Revision = s3Key
	} else {
		// Dev fallback: store the package bytes inline in revision as a
		// data URL. This is only suitable for local testing — production
		// MUST configure object store.
		version.Revision = "data:application/zip;base64," + base64EncodeBytes(packageBytes)
		log.Warn("skill package stored inline (no object store configured); "+
			"this is dev-only and will not scale to production",
			logx.String("name", skill.Name),
			logx.String("version", version.Version),
		)
	}

	// 2. Persist skill row (upsert if name exists) + version row + file
	// rows in one transaction. On failure, compensating-delete the S3
	// object so we don't orphan it.
	skillRow, err := skillToRow(skill)
	if err != nil {
		r.compensateS3Delete(ctx, s3Key)
		return nil, err
	}
	versionRow, err := skillVersionToRow(version)
	if err != nil {
		r.compensateS3Delete(ctx, s3Key)
		return nil, err
	}
	fileRows := make([]skillFileModel, 0, len(files))
	for _, f := range files {
		row, err := skillFileToRow(f)
		if err != nil {
			r.compensateS3Delete(ctx, s3Key)
			return nil, err
		}
		fileRows = append(fileRows, *row)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		// Upsert skill row. We use OnConflict DoNothing on the name
		// unique index so a re-upload to an existing skill does not fail
		// — the existing skill row is kept (ownership was already checked
		// in biz). The unique constraint is on `name`.
		if err := tx.Where("name = ?", skillRow.Name).FirstOrCreate(skillRow).Error; err != nil {
			return mapSkillDBError(err)
		}
		// Update skill row's display_name / description / version from
		// the latest upload (these are mutable on upload).
		if err := tx.Model(skillRow).Updates(map[string]any{
			"display_name":  skillRow.DisplayName,
			"description":   skillRow.Description,
			"version":       skillRow.Version,
			"source_type":   skillRow.SourceType,
			"source_uri":    skillRow.SourceURI,
			"manifest_json": skillRow.ManifestJSON,
			"tags":          skillRow.TagsJSON,
			"updated_at":    time.Now(),
		}).Error; err != nil {
			return mapSkillDBError(err)
		}

		// Version row: insert or (when overwrite=true) replace.
		if overwrite {
			if err := tx.Where("skill_name = ? AND version = ?", versionRow.SkillName, versionRow.Version).
				Delete(&skillFileModel{}).Error; err != nil {
				return mapSkillDBError(err)
			}
			if err := tx.Where("skill_name = ? AND version = ?", versionRow.SkillName, versionRow.Version).
				Delete(&skillVersionModel{}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		if err := tx.Create(versionRow).Error; err != nil {
			if errors.Is(mapSkillDBError(err), dbx.ErrDuplicateKey) {
				return biz.ErrSkillVersionAlreadyExists
			}
			return mapSkillDBError(err)
		}

		// File rows: bulk insert.
		if len(fileRows) > 0 {
			if err := tx.Create(&fileRows).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		return nil
	})
	if err != nil {
		r.compensateS3Delete(ctx, s3Key)
		return nil, err
	}

	log.Info("skill package saved",
		logx.String("name", skill.Name),
		logx.String("version", version.Version),
		logx.Int64("size", version.SizeBytes),
		logx.Int("files", len(fileRows)),
	)
	return rowToSkillVersion(versionRow), nil
}

func (r *skillRepo) DownloadSkillPackage(ctx context.Context, name, version, ifNoneMatch string) (*biz.SkillPackageDownload, error) {
	if r.resources != nil && r.resources.ObjectStore != nil {
		return r.downloadSkillPackageS3First(ctx, name, version, ifNoneMatch)
	}
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	log := r.logger(ctx)

	// 1. Load version metadata for ETag check.
	meta, err := r.GetSkillVersion(ctx, name, version)
	if err != nil {
		return nil, err
	}
	etag := meta.SHA256
	if ifNoneMatch != "" && ifNoneMatch == etag {
		return &biz.SkillPackageDownload{
			SkillName:   name,
			Version:     version,
			ETag:        etag,
			MD5:         meta.MD5,
			SHA256:      meta.SHA256,
			NotModified: true,
		}, nil
	}

	// 2. Fetch package bytes.
	var packageBytes []byte
	if r.resources != nil && r.resources.ObjectStore != nil && strings.HasPrefix(meta.Revision, "objectstore://") {
		key := strings.TrimPrefix(meta.Revision, "objectstore://")
		rc, _, err := r.resources.ObjectStore.GetObject(ctx, key, objectstorex.GetOptions{})
		if err != nil {
			if errors.Is(err, objectstorex.ErrNotFound) {
				return nil, errorx.Wrap(biz.ErrSkillVersionNotFound, errorx.Code("SKILL_PACKAGE_MISSING"),
					errorx.WithMessage("skill package object not found in object store"))
			}
			log.Warn("skill package s3 download failed",
				logx.String("name", name),
				logx.String("version", version),
				logx.Err(err),
			)
			return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_DOWNLOAD_FAILED"),
				errorx.WithMessage("failed to download skill package from object store"))
		}
		defer rc.Close()
		body, err := io.ReadAll(io.LimitReader(rc, downloadSkillPackageMaxBytes+1))
		if err != nil {
			return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_DOWNLOAD_FAILED"),
				errorx.WithMessage("failed to read skill package body"))
		}
		if int64(len(body)) > downloadSkillPackageMaxBytes {
			return nil, errorx.BadRequest(errorx.Code("SKILL_PACKAGE_TOO_LARGE"),
				"skill package exceeds download size limit")
		}
		packageBytes = body
	} else if strings.HasPrefix(meta.Revision, "data:application/zip;base64,") {
		// Dev fallback: inline data URL.
		encoded := strings.TrimPrefix(meta.Revision, "data:application/zip;base64,")
		packageBytes = base64DecodeBytes(encoded)
		if packageBytes == nil {
			return nil, errorx.Internal(errorx.Code("SKILL_PACKAGE_INVALID"),
				"inline skill package is not valid base64")
		}
	} else {
		return nil, errorx.NotFound(errorx.Code("SKILL_PACKAGE_MISSING"),
			"skill version has no downloadable package (revision is empty)")
	}

	return &biz.SkillPackageDownload{
		SkillName:    name,
		Version:      version,
		ETag:         etag,
		MD5:          meta.MD5,
		SHA256:       meta.SHA256,
		NotModified:  false,
		PackageBytes: packageBytes,
	}, nil
}

func (r *skillRepo) IncrementSkillVersionDownloadCount(ctx context.Context, name, version string) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	// Use GORM's gorm.Expr for an atomic server-side increment.
	return db.Model(&skillVersionModel{}).
		Where("skill_name = ? AND version = ?", name, version).
		UpdateColumn("download_count", gorm.Expr("download_count + 1")).Error
}

// --- object store helpers ---

func (r *skillRepo) putObject(ctx context.Context, key string, body []byte, contentType string) error {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return errors.New("object store is not configured")
	}
	reader := bytes.NewReader(body)
	_, err := r.resources.ObjectStore.PutObject(ctx, key, reader, int64(len(body)), objectstorex.PutOptions{
		ContentType: contentType,
	})
	return err
}

// compensateS3Delete best-effort deletes an S3 object after a DB
// transaction failure. Failure here is logged but not returned to the
// caller — the caller already has a DB error to surface, and the orphaned
// object is recoverable via a separate sweep job.
func (r *skillRepo) compensateS3Delete(ctx context.Context, key string) {
	if key == "" || r.resources == nil || r.resources.ObjectStore == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.resources.ObjectStore.DeleteObject(cleanupCtx, key); err != nil {
		r.logger(ctx).Warn("compensating s3 delete failed",
			logx.String("key", key),
			logx.Err(err),
		)
	}
}

// cleanupS3ObjectsAsync best-effort deletes a batch of S3 objects. Each
// delete is independent — one failure does not abort the others.
func (r *skillRepo) cleanupS3ObjectsAsync(keys []string) {
	if r.resources == nil || r.resources.ObjectStore == nil || len(keys) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		r.cleanupS3ObjectsSync(ctx, keys)
	}()
}

// skillPackageObjectKey builds the S3 object key for one skill version's
// package. Format: skills/{name}/versions/{version}/package.zip
func skillPackageObjectKey(skillName, version string) string {
	return fmt.Sprintf("skills/%s/versions/%s/package.zip", skillName, version)
}

// --- row <-> biz DO conversion ---

func skillToRow(skill *biz.Skill) (*skillModel, error) {
	if skill == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	manifest := []byte(strings.TrimSpace(skill.ManifestJSON))
	if len(manifest) == 0 {
		manifest = []byte(`{}`)
	}
	if !json.Valid(manifest) {
		return nil, biz.ErrSkillInvalidArgument
	}
	tagsValue := skill.Tags
	if tagsValue == nil {
		tagsValue = []string{}
	}
	tags, err := json.Marshal(tagsValue)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	createdAt := skill.CreateTime
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := skill.UpdateTime
	if updatedAt.IsZero() {
		updatedAt = now
	}
	return &skillModel{
		ID:           skill.ID,
		Name:         skill.Name,
		DisplayName:  skill.DisplayName,
		Description:  skill.Description,
		Version:      skill.Version,
		Status:       skill.Status,
		Visibility:   skill.Visibility,
		OwnerID:      skill.OwnerID,
		OrgID:        skill.OrgID,
		ProjectID:    skill.ProjectID,
		SourceType:   skill.SourceType,
		SourceURI:    skill.SourceURI,
		ManifestJSON: manifest,
		TagsJSON:     tags,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}

func rowToSkill(row *skillModel) *biz.Skill {
	if row == nil {
		return nil
	}
	createdAt := row.CreatedAt
	updatedAt := row.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	return &biz.Skill{
		ID:           row.ID,
		Name:         row.Name,
		DisplayName:  row.DisplayName,
		Description:  row.Description,
		Version:      row.Version,
		Status:       row.Status,
		Visibility:   row.Visibility,
		OwnerID:      row.OwnerID,
		OrgID:        row.OrgID,
		ProjectID:    row.ProjectID,
		SourceType:   row.SourceType,
		SourceURI:    row.SourceURI,
		ManifestJSON: bytesToJSONString(row.ManifestJSON, "{}"),
		Tags:         decodeStringSlice(row.TagsJSON),
		CreateTime:   createdAt,
		UpdateTime:   updatedAt,
	}
}

func skillVersionToRow(version *biz.SkillVersion) (*skillVersionModel, error) {
	if version == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	manifest := []byte(strings.TrimSpace(version.ManifestJSON))
	if len(manifest) == 0 {
		manifest = []byte(`{}`)
	}
	if !json.Valid(manifest) {
		return nil, biz.ErrSkillInvalidArgument
	}
	now := time.Now()
	createdAt := version.CreateTime
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := version.UpdateTime
	if updatedAt.IsZero() {
		updatedAt = now
	}
	return &skillVersionModel{
		ID:                  version.ID,
		SkillName:           version.SkillName,
		Version:             version.Version,
		Status:              version.Status,
		Author:              version.Author,
		CommitMsg:           version.CommitMsg,
		PublishPipelineInfo: version.PublishPipelineInfo,
		DownloadCount:       version.DownloadCount,
		MD5:                 version.MD5,
		SHA256:              version.SHA256,
		Revision:            version.Revision,
		SizeBytes:           version.SizeBytes,
		ManifestJSON:        manifest,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
	}, nil
}

func rowToSkillVersion(row *skillVersionModel) *biz.SkillVersion {
	if row == nil {
		return nil
	}
	return &biz.SkillVersion{
		ID:                  row.ID,
		SkillName:           row.SkillName,
		Version:             row.Version,
		Status:              row.Status,
		Author:              row.Author,
		CommitMsg:           row.CommitMsg,
		PublishPipelineInfo: row.PublishPipelineInfo,
		DownloadCount:       row.DownloadCount,
		MD5:                 row.MD5,
		SHA256:              row.SHA256,
		Revision:            row.Revision,
		SizeBytes:           row.SizeBytes,
		ManifestJSON:        bytesToJSONString(row.ManifestJSON, "{}"),
		CreateTime:          row.CreatedAt,
		UpdateTime:          row.UpdatedAt,
	}
}

func skillFileToRow(file *biz.SkillFile) (*skillFileModel, error) {
	if file == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	now := time.Now()
	createdAt := file.CreateTime
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := file.UpdateTime
	if updatedAt.IsZero() {
		updatedAt = now
	}
	return &skillFileModel{
		ID:        file.ID,
		SkillName: file.SkillName,
		Version:   file.Version,
		Path:      file.Path,
		Name:      file.Name,
		Type:      file.Type,
		Size:      file.Size,
		Binary:    file.Binary,
		Content:   file.Content,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func rowToSkillFile(row *skillFileModel) *biz.SkillFile {
	if row == nil {
		return nil
	}
	return &biz.SkillFile{
		ID:         row.ID,
		SkillName:  row.SkillName,
		Version:    row.Version,
		Path:       row.Path,
		Name:       row.Name,
		Type:       row.Type,
		Size:       row.Size,
		Binary:     row.Binary,
		Content:    row.Content,
		CreateTime: row.CreatedAt,
		UpdateTime: row.UpdatedAt,
	}
}

func createInitialSkillDraftInTx(tx *gorm.DB, row *skillModel) error {
	// Create an empty draft version row so ListSkillVersions returns at
	// least one entry for a freshly-created skill. The draft has no
	// package bytes yet — those are added by UploadSkillPackage.
	draft := &skillVersionModel{
		SkillName:    row.Name,
		Version:      row.Version,
		Status:       biz.SkillVersionStatusDraft,
		Author:       row.OwnerID,
		ManifestJSON: []byte(`{}`),
	}
	if err := tx.Create(draft).Error; err != nil {
		// If the draft already exists (e.g. create skill called twice for
		// the same name due to a race), ignore the duplicate. The skill
		// row's unique constraint already protected us from the real
		// duplicate; this draft insert is best-effort.
		if !errors.Is(mapSkillDBError(err), dbx.ErrDuplicateKey) {
			return mapSkillDBError(err)
		}
	}
	return nil
}

// --- error mapping ---

// mapSkillDBError translates a raw DB error into a biz-friendly error.
// The kernel postgres/mysql driver already maps driver-specific error
// codes to dbx sentinels (ErrDuplicateKey, ErrSchemaNotReady, etc.), so
// we mostly just translate those into biz-level sentinels.
func mapSkillDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dbx.ErrDuplicateKey) {
		return biz.ErrSkillAlreadyExists
	}
	if errors.Is(err, dbx.ErrSchemaNotReady) {
		return errorx.Wrap(err, errorx.Code("SKILL_SCHEMA_NOT_READY"),
			errorx.WithMessage("aihub skill tables are not ready; run migrations"))
	}
	if errors.Is(err, dbx.ErrNoRows) {
		return biz.ErrSkillNotFound
	}
	return err
}

// errDBNotConfigured returns a clear error when resources.DB is nil. We
// use errorx.Unavailable so the service layer returns 503 instead of 500.
func errDBNotConfigured() error {
	return errorx.Unavailable(errorx.Code("DB_NOT_CONFIGURED"),
		"database is not configured; set data.database.enabled=true in config")
}

// --- small helpers ---

func bytesToJSONString(b []byte, fallback string) string {
	if len(b) == 0 {
		return fallback
	}
	return string(b)
}

func decodeStringSlice(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// base64EncodeBytes / base64DecodeBytes are thin wrappers around
// encoding/base64 so the dev fallback (no object store) can store
// package bytes inline in the revision column.
func base64EncodeBytes(b []byte) string {
	// Implemented in skill_base64.go to keep this file focused on DB logic.
	return base64StdEncodingEncode(b)
}

func base64DecodeBytes(s string) []byte {
	return base64StdEncodingDecode(s)
}
