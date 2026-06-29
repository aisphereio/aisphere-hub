package data

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
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
	"strconv"
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
	skillManifestSchemaV2    = 2

	// skillStorageFormatDirectory means the released version is stored as a
	// real S3 prefix directory. Zip files are only import/export artifacts.
	skillStorageFormatDirectory = "directory"
)

type skillStorageControl struct {
	Storage string `json:"storage"`
	Format  string `json:"format"`

	// RevisionPrefix is the immutable root for one committed version snapshot:
	// skills/{name}/versions/{version}/revisions/{tree_sha}/
	RevisionPrefix string `json:"revision_prefix,omitempty"`
	FilesPrefix    string `json:"files_prefix,omitempty"`
	TreeSHA256     string `json:"tree_sha256,omitempty"`

	// Package* are kept for backward-compatible JSON parsing. New versions do
	// not store package.zip as the primary object.
	PackageObjectKey string `json:"package_object_key,omitempty"`
	PackageSHA256    string `json:"package_sha256,omitempty"`
	PackageSize      int64  `json:"package_size,omitempty"`

	ManifestObjectKey string `json:"manifest_object_key"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ManifestSize      int64  `json:"manifest_size"`
	FileCount         int    `json:"file_count"`
	TotalFileSize     int64  `json:"total_file_size"`
	UpdatedAtUnix     int64  `json:"updated_at_unix"`
}

type skillPackageManifest struct {
	SchemaVersion int    `json:"schema_version"`
	StorageFormat string `json:"storage_format"`
	SkillName     string `json:"skill_name"`
	Version       string `json:"version"`

	RevisionPrefix string `json:"revision_prefix,omitempty"`
	FilesPrefix    string `json:"files_prefix,omitempty"`
	TreeSHA256     string `json:"tree_sha256,omitempty"`
	TotalFileSize  int64  `json:"total_file_size,omitempty"`

	// Package* are deprecated. They exist only so older JSON does not fail to
	// unmarshal while we migrate to directory-first storage.
	PackageObjectKey string `json:"package_object_key,omitempty"`
	PackageSHA256    string `json:"package_sha256,omitempty"`
	PackageSize      int64  `json:"package_size,omitempty"`

	Files     []skillPackageManifestFile `json:"files"`
	Metadata  map[string]any             `json:"metadata,omitempty"`
	CreatedAt string                     `json:"created_at"`
}

type skillPackageManifestFile struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Kind        string `json:"kind,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size"`
	Binary      bool   `json:"binary"`
	SHA256      string `json:"sha256,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
}

