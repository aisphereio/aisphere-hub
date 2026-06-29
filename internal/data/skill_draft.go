package data

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/dtmx"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/objectstorex"

	"gorm.io/gorm"
)

const (
	skillDraftKindFile      = "file"
	skillDraftKindDirectory = "directory"
	skillDraftTextType      = "text/plain; charset=utf-8"
)

type skillDraftFileModel struct {
	ID          int64          `gorm:"primaryKey;autoIncrement;column:id"`
	SkillName   string         `gorm:"column:skill_name;size:128;index;not null"`
	Version     string         `gorm:"column:version;size:64;index;not null"`
	Path        string         `gorm:"column:path;size:1024;not null"`
	Name        string         `gorm:"column:name;size:256;not null;default:''"`
	Kind        string         `gorm:"column:kind;size:32;not null;default:'file'"`
	ContentType string         `gorm:"column:content_type;size:128;not null;default:''"`
	SizeBytes   int64          `gorm:"column:size_bytes;not null;default:0"`
	Binary      bool           `gorm:"column:binary;not null;default:false"`
	SHA256      string         `gorm:"column:sha256;size:128;not null;default:''"`
	ObjectKey   string         `gorm:"column:object_key;type:text;not null;default:''"`
	CreatedBy   string         `gorm:"column:created_by;size:128;not null;default:''"`
	UpdatedBy   string         `gorm:"column:updated_by;size:128;not null;default:''"`
	CreatedAt   time.Time      `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;not null;autoUpdateTime"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (skillDraftFileModel) TableName() string { return "aihub_skill_draft_files" }

type skillDraftFilePayload struct {
	GID         string `json:"gid"`
	SkillName   string `json:"skill_name"`
	Version     string `json:"version"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Binary      bool   `json:"binary"`
	SHA256      string `json:"sha256"`
	TempKey     string `json:"temp_key"`
	ObjectKey   string `json:"object_key"`
	Actor       string `json:"actor"`
}

func (r *skillRepo) ListSkillDraftFiles(ctx context.Context, name, version string) ([]*biz.SkillFile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = biz.DefaultSkillVersion
	}
	var rows []skillDraftFileModel
	if err := db.Where("skill_name = ? AND version = ?", name, version).Order("kind DESC, path ASC").Find(&rows).Error; err != nil {
		return nil, mapSkillDBError(err)
	}
	out := make([]*biz.SkillFile, 0, len(rows))
	for i := range rows {
		out = append(out, draftRowToSkillFile(&rows[i], false))
	}
	return out, nil
}

func (r *skillRepo) GetSkillDraftFile(ctx context.Context, name, version, filePath string) (*biz.SkillFile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	cleanPath, err := cleanSkillPath(filePath)
	if err != nil {
		return nil, err
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = biz.DefaultSkillVersion
	}
	var row skillDraftFileModel
	err = db.Where("skill_name = ? AND version = ? AND path = ?", name, version, cleanPath).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, biz.ErrSkillFileNotFound
		}
		return nil, mapSkillDBError(err)
	}
	out := draftRowToSkillFile(&row, false)
	if row.Kind == skillDraftKindDirectory {
		return out, nil
	}
	content, binary, err := r.readDraftObjectContent(ctx, row)
	if err != nil {
		return nil, err
	}
	out.Content = content
	out.Binary = binary
	return out, nil
}

