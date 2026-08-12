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
		"skillsets":[{"name":"backend-engineer","revision":8}]
	}`)
	canonical, projection, err := normalizeAgentDefinition(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projection.SkillSets) != 1 || projection.SkillSets[0].Name != "backend-engineer" || projection.SkillSets[0].Revision != 8 {
		t.Fatalf("unexpected projection: %+v", projection.SkillSets)
	}
	if !json.Valid(canonical) {
		t.Fatal("canonical definition is not valid JSON")
	}
}

func TestNormalizeAgentDefinitionSkillSetRejectsMissingRevision(t *testing.T) {
	raw := json.RawMessage(`{
		"entryPoint":"root.yaml",
		"files":{"root.yaml":"name: demo"},
		"skillsets":[{"name":"backend-engineer"}]
	}`)
	if _, _, err := normalizeAgentDefinition(raw); err == nil {
		t.Fatal("expected error for skillset without revision")
	}
}

func TestResolveAgentSkillSnapshotsBuiltinOnly(t *testing.T) {
	h := &agentHTTPHandler{}
	items, err := h.resolveAgentSkillSnapshots(context.Background(), authn.Principal{}, []agentSkillBinding{{Name: "sandbox-workspace-tools", Version: "builtin-d9f6a0bea925"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0]["source"] != "builtin" || items[0]["revision"] != "builtin-d9f6a0bea925" {
		t.Fatalf("unexpected skill snapshot: %+v", items)
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
