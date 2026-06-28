package data

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"

	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/dtmx"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/objectstorex"

	"gorm.io/gorm"
)

const (
	skillPackageContentType  = "application/zip"
	skillManifestContentType = "application/json"
	skillManifestSchemaV1    = 1
)

type skillStorageControl struct {
	Storage           string `json:"storage"`
	PackageObjectKey  string `json:"package_object_key"`
	PackageSHA256     string `json:"package_sha256"`
	PackageSize       int64  `json:"package_size"`
	ManifestObjectKey string `json:"manifest_object_key"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ManifestSize      int64  `json:"manifest_size"`
	FileCount         int    `json:"file_count"`
	TotalFileSize     int64  `json:"total_file_size"`
	UpdatedAtUnix     int64  `json:"updated_at_unix"`
}

type skillPackageManifest struct {
	SchemaVersion    int                        `json:"schema_version"`
	SkillName        string                     `json:"skill_name"`
	Version          string                     `json:"version"`
	PackageObjectKey string                     `json:"package_object_key"`
	PackageSHA256    string                     `json:"package_sha256"`
	PackageSize      int64                      `json:"package_size"`
	Files            []skillPackageManifestFile `json:"files"`
	Metadata         map[string]any             `json:"metadata,omitempty"`
	CreatedAt        string                     `json:"created_at"`
}

type skillPackageManifestFile struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary"`
	SHA256 string `json:"sha256,omitempty"`
}

type skillPackageSagaPayload struct {
	GID               string            `json:"gid"`
	Skill             *biz.Skill        `json:"skill"`
	Version           *biz.SkillVersion `json:"version"`
	Overwrite         bool              `json:"overwrite"`
	TempPackageKey    string            `json:"temp_package_key"`
	PackageObjectKey  string            `json:"package_object_key"`
	ManifestObjectKey string            `json:"manifest_object_key"`
	ManifestJSON      string            `json:"manifest_json"`
	ManifestSHA256    string            `json:"manifest_sha256"`
	PackageSHA256     string            `json:"package_sha256"`
	PackageSize       int64             `json:"package_size"`
	FileCount         int               `json:"file_count"`
	TotalFileSize     int64             `json:"total_file_size"`
	RetentionMax      int               `json:"retention_max"`
}

func (r *skillRepo) saveSkillPackageS3First(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, files []*biz.SkillFile, packageBytes []byte, overwrite bool) (*biz.SkillVersion, error) {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return nil, errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill storage requires object store; enable data.object_store")
	}
	if skill == nil || version == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	packageKey := skillPackageObjectKey(skill.Name, version.Version)
	manifestKey := skillManifestObjectKey(skill.Name, version.Version)
	manifestBytes, manifestHash, fileCount, totalFileSize, err := buildSkillPackageManifest(skill, version, files, packageKey)
	if err != nil {
		return nil, err
	}

	version.Revision = packageKey
	version.ManifestJSON = buildSkillStorageControlJSON(skillStorageControl{
		Storage:           "s3",
		PackageObjectKey:  packageKey,
		PackageSHA256:     version.SHA256,
		PackageSize:       int64(len(packageBytes)),
		ManifestObjectKey: manifestKey,
		ManifestSHA256:    manifestHash,
		ManifestSize:      int64(len(manifestBytes)),
		FileCount:         fileCount,
		TotalFileSize:     totalFileSize,
		UpdatedAtUnix:     time.Now().Unix(),
	})
	skill.SourceType = "s3"
	skill.SourceURI = "objectstore://" + packageKey

	if r.resources.DTM != nil {
		return r.saveSkillPackageWithDTM(ctx, skill, version, overwrite, packageBytes, packageKey, manifestKey, manifestBytes, manifestHash, fileCount, totalFileSize)
	}
	return r.saveSkillPackageDirectS3(ctx, skill, version, overwrite, packageBytes, packageKey, manifestKey, manifestBytes)
}

