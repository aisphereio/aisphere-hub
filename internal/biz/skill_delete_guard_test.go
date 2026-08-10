package biz

import (
	"context"
	"strings"
	"testing"

	"github.com/aisphereio/kernel/errorx"
)

type fakeRefReader struct {
	refs []AgentSkillReference
}

func (f *fakeRefReader) ListSkillReferences(context.Context, string) ([]AgentSkillReference, error) {
	return f.refs, nil
}

// TestDeleteSkillRefusedWhenReferenced: deleting a Skill that Agents still
// bind must fail with AGENT_SKILL_REFERENCED before any state mutation.
func TestDeleteSkillRefusedWhenReferenced(t *testing.T) {
	uc := &SkillUsecase{
		agentRefs: &fakeRefReader{refs: []AgentSkillReference{
			{AgentID: "agent-a", DisplayName: "Agent A", LatestVersion: "v1"},
		}},
	}
	err := uc.DeleteSkill(context.Background(), "k8s-debug")
	if err == nil {
		t.Fatal("expected error when skill is referenced")
	}
	if !errorx.IsCode(err, "AGENT_SKILL_REFERENCED") {
		t.Fatalf("error = %v, want AGENT_SKILL_REFERENCED", err)
	}
	if !strings.Contains(err.Error(), "agent-a") || !strings.Contains(err.Error(), "Agent A") {
		t.Fatalf("error should mention referencing agents: %v", err)
	}
}