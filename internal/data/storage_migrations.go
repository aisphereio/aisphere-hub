package data

import (
	"context"
	"fmt"

	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/migrationx"
	softdb "github.com/aisphereio/soft-serve/pkg/db"
	softmigrate "github.com/aisphereio/soft-serve/pkg/db/migrate"
)

const softServePostgresDriver = "postgres"

// ApplyStorageMigrations owns the startup ordering for the shared logical
// database. Soft Serve must create its canonical repository tables before Hub
// migrations are allowed to add foreign keys to repos(id).
//
// The Kernel dbx pool remains the only connection pool. Soft Serve receives a
// non-owning wrapper and retains its independent `migrations` history table;
// Hub continues to use the migration table configured for migrationx.
func ApplyStorageMigrations(ctx context.Context, database dbx.DB, hubConfig migrationx.Config) error {
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

	softDatabase, err := softdb.NewWithSQLDB(ctx, sqlDB, softServePostgresDriver, false)
	if err != nil {
		return fmt.Errorf("storage migrations: wrap database for Soft Serve: %w", err)
	}
	if err := softmigrate.Migrate(ctx, softDatabase); err != nil {
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