func (r *skillRepo) UpsertSkillDraftFile(ctx context.Context, file *biz.SkillFile, actor string) (*biz.SkillFile, error) {
	if file == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	if err := r.ensureDraftVersionForWrite(ctx, file.SkillName, file.Version, actor); err != nil {
		return nil, err
	}
	cleanPath, err := cleanSkillPath(file.Path)
	if err != nil {
		return nil, err
	}
	kind := skillDraftKindFile
	if strings.EqualFold(strings.TrimSpace(file.Type), skillDraftKindDirectory) {
		kind = skillDraftKindDirectory
	}
	contentType := strings.TrimSpace(file.Type)
	if kind == skillDraftKindDirectory {
		return r.upsertDraftDirectory(ctx, file.SkillName, file.Version, cleanPath, actor)
	}
	body, binary, err := skillFileContentBytes(file)
	if err != nil {
		return nil, err
	}
	if contentType == "" || contentType == skillDraftKindFile {
		contentType = detectSkillContentType(cleanPath, body, binary)
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	finalKey := skillDraftObjectKey(file.SkillName, file.Version, sha)
	payload := skillDraftFilePayload{
		SkillName:   file.SkillName,
		Version:     file.Version,
		Path:        cleanPath,
		Name:        path.Base(cleanPath),
		Kind:        kind,
		ContentType: contentType,
		SizeBytes:   int64(len(body)),
		Binary:      binary,
		SHA256:      sha,
		ObjectKey:   finalKey,
		Actor:       actor,
	}
	if r.resources == nil || r.resources.ObjectStore == nil {
		return nil, errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill draft files require object store; enable data.object_store")
	}
	if r.resources.DTM != nil && r.resources.DTM.Enabled() {
		return r.upsertDraftFileWithDTM(ctx, payload, body)
	}
	return r.upsertDraftFileDirect(ctx, payload, body)
}

func (r *skillRepo) DeleteSkillDraftPath(ctx context.Context, name, version, filePath string, recursive bool) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	if err := r.ensureDraftVersion(ctx, name, version); err != nil {
		return err
	}
	cleanPath, err := cleanSkillPath(filePath)
	if err != nil {
		return err
	}
	q := db.Where("skill_name = ? AND version = ?", name, version)
	if recursive {
		q = q.Where("path = ? OR path LIKE ?", cleanPath, cleanPath+"/%")
	} else {
		q = q.Where("path = ?", cleanPath)
	}
	var rows []skillDraftFileModel
	if err := q.Find(&rows).Error; err != nil {
		return mapSkillDBError(err)
	}
	if len(rows) == 0 {
		return biz.ErrSkillFileNotFound
	}
	keys := make([]string, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
		if rows[i].ObjectKey != "" {
			keys = append(keys, rows[i].ObjectKey)
		}
	}
	if err := db.Where("id IN ?", ids).Delete(&skillDraftFileModel{}).Error; err != nil {
		return mapSkillDBError(err)
	}
	for _, key := range dedupeStrings(keys) {
		_ = r.cleanupDraftObjectIfUnreferenced(ctx, key)
	}
	return nil
}

