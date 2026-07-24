package biz

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

var semverReleasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

var (
	ErrSkillReleaseAlreadyExists = errorx.Conflict(errorx.Code("SKILL_RELEASE_ALREADY_EXISTS"), "skill release already exists")
	ErrSkillReleaseNotFound      = errorx.NotFound(errorx.Code("SKILL_RELEASE_NOT_FOUND"), "skill release not found")
	ErrSkillReleaseStale         = errorx.Conflict(errorx.Code("SKILL_RELEASE_STALE"), "skill release source changed")
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
	if !semverReleasePattern.MatchString(version) {
		return "", false
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return version, true
}

func (uc *SkillUsecase) ResolveRef(ctx context.Context, skill, ref string) (*SkillRef, error) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return nil, ErrSkillInvalidArgument
	}
	if uc.git == nil {
		return nil, ErrSkillDependencyFailed
	}
	ref = normalizeBranchRef(ref)
	commitSHA, err := uc.git.ResolveRef(ctx, skill, ref)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(commitSHA) == "" {
		return nil, ErrSkillInvalidArgument
	}
	return &SkillRef{Ref: ref, CommitSHA: commitSHA}, nil
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
