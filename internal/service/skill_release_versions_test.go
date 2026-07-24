package service

import (
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestFilterSkillReleaseVersions(t *testing.T) {
	items := []biz.SkillRelease{
		{Tag: "backup-0724"},
		{Tag: "v2.0.0-beta.1", CommitSHA: "beta"},
		{Tag: "1.2.3", CommitSHA: "stable"},
		{Tag: "v1.2.3", CommitSHA: "duplicate"},
		{Tag: "demo"},
	}

	got := filterSkillReleaseVersions(items)
	if len(got) != 2 {
		t.Fatalf("filterSkillReleaseVersions() = %+v, want 2 semantic versions", got)
	}
	if got[0].Tag != "v2.0.0-beta.1" || got[0].CommitSHA != "beta" {
		t.Fatalf("prerelease = %+v", got[0])
	}
	if got[1].Tag != "v1.2.3" || got[1].CommitSHA != "stable" {
		t.Fatalf("stable release = %+v", got[1])
	}
}