func (r *skillRepo) MoveSkillDraftPath(ctx context.Context, name, version, oldPath, newPath string, overwrite bool) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	if err := r.ensureDraftVersion(ctx, name, version); err != nil {
		return err
	}
	oldClean, err := cleanSkillPath(oldPath)
	if err != nil {
		return err
	}
	newClean, err := cleanSkillPath(newPath)
	if err != nil {
		return err
	}
	if oldClean == newClean {
		return nil
	}
	var replacedKeys []string
	err = db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&skillDraftFileModel{}).Where("skill_name = ? AND version = ? AND (path = ? OR path LIKE ?)", name, version, oldClean, oldClean+"/%").Count(&existing).Error; err != nil {
			return mapSkillDBError(err)
		}
		if existing == 0 {
			return biz.ErrSkillFileNotFound
		}
		var conflicts int64
		if err := tx.Model(&skillDraftFileModel{}).Where("skill_name = ? AND version = ? AND (path = ? OR path LIKE ?)", name, version, newClean, newClean+"/%").Count(&conflicts).Error; err != nil {
			return mapSkillDBError(err)
		}
		if conflicts > 0 && !overwrite {
			return errorx.Conflict(errorx.Code("SKILL_DRAFT_PATH_EXISTS"), "target path already exists")
		}
		if conflicts > 0 {
			var replaced []skillDraftFileModel
			if err := tx.Where("skill_name = ? AND version = ? AND (path = ? OR path LIKE ?)", name, version, newClean, newClean+"/%").Find(&replaced).Error; err != nil {
				return mapSkillDBError(err)
			}
			for i := range replaced {
				if replaced[i].ObjectKey != "" {
					replacedKeys = append(replacedKeys, replaced[i].ObjectKey)
				}
			}
			if err := tx.Unscoped().Where("skill_name = ? AND version = ? AND (path = ? OR path LIKE ?)", name, version, newClean, newClean+"/%").Delete(&skillDraftFileModel{}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		var rows []skillDraftFileModel
		if err := tx.Where("skill_name = ? AND version = ? AND (path = ? OR path LIKE ?)", name, version, oldClean, oldClean+"/%").Find(&rows).Error; err != nil {
			return mapSkillDBError(err)
		}
		for i := range rows {
			updatedPath := newClean
			if rows[i].Path != oldClean {
				updatedPath = newClean + strings.TrimPrefix(rows[i].Path, oldClean)
			}
			if err := tx.Model(&skillDraftFileModel{}).Where("id = ?", rows[i].ID).Updates(map[string]any{"path": updatedPath, "name": path.Base(updatedPath), "updated_at": time.Now()}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range dedupeStrings(replacedKeys) {
		_ = r.cleanupDraftObjectIfUnreferenced(ctx, key)
	}
	return nil
}

func (r *skillRepo) BuildSkillPackageFromDraft(ctx context.Context, name, version string) ([]byte, []*biz.SkillFile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, nil, errDBNotConfigured()
	}
	if err := r.ensureDraftVersion(ctx, name, version); err != nil {
		return nil, nil, err
	}
	var rows []skillDraftFileModel
	if err := db.Where("skill_name = ? AND version = ?", name, version).Order("path ASC").Find(&rows).Error; err != nil {
		return nil, nil, mapSkillDBError(err)
	}
	if len(rows) == 0 {
		return nil, nil, errorx.BadRequest(errorx.Code("SKILL_DRAFT_EMPTY"), "skill draft has no files")
	}
	files := make([]*biz.SkillFile, 0, len(rows))
	for _, row := range rows {
		if row.Kind == skillDraftKindDirectory {
			files = append(files, draftRowToSkillFile(&row, false))
			continue
		}
		content, binary, err := r.readDraftObjectContent(ctx, row)
		if err != nil {
			return nil, nil, err
		}
		f := draftRowToSkillFile(&row, false)
		f.Content = content
		f.Binary = binary
		files = append(files, f)
	}
	// Directory-first storage does not build a zip during commit. The first
	// return value is kept for interface compatibility and export downloads are
	// zipped on demand from the immutable version directory.
	return nil, files, nil
}

func (r *skillRepo) upsertDraftDirectory(ctx context.Context, name, version, dirPath, actor string) (*biz.SkillFile, error) {
	db := r.db(ctx)
	if db == nil {
		return nil, errDBNotConfigured()
	}
	payload := skillDraftFilePayload{SkillName: name, Version: version, Path: dirPath, Name: path.Base(dirPath), Kind: skillDraftKindDirectory, Actor: actor}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensureParentDraftDirs(ctx, tx, payload); err != nil {
			return err
		}
		return upsertDraftMetadataInTx(tx, payload)
	}); err != nil {
		return nil, err
	}
	return r.GetSkillDraftFile(ctx, name, version, dirPath)
}

func (r *skillRepo) upsertDraftFileWithDTM(ctx context.Context, payload skillDraftFilePayload, body []byte) (*biz.SkillFile, error) {
	gid, err := r.resources.DTM.NewGID(ctx)
	if err != nil {
		return nil, err
	}
	payload.GID = gid
	payload.TempKey = skillTempDraftObjectKey(gid, payload.SHA256)
	if err := r.putObject(ctx, payload.TempKey, body, payload.ContentType); err != nil {
		return nil, errorx.Wrap(err, errorx.Code("SKILL_DRAFT_STAGE_FAILED"), errorx.WithMessage("failed to stage skill draft file"))
	}
	saga := dtmx.NewSaga(gid, "skill.draft.file.upsert", dtmx.WithSagaWaitResult(true))
	saga = saga.AddHTTP("promote_draft_object", r.resources.DTM.BranchURL("/skill/draft/object/promote"), r.resources.DTM.BranchURL("/skill/draft/object/promote_compensate"), payload)
	saga = saga.AddHTTP("upsert_draft_metadata", r.resources.DTM.BranchURL("/skill/draft/metadata/upsert"), r.resources.DTM.BranchURL("/skill/draft/metadata/upsert_compensate"), payload)
	if _, err := r.resources.DTM.SubmitSaga(ctx, saga); err != nil {
		r.compensateS3Delete(ctx, payload.TempKey)
		return nil, err
	}
	go r.bestEffortDeleteObject(payload.TempKey)
	return r.GetSkillDraftFile(ctx, payload.SkillName, payload.Version, payload.Path)
}