func (r *skillRepo) saveSkillPackageWithDTM(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, overwrite bool, packageBytes []byte, packageKey, manifestKey string, manifestBytes []byte, manifestHash string, fileCount int, totalFileSize int64) (*biz.SkillVersion, error) {
	gid, err := r.resources.DTM.NewGID(ctx)
	if err != nil {
		return nil, err
	}
	tmpKey := skillTempPackageObjectKey(gid)
	if err := r.putObject(ctx, tmpKey, packageBytes, skillPackageContentType); err != nil {
		return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_STAGE_FAILED"), errorx.WithMessage("failed to stage skill package in object store"))
	}

	payload := skillPackageSagaPayload{
		GID:               gid,
		Skill:             cloneSkillForSaga(skill),
		Version:           cloneVersionForSaga(version),
		Overwrite:         overwrite,
		TempPackageKey:    tmpKey,
		PackageObjectKey:  packageKey,
		ManifestObjectKey: manifestKey,
		ManifestJSON:      string(manifestBytes),
		ManifestSHA256:    manifestHash,
		PackageSHA256:     version.SHA256,
		PackageSize:       int64(len(packageBytes)),
		FileCount:         fileCount,
		TotalFileSize:     totalFileSize,
		RetentionMax:      r.resources.SkillConfig.Storage.MaxVersions,
	}

	saga := dtmx.NewSaga(gid)
	saga.WaitResult = true
	saga.Add(r.resources.DTM.BranchURL("/skill/package/promote"), r.resources.DTM.BranchURL("/skill/package/promote_compensate"), payload)
	saga.Add(r.resources.DTM.BranchURL("/skill/metadata/upsert"), r.resources.DTM.BranchURL("/skill/metadata/upsert_compensate"), payload)
	if err := r.resources.DTM.SubmitSaga(ctx, saga); err != nil {
		r.compensateS3Delete(ctx, tmpKey)
		return nil, err
	}
	go r.bestEffortDeleteObject(tmpKey)
	return r.GetSkillVersion(ctx, skill.Name, version.Version)
}

func (r *skillRepo) saveSkillPackageDirectS3(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, overwrite bool, packageBytes []byte, packageKey, manifestKey string, manifestBytes []byte) (*biz.SkillVersion, error) {
	if err := r.putObject(ctx, packageKey, packageBytes, skillPackageContentType); err != nil {
		return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill package to object store"))
	}
	if err := r.putObject(ctx, manifestKey, manifestBytes, skillManifestContentType); err != nil {
		r.compensateS3Delete(ctx, packageKey)
		return nil, errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill manifest to object store"))
	}
	if err := r.commitSkillPackageMetadata(ctx, skillPackageSagaPayload{Skill: skill, Version: version, Overwrite: overwrite, PackageObjectKey: packageKey, ManifestObjectKey: manifestKey, RetentionMax: r.resources.SkillConfig.Storage.MaxVersions}); err != nil {
		r.cleanupS3ObjectsAsync([]string{packageKey, manifestKey})
		return nil, err
	}
	return r.GetSkillVersion(ctx, skill.Name, version.Version)
}

// BranchPromoteSkillPackage is the S3 action branch invoked by DTM.
func (r *skillRepo) BranchPromoteSkillPackage(ctx context.Context, payload skillPackageSagaPayload) error {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill storage requires object store")
	}
	if payload.TempPackageKey == "" || payload.PackageObjectKey == "" || payload.ManifestObjectKey == "" {
		return biz.ErrSkillInvalidArgument
	}
	// Idempotency: if final object already exists with the expected SHA256,
	// treat the promote as success. DTM may retry action branches.
	if ok := r.objectExists(ctx, payload.PackageObjectKey); !ok {
		if _, err := r.resources.ObjectStore.CopyObject(ctx, payload.TempPackageKey, payload.PackageObjectKey, objectstorex.PutOptions{ContentType: skillPackageContentType}); err != nil {
			return errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_PROMOTE_FAILED"), errorx.WithMessage("failed to promote staged skill package"))
		}
	}
	if err := r.putObject(ctx, payload.ManifestObjectKey, []byte(payload.ManifestJSON), skillManifestContentType); err != nil {
		return errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill manifest"))
	}
	return nil
}

