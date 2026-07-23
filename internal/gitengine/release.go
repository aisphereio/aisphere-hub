package gitengine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func (e *Engine) CreateRelease(ctx context.Context, in biz.CreateSkillRelease) (*biz.SkillRelease, error) {
	repo, err := e.open(ctx, strings.TrimSpace(in.SkillName))
	if err != nil {
		return nil, err
	}
	tag, valid := biz.NormalizeReleaseVersion(in.Version)
	if !valid {
		return nil, biz.ErrSkillInvalidArgument
	}
	ref := "refs/tags/" + tag
	if _, err := repo.ShowRefVerify(ref); err == nil {
		return nil, biz.ErrSkillReleaseAlreadyExists
	}

	commitSHA := strings.TrimSpace(in.ExpectedCommitSHA)
	if commitSHA == "" {
		return nil, biz.ErrSkillInvalidArgument
	}
	if _, _, err := e.readSkillMetadata(ctx, repo, commitSHA, in.SkillName); err != nil {
		return nil, fmt.Errorf("gitengine: validate release SKILL.md: %w", err)
	}

	createdAt := in.CreateTime.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	actorName := strings.TrimSpace(in.ActorName)
	if actorName == "" {
		actorName = strings.TrimSpace(in.ActorID)
	}
	if actorName == "" {
		actorName = "AISphere Publisher"
	}
	actorEmail := strings.TrimSpace(in.ActorEmail)
	if actorEmail == "" {
		actorEmail = "noreply@aisphere.io"
	}
	message := strings.TrimSpace(in.ReleaseNotes)
	if message == "" {
		message = "Release " + tag
	}
	env := []string{
		"GIT_COMMITTER_NAME=" + actorName,
		"GIT_COMMITTER_EMAIL=" + actorEmail,
		"GIT_COMMITTER_DATE=" + createdAt.Format(time.RFC3339),
	}
	if _, err := runGitRepoEnv(ctx, repo.Path, nil, env, "tag", "-a", tag, commitSHA, "-m", message); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil, biz.ErrSkillReleaseAlreadyExists
		}
		return nil, fmt.Errorf("gitengine: create annotated release tag: %w", err)
	}
	return e.GetRelease(ctx, in.SkillName, tag)
}

func (e *Engine) GetRelease(ctx context.Context, skill, version string) (*biz.SkillRelease, error) {
	repo, err := e.open(ctx, strings.TrimSpace(skill))
	if err != nil {
		return nil, err
	}
	tag, valid := biz.NormalizeReleaseVersion(version)
	if !valid {
		return nil, biz.ErrSkillInvalidArgument
	}
	commit, err := repo.TagCommit(tag)
	if err != nil {
		return nil, biz.ErrSkillReleaseNotFound
	}
	return &biz.SkillRelease{
		Tag:        tag,
		CommitSHA:  commit.ID.String(),
		CreateTime: commit.Committer.When,
	}, nil
}
