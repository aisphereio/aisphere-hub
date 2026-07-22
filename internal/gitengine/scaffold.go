package gitengine

import (
	"fmt"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
)

// scaffoldContent returns the only identity/description document seeded into
// a new repository. The repository name is the canonical skill identity; the
// SKILL.md front-matter is the source of truth for content metadata.
func scaffoldContent(skill *biz.GitSkill) string {
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
	return fmt.Sprintf("---\nname: %s\ndescription: %q\nversion: \"0.1.0\"\n---\n\n# %s\n\n%s\n",
		name, desc, title, body)
}