func (r *skillRepo) BranchCompensateSkillPackage(ctx context.Context, payload skillPackageSagaPayload) error {
	r.cleanupS3ObjectsSync(ctx, []string{payload.PackageObjectKey, payload.ManifestObjectKey})
	return nil
}

func (r *skillRepo) BranchUpsertSkillMetadata(ctx context.Context, payload skillPackageSagaPayload) error {
	return r.commitSkillPackageMetadata(ctx, payload)
}

func (r *skillRepo) BranchCompensateSkillMetadata(ctx context.Context, payload skillPackageSagaPayload) error {
	return r.deleteSkillVersionMetadata(ctx, payload)
}

func (r *skillRepo) commitSkillPackageMetadata(ctx context.Context, payload skillPackageSagaPayload) error {
	db := r.db(ctx)
	if db == nil {
		return errDBNotConfigured()
	}
	if payload.Skill == nil || payload.Version == nil {
		return biz.ErrSkillInvalidArgument
	}
	skillRow, err := skillToRow(payload.Skill)
	if err != nil {
		return err
	}
	versionRow, err := skillVersionToRow(payload.Version)
	if err != nil {
		return err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("name = ?", skillRow.Name).FirstOrCreate(skillRow).Error; err != nil {
			return mapSkillDBError(err)
		}
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
		var existing skillVersionModel
		existingErr := tx.Where("skill_name = ? AND version = ?", versionRow.SkillName, versionRow.Version).First(&existing).Error
		if existingErr == nil {
			if !payload.Overwrite && existing.SHA256 == versionRow.SHA256 && skillVersionPackageObjectKey(rowToSkillVersion(&existing)) == payload.PackageObjectKey {
				// Idempotent DTM retry of an already committed metadata branch.
				return nil
			}
			if !payload.Overwrite {
				return biz.ErrSkillVersionAlreadyExists
			}
		} else if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return mapSkillDBError(existingErr)
		}
		if payload.Overwrite {
			if err := tx.Unscoped().Where("skill_name = ? AND version = ?", versionRow.SkillName, versionRow.Version).Delete(&skillFileModel{}).Error; err != nil {
				return mapSkillDBError(err)
			}
			if err := tx.Unscoped().Where("skill_name = ? AND version = ?", versionRow.SkillName, versionRow.Version).Delete(&skillVersionModel{}).Error; err != nil {
				return mapSkillDBError(err)
			}
		}
		if err := tx.Create(versionRow).Error; err != nil {
			mapped := mapSkillDBError(err)
			if errors.Is(err, dbx.ErrDuplicateKey) || errors.Is(mapped, biz.ErrSkillAlreadyExists) {
				return biz.ErrSkillVersionAlreadyExists
			}
			return mapped
		}
		// S3-first: do not insert aihub_skill_files rows. The manifest stored in
		// S3 is the file tree source of truth; PG only stores control metadata.
		return nil
	})
	if err != nil {
		return err
	}
	if payload.RetentionMax > 0 {
		r.enforceSkillVersionRetention(ctx, payload.Skill.Name, payload.RetentionMax)
	}
	return nil
}

func (r *skillRepo) deleteSkillVersionMetadata(ctx context.Context, payload skillPackageSagaPayload) error {
	db := r.db(ctx)
	if db == nil || payload.Version == nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Where("skill_name = ? AND version = ?", payload.Version.SkillName, payload.Version.Version).Delete(&skillFileModel{}).Error; err != nil {
			return mapSkillDBError(err)
		}
		if err := tx.Unscoped().Where("skill_name = ? AND version = ?", payload.Version.SkillName, payload.Version.Version).Delete(&skillVersionModel{}).Error; err != nil {
			return mapSkillDBError(err)
		}
		return nil
	})
}