type skillVersionFileObjectPayload struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Binary      bool   `json:"binary"`
	SHA256      string `json:"sha256,omitempty"`
	TempKey     string `json:"temp_key,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
}

type skillVersionFileObjectWrite struct {
	skillVersionFileObjectPayload
	Body []byte `json:"-"`
}

type skillPackageSagaPayload struct {
	GID       string            `json:"gid"`
	Skill     *biz.Skill        `json:"skill"`
	Version   *biz.SkillVersion `json:"version"`
	Overwrite bool              `json:"overwrite"`

	RevisionPrefix    string                          `json:"revision_prefix"`
	FilesPrefix       string                          `json:"files_prefix"`
	Files             []skillVersionFileObjectPayload `json:"files"`
	ManifestObjectKey string                          `json:"manifest_object_key"`
	ManifestJSON      string                          `json:"manifest_json"`
	ManifestSHA256    string                          `json:"manifest_sha256"`
	TreeSHA256        string                          `json:"tree_sha256"`
	FileCount         int                             `json:"file_count"`
	TotalFileSize     int64                           `json:"total_file_size"`
	RetentionMax      int                             `json:"retention_max"`

	// Deprecated package fields kept so old branch payloads decode safely.
	TempPackageKey   string `json:"temp_package_key,omitempty"`
	PackageObjectKey string `json:"package_object_key,omitempty"`
	PackageSHA256    string `json:"package_sha256,omitempty"`
	PackageSize      int64  `json:"package_size,omitempty"`
}

type skillVersionDirectoryPlan struct {
	RevisionPrefix    string
	FilesPrefix       string
	ManifestObjectKey string
	ManifestBytes     []byte
	ManifestSHA256    string
	TreeSHA256        string
	FileCount         int
	TotalFileSize     int64
	Files             []skillVersionFileObjectWrite
}

func (r *skillRepo) saveSkillPackageS3First(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, files []*biz.SkillFile, packageBytes []byte, overwrite bool) (*biz.SkillVersion, error) {
	_ = packageBytes // Zip is now an import/export artifact, not primary storage.
	if r.resources == nil || r.resources.ObjectStore == nil {
		return nil, errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill storage requires object store; enable data.object_store")
	}
	if skill == nil || version == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	plan, err := buildSkillVersionDirectoryPlan(skill, version, files)
	if err != nil {
		return nil, err
	}

	version.MD5 = md5HexData([]byte(plan.TreeSHA256))
	version.SHA256 = plan.TreeSHA256
	version.SizeBytes = plan.TotalFileSize
	version.Revision = "objectstore://" + plan.RevisionPrefix
	version.ManifestJSON = buildSkillStorageControlJSON(skillStorageControl{
		Storage:           "s3",
		Format:            skillStorageFormatDirectory,
		RevisionPrefix:    plan.RevisionPrefix,
		FilesPrefix:       plan.FilesPrefix,
		TreeSHA256:        plan.TreeSHA256,
		ManifestObjectKey: plan.ManifestObjectKey,
		ManifestSHA256:    plan.ManifestSHA256,
		ManifestSize:      int64(len(plan.ManifestBytes)),
		FileCount:         plan.FileCount,
		TotalFileSize:     plan.TotalFileSize,
		UpdatedAtUnix:     time.Now().Unix(),
	})
	skill.SourceType = "s3-prefix"
	skill.SourceURI = "objectstore://" + plan.FilesPrefix

	if r.resources.DTM != nil && r.resources.DTM.Enabled() {
		return r.saveSkillPackageWithDTM(ctx, skill, version, overwrite, plan)
	}
	return r.saveSkillPackageDirectS3(ctx, skill, version, overwrite, plan)
}

func (r *skillRepo) saveSkillPackageWithDTM(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, overwrite bool, plan *skillVersionDirectoryPlan) (*biz.SkillVersion, error) {
	gid, err := r.resources.DTM.NewGID(ctx)
	if err != nil {
		return nil, err
	}
	payloadFiles := make([]skillVersionFileObjectPayload, 0, len(plan.Files))
	stagedKeys := make([]string, 0, len(plan.Files))
	for i := range plan.Files {
		f := plan.Files[i]
		if f.Kind == skillDraftKindDirectory {
			payloadFiles = append(payloadFiles, f.skillVersionFileObjectPayload)
			continue
		}
		f.TempKey = skillTempVersionFileObjectKey(gid, f.SHA256, i)
		if err := r.putObject(ctx, f.TempKey, f.Body, f.ContentType); err != nil {
			r.cleanupS3ObjectsSync(ctx, stagedKeys)
			return nil, errorx.Wrap(err, errorx.Code("SKILL_VERSION_FILE_STAGE_FAILED"), errorx.WithMessage("failed to stage skill version file"))
		}
		stagedKeys = append(stagedKeys, f.TempKey)
		payloadFiles = append(payloadFiles, f.skillVersionFileObjectPayload)
	}

	payload := skillPackageSagaPayload{
		GID:               gid,
		Skill:             cloneSkillForSaga(skill),
		Version:           cloneVersionForSaga(version),
		Overwrite:         overwrite,
		RevisionPrefix:    plan.RevisionPrefix,
		FilesPrefix:       plan.FilesPrefix,
		Files:             payloadFiles,
		ManifestObjectKey: plan.ManifestObjectKey,
		ManifestJSON:      string(plan.ManifestBytes),
		ManifestSHA256:    plan.ManifestSHA256,
		TreeSHA256:        plan.TreeSHA256,
		FileCount:         plan.FileCount,
		TotalFileSize:     plan.TotalFileSize,
		RetentionMax:      r.resources.SkillConfig.Storage.MaxVersions,
	}

	saga := dtmx.NewSaga(gid, "skill.version.directory.save", dtmx.WithSagaWaitResult(true))
	saga = saga.AddHTTP("promote_version_directory", r.resources.DTM.BranchURL("/skill/package/promote"), r.resources.DTM.BranchURL("/skill/package/promote_compensate"), payload)
	saga = saga.AddHTTP("upsert_metadata", r.resources.DTM.BranchURL("/skill/metadata/upsert"), r.resources.DTM.BranchURL("/skill/metadata/upsert_compensate"), payload)
	if _, err := r.resources.DTM.SubmitSaga(ctx, saga); err != nil {
		r.cleanupS3ObjectsSync(ctx, stagedKeys)
		return nil, err
	}
	go func(keys []string) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.cleanupS3ObjectsSync(cleanupCtx, keys)
	}(stagedKeys)
	return r.GetSkillVersion(ctx, skill.Name, version.Version)
}

func (r *skillRepo) saveSkillPackageDirectS3(ctx context.Context, skill *biz.Skill, version *biz.SkillVersion, overwrite bool, plan *skillVersionDirectoryPlan) (*biz.SkillVersion, error) {
	uploaded := make([]string, 0, len(plan.Files)+1)
	for i := range plan.Files {
		f := plan.Files[i]
		if f.Kind == skillDraftKindDirectory {
			continue
		}
		if err := r.putObject(ctx, f.ObjectKey, f.Body, f.ContentType); err != nil {
			r.cleanupS3ObjectsSync(ctx, uploaded)
			return nil, errorx.Wrap(err, errorx.Code("SKILL_VERSION_FILE_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill version file"))
		}
		uploaded = append(uploaded, f.ObjectKey)
	}
	if err := r.putObject(ctx, plan.ManifestObjectKey, plan.ManifestBytes, skillManifestContentType); err != nil {
		r.cleanupS3ObjectsSync(ctx, uploaded)
		return nil, errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill manifest to object store"))
	}
	uploaded = append(uploaded, plan.ManifestObjectKey)
	if err := r.commitSkillPackageMetadata(ctx, skillPackageSagaPayload{Skill: skill, Version: version, Overwrite: overwrite, RevisionPrefix: plan.RevisionPrefix, FilesPrefix: plan.FilesPrefix, Files: payloadFilesFromWrites(plan.Files), ManifestObjectKey: plan.ManifestObjectKey, TreeSHA256: plan.TreeSHA256, RetentionMax: r.resources.SkillConfig.Storage.MaxVersions}); err != nil {
		r.cleanupS3ObjectsAsync(uploaded)
		return nil, err
	}
	return r.GetSkillVersion(ctx, skill.Name, version.Version)
}

// BranchPromoteSkillPackage is the S3 action branch invoked by DTM. Despite
// the historical name, this now promotes a staged version directory, not a zip.
func (r *skillRepo) BranchPromoteSkillPackage(ctx context.Context, payload skillPackageSagaPayload) error {
	if r.resources == nil || r.resources.ObjectStore == nil {
		return errorx.Unavailable(errorx.Code("SKILL_OBJECT_STORE_REQUIRED"), "skill storage requires object store")
	}
	if payload.ManifestObjectKey == "" || payload.ManifestJSON == "" {
		return biz.ErrSkillInvalidArgument
	}
	for _, f := range payload.Files {
		if f.Kind == skillDraftKindDirectory {
			continue
		}
		if f.ObjectKey == "" {
			return biz.ErrSkillInvalidArgument
		}
		if r.objectExists(ctx, f.ObjectKey) {
			continue
		}
		if f.TempKey == "" {
			return biz.ErrSkillInvalidArgument
		}
		if _, err := r.resources.ObjectStore.CopyObject(ctx, f.TempKey, f.ObjectKey, objectstorex.PutOptions{ContentType: f.ContentType}); err != nil {
			return errorx.Wrap(err, errorx.Code("SKILL_VERSION_FILE_PROMOTE_FAILED"), errorx.WithMessage("failed to promote staged skill version file"))
		}
	}
	if err := r.putObject(ctx, payload.ManifestObjectKey, []byte(payload.ManifestJSON), skillManifestContentType); err != nil {
		return errorx.Wrap(err, errorx.Code("SKILL_MANIFEST_UPLOAD_FAILED"), errorx.WithMessage("failed to upload skill manifest"))
	}
	return nil
}

func (r *skillRepo) BranchCompensateSkillPackage(ctx context.Context, payload skillPackageSagaPayload) error {
	r.cleanupS3ObjectsSync(ctx, append(payloadFinalObjectKeys(payload), payload.ManifestObjectKey, payload.RevisionPrefix))
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
			existingVersion := rowToSkillVersion(&existing)
			if !payload.Overwrite && existing.SHA256 == versionRow.SHA256 && skillVersionManifestObjectKey(existingVersion) == payload.ManifestObjectKey {
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
		// Directory-first: PG stores control metadata only. The manifest in S3 is
		// the version file tree source of truth.
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
		out = append(out, manifestFileToSkillFile(name, version, f, false))
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
	out := manifestFileToSkillFile(name, version, *target, false)
	if isManifestDirectory(*target) {
		return out, nil
	}
	if target.ObjectKey == "" {
		return nil, errorx.NotFound(errorx.Code("SKILL_FILE_OBJECT_MISSING"), "skill file has no object key")
	}
	body, err := r.readObjectBytes(ctx, target.ObjectKey, downloadSkillPackageMaxBytes, "SKILL_FILE_DOWNLOAD_FAILED")
	if err != nil {
		return nil, err
	}
	if target.Binary {
		out.Content = base64.StdEncoding.EncodeToString(body)
		out.Binary = true
	} else {
		out.Content = string(body)
		out.Binary = false
	}
	return out, nil
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
	manifest, err := r.loadSkillManifest(ctx, name, version)
	if err != nil {
		return nil, err
	}
	body, err := r.buildZipFromVersionManifest(ctx, manifest)
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

func (r *skillRepo) readObjectBytes(ctx context.Context, key string, maxBytes int64, code string) ([]byte, error) {
	rc, _, err := r.resources.ObjectStore.GetObject(ctx, key, objectstorex.GetOptions{})
	if err != nil {
		if errors.Is(err, objectstorex.ErrNotFound) {
			return nil, errorx.Wrap(biz.ErrSkillVersionNotFound, errorx.Code("SKILL_OBJECT_MISSING"), errorx.WithMessage("skill object not found in object store"))
		}
		return nil, errorx.Wrap(err, errorx.Code(code), errorx.WithMessage("failed to download skill object from object store"))
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return nil, errorx.Wrap(err, errorx.Code(code), errorx.WithMessage("failed to read skill object body"))
	}
	if int64(len(body)) > maxBytes {
		return nil, errorx.BadRequest(errorx.Code("SKILL_OBJECT_TOO_LARGE"), "skill object exceeds size limit")
	}
	return body, nil
}

func (r *skillRepo) buildZipFromVersionManifest(ctx context.Context, manifest *skillPackageManifest) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	zw := zip.NewWriter(buf)
	files := append([]skillPackageManifestFile(nil), manifest.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		if isManifestDirectory(f) {
			_, _ = zw.Create(f.Path + "/")
			continue
		}
		if f.ObjectKey == "" {
			_ = zw.Close()
			return nil, errorx.NotFound(errorx.Code("SKILL_FILE_OBJECT_MISSING"), "skill file has no object key")
		}
		body, err := r.readObjectBytes(ctx, f.ObjectKey, downloadSkillPackageMaxBytes, "SKILL_PACKAGE_EXPORT_FAILED")
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		w, err := zw.Create(f.Path)
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := w.Write(body); err != nil {
			_ = zw.Close()
			return nil, err
		}
		if int64(buf.Len()) > downloadSkillPackageMaxBytes {
			_ = zw.Close()
			return nil, errorx.BadRequest(errorx.Code("SKILL_PACKAGE_TOO_LARGE"), "exported skill package exceeds download size limit")
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > downloadSkillPackageMaxBytes {
		return nil, errorx.BadRequest(errorx.Code("SKILL_PACKAGE_TOO_LARGE"), "exported skill package exceeds download size limit")
	}
	return buf.Bytes(), nil
}

func buildSkillVersionDirectoryPlan(skill *biz.Skill, version *biz.SkillVersion, files []*biz.SkillFile) (*skillVersionDirectoryPlan, error) {
	entries := make(map[string]*skillVersionFileObjectWrite, len(files)*2)
	addDir := func(p string) error {
		clean, err := cleanSkillPath(p)
		if err != nil {
			return err
		}
		if existing, ok := entries[clean]; ok && existing.Kind != skillDraftKindDirectory {
			return errorx.Conflict(errorx.Code("SKILL_VERSION_PATH_CONFLICT"), "skill path conflicts with directory")
		}
		entries[clean] = &skillVersionFileObjectWrite{skillVersionFileObjectPayload: skillVersionFileObjectPayload{Path: clean, Name: path.Base(clean), Kind: skillDraftKindDirectory, Type: skillDraftKindDirectory}}
		return nil
	}
	for _, f := range files {
		if f == nil {
			continue
		}
		clean, err := cleanSkillPath(f.Path)
		if err != nil {
			return nil, err
		}
		kind := skillDraftKindFile
		if strings.EqualFold(strings.TrimSpace(f.Type), skillDraftKindDirectory) {
			kind = skillDraftKindDirectory
		}
		if kind == skillDraftKindDirectory {
			for _, parent := range parentDirs(clean) {
				if err := addDir(parent); err != nil {
					return nil, err
				}
			}
			if err := addDir(clean); err != nil {
				return nil, err
			}
			continue
		}
		for _, parent := range parentDirs(clean) {
			if err := addDir(parent); err != nil {
				return nil, err
			}
		}
		body, binary, err := skillFileContentBytes(f)
		if err != nil {
			return nil, err
		}
		sha := sha256.Sum256(body)
		shaHex := hex.EncodeToString(sha[:])
		contentType := skillVersionContentType(f, clean, body, binary)
		entries[clean] = &skillVersionFileObjectWrite{skillVersionFileObjectPayload: skillVersionFileObjectPayload{
			Path:        clean,
			Name:        firstNonEmptyStringData(f.Name, path.Base(clean)),
			Type:        strings.TrimSpace(f.Type),
			Kind:        skillDraftKindFile,
			ContentType: contentType,
			Size:        int64(len(body)),
			Binary:      binary,
			SHA256:      shaHex,
		}, Body: body}
	}
	if len(entries) == 0 {
		return nil, errorx.BadRequest(errorx.Code("SKILL_VERSION_EMPTY"), "skill version has no files")
	}
	writes := make([]skillVersionFileObjectWrite, 0, len(entries))
	for _, w := range entries {
		writes = append(writes, *w)
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].Path < writes[j].Path })
	treeHash := computeSkillTreeSHA256(writes)
	revisionPrefix := skillVersionRevisionPrefix(skill.Name, version.Version, treeHash)
	filesPrefix := revisionPrefix + "files/"
	manifestKey := revisionPrefix + "manifest.json"
	var total int64
	var fileCount int
	manifestFiles := make([]skillPackageManifestFile, 0, len(writes))
	for i := range writes {
		if writes[i].Kind != skillDraftKindDirectory {
			writes[i].ObjectKey = skillVersionFileObjectKey(filesPrefix, writes[i].Path)
			fileCount++
			total += writes[i].Size
		}
		manifestFiles = append(manifestFiles, skillPackageManifestFile{
			Path:        writes[i].Path,
			Name:        writes[i].Name,
			Type:        writes[i].Type,
			Kind:        writes[i].Kind,
			ContentType: writes[i].ContentType,
			Size:        writes[i].Size,
			Binary:      writes[i].Binary,
			SHA256:      writes[i].SHA256,
			ObjectKey:   writes[i].ObjectKey,
		})
	}
	manifest := skillPackageManifest{SchemaVersion: skillManifestSchemaV2, StorageFormat: skillStorageFormatDirectory, SkillName: skill.Name, Version: version.Version, RevisionPrefix: revisionPrefix, FilesPrefix: filesPrefix, TreeSHA256: treeHash, TotalFileSize: total, Files: manifestFiles, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	manifestHash := sha256.Sum256(body)
	return &skillVersionDirectoryPlan{RevisionPrefix: revisionPrefix, FilesPrefix: filesPrefix, ManifestObjectKey: manifestKey, ManifestBytes: body, ManifestSHA256: hex.EncodeToString(manifestHash[:]), TreeSHA256: treeHash, FileCount: fileCount, TotalFileSize: total, Files: writes}, nil
}

func computeSkillTreeSHA256(files []skillVersionFileObjectWrite) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = io.WriteString(h, f.Path)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, f.Kind)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, f.SHA256)
		_, _ = io.WriteString(h, "\x00")
		_, _ = io.WriteString(h, strconv.FormatInt(f.Size, 10))
		_, _ = io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func skillVersionContentType(f *biz.SkillFile, filePath string, body []byte, binary bool) string {
	if f != nil {
		t := strings.TrimSpace(f.Type)
		if strings.Contains(t, "/") {
			return t
		}
	}
	return detectSkillContentType(filePath, body, binary)
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
	if c.RevisionPrefix != "" {
		out = append(out, c.RevisionPrefix)
	}
	if c.FilesPrefix != "" {
		out = append(out, c.FilesPrefix)
	}
	if c.PackageObjectKey != "" {
		out = append(out, c.PackageObjectKey)
	}
	if c.ManifestObjectKey != "" {
		out = append(out, c.ManifestObjectKey)
	}
	return dedupeStrings(out)
}

func skillVersionRevisionPrefix(skillName, version, treeHash string) string {
	return fmt.Sprintf("skills/%s/versions/%s/revisions/%s/", skillName, version, treeHash)
}

func skillVersionFileObjectKey(filesPrefix, filePath string) string {
	return filesPrefix + strings.TrimPrefix(filePath, "/")
}

func skillTempVersionFileObjectKey(gid, sha string, index int) string {
	return fmt.Sprintf("skills/_tmp/dtm/%s/version/%06d-%s", gid, index, sha)
}

func payloadFilesFromWrites(in []skillVersionFileObjectWrite) []skillVersionFileObjectPayload {
	out := make([]skillVersionFileObjectPayload, 0, len(in))
	for _, f := range in {
		out = append(out, f.skillVersionFileObjectPayload)
	}
	return out
}

func payloadFinalObjectKeys(payload skillPackageSagaPayload) []string {
	out := make([]string, 0, len(payload.Files)+3)
	for _, f := range payload.Files {
		if f.ObjectKey != "" {
			out = append(out, f.ObjectKey)
		}
	}
	return out
}

func manifestFileToSkillFile(name, version string, f skillPackageManifestFile, includeContent bool) *biz.SkillFile {
	_ = includeContent
	return &biz.SkillFile{SkillName: name, Version: version, Path: f.Path, Name: f.Name, Type: firstNonEmptyStringData(f.Type, f.ContentType, f.Kind), Size: f.Size, Binary: f.Binary}
}

func isManifestDirectory(f skillPackageManifestFile) bool {
	return f.Kind == skillDraftKindDirectory || f.Type == skillDraftKindDirectory
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
		if strings.HasSuffix(key, "/") {
			objects, err := r.resources.ObjectStore.ListObjects(ctx, objectstorex.ListOptions{Prefix: key, Recursive: true})
			if err != nil {
				r.logger(ctx).Warn("skill s3 prefix cleanup list failed", logx.String("prefix", key), logx.Err(err))
				continue
			}
			for _, obj := range objects {
				if err := r.resources.ObjectStore.DeleteObject(ctx, obj.Key); err != nil {
					r.logger(ctx).Warn("skill s3 cleanup failed", logx.String("key", obj.Key), logx.Err(err))
				}
			}
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

func md5HexData(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
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
type SkillDTMBranchHandler struct {
	repo         *skillRepo
	branchSecret string
}

func NewSkillDTMBranchHandler(resources *Resources) *SkillDTMBranchHandler {
	secret := ""
	if resources != nil {
		secret = resources.DTMConfig.BranchSecret
	}
	return &SkillDTMBranchHandler{repo: &skillRepo{resources: resources}, branchSecret: secret}
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
func (h *SkillDTMBranchHandler) PromoteDraftObject(w http.ResponseWriter, req *http.Request) {
	h.handleDraft(w, req, h.repo.BranchPromoteDraftObject)
}
func (h *SkillDTMBranchHandler) CompensateDraftObject(w http.ResponseWriter, req *http.Request) {
	h.handleDraft(w, req, h.repo.BranchCompensateDraftObject)
}
func (h *SkillDTMBranchHandler) UpsertDraftMetadata(w http.ResponseWriter, req *http.Request) {
	h.handleDraft(w, req, h.repo.BranchUpsertDraftMetadata)
}
func (h *SkillDTMBranchHandler) CompensateDraftMetadata(w http.ResponseWriter, req *http.Request) {
	h.handleDraft(w, req, h.repo.BranchCompensateDraftMetadata)
}

func (h *SkillDTMBranchHandler) handle(w http.ResponseWriter, req *http.Request, fn func(context.Context, skillPackageSagaPayload) error) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := dtmx.ValidateBranchRequest(req, h.branchSecret); err != nil {
		writeDTMJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "code": errorx.CodeOf(err).String()})
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
