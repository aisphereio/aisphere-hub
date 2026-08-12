package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

type agentSkillFrontMatter struct {
	AllowedTools []string `yaml:"allowed-tools"`
}

func parseAgentSkillAllowedTools(content string) ([]string, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("SKILL.md must start with YAML front-matter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("SKILL.md front-matter is not closed")
	}
	var frontMatter agentSkillFrontMatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &frontMatter); err != nil {
		return nil, fmt.Errorf("parse SKILL.md front-matter: %w", err)
	}
	return normalizeAgentToolNames(frontMatter.AllowedTools), nil
}

func normalizeAgentToolNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// skillToolCompatibilityWarnings compares declarative Skill recommendations
// with the explicit Agent Tool allowlist. It never mutates the definition and
// never grants Tool permission. Manifest read/parse failures are reported as a
// warning too, because compatibility advice must not become an authorization
// gate after the release itself has already passed validation.
func (h *agentHTTPHandler) skillToolCompatibilityWarnings(ctx context.Context, skills []map[string]any, tools []resolvedAgentTool) []agentCompatibilityWarning {
	if h == nil || h.skillManifests == nil {
		return nil
	}
	bound := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		bound[strings.ToLower(strings.TrimSpace(tool.Binding.Name))] = struct{}{}
	}
	warnings := make([]agentCompatibilityWarning, 0)
	for _, skill := range skills {
		if !strings.EqualFold(strings.TrimSpace(fmt.Sprint(skill["source"])), "catalog") {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(skill["name"]))
		revision := strings.TrimSpace(fmt.Sprint(skill["revision"]))
		file, err := h.skillManifests.GetFileContent(ctx, name, "SKILL.md", revision)
		if err != nil {
			warnings = append(warnings, agentCompatibilityWarning{Code: "SKILL_TOOL_COMPATIBILITY_UNAVAILABLE", Skill: name, Message: "Could not inspect allowed-tools for Skill " + name})
			continue
		}
		content, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			warnings = append(warnings, agentCompatibilityWarning{Code: "SKILL_TOOL_COMPATIBILITY_UNAVAILABLE", Skill: name, Message: "Could not decode allowed-tools for Skill " + name})
			continue
		}
		allowed, err := parseAgentSkillAllowedTools(string(content))
		if err != nil {
			warnings = append(warnings, agentCompatibilityWarning{Code: "SKILL_TOOL_COMPATIBILITY_UNAVAILABLE", Skill: name, Message: "Could not parse allowed-tools for Skill " + name})
			continue
		}
		missing := make([]string, 0)
		for _, tool := range allowed {
			if _, exists := bound[strings.ToLower(tool)]; !exists {
				missing = append(missing, tool)
			}
		}
		if len(missing) > 0 {
			warnings = append(warnings, agentCompatibilityWarning{
				Code: "SKILL_TOOL_COMPATIBILITY_MISSING", Skill: name, MissingTools: missing,
				Message: fmt.Sprintf("Skill %s may not function completely; missing Agent Tools: %s", name, strings.Join(missing, ", ")),
			})
		}
	}
	return warnings
}