func (r *skillRepo) listSkillVersionFilesFromManifest(ctx context.Context, name, version string) ([]*biz.SkillFile, error) {
	manifest, err := r.loadSkillManifest(ctx, name, version)
	if err != nil {
		return nil, err
	}
	out := make([]*biz.SkillFile, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		out = append(out, &biz.SkillFile{SkillName: name, Version: version, Path: f.Path, Name: f.Name, Type: f.Type, Size: f.Size, Binary: f.Binary})
	}
	return out, nil
}

func (r *skillRepo) getSkillVersionFileFromS3(ctx context.Context, name, version, filePath string) (*biz.SkillFile, error) {
	manifest, err := r.loadSkillManifest(ctx, name, version)
	if err != nil {
		return nil, err
	}
	cleanPath := path.Clean(strings.TrimPrefix(filePath, "/"))
	var target *skillPackageManifestFile
	for i := range manifest.Files {
		if manifest.Files[i].Path == cleanPath {
			target = &manifest.Files[i]
			break
		}
	}
	if target == nil {
		return nil, biz.ErrSkillFileNotFound
	}
	body, err := r.readPackageObject(ctx, manifest.PackageObjectKey)
	if err != nil {
		return nil, err
	}
	content, binary, err := extractFileFromZip(body, cleanPath)
	if err != nil {
		return nil, err
	}
	return &biz.SkillFile{SkillName: name, Version: version, Path: target.Path, Name: target.Name, Type: target.Type, Size: target.Size, Binary: binary, Content: content}, nil
}

func (r *skillRepo) downloadSkillPackageS3First(ctx context.Context, name, version, ifNoneMatch string) (*biz.SkillPackageDownload, error) {
	meta, err := r.GetSkillVersion(ctx, name, version)
	if err != nil {
		return nil, err
	}
	etag := meta.SHA256
	if ifNoneMatch != "" && ifNoneMatch == etag {
		return &biz.SkillPackageDownload{SkillName: name, Version: version, ETag: etag, MD5: meta.MD5, SHA256: meta.SHA256, NotModified: true}, nil
	}
	key := skillVersionPackageObjectKey(meta)
	if key == "" {
		return nil, errorx.NotFound(errorx.Code("SKILL_PACKAGE_MISSING"), "skill version has no package object key")
	}
	body, err := r.readPackageObject(ctx, key)
	if err != nil {
		return nil, err
	}
	return &biz.SkillPackageDownload{SkillName: name, Version: version, ETag: etag, MD5: meta.MD5, SHA256: meta.SHA256, PackageBytes: body}, nil
}

func (r *skillRepo) loadSkillManifest(ctx context.Context, name, version string) (*skillPackageManifest, error) {
	meta, err := r.GetSkillVersion(ctx, name, version)
	if err != nil {
		return nil, err
	}
	key := skillVersionManifestObjectKey(meta)
	if key == "" {
		return nil, errorx.NotFound(errorx.Code("SKILL_MANIFEST_MISSING"), "skill version has no manifest object key")
	}
	rc, _, err := r.resources.ObjectStore.GetObject(ctx, key, objectstorex.GetOptions{})
	if err != nil {
		if errors.Is(err, objectstorex.ErrNotFound) {
			return nil, errorx.Wrap(biz.ErrSkillVersionNotFound, errorx.Code("SKILL_MANIFEST_MISSING"), errorx.WithMessage("skill manifest object not found in object store"))
		}
		return nil, errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_DOWNLOAD_FAILED"), errorx.WithMessage("failed to download skill manifest"))
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return nil, err
	}
	var manifest skillPackageManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_INVALID"), errorx.WithMessage("skill manifest is invalid"))
	}
	return &manifest, nil
}

