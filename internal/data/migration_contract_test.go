package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresMigrationsGuardDollarQuotedStatements(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "000001_create_aihub_skills.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.Contains(sql, "$$") {
		t.Fatal("migration no longer exercises a dollar-quoted PostgreSQL statement")
	}
	if !strings.Contains(sql, "-- +goose StatementBegin") || !strings.Contains(sql, "-- +goose StatementEnd") {
		t.Fatal("dollar-quoted PostgreSQL statements must use goose StatementBegin/StatementEnd guards")
	}
}

func TestSkillSetMigrationReferencesGitNativeSkills(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "202607150001_create_aihub_skillsets.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	if !strings.Contains(sql, "REFERENCES skills(name)") {
		t.Fatal("SkillSet members must reference the Git-native skills table")
	}
	if strings.Contains(sql, "REFERENCES aihub_skills(name)") {
		t.Fatal("SkillSet migration must not depend on the removed package-lifecycle table")
	}
}

func TestSkillSetMigrationGuardsQuotedSemicolonComments(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "202607150001_create_aihub_skillsets.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	commentStart := strings.Index(sql, "COMMENT ON TABLE aihub_skillsets")
	if commentStart < 0 {
		t.Fatal("SkillSet table comment is missing")
	}
	if strings.LastIndex(sql[:commentStart], "-- +goose StatementBegin") < 0 || strings.Index(sql[commentStart:], "-- +goose StatementEnd") < 0 {
		t.Fatal("quoted comments containing semicolons must be guarded from statement splitting")
	}
}

func TestRepositoryBackedSkillMigrationUsesCanonicalRepos(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "202607210001_create_hub_skill_profiles.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, fragment := range []string{
		"repository_id      BIGINT PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE",
		"FOREIGN KEY (skill_name) REFERENCES repos(name) ON DELETE CASCADE",
		"JOIN repos r ON r.name = s.name",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("repository-backed migration missing %q", fragment)
		}
	}
}
