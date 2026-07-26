package biz

import (
	"encoding/json"
	"testing"
)

func TestSkillResourceFromInput(t *testing.T) {
	t.Run("valid name", func(t *testing.T) {
		ref, err := skillResourceFromInput(`{"name":"ttt1","version":"1.4.2"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ref.Type != "skill" || ref.ID != "ttt1" {
			t.Errorf("got %+v, want {skill ttt1}", ref)
		}
	})
	t.Run("trims whitespace", func(t *testing.T) {
		ref, err := skillResourceFromInput(`{"name":"  ttt1  "}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ref.ID != "ttt1" {
			t.Errorf("got ID %q, want ttt1", ref.ID)
		}
	})
	t.Run("missing name", func(t *testing.T) {
		_, err := skillResourceFromInput(`{"version":"1.4.2"}`)
		if err == nil {
			t.Error("expected error for missing name, got nil")
		}
	})
	t.Run("empty name", func(t *testing.T) {
		_, err := skillResourceFromInput(`{"name":""}`)
		if err == nil {
			t.Error("expected error for empty name, got nil")
		}
	})
	t.Run("malformed json", func(t *testing.T) {
		_, err := skillResourceFromInput(`{not json`)
		if err == nil {
			t.Error("expected error for malformed json, got nil")
		}
	})
}

// TestPrivilegedToolRegistryInvariants verifies every privileged tool has the
// fields required for the Tool-level authz gate in CallSandboxTool.
func TestPrivilegedToolRegistryInvariants(t *testing.T) {
	for _, tool := range sandboxToolRegistry {
		if !tool.Privileged {
			continue
		}
		if tool.Permission == "" {
			t.Errorf("privileged tool %q has empty Permission", tool.Name)
		}
		if tool.ResourceFromInput == nil {
			t.Errorf("privileged tool %q has nil ResourceFromInput", tool.Name)
		}
		// ResourceFromInput must reject empty/missing name.
		if _, err := tool.ResourceFromInput(`{}`); err == nil {
			t.Errorf("privileged tool %q ResourceFromInput accepted empty input", tool.Name)
		}
	}
}

// TestNonPrivilegedToolsNotGated ensures workspace.*/browser.* tools are not
// marked privileged (they are gated only by sandbox.use, not Tool-level authz).
func TestNonPrivilegedToolsNotGated(t *testing.T) {
	nonPrivileged := map[string]bool{
		"workspace.read":      true,
		"workspace.write":     true,
		"workspace.list":      true,
		"workspace.delete":    true,
		"workspace.search_text": true,
		"browser.open":        true,
	}
	for _, tool := range sandboxToolRegistry {
		if nonPrivileged[tool.Name] && tool.Privileged {
			t.Errorf("tool %q should not be privileged", tool.Name)
		}
	}
}

// TestPrivilegedToolInputSchemaHasName verifies the input schema of every
// privileged tool requires a "name" field (used by ResourceFromInput).
func TestPrivilegedToolInputSchemaHasName(t *testing.T) {
	for _, tool := range sandboxToolRegistry {
		if !tool.Privileged {
			continue
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal([]byte(tool.InputSchema), &schema); err != nil {
			t.Errorf("tool %q has malformed InputSchema: %v", tool.Name, err)
			continue
		}
		hasName := false
		for _, r := range schema.Required {
			if r == "name" {
				hasName = true
			}
		}
		if !hasName {
			t.Errorf("privileged tool %q input schema must require 'name' (for ResourceFromInput)", tool.Name)
		}
	}
}
