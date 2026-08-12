package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aisphereio/kernel/authn"
)

func TestNormalizeApprovalMode(t *testing.T) {
	cases := map[string]string{
		"always":   agentApprovalAlways,
		"per_run":  agentApprovalPerRun,
		"prompt":   agentApprovalPerRun,
		"ask":      agentApprovalPerRun,
		"disabled": agentApprovalDisabled,
	}
	for input, want := range cases {
		if got := normalizeApprovalMode(input); got != want {
			t.Fatalf("normalizeApprovalMode(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeAgentDefinitionRequiresEntryPointFile(t *testing.T) {
	_, _, err := normalizeAgentDefinition(json.RawMessage(`{"entryPoint":"root.yaml","files":{"other.yaml":"x"}}`))
	if err == nil {
		t.Fatal("expected missing entryPoint file to fail")
	}
}

func TestNormalizeAgentDefinitionToolApproval(t *testing.T) {
	raw := json.RawMessage(`{
		"entryPoint":"root.yaml",
		"files":{"root.yaml":"name: demo"},
		"tools":[{"name":"skill.publish","approvalMode":"prompt","required":true}]
	}`)
	canonical, projection, err := normalizeAgentDefinition(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projection.Tools) != 1 || projection.Tools[0].ApprovalMode != agentApprovalPerRun {
		t.Fatalf("unexpected projection: %+v", projection.Tools)
	}
	if !json.Valid(canonical) {
		t.Fatal("canonical definition is not valid JSON")
	}
}

func TestNormalizeAgentDefinitionSkillBinding(t *testing.T) {
	raw := json.RawMessage(`{
		"entryPoint":"root.yaml",
		"files":{"root.yaml":"name: demo"},
		"skills":[{"name":"sandbox-workspace-tools","version":"builtin-d9f6a0bea925"}]
	}`)
	canonical, projection, err := normalizeAgentDefinition(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projection.Skills) != 1 || projection.Skills[0].Name != "sandbox-workspace-tools" || projection.Skills[0].Version != "builtin-d9f6a0bea925" {
		t.Fatalf("unexpected projection: %+v", projection.Skills)
	}
	if !json.Valid(canonical) {
		t.Fatal("canonical definition is not valid JSON")
	}
}

func TestNormalizeAgentDefinitionSkillSetBinding(t *testing.T) {
	raw := json.RawMessage(`{
		"entryPoint":"root.yaml",
		"files":{"root.yaml":"name: demo"},
		"skillSets":[{"name":"backend-engineer","revision":8,"required":true}]
	}`)
	canonical, projection, err := normalizeAgentDefinition(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projection.SkillSets) != 1 || projection.SkillSets[0].Name != "backend-engineer" || projection.SkillSets[0].Revision != 8 || !projection.SkillSets[0].Required {
		t.Fatalf("unexpected skillset projection: %+v", projection.SkillSets)
	}
	if !json.Valid(canonical) {
		t.Fatal("canonical definition is not valid JSON")
	}
}

func TestResolveAgentSkillSnapshotsBuiltinOnly(t *testing.T) {
	h := &agentHTTPHandler{}
	items, err := h.resolveAgentSkillSnapshots(context.Background(), authn.Principal{}, []agentSkillBinding{{Name: "sandbox-workspace-tools", Version: "builtin-d9f6a0bea925"}}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0]["source"] != "builtin" || items[0]["revision"] != "builtin-d9f6a0bea925" {
		t.Fatalf("unexpected skill snapshot: %+v", items)
	}
}

func TestMergeAgentSkillSnapshotDeduplicatesExactPinAndKeepsProvenance(t *testing.T) {
	out := []map[string]any{}
	index := map[string]int{}
	first := map[string]any{"name": "k8s-debug", "version": "v1.2.0", "revision": "abc123", "source": "catalog"}
	second := map[string]any{"name": "k8s-debug", "version": "v1.2.0", "revision": "abc123", "source": "catalog"}

	if err := mergeAgentSkillSnapshot(&out, index, first, map[string]any{"type": "direct"}); err != nil {
		t.Fatalf("first merge failed: %v", err)
	}
	if err := mergeAgentSkillSnapshot(&out, index, second, map[string]any{"type": "skillset", "name": "ops", "revision": int64(3)}); err != nil {
		t.Fatalf("second merge failed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one deduplicated skill, got %d", len(out))
	}
	provenance, ok := out[0]["provenance"].([]any)
	if !ok || len(provenance) != 2 {
		t.Fatalf("expected two provenance entries, got %#v", out[0]["provenance"])
	}
}

func TestMergeAgentSkillSnapshotRejectsDifferentImmutablePin(t *testing.T) {
	out := []map[string]any{}
	index := map[string]int{}
	if err := mergeAgentSkillSnapshot(&out, index,
		map[string]any{"name": "k8s-debug", "version": "v1.2.0", "revision": "abc123", "source": "catalog"},
		map[string]any{"type": "direct"}); err != nil {
		t.Fatalf("first merge failed: %v", err)
	}
	if err := mergeAgentSkillSnapshot(&out, index,
		map[string]any{"name": "k8s-debug", "version": "v1.3.0", "revision": "def456", "source": "catalog"},
		map[string]any{"type": "skillset", "name": "ops", "revision": int64(4)}); err == nil {
		t.Fatal("expected conflicting immutable skill pins to fail")
	}
}

func TestSkillSnapshotStringMissingValueIsEmpty(t *testing.T) {
	if got := skillSnapshotString(map[string]any{"name": "demo"}, "revision"); got != "" {
		t.Fatalf("missing snapshot field returned %q, want empty", got)
	}
}

func TestParseAgentIAMCapability(t *testing.T) {
	permission, ok := parseAgentIAMCapability("skill:publish")
	if !ok {
		t.Fatal("expected skill:publish to be an IAM capability")
	}
	if permission.ResourceType != "skill" || permission.Permission != "publish" || permission.Enforcement != "iam_at_resource_service" {
		t.Fatalf("unexpected permission: %+v", permission)
	}
	if _, ok := parseAgentIAMCapability("network.restricted"); ok {
		t.Fatal("network.restricted must not be presented as an IAM permission")
	}
}