func (r *skillRepo) readPackageObject(ctx context.Context, key string) ([]byte, error) {
	rc, _, err := r.resources.ObjectStore.GetObject(ctx, key, objectstorex.GetOptions{})
	if err != nil {
		if errors.Is(err, objectstorex.ErrNotFound) {
			return nil, errorx.Wrap(biz.ErrSkillVersionNotFound, errorx.Code("SKILL_PACKAGE_MISSING"), errorx.WithMessage("skill package object not found in object store"))
		}
		return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_DOWNLOAD_FAILED"), errorx.WithMessage("failed to download skill package from object store"))
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, downloadSkillPackageMaxBytes+1))
	if err != nil {
		return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_DOWNLOAD_FAILED"), errorx.WithMessage("failed to read skill package body"))
	}
	if int64(len(body)) > downloadSkillPackageMaxBytes {
		return nil, errorx.BadRequest(errorx.Code("SKILL_PACKAGE_TOO_LARGE"), "skill package exceeds download size limit")
	}
	return body, nil
}

func buildSkillPackageManifest(skill *biz.Skill, version *biz.SkillVersion, files []*biz.SkillFile, packageKey string) ([]byte, string, int, int64, error) {
	items := make([]skillPackageManifestFile, 0, len(files))
	var total int64
	for _, f := range files {
		if f == nil {
			continue
		}
		sha := sha256FileContent(f)
		items = append(items, skillPackageManifestFile{Path: f.Path, Name: f.Name, Type: f.Type, Size: f.Size, Binary: f.Binary, SHA256: sha})
		total += f.Size
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	manifest := skillPackageManifest{SchemaVersion: skillManifestSchemaV1, SkillName: skill.Name, Version: version.Version, PackageObjectKey: packageKey, PackageSHA256: version.SHA256, PackageSize: version.SizeBytes, Files: items, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", 0, 0, err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), len(items), total, nil
}

func buildSkillStorageControlJSON(c skillStorageControl) string {
	b, _ := json.Marshal(c)
	return string(b)
}

func parseSkillStorageControl(meta string) skillStorageControl {
	var c skillStorageControl
	_ = json.Unmarshal([]byte(meta), &c)
	return c
}

func skillVersionPackageObjectKey(v *biz.SkillVersion) string {
	if v == nil {
		return ""
	}
	c := parseSkillStorageControl(v.ManifestJSON)
	if c.PackageObjectKey != "" {
		return c.PackageObjectKey
	}
	return strings.TrimPrefix(strings.TrimPrefix(v.Revision, "objectstore://"), "s3://")
}

func skillVersionManifestObjectKey(v *biz.SkillVersion) string {
	if v == nil {
		return ""
	}
	return parseSkillStorageControl(v.ManifestJSON).ManifestObjectKey
}

func collectVersionObjectKeys(v *skillVersionModel) []string {
	if v == nil {
		return nil
	}
	out := []string{}
	if key := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(v.Revision), "objectstore://"), "s3://"); key != "" && !strings.HasPrefix(key, "data:") {
		out = append(out, key)
	}
	var c skillStorageControl
	_ = json.Unmarshal(v.ManifestJSON, &c)
	if c.PackageObjectKey != "" {
		out = append(out, c.PackageObjectKey)
	}
	if c.ManifestObjectKey != "" {
		out = append(out, c.ManifestObjectKey)
	}
	return dedupeStrings(out)
}

func skillManifestObjectKey(skillName, version string) string {
	return fmt.Sprintf("skills/%s/versions/%s/manifest.json", skillName, version)
}

func skillTempPackageObjectKey(gid string) string {
	return fmt.Sprintf("skills/_tmp/dtm/%s/package.zip", gid)
}

func (r *skillRepo) objectExists(ctx context.Context, key string) bool {
	if key == "" || r.resources == nil || r.resources.ObjectStore == nil {
		return false
	}
	_, err := r.resources.ObjectStore.StatObject(ctx, key)
	return err == nil
}

func (r *skillRepo) cleanupS3ObjectsSync(ctx context.Context, keys []string) {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return
	}
	for _, key := range dedupeStrings(keys) {
		if key == "" {
			continue
		}
		if err := r.resources.ObjectStore.DeleteObject(ctx, key); err != nil {
			r.logger(ctx).Warn("skill s3 cleanup failed", logx.String("key", key), logx.Err(err))
		}
	}
}

