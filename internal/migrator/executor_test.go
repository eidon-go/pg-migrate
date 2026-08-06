package migrator_test

import (
	"context"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

// TestMigrationExecutorInterface checks that CustomExecutor implements the interface.
func TestMigrationExecutorInterface(t *testing.T) {
	t.Parallel()

	// This is a compile-time check
	var _ interface {
		EnsureMigrationsTable(ctx context.Context) error
		ApplyMigration(ctx context.Context, migration *migrator.Migration) error
		RollbackMigration(ctx context.Context, id string) error
		GetAppliedMigrations(ctx context.Context) ([]migrator.AppliedMigration, error)
	} = (*migrator.CustomExecutor)(nil)
}
