package biz

import (
	"context"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

var (
	ErrSkillReleaseAlreadyExists = errorx.Conflict(errorx.Code("SKILL_RELEASE_ALREADY_EXISTS"), "skill release already exists")
	ErrSkillReleaseNotFound      = errorx.NotFound(errorx.Code("SKILL_RELEASE_NOT_FOUND"), "skill release not found")
	ErrSkillReleaseStale         = errorx.Conflict(errorx.Code("SKILL_RELEASE_STALE"), "skill release source changed")
	ErrSkillRestoreStale         = errorx.Conflict(errorx.Code("SKILL_RESTORE_STALE"), "skill restore target changed")
)

// SkillReleaseEngine is deliberately separate from SkillGitEngine so release
// support can be added without forcing unrelated Git engine test doubles and
// external implementations to change atomically.
type SkillReleaseEngine interface {
	CreateRelease(context.Context, CreateSkillRelease) (*SkillRelease, error)
	GetRelease(context.Context, string, string) (*SkillRelease, error)
}

type CreateSkillRelease struct {
	SkillName         string
	Version           string
	SourceRef         string
	ExpectedCommitSHA string
	ReleaseNotes      string
	ActorID           string
	ActorName         string
	ActorEmail        string
	CreateTime        time.Time
}

func NormalizeReleaseVersion(version string) (string, bool) {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	parsed, err := semver.StrictNewVersion(version)
	if err != nil {
		return "", false
	}
	return "v" + parsed.String(), true
}

func (uc *SkillUsecase) CreateRelease(ctx context.Context, principal authn.Principal, in CreateSkillRelease) (*SkillRelease, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	engine, ok := uc.git.(SkillReleaseEngine)
	if !ok || engine == nil {
		return nil, ErrSkillDependencyFailed
	}
	in.SkillName = strings.TrimSpace(in.SkillName)
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	in.ExpectedCommitSHA = strings.TrimSpace(in.ExpectedCommitSHA)
	if in.SkillName == "" || in.ExpectedCommitSHA == "" {
		return nil, ErrSkillInvalidArgument
	}
	version, valid := NormalizeReleaseVersion(in.Version)
	if !valid {
		return nil, ErrSkillInvalidArgument
	}
	if in.SourceRef == "" {
		in.SourceRef = "refs/heads/" + SkillDefaultBranch
	} else {
		in.SourceRef = normalizeBranchRef(in.SourceRef)
	}
	currentSHA, err := uc.git.ResolveRef(ctx, in.SkillName, in.SourceRef)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(currentSHA) != in.ExpectedCommitSHA {
		return nil, ErrSkillReleaseStale
	}
	in.Version = version
	in.ActorID = principal.SubjectID
	in.ActorName = principal.Name
	in.ActorEmail = principal.Email
	if strings.TrimSpace(in.ActorName) == "" {
		in.ActorName = principal.SubjectID
	}
	if in.CreateTime.IsZero() {
		in.CreateTime = time.Now().UTC()
	}
	return engine.CreateRelease(ctx, in)
}

func (uc *SkillUsecase) GetRelease(ctx context.Context, skill, version string) (*SkillRelease, error) {
	engine, ok := uc.git.(SkillReleaseEngine)
	if !ok || engine == nil {
		return nil, ErrSkillDependencyFailed
	}
	tag, valid := NormalizeReleaseVersion(version)
	if strings.TrimSpace(skill) == "" || !valid {
		return nil, ErrSkillInvalidArgument
	}
	return engine.GetRelease(ctx, strings.TrimSpace(skill), tag)
}

func (uc *SkillUsecase) ListRefs(ctx context.Context, skill string) ([]SkillGitRef, error) {
	skill = strings.TrimSpace(skill)
	if skill == "" || uc.git == nil {
		return nil, ErrSkillInvalidArgument
	}
	return uc.git.ListRefs(ctx, skill)
}

func (uc *SkillUsecase) ListCommits(ctx context.Context, skill, ref string, limit, offset int) ([]SkillCommit, error) {
	skill, ref = strings.TrimSpace(skill), strings.TrimSpace(ref)
	if skill == "" || uc.git == nil {
		return nil, ErrSkillInvalidArgument
	}
	if ref == "" {
		ref = "refs/heads/" + SkillDefaultBranch
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return uc.git.ListCommits(ctx, skill, ref, limit, offset)
}

func (uc *SkillUsecase) CompareRefs(ctx context.Context, skill, baseRef, targetRef string) (*SkillComparison, error) {
	skill, baseRef, targetRef = strings.TrimSpace(skill), strings.TrimSpace(baseRef), strings.TrimSpace(targetRef)
	if skill == "" || baseRef == "" || targetRef == "" || uc.git == nil {
		return nil, ErrSkillInvalidArgument
	}
	return uc.git.CompareRefs(ctx, skill, baseRef, targetRef)
}

func (uc *SkillUsecase) RestoreRef(ctx context.Context, principal authn.Principal, in RestoreSkillRef) (*SkillCommit, error) {
	if err := requirePrincipal(principal); err != nil {
		return nil, err
	}
	if uc.git == nil {
		return nil, ErrSkillDependencyFailed
	}
	in.SkillName = strings.TrimSpace(in.SkillName)
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	in.TargetBranch = strings.TrimSpace(in.TargetBranch)
	in.ExpectedHeadSHA = strings.TrimSpace(in.ExpectedHeadSHA)
	if in.SkillName == "" || in.SourceRef == "" || in.ExpectedHeadSHA == "" {
		return nil, ErrSkillInvalidArgument
	}
	if in.TargetBranch == "" {
		in.TargetBranch = SkillDefaultBranch
	}
	in.TargetBranch = strings.TrimPrefix(normalizeBranchRef(in.TargetBranch), "refs/heads/")
	current, err := uc.git.ResolveRef(ctx, in.SkillName, "refs/heads/"+in.TargetBranch)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(current) != in.ExpectedHeadSHA {
		return nil, ErrSkillRestoreStale
	}
	in.ActorID = principal.SubjectID
	in.ActorName = principal.Name
	in.ActorEmail = principal.Email
	if in.ActorName == "" {
		in.ActorName = principal.SubjectID
	}
	if in.CreateTime.IsZero() {
		in.CreateTime = time.Now().UTC()
	}
	return uc.git.RestoreRef(ctx, in)
}
