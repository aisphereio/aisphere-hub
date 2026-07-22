package gitengine

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// newTestEngine stands up an Engine backed by SQLite + a temp DataPath,
// creates the hub_skill_profiles table the Engine expects, and seeds a
// skill named "search" with an initial scaffold commit (SKILL.md +
// skill.yaml on main). It mirrors the setup in engine_test.go so the
// file API can be exercised against a real (embedded) bare repo.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "kernel.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database.DB() error = %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	engine, err := newWithDatabase(context.Background(), Config{DataPath: t.TempDir()}, database, "sqlite")
	if err != nil {
		t.Fatalf("newWithDatabase() error = %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	if err := database.Exec(`CREATE TABLE hub_skill_profiles (
		repository_id INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
		display_name TEXT NOT NULL DEFAULT '', org_id TEXT NOT NULL, project_id TEXT NOT NULL DEFAULT '',
		created_by_type TEXT NOT NULL, created_by_id TEXT NOT NULL, created_by_name TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL,
		lifecycle_status TEXT NOT NULL, default_branch TEXT NOT NULL, provision_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create profile table: %v", err)
	}

	if _, err := engine.CreateSkill(context.Background(), &biz.GitSkill{
		Name: "search", DisplayName: "Search", Description: "Search tools",
		OwnerID: "owner-1", OwnerType: "user", OrgID: "org-1",
		Visibility: biz.SkillVisibilityPrivate, Status: biz.SkillStatusProvisioning,
	}); err != nil {
		t.Fatalf("CreateSkill() error = %v", err)
	}
	return engine
}

// TestListFilesRoot verifies the seeded scaffold is readable via the
// file API and reports both seeded files at the root.
func TestListFilesRoot(t *testing.T) {
	engine := newTestEngine(t)
	entries, err := engine.ListFiles(context.Background(), "search", "", "HEAD")
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	names := map[string]bool{}
	for _, e := range entries {
		if e.Type != "file" {
			t.Errorf("entry %q type = %q, want file", e.Name, e.Type)
		}
		names[e.Name] = true
	}
	if !names["SKILL.md"] || !names["skill.yaml"] {
		t.Errorf("expected SKILL.md + skill.yaml, got %v", names)
	}
}

// TestCreateGetFile covers the create→get round trip on a fresh path.
func TestCreateGetFile(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	created, err := engine.CreateFile(ctx, "search", "docs/intro.md", "# Intro", "add intro", "main", "tester", "tester@example.com")
	if err != nil {
		t.Fatalf("CreateFile() error = %v", err)
	}
	if created.SHA == "" {
		t.Fatal("CreateFile() returned empty SHA")
	}
	got, err := engine.GetFileContent(ctx, "search", "docs/intro.md", "HEAD")
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
	if got.SHA != created.SHA {
		t.Errorf("got SHA = %q, want %q", got.SHA, created.SHA)
	}
	if got.Encoding != "base64" {
		t.Errorf("encoding = %q, want base64", got.Encoding)
	}
	// Content is base64; decode and compare.
	dec, err := decodeB64(got.Content)
	if err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if string(dec) != "# Intro" {
		t.Errorf("content = %q, want %q", string(dec), "# Intro")
	}
}