func (r *skillRepo) upsertDraftFileDirect(ctx context.Context, payload skillDraftFilePayload, body []byte) (*biz.SkillFile, error) {
	alreadyExists := r.objectExists(ctx, payload.ObjectKey)
	if !alreadyExists {
		if err := r.putObject(ctx, payload.ObjectKey, body, payload.ContentType); err != nil {
			return nil, errorx.Wrap(err, errorx.Code("SKILL_DRAFT_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill draft file"))
		}
	}
	db := r.db(ctx)
	if db == nil {
		if !alreadyExists {
			r.compensateS3Delete(ctx, payload.ObjectKey)
		}
		return nil, errDBNotConfigured()
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := ensureParentDraftDirs(ctx, tx, payload); err != nil {
			return err
		}
		return upsertDraftMetadataInTx(tx, payload)
	}); err != nil {
		if !alreadyExists {
			r.compensateS3Delete(ctx, payload.ObjectKey)
		}
		return nil, err
	}
	return r.GetSkillDraftFile(ctx, payload.SkillName, payload.Version, payload.Path)
}

func (r *skillRepo) BranchPromoteDraftObject(ctx context.Context, payload skillDraftFilePayload) error {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill storage requires object store")
	}
	if payload.TempKey == "" || payload.ObjectKey == "" {
		return biz.ErrSkillInvalidArgument
	}
	if !r.objectExists(ctx, payload.ObjectKey) {
		if _, err := r.resources.ObjectStore.CopyObject(ctx, payload.TempKey, payload.ObjectKey, objectstorex.PutOptions{ContentType: payload.ContentType}); err != nil {
			return errorx.Wrap(err, errorx.Code("SKILL_DRAFT_PROMOTE_FAILED"), errorx.WithMessage("failed to promote staged draft file"))
		}
	}
	return nil
}

func (r *skillRepo) BranchCompensateDraftObject(ctx context.Context, payload skillDraftFilePayload) error {
	return r.cleanupDraftObjectIfUnreferenced(ctx, payload.ObjectKey)
}

func (r *skillRepo) BranchUpsertDraftMetadata(ctx context.Context, payload skillDraftFilePayload) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := ensureParentDraftDirs(ctx, tx, payload); err != nil {
			return err
		}
		return upsertDraftMetadataInTx(tx, payload)
	})
}

func (r *skillRepo) BranchCompensateDraftMetadata(ctx context.Context, payload skillDraftFilePayload) error {
	// Metadata is the last DTM branch today, so compensation normally does not
	// run. Keep it idempotent and conservative: remove only the exact row that
	// points at the object written by this saga, preserving any older row that
	// may have been restored by a user retry.
	db := r.db(ctx)
	if db == nil {
		return nil
	}
	if err := db.Unscoped().Where("skill_name = ? AND version = ? AND path = ? AND object_key = ?", payload.SkillName, payload.Version, payload.Path, payload.ObjectKey).Delete(&skillDraftFileModel{}).Error; err != nil {
		return err
	}
	return r.cleanupDraftObjectIfUnreferenced(ctx, payload.ObjectKey)
}

func (r *skillRepo) cleanupDraftObjectIfUnreferenced(ctx context.Context, objectKey string) error {
	objectKey = strings.TrimSpace(objectKey)
	if objectKey == "" || r.resources == nil || r.resources.ObjectStore == nil {
		return nil
	}
	db := r.db(ctx)
	if db != nil {
		var refs int64
		if err := db.Model(&skillDraftFileModel{}).Where("object_key = ?", objectKey).Count(&refs).Error; err == nil && refs > 0 {
			return nil
		}
	}
	r.cleanupS3ObjectsSync(ctx, []string{objectKey})
	return nil
}

