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
