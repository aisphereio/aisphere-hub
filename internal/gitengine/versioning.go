package gitengine

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Masterminds/semver/v3"
	"github.com/aisphereio/aisphere-hub/internal/biz"
)

const maxComparisonPatchBytes = 1024 * 1024

var fullCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (e *Engine) ListRefs(ctx context.Context, name string) ([]biz.SkillGitRef, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, err
	}
	raw, err := runGitRepo(ctx, repo.Path, nil, "for-each-ref",
		"--format=%(refname)%00%(objecttype)%00%(objectname)%00%(*objectname)",
		"refs/heads", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("gitengine: list refs: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return []biz.SkillGitRef{}, nil
	}
	out := make([]biz.SkillGitRef, 0)
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			continue
		}
		fullRef := strings.TrimSpace(parts[0])
		refType, shortName := "branch", strings.TrimPrefix(fullRef, "refs/heads/")
		commitSHA := strings.TrimSpace(parts[2])
		if strings.HasPrefix(fullRef, "refs/tags/") {
			refType, shortName = "tag", strings.TrimPrefix(fullRef, "refs/tags/")
			if peeled := strings.TrimSpace(parts[3]); peeled != "" {
				commitSHA = peeled
			}
		}
		out = append(out, biz.SkillGitRef{
			Name: shortName, FullRef: fullRef, Type: refType, CommitSHA: commitSHA,
			IsDefault: fullRef == "refs/heads/"+biz.SkillDefaultBranch,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (e *Engine) ListCommits(ctx context.Context, name, ref string, limit, offset int) ([]biz.SkillCommit, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, err
	}
	commitSHA, err := resolveVersionRef(ctx, repo.Path, ref)
	if err != nil {
		return nil, err
	}
	raw, err := runGitRepo(ctx, repo.Path, nil, "log",
		"--max-count="+strconv.Itoa(limit), "--skip="+strconv.Itoa(offset),
		"--format=%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%s%x1e", commitSHA)
	if err != nil {
		return nil, fmt.Errorf("gitengine: list commits: %w", err)
	}
	records := strings.Split(raw, "\x1e")
	out := make([]biz.SkillCommit, 0, len(records))
	for _, record := range records {
		parts := strings.Split(strings.TrimSpace(record), "\x00")
		if len(parts) != 7 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, strings.TrimSpace(parts[5]))
		parents := strings.Fields(strings.TrimSpace(parts[2]))
		out = append(out, biz.SkillCommit{
			CommitSHA: strings.TrimSpace(parts[0]), TreeSHA: strings.TrimSpace(parts[1]),
			ParentSHAs: parents, AuthorName: strings.TrimSpace(parts[3]),
			AuthorEmail: strings.TrimSpace(parts[4]), CreateTime: at.UTC(),
			Subject: strings.TrimSpace(parts[6]),
		})
	}
	return out, nil
}

func (e *Engine) CompareRefs(ctx context.Context, name, baseRef, targetRef string) (*biz.SkillComparison, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, err
	}
	baseSHA, err := resolveVersionRef(ctx, repo.Path, baseRef)
	if err != nil {
		return nil, err
	}
	targetSHA, err := resolveVersionRef(ctx, repo.Path, targetRef)
	if err != nil {
		return nil, err
	}
	mergeBase, _ := runGitRepo(ctx, repo.Path, nil, "merge-base", baseSHA, targetSHA)
	numstat, err := runGitRepo(ctx, repo.Path, nil, "diff", "--numstat", "--find-renames", baseSHA, targetSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("gitengine: diff stats: %w", err)
	}
	statuses, err := runGitRepo(ctx, repo.Path, nil, "diff", "--name-status", "--find-renames", baseSHA, targetSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("gitengine: diff status: %w", err)
	}
	files := parseDiffFiles(numstat, statuses)
	patch, err := runGitRepo(ctx, repo.Path, nil, "diff", "--no-color", "--unified=3", "--find-renames", baseSHA, targetSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("gitengine: diff patch: %w", err)
	}
	truncated := len(patch) > maxComparisonPatchBytes
	if truncated {
		patch = patch[:maxComparisonPatchBytes]
		for !utf8.ValidString(patch) {
			patch = patch[:len(patch)-1]
		}
	}
	return &biz.SkillComparison{
		BaseRef: baseRef, TargetRef: targetRef, BaseCommitSHA: baseSHA,
		TargetCommitSHA: targetSHA, MergeBaseSHA: strings.TrimSpace(mergeBase),
		Files: files, Patch: patch, PatchTruncated: truncated,
	}, nil
}