func (h *SkillDTMBranchHandler) handleDraft(w http.ResponseWriter, req *http.Request, fn func(context.Context, skillDraftFilePayload) error) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := dtmx.ValidateBranchRequest(req, h.branchSecret); err != nil {
		writeDTMJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "code": errorx.CodeOf(err).String()})
		return
	}
	defer req.Body.Close()
	var payload skillDraftFilePayload
	if err := json.NewDecoder(io.LimitReader(req.Body, 2<<20)).Decode(&payload); err != nil {
		writeDTMJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid payload"})
		return
	}
	if err := fn(req.Context(), payload); err != nil {
		status := errorx.HTTPStatusOf(err)
		if status < 400 {
			status = http.StatusInternalServerError
		}
		writeDTMJSON(w, status, map[string]string{"error": err.Error(), "code": errorx.CodeOf(err).String()})
		return
	}
	writeDTMJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (r *skillRepo) ensureDraftVersionForWrite(ctx context.Context, name, version, actor string) error {
	if err := r.ensureDraftVersion(ctx, name, version); err != nil {
		if !errors.Is(err, biz.ErrSkillVersionNotFound) {
			return err
		}
		return r.createDraftVersion(ctx, name, version, actor)
	}
	return nil
}

func (r *skillRepo) createDraftVersion(ctx context.Context, name, version, actor string) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = biz.DefaultSkillVersion
	}
	if _, err := r.GetSkill(ctx, name); err != nil {
		return err
	}
	row := &skillVersionModel{
		SkillName:    name,
		Version:      version,
		Status:       biz.SkillVersionStatusDraft,
		Author:       actor,
		ManifestJSON: []byte(`{"source":"draft_workspace"}`),
	}
	if err := db.Create(row).Error; err != nil {
		mapped := mapSkillDBError(err)
		if errors.Is(err, dbx.ErrDuplicateKey) || errors.Is(mapped, biz.ErrSkillAlreadyExists) {
			return r.ensureDraftVersion(ctx, name, version)
		}
		return mapped
	}
	return nil
}

func (r *skillRepo) ensureDraftVersion(ctx context.Context, name, version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		version = biz.DefaultSkillVersion
	}
	v, err := r.GetSkillVersion(ctx, name, version)
	if err != nil {
		return err
	}
	if v.Status != biz.SkillVersionStatusDraft {
		return errorx.Conflict(errorx.Code("SKILL_VERSION_NOT_DRAFT"), "only draft versions can be edited")
	}
	return nil
}

func ensureParentDraftDirs(ctx context.Context, tx *gorm.DB, payload skillDraftFilePayload) error {
	_ = ctx
	parents := parentDirs(payload.Path)
	for _, dir := range parents {
		p := skillDraftFilePayload{SkillName: payload.SkillName, Version: payload.Version, Path: dir, Name: path.Base(dir), Kind: skillDraftKindDirectory, Actor: payload.Actor}
		if err := upsertDraftMetadataInTx(tx, p); err != nil {
			return err
		}
	}
	return nil
}

