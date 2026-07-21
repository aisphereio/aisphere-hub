package data

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aisphereio/soft-serve/pkg/config"
	softdb "github.com/aisphereio/soft-serve/pkg/db"
	softmigrate "github.com/aisphereio/soft-serve/pkg/db/migrate"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const softServeSQLiteDriver = "sqlite"

// TestSoftServeConfigPreventsMigrationPanic reproduces the production startup
// failure fixed in ApplyStorageMigrations: Soft Serve migrations read a
// config.Config from the context, and without one attached they dereference a
// nil config. The config produced by softServeConfig must be sufficient for
// every Soft Serve migration to run against a fresh database without panicking.
func TestSoftServeConfigPreventsMigrationPanic(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "soft.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database.DB() error = %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	dataPath := t.TempDir()
	softCfg := softServeConfig(dataPath, softServeSQLiteDriver)
	ctx := config.WithContext(context.Background(), softCfg)

	softDatabase, err := softdb.NewWithSQLDB(ctx, sqlDB, softServeSQLiteDriver, false)
	if err != nil {
		t.Fatalf("NewWithSQLDB() error = %v", err)
	}

	// Before the fix this call panicked with a nil pointer dereference inside
	// 0001_create_tables because config.FromContext returned nil.
	if err := softmigrate.Migrate(ctx, softDatabase); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	// The full migration set must have been recorded. Soft Serve tracks applied
	// migrations in a `migrations` table; the highest version is 3.
	var applied softmigrate.Migrations
	if err := database.Table("migrations").Order("version DESC").First(&applied).Error; err != nil {
		t.Fatalf("query migrations table: %v", err)
	}
	if applied.Version != 3 {
		t.Fatalf("applied migration version = %d, want 3", applied.Version)
	}

	// repos is the canonical table Hub migrations depend on; ensure it exists
	// so subsequent foreign keys to repos(id) can be created.
	if !database.Migrator().HasTable("repos") {
		t.Fatal("repos table was not created by Soft Serve migrations")
	}
}