func (e *Engine) RestoreRef(ctx context.Context, in biz.RestoreSkillRef) (*biz.SkillCommit, error) {
	repo, err := e.open(ctx, in.SkillName)
	if err != nil {
		return nil, err
	}
	sourceSHA, err := resolveVersionRef(ctx, repo.Path, in.SourceRef)
	if err != nil {
		return nil, err
	}
	if _, _, err := e.readSkillMetadata(ctx, repo, sourceSHA, in.SkillName); err != nil {
		return nil, fmt.Errorf("gitengine: validate restored SKILL.md: %w", err)
	}
	targetRef := "refs/heads/" + strings.TrimPrefix(strings.TrimSpace(in.TargetBranch), "refs/heads/")
	currentSHA, err := repo.ShowRefVerify(targetRef)
	if err != nil {
		return nil, errBranchNotFound(targetRef)
	}
	if strings.TrimSpace(currentSHA) != strings.TrimSpace(in.ExpectedHeadSHA) {
		return nil, biz.ErrSkillRestoreStale
	}
	treeSHA, err := runGitRepo(ctx, repo.Path, nil, "show", "-s", "--format=%T", sourceSHA)
	if err != nil {
		return nil, fmt.Errorf("gitengine: read restore tree: %w", err)
	}
	message := strings.TrimSpace(in.CommitMessage)
	if message == "" {
		message = "Restore " + in.SourceRef
	}
	name := strings.TrimSpace(in.ActorName)
	if name == "" {
		name = strings.TrimSpace(in.ActorID)
	}
	email := strings.TrimSpace(in.ActorEmail)
	if email == "" {
		email = "noreply@aisphere.io"
	}
	at := in.CreateTime.UTC()
	env := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + at.Format(time.RFC3339),
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + at.Format(time.RFC3339),
	}
	newSHA, err := runGitRepoEnv(ctx, repo.Path, nil, env, "commit-tree", strings.TrimSpace(treeSHA), "-p", currentSHA, "-m", message)
	if err != nil {
		return nil, fmt.Errorf("gitengine: create restore commit: %w", err)
	}
	if _, err := runGitRepo(ctx, repo.Path, nil, "update-ref", targetRef, newSHA, currentSHA); err != nil {
		return nil, biz.ErrSkillRestoreStale
	}
	commits, err := e.ListCommits(ctx, in.SkillName, newSHA, 1, 0)
	if err != nil || len(commits) != 1 {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("gitengine: restored commit not found")
	}
	return &commits[0], nil
}

func resolveVersionRef(ctx context.Context, gitDir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", biz.ErrSkillInvalidArgument
	}
	candidates := []string{ref}
	if !strings.HasPrefix(ref, "refs/") && !fullCommitSHA.MatchString(ref) {
		candidates = []string{"refs/heads/" + ref, "refs/tags/" + ref}
	}
	for _, candidate := range candidates {
		sha, err := runGitRepo(ctx, gitDir, nil, "rev-parse", "--verify", candidate+"^{commit}")
		if err == nil && fullCommitSHA.MatchString(strings.TrimSpace(sha)) {
			return strings.TrimSpace(sha), nil
		}
	}
	return "", errBranchNotFound(ref)
}

func parseDiffFiles(numstat, statuses string) []biz.SkillDiffFile {
	type stat struct {
		additions, deletions int64
		binary               bool
	}
	stats := map[string]stat{}
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		path := parts[len(parts)-1]
		item := stat{}
		if parts[0] == "-" || parts[1] == "-" {
			item.binary = true
		} else {
			item.additions, _ = strconv.ParseInt(parts[0], 10, 64)
			item.deletions, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		stats[path] = item
	}
	out := make([]biz.SkillDiffFile, 0)
	for _, line := range strings.Split(strings.TrimSpace(statuses), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		status, path, previousPath := parts[0], parts[len(parts)-1], ""
		if (strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C")) && len(parts) >= 3 {
			previousPath = parts[1]
		}
		item := stats[path]
		out = append(out, biz.SkillDiffFile{
			Path: path, PreviousPath: previousPath, Status: status,
			Additions: item.additions, Deletions: item.deletions, Binary: item.binary,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func compareReleaseTags(left, right string) int {
	l, lerr := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(left), "v"))
	r, rerr := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(right), "v"))
	if lerr != nil || rerr != nil {
		return strings.Compare(left, right)
	}
	return l.Compare(r)
}
