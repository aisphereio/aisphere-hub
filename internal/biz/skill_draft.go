package biz

import (
	"context"
	"strings"

	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
)

// SkillDraftFileInput is the online editor input for one draft file or
// directory. A draft is a real persisted workspace, not an in-browser cache:
// each upsert writes the file body to S3 and the path/hash metadata to PG.
type SkillDraftFileInput struct {
	SkillName     string
	Version       string
	Path          string
	Type          string // content type, or "directory" for a directory node
	Binary        bool
	Content       string // UTF-8 text, or base64 when Binary=true
	CreateParents bool
}

type SkillDraftDeleteInput struct {
	SkillName string
	Version   string
	Path      string
	Recursive bool
}

type SkillDraftMoveInput struct {
	SkillName string
	Version   string
	OldPath   string
	NewPath   string
	Overwrite bool
}

type SkillDraftCommitInput struct {
	SkillName string
	Version   string
	CommitMsg string
	Overwrite bool
	Submit    bool
	Publish   bool
	Online    bool
}

func (uc *SkillUsecase) ListSkillDraftFiles(ctx context.Context, principal authn.Principal, name, version string) ([]*SkillFile, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	version = normalizeDraftVersion(version)
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.ListSkillDraftFiles(ctx, name, version)
}

func (uc *SkillUsecase) GetSkillDraftFile(ctx context.Context, principal authn.Principal, name, version, filePath string) (*SkillFile, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(filePath) == "" {
		return nil, ErrSkillInvalidArgument
	}
	version = normalizeDraftVersion(version)
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.GetSkillDraftFile(ctx, name, version, filePath)
}

func (uc *SkillUsecase) UpsertSkillDraftFile(ctx context.Context, principal authn.Principal, in SkillDraftFileInput) (out *SkillFile, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "draft_upsert_file", logx.String("name", in.SkillName), logx.String("path", in.Path))
	defer func() {
		uc.end(ctx, logger, "draft_upsert_file", started, err, logx.String("name", in.SkillName), logx.String("path", in.Path))
	}()
	if err := ValidateSkillName(in.SkillName); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, ErrSkillInvalidArgument
	}
	version := normalizeDraftVersion(in.Version)
	if err := uc.requireSkillPermission(ctx, principal, in.SkillName, "edit"); err != nil {
		return nil, err
	}
	out, err = uc.repo.UpsertSkillDraftFile(ctx, &SkillFile{
		SkillName: in.SkillName,
		Version:   version,
		Path:      in.Path,
		Type:      strings.TrimSpace(in.Type),
		Binary:    in.Binary,
		Content:   in.Content,
	}, principal.SubjectID)
	if err != nil {
		uc.recordAudit(ctx, principal, "skill.draft.file.upsert", auditx.ResultFailure, err.Error(), "skill", in.SkillName, map[string]any{"version": version, "path": in.Path})
		return nil, err
	}
	uc.recordAudit(ctx, principal, "skill.draft.file.upsert", auditx.ResultSuccess, "", "skill", in.SkillName, map[string]any{"version": version, "path": out.Path, "type": out.Type, "size": out.Size})
	return out, nil
}

func (uc *SkillUsecase) DeleteSkillDraftPath(ctx context.Context, principal authn.Principal, in SkillDraftDeleteInput) (err error) {
	ctx, logger, started := uc.begin(ctx, principal, "draft_delete_path", logx.String("name", in.SkillName), logx.String("path", in.Path))
	defer func() {
		uc.end(ctx, logger, "draft_delete_path", started, err, logx.String("name", in.SkillName), logx.String("path", in.Path))
	}()
	if err := ValidateSkillName(in.SkillName); err != nil {
		return err
	}
	if strings.TrimSpace(in.Path) == "" {
		return ErrSkillInvalidArgument
	}
	version := normalizeDraftVersion(in.Version)
	if err := uc.requireSkillPermission(ctx, principal, in.SkillName, "edit"); err != nil {
		return err
	}
	if err := uc.repo.DeleteSkillDraftPath(ctx, in.SkillName, version, in.Path, in.Recursive); err != nil {
		uc.recordAudit(ctx, principal, "skill.draft.path.delete", auditx.ResultFailure, err.Error(), "skill", in.SkillName, map[string]any{"version": version, "path": in.Path})
		return err
	}
	uc.recordAudit(ctx, principal, "skill.draft.path.delete", auditx.ResultSuccess, "", "skill", in.SkillName, map[string]any{"version": version, "path": in.Path})
	return nil
}

