package service

import (
	"testing"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestGitNativeSkillDTOsExposeRepositoryAndPullRequestState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	skill := skillToProto(&biz.GitSkill{Name: "search", DefaultBranch: "main", Status: "active", CreateTime: now}, "v1.2.0")
	if skill.GetName() != "search" || skill.GetDefaultBranch() != "main" || skill.GetStatus() != "active" {
		t.Fatalf("skill DTO = %+v", skill)
	}
	if skill.GetLatestVersion() != "v1.2.0" {
		t.Fatalf("skill latestVersion = %q, want v1.2.0", skill.GetLatestVersion())
	}
	pr := pullRequestToProto(&biz.SkillPullRequest{ID: "pr-1", SkillName: "search", SourceSHA: "source", TargetSHA: "main", State: biz.PullRequestStateOpen})
	if pr.GetId() != "pr-1" || pr.GetSourceSha() != "source" || pr.GetTargetSha() != "main" || pr.GetState() != "open" {
		t.Fatalf("PR DTO = %+v", pr)
	}
}

func TestLatestStableReleaseVersionSkipsPrereleases(t *testing.T) {
	releases := []biz.SkillRelease{
		{Tag: "v1.0.0"},
		{Tag: "v1.1.0-rc.1"},
		{Tag: "v1.2.0"},
		{Tag: "v1.1.0"},
		{Tag: "backup-tag"}, // non-canonical, filtered upstream normally
	}
	got := latestStableReleaseVersion(releases)
	if got != "v1.2.0" {
		t.Fatalf("latestStableReleaseVersion = %q, want v1.2.0", got)
	}

	prereleaseOnly := []biz.SkillRelease{{Tag: "v1.1.0-rc.1"}}
	if got := latestStableReleaseVersion(prereleaseOnly); got != "" {
		t.Fatalf("prerelease-only latest = %q, want empty", got)
	}
}
