package gitengine

import (
	"fmt"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

// scaffoldContent returns the initial SKILL.md and skill.yaml content written
// to a freshly created skill repository. SKILL.md carries YAML front-matter
// (name/description/version) matching the convention the legacy upload
// endpoint documented; skill.yaml is a placeholder manifest consumed by
// future release tooling. Both are intentionally minimal — owners edit them
// via git push.
func scaffoldContent(skill *biz.GitSkill) (skillMd, skillYaml string) {
	name := strings.TrimSpace(skill.Name)
	desc := strings.TrimSpace(skill.Description)
	title := strings.TrimSpace(skill.DisplayName)
	if title == "" {
		title = name
	}
	body := desc
	if body == "" {
		body = "Edit this SKILL.md to describe what this skill does."
	}
	skillMd = fmt.Sprintf("---\nname: %s\ndescription: %q\nversion: \"0.1.0\"\n---\n\n# %s\n\n%s\n",
		name, desc, title, body)
	skillYaml = fmt.Sprintf("# Skill manifest placeholder (consumed by future release tooling).\nname: %s\nversion: 0.1.0\n",
		name)
	return skillMd, skillYaml
}
