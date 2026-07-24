package gitengine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	softgit "github.com/aisphereio/soft-serve/git"
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
	notes := strings.TrimSpace(in.ReleaseNotes)
	if notes == "" {
		notes = "Release " + tag
	}
	message := notes + "\n\n" +
		"AISphere-Source-Ref: " + strings.TrimSpace(in.SourceRef) + "\n" +
		"AISphere-Publisher-ID: " + strings.TrimSpace(in.ActorID) + "\n"
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
	return e.releaseForTag(ctx, repo, tag)
}

func (e *Engine) releaseForTag(ctx context.Context, repo *softgit.Repository, tag string) (*biz.SkillRelease, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, biz.ErrSkillInvalidArgument
	}

	// The Git repository may contain operational tags such as backup, demo or
	// before-refactor. They are valid Git refs but are not SkillHub versions.
	// ListReleases enumerates every repository tag, so return a lightweight
	// placeholder here and let the service boundary filter it out. This keeps
	// ordinary Git tags from making the complete Skill version list fail while
	// exact GetRelease calls remain strict because they normalize SemVer first.
	if _, valid := biz.NormalizeReleaseVersion(tag); !valid {
		return &biz.SkillRelease{Tag: tag}, nil
	}

	commitSHA, err := runGitRepo(ctx, repo.Path, nil, "rev-parse", "--verify", "refs/tags/"+tag+"^{commit}")
	if err != nil {
		return nil, biz.ErrSkillReleaseNotFound
	}
	commitSHA = strings.TrimSpace(commitSHA)
	treeSHA, err := runGitRepo(ctx, repo.Path, nil, "show", "-s", "--format=%T", commitSHA)
	if err != nil {
		return nil, fmt.Errorf("gitengine: read release tree: %w", err)
	}
	committedAtRaw, err := runGitRepo(ctx, repo.Path, nil, "show", "-s", "--format=%cI", commitSHA)
	if err != nil {
		return nil, fmt.Errorf("gitengine: read release commit time: %w", err)
	}
	committedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(committedAtRaw))
	if err != nil {
		return nil, fmt.Errorf("gitengine: parse release commit time: %w", err)
	}
	manifest, err := runGitRepoRaw(ctx, repo.Path, "show", commitSHA+":SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("gitengine: read release manifest: %w", err)
	}
	manifestHash := fmt.Sprintf("%x", sha256.Sum256(manifest))

	raw, err := runGitRepo(ctx, repo.Path, nil, "for-each-ref",
		"--format=%(objecttype)%00%(taggername)%00%(taggeremail)%00%(taggerdate:iso-strict)%00%(contents)",
		"refs/tags/"+tag)
	if err != nil {
		return nil, fmt.Errorf("gitengine: read release tag: %w", err)
	}
	parts := strings.SplitN(raw, "\x00", 5)
	release := &biz.SkillRelease{
		Tag:            tag,
		CommitSHA:      commitSHA,
		TreeSHA:        strings.TrimSpace(treeSHA),
		ManifestSHA256: manifestHash,
		CreateTime:     committedAt.UTC(),
	}
	if len(parts) == 5 && strings.TrimSpace(parts[0]) == "tag" {
		release.PublisherName = strings.TrimSpace(parts[1])
		release.PublisherEmail = strings.Trim(strings.TrimSpace(parts[2]), "<>")
		if publishedAt, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(parts[3])); parseErr == nil {
			release.CreateTime = publishedAt.UTC()
		}
		release.ReleaseNotes, release.SourceRef, release.PublisherID = parseReleaseMessage(parts[4])
	}
	return release, nil
}

func parseReleaseMessage(message string) (notes, sourceRef, publisherID string) {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	noteLines := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "AISphere-Source-Ref:"):
			sourceRef = strings.TrimSpace(strings.TrimPrefix(line, "AISphere-Source-Ref:"))
		case strings.HasPrefix(line, "AISphere-Publisher-ID:"):
			publisherID = strings.TrimSpace(strings.TrimPrefix(line, "AISphere-Publisher-ID:"))
		default:
			noteLines = append(noteLines, line)
		}
	}
	return strings.TrimSpace(strings.Join(noteLines, "\n")), sourceRef, publisherID
}