func (r *skillRepo) bestEffortDeleteObject(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.cleanupS3ObjectsSync(ctx, []string{key})
}

func (r *skillRepo) enforceSkillVersionRetention(ctx context.Context, skillName string, maxVersions int) {
	if maxVersions <= 0 {
		return
	}
	db := r.db(ctx)
	if db == nil {
		return
	}
	var rows []skillVersionModel
	if err := db.Where("skill_name = ?", skillName).Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		r.logger(ctx).Warn("skill retention query failed", logx.String("name", skillName), logx.Err(err))
		return
	}
	if len(rows) <= maxVersions {
		return
	}
	stale := rows[maxVersions:]
	staleIDs := make([]int64, 0, len(stale))
	keys := make([]string, 0, len(stale)*2)
	for i := range stale {
		staleIDs = append(staleIDs, stale[i].ID)
		keys = append(keys, collectVersionObjectKeys(&stale[i])...)
	}
	if err := db.Where("id IN ?", staleIDs).Delete(&skillVersionModel{}).Error; err != nil {
		r.logger(ctx).Warn("skill retention metadata cleanup failed", logx.String("name", skillName), logx.Err(err))
		return
	}
	r.cleanupS3ObjectsAsync(keys)
}

func extractFileFromZip(zipBytes []byte, filePath string) (content string, binary bool, err error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return "", false, err
	}
	for _, f := range zr.File {
		clean := path.Clean(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "/"))
		if clean == filePath || strings.HasSuffix(clean, "/"+filePath) {
			rc, err := f.Open()
			if err != nil {
				return "", false, err
			}
			defer rc.Close()
			data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
			if err != nil {
				return "", false, err
			}
			binary = containsNUL(data)
			if binary {
				return base64.StdEncoding.EncodeToString(data), true, nil
			}
			return string(data), false, nil
		}
	}
	return "", false, biz.ErrSkillFileNotFound
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

func sha256FileContent(f *biz.SkillFile) string {
	if f == nil {
		return ""
	}
	var data []byte
	if f.Binary {
		data, _ = base64.StdEncoding.DecodeString(f.Content)
	} else {
		data = []byte(f.Content)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneSkillForSaga(s *biz.Skill) *biz.Skill {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func cloneVersionForSaga(v *biz.SkillVersion) *biz.SkillVersion {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// SkillDTMBranchHandler exposes idempotent HTTP branch endpoints for DTM.
type SkillDTMBranchHandler struct{ repo *skillRepo }

func NewSkillDTMBranchHandler(resources *Resources) *SkillDTMBranchHandler {
	return &SkillDTMBranchHandler{repo: &skillRepo{resources: resources}}
}

func (h *SkillDTMBranchHandler) PromotePackage(w http.ResponseWriter, req *http.Request) {
	h.handle(w, req, h.repo.BranchPromoteSkillPackage)
}
func (h *SkillDTMBranchHandler) CompensatePackage(w http.ResponseWriter, req *http.Request) {
	h.handle(w, req, h.repo.BranchCompensateSkillPackage)
}
func (h *SkillDTMBranchHandler) UpsertMetadata(w http.ResponseWriter, req *http.Request) {
	h.handle(w, req, h.repo.BranchUpsertSkillMetadata)
}
func (h *SkillDTMBranchHandler) CompensateMetadata(w http.ResponseWriter, req *http.Request) {
	h.handle(w, req, h.repo.BranchCompensateSkillMetadata)
}

func (h *SkillDTMBranchHandler) handle(w http.ResponseWriter, req *http.Request, fn func(context.Context, skillPackageSagaPayload) error) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer req.Body.Close()
	var payload skillPackageSagaPayload
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

func writeDTMJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func isSkillManifestMissing(err error) bool {
	if err == nil {
		return false
	}
	code := errorx.CodeOf(err).String()
	return code == "SKILL_MANIFEST_MISSING"
}
