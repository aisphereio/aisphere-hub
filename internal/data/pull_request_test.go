package data

import (
	"context"
	"errors"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestPullRequestRepositoryTransitionsAndReviewUniqueness(t *testing.T) {
	db := openGitNativeTestDB(t)
	repo := newPullRequestRepoForDB(db)
	ctx := context.Background()

	pr, err := repo.CreatePullRequest(ctx, &biz.SkillPullRequest{
		SkillName: "search-tools",
		SourceRef: "refs/heads/feature/ranking",
		SourceSHA: "source-sha",
		TargetSHA: "main-sha",
		Title:     "Improve ranking",
		AuthorID:  "editor-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.TargetRef != "refs/heads/main" || pr.State != biz.PullRequestStateOpen {
		t.Fatalf("created PR = target %q state %q", pr.TargetRef, pr.State)
	}

	review := &biz.SkillPullRequestReview{PullRequestID: pr.ID, ReviewerID: "reviewer-1", Verdict: biz.ReviewVerdictApprove}
	if _, err := repo.CreateReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateReview(ctx, review); !errors.Is(err, biz.ErrPullRequestReviewExists) {
		t.Fatalf("duplicate review error = %v, want ErrPullRequestReviewExists", err)
	}

	if _, err := repo.MergePullRequest(ctx, "search-tools", pr.ID, "stale-main", "merge-sha", "publisher-1"); !errors.Is(err, biz.ErrPullRequestStale) {
		t.Fatalf("stale merge error = %v, want ErrPullRequestStale", err)
	}
	merged, err := repo.MergePullRequest(ctx, "search-tools", pr.ID, "main-sha", "merge-sha", "publisher-1")
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != biz.PullRequestStateMerged || merged.MergedSHA != "merge-sha" {
		t.Fatalf("merged PR = state %q sha %q", merged.State, merged.MergedSHA)
	}
	if _, err := repo.MergePullRequest(ctx, "search-tools", pr.ID, "main-sha", "other-sha", "publisher-1"); !errors.Is(err, biz.ErrPullRequestNotOpen) {
		t.Fatalf("second merge error = %v, want ErrPullRequestNotOpen", err)
	}
}
