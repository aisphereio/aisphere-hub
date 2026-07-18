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
