package data

import (
	"context"
	"fmt"
	"strings"

	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/migrationx"
	"github.com/aisphereio/soft-serve/pkg/config"
	softdb "github.com/aisphereio/soft-serve/pkg/db"
	softmigrate "github.com/aisphereio/soft-serve/pkg/db/migrate"
)

const softServePostgresDriver = "postgres"

// softServeConfig builds the Soft Serve config required by the embedded
// migration path. Soft Serve migrations read config.Config from the context
// (admin keys in 0001_create_tables, the data path in 0003_migrate_lfs_objects);
// without a config attached, config.FromContext returns nil and the migrations
// panic with a nil pointer dereference.
//
// dataPath must match the value passed to gitengine.New so that LFS object
// relocation resolves to the same directory the running engine manages.
func softServeConfig(dataPath, driverName string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.DataPath = strings.TrimSpace(dataPath)
	cfg.DB.Driver = driverName
	cfg.DB.DataSource = ""
	return cfg
}

// ApplyStorageMigrations owns the startup ordering for the shared logical
// database. Soft Serve must create its canonical repository tables before Hub
// migrations are allowed to add foreign keys to repos(id).
//
// The Kernel dbx pool remains the only connection pool. Soft Serve receives a
// non-owning wrapper and retains its independent `migrations` history table;
// Hub continues to use the migration table configured for migrationx.
//
// dataPath is the Git data directory that the embedded Soft Serve engine
// manages. It is attached to the Soft Serve config so that migrations which
// reference the data path (for example LFS object relocation) resolve to the
// same directory used by the running engine. It must match the value passed to
// gitengine.New.
func ApplyStorageMigrations(ctx context.Context, database dbx.DB, dataPath string, hubConfig migrationx.Config) error {
	if database == nil {
		return fmt.Errorf("storage migrations: Kernel database is required")
	}
	gormDB := database.GORM(ctx)
	if gormDB == nil {
		return fmt.Errorf("storage migrations: Kernel GORM database is required")
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return fmt.Errorf("storage migrations: resolve Kernel SQL database: %w", err)
	}

	softCtx := config.WithContext(ctx, softServeConfig(dataPath, softServePostgresDriver))

	softDatabase, err := softdb.NewWithSQLDB(softCtx, sqlDB, softServePostgresDriver, false)
	if err != nil {
		return fmt.Errorf("storage migrations: wrap database for Soft Serve: %w", err)
	}
	if err := softmigrate.Migrate(softCtx, softDatabase); err != nil {
		return fmt.Errorf("storage migrations: apply Soft Serve migrations: %w", err)
	}

	hubConfig = hubConfig.Normalize()
	if !hubConfig.Enabled {
		return nil
	}
	if err := migrationx.Apply(ctx, database, hubConfig); err != nil {
		return fmt.Errorf("storage migrations: apply Hub migrations: %w", err)
	}
	return nil
}