func upsertDraftMetadataInTx(tx *gorm.DB, payload skillDraftFilePayload) error {
	if tx == nil {
		return errDBNotConfigured()
	}
	if payload.SkillName == "" || payload.Version == "" || payload.Path == "" {
		return biz.ErrSkillInvalidArgument
	}
	kind := payload.Kind
	if kind == "" {
		kind = skillDraftKindFile
	}
	now := time.Now()
	row := &skillDraftFileModel{
		SkillName:   payload.SkillName,
		Version:     payload.Version,
		Path:        payload.Path,
		Name:        firstNonEmptyStringData(payload.Name, path.Base(payload.Path)),
		Kind:        kind,
		ContentType: payload.ContentType,
		SizeBytes:   payload.SizeBytes,
		Binary:      payload.Binary,
		SHA256:      payload.SHA256,
		ObjectKey:   payload.ObjectKey,
		CreatedBy:   payload.Actor,
		UpdatedBy:   payload.Actor,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	var existing skillDraftFileModel
	err := tx.Unscoped().Where("skill_name = ? AND version = ? AND path = ?", payload.SkillName, payload.Version, payload.Path).First(&existing).Error
	if err == nil {
		updates := map[string]any{
			"name":         row.Name,
			"kind":         row.Kind,
			"content_type": row.ContentType,
			"size_bytes":   row.SizeBytes,
			"binary":       row.Binary,
			"sha256":       row.SHA256,
			"object_key":   row.ObjectKey,
			"updated_by":   row.UpdatedBy,
			"updated_at":   now,
			"deleted_at":   nil,
		}
		return mapSkillDBError(tx.Unscoped().Model(&skillDraftFileModel{}).Where("id = ?", existing.ID).Updates(updates).Error)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return mapSkillDBError(err)
	}
	if err := tx.Create(row).Error; err != nil {
		mapped := mapSkillDBError(err)
		if errors.Is(err, dbx.ErrDuplicateKey) || errors.Is(mapped, biz.ErrSkillAlreadyExists) {
			return errorx.Conflict(errorx.Code("SKILL_DRAFT_PATH_EXISTS"), "draft path already exists")
		}
		return mapped
	}
	return nil
}

func (r *skillRepo) readDraftObjectContent(ctx context.Context, row skillDraftFileModel) (string, bool, error) {
	if row.ObjectKey == "" {
		return "", row.Binary, nil
	}
	if r.resources == nil || r.resources.ObjectStore == nil {
		return "", row.Binary, errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill draft file content requires object store")
	}
	rc, _, err := r.resources.ObjectStore.GetObject(ctx, row.ObjectKey, objectstorex.GetOptions{})
	if err != nil {
		if errors.Is(err, objectstorex.ErrNotFound) {
			return "", row.Binary, errorx.NotFound(errorx.Code("SKILL_DRAFT_OBJECT_MISSING"), "skill draft object not found")
		}
		return "", row.Binary, errorx.Wrap(err, errorx.Code("SKILL_DRAFT_DOWNLOAD_FAILED"), errorx.WithMessage("failed to download skill draft file"))
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, downloadSkillPackageMaxBytes+1))
	if err != nil {
		return "", row.Binary, err
	}
	if int64(len(body)) > downloadSkillPackageMaxBytes {
		return "", row.Binary, errorx.BadRequest(errorx.Code("SKILL_DRAFT_FILE_TOO_LARGE"), "skill draft file exceeds read size limit")
	}
	if row.Binary {
		return base64.StdEncoding.EncodeToString(body), true, nil
	}
	return string(body), false, nil
}

func draftRowToSkillFile(row *skillDraftFileModel, includeContent bool) *biz.SkillFile {
	if row == nil {
		return nil
	}
	_ = includeContent
	return &biz.SkillFile{
		ID:         row.ID,
		SkillName:  row.SkillName,
		Version:    row.Version,
		Path:       row.Path,
		Name:       row.Name,
		Type:       firstNonEmptyStringData(row.ContentType, row.Kind),
		Size:       row.SizeBytes,
		Binary:     row.Binary,
		CreateTime: row.CreatedAt,
		UpdateTime: row.UpdatedAt,
	}
}

func skillFileContentBytes(file *biz.SkillFile) ([]byte, bool, error) {
	if file.Binary {
		body, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return nil, true, errorx.Wrap(err, errorx.Code("SKILL_DRAFT_FILE_INVALID"), errorx.WithMessage("binary content must be base64 encoded"))
		}
		return body, true, nil
	}
	return []byte(file.Content), false, nil
}

func cleanSkillPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	p = path.Clean(p)
	if p == "." || p == "" || strings.HasPrefix(p, "../") || p == ".." {
		return "", errorx.From(biz.ErrSkillInvalidArgument, errorx.WithMessage("path is invalid"))
	}
	if len(p) > 1024 {
		return "", errorx.From(biz.ErrSkillInvalidArgument, errorx.WithMessage("path is too long"))
	}
	return p, nil
}

func parentDirs(p string) []string {
	var out []string
	dir := path.Dir(p)
	for dir != "." && dir != "/" && dir != "" {
		out = append(out, dir)
		dir = path.Dir(dir)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func detectSkillContentType(filePath string, body []byte, binary bool) string {
	if ext := path.Ext(filePath); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
	}
	if binary {
		return "application/octet-stream"
	}
	if len(body) == 0 {
		return skillDraftTextType
	}
	return skillDraftTextType
}

func skillDraftObjectKey(skillName, version, sha string) string {
	return fmt.Sprintf("skills/%s/drafts/%s/objects/%s", skillName, version, sha)
}

func skillTempDraftObjectKey(gid, sha string) string {
	return fmt.Sprintf("skills/_tmp/dtm/%s/draft/%s", gid, sha)
}

func firstNonEmptyStringData(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
