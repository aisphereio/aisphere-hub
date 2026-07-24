package gitengine

import (
	"context"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestListReleasesToleratesOperationalGitTags(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()

	head, err := engine.ResolveRef(ctx, "search", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := engine.open(ctx, "search")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGitRepo(ctx, repo.Path, nil, "tag", "backup-0724", head); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CreateRelease(ctx, biz.CreateSkillRelease{
		SkillName: "search", Version: "1.2.3", SourceRef: "refs/heads/main",
		ExpectedCommitSHA: head, ActorID: "publisher-1",
	}); err != nil {
		t.Fatal(err)
	}

	releases, err := engine.ListReleases(ctx, "search")
	if err != nil {
		t.Fatalf("ListReleases returned an error for an operational tag: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %+v, want semantic release plus operational tag placeholder", releases)
	}

	var semantic, operational *biz.SkillRelease
	for i := range releases {
		switch releases[i].Tag {
		case "v1.2.3":
			semantic = &releases[i]
		case "backup-0724":
			operational = &releases[i]
		}
	}
	if semantic == nil || semantic.CommitSHA != head || semantic.ManifestSHA256 == "" {
		t.Fatalf("semantic release = %+v", semantic)
	}
	if operational == nil || operational.CommitSHA != "" {
		t.Fatalf("operational tag placeholder = %+v", operational)
	}
}