// TestCreateFilePreservesSiblings is the regression test for the
// buildTreeWithFile bug we ported from aisphere-git-server: writing a
// file into a directory that already has files must NOT delete the
// existing siblings. We create two files in docs/, then a third, and
// assert all three remain.
func TestCreateFilePreservesSiblings(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	for _, p := range []string{"docs/a.md", "docs/b.md"} {
		if _, err := engine.CreateFile(ctx, "search", p, p, "add "+p, "main", "tester", "tester@example.com"); err != nil {
			t.Fatalf("CreateFile(%s) error = %v", p, err)
		}
	}
	if _, err := engine.CreateFile(ctx, "search", "docs/c.md", "c", "add c", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("CreateFile(docs/c.md) error = %v", err)
	}
	entries, err := engine.ListFiles(ctx, "search", "docs", "HEAD")
	if err != nil {
		t.Fatalf("ListFiles(docs) error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("siblings after create = %d, want 3 (buildTreeWithFile regression)", len(entries))
	}
}

// TestUpdateFilePreservesSiblings is the same regression on the update
// path: updating one file in a populated directory must not drop the
// others.
func TestUpdateFilePreservesSiblings(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	if _, err := engine.CreateFile(ctx, "search", "docs/a.md", "a", "add a", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("CreateFile(a) error = %v", err)
	}
	b, err := engine.CreateFile(ctx, "search", "docs/b.md", "b", "add b", "main", "tester", "tester@example.com")
	if err != nil {
		t.Fatalf("CreateFile(b) error = %v", err)
	}
	if _, err := engine.UpdateFile(ctx, "search", "docs/b.md", "b-v2", "update b", b.SHA, "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("UpdateFile(b) error = %v", err)
	}
	entries, err := engine.ListFiles(ctx, "search", "docs", "HEAD")
	if err != nil {
		t.Fatalf("ListFiles(docs) error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("siblings after update = %d, want 2 (buildTreeWithFile regression)", len(entries))
	}
	// Verify the updated file actually has new content.
	got, err := engine.GetFileContent(ctx, "search", "docs/b.md", "HEAD")
	if err != nil {
		t.Fatalf("GetFileContent(b) error = %v", err)
	}
	dec, _ := decodeB64(got.Content)
	if string(dec) != "b-v2" {
		t.Errorf("b content = %q, want b-v2", string(dec))
	}
}

// TestUpdateFileShaMismatch confirms the optimistic-concurrency guard:
// an update with a stale sha must fail with ErrFileAlreadyExists (409)
// and must NOT modify the file.
func TestUpdateFileShaMismatch(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	created, err := engine.CreateFile(ctx, "search", "notes.md", "v1", "add", "main", "tester", "tester@example.com")
	if err != nil {
		t.Fatalf("CreateFile() error = %v", err)
	}
	_ = created // we deliberately use a stale sha below, not created.SHA
	stale := "0000000000000000000000000000000000000000"
	_, err = engine.UpdateFile(ctx, "search", "notes.md", "v2", "stale update", stale, "main", "tester", "tester@example.com")
	if !errorx.IsCode(err, biz.ErrFileAlreadyExists.Code()) {
		t.Fatalf("UpdateFile with stale sha error = %v, want ErrFileAlreadyExists", err)
	}
	// File content must be unchanged.
	got, err := engine.GetFileContent(ctx, "search", "notes.md", "HEAD")
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
	dec, _ := decodeB64(got.Content)
	if string(dec) != "v1" {
		t.Errorf("content after rejected update = %q, want v1", string(dec))
	}
}

// TestUpdateFileEmptyShaAllowed: an empty sha means "no CAS" and must
// succeed. The editor uses this on its first save before it has fetched
// the current blob hash.
func TestUpdateFileEmptyShaAllowed(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	if _, err := engine.CreateFile(ctx, "search", "notes.md", "v1", "add", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("CreateFile() error = %v", err)
	}
	if _, err := engine.UpdateFile(ctx, "search", "notes.md", "v2", "update", "", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("UpdateFile with empty sha error = %v", err)
	}
}

// TestCreateFileAlreadyExists: creating a path that already exists must
// fail with ErrFileAlreadyExists.
func TestCreateFileAlreadyExists(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	if _, err := engine.CreateFile(ctx, "search", "SKILL.md", "overwrite", "dup", "main", "tester", "tester@example.com"); err == nil {
		t.Fatal("CreateFile on existing path error = nil, want ErrFileAlreadyExists")
	} else if !errorx.IsCode(err, biz.ErrFileAlreadyExists.Code()) {
		t.Errorf("error = %v, want ErrFileAlreadyExists", err)
	}
}

// TestDeleteFilePreservesSiblings: deleting one file in a directory
// must not delete its siblings.
func TestDeleteFilePreservesSiblings(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	if _, err := engine.CreateFile(ctx, "search", "docs/a.md", "a", "add a", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("CreateFile(a) error = %v", err)
	}
	if _, err := engine.CreateFile(ctx, "search", "docs/b.md", "b", "add b", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("CreateFile(b) error = %v", err)
	}
	if _, _, err := engine.DeleteFile(ctx, "search", "docs/a.md", "delete a", "", "main", "tester", "tester@example.com"); err != nil {
		t.Fatalf("DeleteFile(a) error = %v", err)
	}
	entries, err := engine.ListFiles(ctx, "search", "docs", "HEAD")
	if err != nil {
		t.Fatalf("ListFiles(docs) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "b.md" {
		t.Fatalf("after delete entries = %v, want [b.md]", entries)
	}
}

// TestListFilesNested: listing a subdirectory returns only its direct
// children.
func TestListFilesNested(t *testing.T) {
	engine := newTestEngine(t)
	ctx := context.Background()
	for _, p := range []string{"docs/a.md", "docs/sub/b.md", "docs/sub/c.md"} {
		if _, err := engine.CreateFile(ctx, "search", p, p, "add", "main", "tester", "tester@example.com"); err != nil {
			t.Fatalf("CreateFile(%s) error = %v", p, err)
		}
	}
	entries, err := engine.ListFiles(ctx, "search", "docs/sub", "HEAD")
	if err != nil {
		t.Fatalf("ListFiles(docs/sub) error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("nested entries = %d, want 2", len(entries))
	}
}

// TestGetFileNotFound: reading a missing path returns ErrFileNotFound.
func TestGetFileNotFound(t *testing.T) {
	engine := newTestEngine(t)
	_, err := engine.GetFileContent(context.Background(), "search", "nope.md", "HEAD")
	if err == nil {
		t.Fatal("GetFileContent(missing) error = nil, want ErrFileNotFound")
	}
	if !errorx.IsCode(err, biz.ErrFileNotFound.Code()) {
		t.Errorf("error = %v, want ErrFileNotFound", err)
	}
}
