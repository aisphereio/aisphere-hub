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

// TestKubernetesMigrationGuardsQuotedStatements ensures the COMMENT block
// (which contains semicolons inside quoted strings) is wrapped in goose
// StatementBegin/End so migrationx.splitStatements does not break it.
func TestKubernetesMigrationGuardsQuotedStatements(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "202607220001_create_kubernetes_environment.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	commentStart := strings.Index(sql, "COMMENT ON TABLE k8s_clusters")
	if commentStart < 0 {
		t.Fatal("k8s_clusters table comment is missing")
	}
	if strings.LastIndex(sql[:commentStart], "-- +goose StatementBegin") < 0 || strings.Index(sql[commentStart:], "-- +goose StatementEnd") < 0 {
		t.Fatal("k8s migration COMMENT block must be guarded by goose StatementBegin/StatementEnd")
	}
}

// TestKubernetesMigrationHasExpectedTablesAndConstraints guards the frozen
// schema contract (design §8 + plan decision 1). Touching any of these
// without a follow-up migration will fail this test, preventing accidental
// contract drift.
func TestKubernetesMigrationHasExpectedTablesAndConstraints(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "postgres", "202607220001_create_kubernetes_environment.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)

	// Five tables required by design §8.
	for _, table := range []string{
		"CREATE TABLE IF NOT EXISTS k8s_clusters",
		"CREATE TABLE IF NOT EXISTS k8s_cluster_credentials",
		"CREATE TABLE IF NOT EXISTS k8s_namespaces",
		"CREATE TABLE IF NOT EXISTS k8s_namespace_shares",
		"CREATE TABLE IF NOT EXISTS k8s_outbox",
	} {
		if !strings.Contains(sql, table) {
			t.Fatalf("k8s migration missing table declaration %q", table)
		}
	}

	// UUIDs stored as VARCHAR(36) (plan decision 1: GORM-compatible, Hub convention).
	for _, col := range []string{
		"id                  VARCHAR(36) NOT NULL",
		"ref                 VARCHAR(36) NOT NULL",
	} {
		if !strings.Contains(sql, col) {
			t.Fatalf("k8s migration missing UUID column %q", col)
		}
	}

	// revision BIGINT optimistic-lock counter on clusters and namespaces.
	for _, fragment := range []string{
		"revision            BIGINT NOT NULL DEFAULT 1",
		"credential_revision BIGINT NOT NULL",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("k8s migration missing revision column %q", fragment)
		}
	}

	// Versioned AEAD triple (design §5.5).
	for _, col := range []string{
		"ciphertext          BYTEA NOT NULL",
		"nonce               BYTEA NOT NULL",
		"key_version         VARCHAR(32) NOT NULL",
	} {
		if !strings.Contains(sql, col) {
			t.Fatalf("k8s migration missing AEAD column %q", col)
		}
	}

	// JSONB labels/annotations on clusters and namespaces.
	for _, col := range []string{
		"labels_json         JSONB NOT NULL DEFAULT '{}'::jsonb",
		"labels_json              JSONB NOT NULL DEFAULT '{}'::jsonb",
		"annotations_json         JSONB NOT NULL DEFAULT '{}'::jsonb",
	} {
		if !strings.Contains(sql, col) {
			t.Fatalf("k8s migration missing JSONB column %q", col)
		}
	}

	// Namespace -> Cluster FK must be RESTRICT (business-level cascade, design §5.7.5).
	if !strings.Contains(sql, "FOREIGN KEY (cluster_id) REFERENCES k8s_clusters(id) ON DELETE RESTRICT") {
		t.Fatal("k8s_namespaces.cluster_id must ON DELETE RESTRICT (business-level cascade, not DB cascade)")
	}

	// Partial unique indexes (soft-delete aware, design §8.1/§8.3).
	for _, fragment := range []string{
		"ON k8s_clusters(org_id, name) WHERE deleted_at IS NULL",
		"ON k8s_namespaces(cluster_id, kube_name) WHERE deleted_at IS NULL",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("k8s migration missing partial unique index %q", fragment)
		}
	}

	// Credential revision uniqueness (design §8.2).
	if !strings.Contains(sql, "UNIQUE (cluster_id, credential_revision)") {
		t.Fatal("k8s_cluster_credentials must enforce UNIQUE(cluster_id, credential_revision)")
	}

	// updated_at trigger reuses hub_set_updated_at (Hub convention, see 000001).
	if !strings.Contains(sql, "EXECUTE FUNCTION hub_set_updated_at()") {
		t.Fatal("k8s migration missing hub_set_updated_at trigger")
	}

	// Status / lifecycle / visibility CHECK constraints (design §5.7.1/§6.3/§7.5).
	for _, fragment := range []string{
		"CHECK (status IN ('CREATING','READY','PROBING','DEGRADED','DELETING','DELETED','FAILED'))",
		"CHECK (visibility IN ('PRIVATE','PUBLIC'))",
		"CHECK (visibility_sync_status IN ('SYNCED','PUBLISHING','REVOKING','SYNC_FAILED'))",
		"CHECK (lifecycle IN ('CREATING','READY','TERMINATING','FAILED','DELETED'))",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("k8s migration missing CHECK constraint %q", fragment)
		}
	}

	// Outbox status enum (design §8.5).
	if !strings.Contains(sql, "CHECK (status IN ('pending','in_progress','done','failed'))") {
		t.Fatal("k8s_outbox missing status CHECK constraint")
	}
}
