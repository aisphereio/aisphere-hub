package gitengine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakePermissionChecker struct {
	request authz.CheckRequest
	allow   bool
}

func (f *fakePermissionChecker) Check(_ context.Context, request authz.CheckRequest) (authz.Decision, error) {
	f.request = request
	return authz.Decision{Allowed: f.allow}, nil
}

func TestEngineCreatesRepositoryUsingSharedDatabase(t *testing.T) {
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
		created_by_type TEXT NOT NULL, created_by_id TEXT NOT NULL, visibility TEXT NOT NULL,
		lifecycle_status TEXT NOT NULL, default_branch TEXT NOT NULL, provision_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create profile table: %v", err)
	}

	created, err := engine.CreateSkill(context.Background(), &biz.GitSkill{
		Name: "search", DisplayName: "Search", Description: "Search tools",
		OwnerID: "owner-1", OwnerType: "user", OrgID: "org-1",
		Visibility: biz.SkillVisibilityPrivate, Status: biz.SkillStatusProvisioning,
	})
	if err != nil {
		t.Fatalf("CreateSkill() error = %v", err)
	}
	if created.RepositoryID == 0 {
		t.Fatal("CreateSkill() did not return canonical repository id")
	}
	var profileCount int64
	if err := database.Table("hub_skill_profiles").Where("repository_id = ?", created.RepositoryID).Count(&profileCount).Error; err != nil {
		t.Fatal(err)
	}
	if profileCount != 1 {
		t.Fatalf("profile rows = %d, want 1", profileCount)
	}
	if _, err := engine.backend.Repository(context.Background(), "search"); err != nil {
		t.Fatalf("Repository() error = %v", err)
	}

	// Closing the engine must not close the Kernel-owned database.
	if err := engine.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("Kernel-owned database was closed: %v", err)
	}
}

func TestNewRequiresKernelDatabase(t *testing.T) {
	if _, err := New(context.Background(), Config{DataPath: t.TempDir()}, nil); err == nil {
		t.Fatal("New(nil database) error = nil, want error")
	}
}

func TestAuthorizeRefUpdateChecksCanonicalSkillPermission(t *testing.T) {
	checker := &fakePermissionChecker{allow: true}
	principal := authn.Principal{SubjectID: "alice", SubjectType: authn.SubjectTypeUser, OrgID: "acme"}
	if err := authorizeRefUpdate(context.Background(), checker, principal, "search", "publish"); err != nil {
		t.Fatalf("authorizeRefUpdate() error = %v", err)
	}
	if got, want := checker.request.Resource, (authz.ObjectRef{Type: "skill", ID: "search"}); got != want {
		t.Fatalf("resource = %#v, want %#v", got, want)
	}
	if got, want := checker.request.Permission, "publish"; got != want {
		t.Fatalf("permission = %q, want %q", got, want)
	}
}

func TestRequiredPermissionForRefUpdate(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		old  string
		new  string
		ff   bool
		want string
	}{
		{name: "feature update", ref: "refs/heads/alice/topic", old: "a", new: "b", ff: true, want: "edit"},
		{name: "publish main", ref: "refs/heads/main", old: "a", new: "b", ff: true, want: "publish"},
		{name: "force main", ref: "refs/heads/main", old: "a", new: "b", ff: false, want: "manage"},
		{name: "delete main", ref: "refs/heads/main", old: "a", new: ZeroHash, ff: false, want: "manage"},
		{name: "create release", ref: "refs/tags/v1.0.0", old: ZeroHash, new: "b", ff: false, want: "publish"},
		{name: "move release", ref: "refs/tags/v1.0.0", old: "a", new: "b", ff: false, want: "manage"},
		{name: "delete release", ref: "refs/tags/v1.0.0", old: "a", new: ZeroHash, ff: false, want: "manage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiredPermissionForRefUpdate("main", tt.ref, tt.old, tt.new, tt.ff); got != tt.want {
				t.Fatalf("RequiredPermissionForRefUpdate() = %q, want %q", got, tt.want)
			}
		})
	}
}