func (uc *SkillUsecase) MoveSkillDraftPath(ctx context.Context, principal authn.Principal, in SkillDraftMoveInput) (err error) {
	ctx, logger, started := uc.begin(ctx, principal, "draft_move_path", logx.String("name", in.SkillName), logx.String("old_path", in.OldPath), logx.String("new_path", in.NewPath))
	defer func() {
		uc.end(ctx, logger, "draft_move_path", started, err, logx.String("name", in.SkillName), logx.String("old_path", in.OldPath), logx.String("new_path", in.NewPath))
	}()
	if err := ValidateSkillName(in.SkillName); err != nil {
		return err
	}
	if strings.TrimSpace(in.OldPath) == "" || strings.TrimSpace(in.NewPath) == "" {
		return ErrSkillInvalidArgument
	}
	version := normalizeDraftVersion(in.Version)
	if err := uc.requireSkillPermission(ctx, principal, in.SkillName, "edit"); err != nil {
		return err
	}
	return uc.repo.MoveSkillDraftPath(ctx, in.SkillName, version, in.OldPath, in.NewPath, in.Overwrite)
}

func (uc *SkillUsecase) CommitSkillDraft(ctx context.Context, principal authn.Principal, in SkillDraftCommitInput) (out *SkillVersion, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "draft_commit", logx.String("name", in.SkillName), logx.String("version", in.Version))
	defer func() {
		uc.end(ctx, logger, "draft_commit", started, err, logx.String("name", in.SkillName), logx.String("version", in.Version))
	}()
	if err := ValidateSkillName(in.SkillName); err != nil {
		return nil, err
	}
	versionName := normalizeDraftVersion(in.Version)
	if err := uc.requireSkillPermission(ctx, principal, in.SkillName, "edit"); err != nil {
		return nil, err
	}
	skill, err := uc.repo.GetSkill(ctx, in.SkillName)
	if err != nil {
		return nil, err
	}
	packageBytes, files, err := uc.repo.BuildSkillPackageFromDraft(ctx, in.SkillName, versionName)
	if err != nil {
		return nil, err
	}
	version := &SkillVersion{
		SkillName: in.SkillName,
		Version:   versionName,
		Status:    SkillVersionStatusDraft,
		Author:    principal.SubjectID,
		CommitMsg: in.CommitMsg,
		ManifestJSON: mustJSON(map[string]any{
			"name":       skill.Name,
			"version":    versionName,
			"commit_msg": in.CommitMsg,
			"source":     "draft",
		}),
	}
	out, err = uc.repo.SaveSkillPackage(ctx, skill, version, files, packageBytes, true)
	if err != nil {
		uc.recordAudit(ctx, principal, "skill.draft.commit", auditx.ResultFailure, err.Error(), "skill", in.SkillName, map[string]any{"version": versionName})
		return nil, err
	}
	if in.Submit || in.Publish || in.Online {
		out, err = uc.repo.UpdateSkillVersionStatus(ctx, in.SkillName, versionName, SkillVersionStatusDraft, SkillVersionStatusSubmitted)
		if err != nil {
			return nil, normalizeDraftTransitionErr(err, versionName, SkillVersionStatusDraft)
		}
	}
	if in.Publish || in.Online {
		out, err = uc.repo.UpdateSkillVersionStatus(ctx, in.SkillName, versionName, SkillVersionStatusSubmitted, SkillVersionStatusPublished)
		if err != nil {
			return nil, normalizeDraftTransitionErr(err, versionName, SkillVersionStatusSubmitted)
		}
	}
	if in.Online {
		out, err = uc.repo.UpdateSkillVersionStatus(ctx, in.SkillName, versionName, SkillVersionStatusPublished, SkillVersionStatusOnline)
		if err != nil {
			return nil, normalizeDraftTransitionErr(err, versionName, SkillVersionStatusPublished)
		}
	}
	uc.recordAudit(ctx, principal, "skill.draft.commit", auditx.ResultSuccess, "", "skill", in.SkillName, map[string]any{"version": versionName, "sha256": out.SHA256, "files": len(files)})
	return out, nil
}

func normalizeDraftVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return DefaultSkillVersion
	}
	return version
}

func normalizeDraftTransitionErr(err error, version, expected string) error {
	if isSkillNotFound(err) {
		return errorx.Conflict(errorx.Code("SKILL_VERSION_STATUS_CONFLICT"), "version "+version+" is not in "+expected+" state")
	}
	return err
}
