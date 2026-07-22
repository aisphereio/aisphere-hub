package gitengine

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

type skillFrontMatter struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

// ParseSkillMetadata parses SKILL.md front-matter and enforces that the file
// does not introduce a second identity. Visibility, ownership and tenancy are
// intentionally not accepted here; they remain Hub/IAM control-plane fields.
func ParseSkillMetadata(repositoryName, content string) (displayName, description string, err error) {
	repositoryName = strings.TrimSpace(repositoryName)
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", fmt.Errorf("SKILL.md must start with YAML front-matter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", fmt.Errorf("SKILL.md front-matter is not closed")
	}
	var fm skillFrontMatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return "", "", fmt.Errorf("parse SKILL.md front-matter: %w", err)
	}
	if strings.TrimSpace(fm.Name) == "" || strings.TrimSpace(fm.Name) != repositoryName {
		return "", "", fmt.Errorf("SKILL.md name %q must match repository name %q", fm.Name, repositoryName)
	}
	displayName = strings.TrimSpace(fm.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(fm.Title)
	}
	if displayName == "" {
		for _, line := range lines[end+1:] {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				displayName = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}
	if displayName == "" {
		displayName = repositoryName
	}
	return displayName, strings.TrimSpace(fm.Description), nil
}
