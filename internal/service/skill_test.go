package service

import (
	"testing"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

func TestGitNativeSkillDTOsExposeRepositoryAndPullRequestState(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	skill := skillToProto(&biz.GitSkill{Name: "search", DefaultBranch: "main", Status: "active", CreateTime: now})
	if skill.GetName() != "search" || skill.GetDefaultBranch() != "main" || skill.GetStatus() != "active" {
		t.Fatalf("skill DTO = %+v", skill)
	}
	pr := pullRequestToProto(&biz.SkillPullRequest{ID: "pr-1", SkillName: "search", SourceSHA: "source", TargetSHA: "main", State: biz.PullRequestStateOpen})
	if pr.GetId() != "pr-1" || pr.GetSourceSha() != "source" || pr.GetTargetSha() != "main" || pr.GetState() != "open" {
		t.Fatalf("PR DTO = %+v", pr)
	}
}
