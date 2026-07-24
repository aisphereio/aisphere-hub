package gitengine

import (
	"context"
	"testing"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestReleaseMetadataRefsAndHistory(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	head, err := engine.ResolveRef(ctx, "search", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	release, err := engine.CreateRelease(ctx, biz.CreateSkillRelease{
		SkillName: "search", Version: "1.2.3", SourceRef: "refs/heads/main",
		ExpectedCommitSHA: head, ReleaseNotes: "stable search",
		ActorID: "publisher-1", ActorName: "Publisher",
		ActorEmail: "publisher@example.com", CreateTime: publishedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.Tag != "v1.2.3" || release.CommitSHA != head {
		t.Fatalf("release = %+v", release)
	}
	if release.TreeSHA == "" || release.ManifestSHA256 == "" {
		t.Fatalf("release integrity metadata missing: %+v", release)
	}
	if release.ReleaseNotes != "stable search" || release.SourceRef != "refs/heads/main" {
		t.Fatalf("release annotation metadata = %+v", release)
	}
	if release.PublisherID != "publisher-1" || release.PublisherName != "Publisher" {
		t.Fatalf("release publisher metadata = %+v", release)
	}
	if !release.CreateTime.Equal(publishedAt) {
		t.Fatalf("release CreateTime = %s, want %s", release.CreateTime, publishedAt)
	}

	refs, err := engine.ListRefs(ctx, "search")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || !refs[0].IsDefault || refs[1].Name != "v1.2.3" {
		t.Fatalf("refs = %+v", refs)
	}
	commits, err := engine.ListCommits(ctx, "search", "v1.2.3", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].CommitSHA != head || commits[0].TreeSHA == "" {
		t.Fatalf("commits = %+v", commits)
	}
}

func TestCompareAndRestoreRef(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	initial, err := engine.ResolveRef(ctx, "search", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CreateFile(ctx, "search", "notes.md", "new", "add notes", "main", "Editor", "editor@example.com"); err != nil {
		t.Fatal(err)
	}
	head, err := engine.ResolveRef(ctx, "search", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := engine.CompareRefs(ctx, "search", initial, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Files) != 1 || comparison.Files[0].Path != "notes.md" || comparison.Patch == "" {
		t.Fatalf("comparison = %+v", comparison)
	}

	restored, err := engine.RestoreRef(ctx, biz.RestoreSkillRef{
		SkillName: "search", SourceRef: initial, TargetBranch: "main",
		ExpectedHeadSHA: head, ActorID: "owner-1", ActorName: "Owner",
		CreateTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.CommitSHA == "" || restored.ParentSHAs[0] != head {
		t.Fatalf("restored commit = %+v", restored)
	}
	files, err := engine.ListFiles(ctx, "search", "", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "SKILL.md" {
		t.Fatalf("restored files = %+v", files)
	}
}

func TestCompareReleaseTagsUsesSemVerPrecedence(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "v1.10.0", right: "v1.9.0", want: 1},
		{left: "v1.0.0", right: "v1.0.0-rc.10", want: 1},
		{left: "v1.0.0-rc.10", right: "v1.0.0-rc.2", want: 1},
		{left: "v1.0.0-1", right: "v1.0.0-alpha", want: -1},
		{left: "v1.0.0-alpha.1", right: "v1.0.0-alpha", want: 1},
		{left: "v1.0.0+build.2", right: "v1.0.0+build.1", want: 0},
	}
	for _, tt := range tests {
		got := compareReleaseTags(tt.left, tt.right)
		if got != tt.want {
			t.Errorf("compareReleaseTags(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
		}
	}
}
