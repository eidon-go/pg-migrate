//go:build integration

package migrator_test

import (
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
)

// newBaselineService wires a service over a directory holding basicMigrations.
func newBaselineService(t *testing.T) (*migrator.Service, *migrator.CustomExecutor, *sql.DB) {
	t.Helper()

	pg := setupTestDB(t)
	cleanupMigrations(t, pg)

	dir := t.TempDir()
	writeMigrations(t, dir, basicMigrations())

	executor := newTestExecutor(t, pg.GetDB())
	service := migrator.NewService(
		source.NewFS(os.DirFS(dir)), executor, &mockLockerForServiceTest{}, nil)

	return service, executor, pg.GetDB()
}

// The point of baseline: the schema already exists, so the migrations that
// describe it must be recorded rather than run. Running 001 here would fail —
// test_users is created by hand first, exactly as it would already exist on a
// database being adopted.
//
//nolint:paralleltest // sequential subtests share a single test database by design
func TestBaselineAdoptsAnExistingSchema(t *testing.T) {
	service, executor, sqlDB := newBaselineService(t)
	ctx := t.Context()

	result, err := service.Baseline(ctx, "002_posts")
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}

	if len(result.Applied) != 2 {
		t.Fatalf("recorded %d migrations, want 2", len(result.Applied))
	}

	applied, err := executor.GetAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("GetAppliedMigrations: %v", err)
	}

	if len(applied) != 2 {
		t.Fatalf("ledger holds %d rows, want 2", len(applied))
	}

	// Nothing ran, so the tables the scripts would have created must not exist.
	for _, table := range []string{"test_users", "test_posts"} {
		var exists bool
		if err := sqlDB.QueryRowContext(ctx,
			"SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}

		if exists {
			t.Errorf("table %s was created; baseline must not execute scripts", table)
		}
	}

	// The stored rollback script must match the file, so a later Down runs the
	// same text a normal apply would have stored.
	if applied[0].DownScript != "DROP TABLE test_users;" {
		t.Errorf("down_script = %q, want the script from the file", applied[0].DownScript)
	}
}

//nolint:paralleltest // sequential subtests share a single test database by design
func TestBaselineRefusesWhenLedgerIsNotEmpty(t *testing.T) {
	service, _, _ := newBaselineService(t)
	ctx := t.Context()

	if _, err := service.Baseline(ctx, "001_init"); err != nil {
		t.Fatalf("first Baseline: %v", err)
	}

	// A second baseline would be indistinguishable from marking a migration
	// applied without running it.
	_, err := service.Baseline(ctx, "003_comments")
	if !errors.Is(err, migrator.ErrAlreadyRecorded) {
		t.Fatalf("error = %v, want ErrAlreadyRecorded", err)
	}
}

//nolint:paralleltest // sequential subtests share a single test database by design
func TestBaselineRejectsUnknownID(t *testing.T) {
	service, executor, _ := newBaselineService(t)
	ctx := t.Context()

	_, err := service.Baseline(ctx, "999_does_not_exist")
	if !errors.Is(err, migrator.ErrBaselineNotFound) {
		t.Fatalf("error = %v, want ErrBaselineNotFound", err)
	}

	// A rejected baseline must leave nothing behind.
	applied, err := executor.GetAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("GetAppliedMigrations: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("ledger holds %d rows after a rejected baseline, want 0", len(applied))
	}
}

// After adopting through 002, Up must apply only what comes after it.
//
//nolint:paralleltest // sequential subtests share a single test database by design
func TestUpAfterBaselineAppliesOnlyTheRest(t *testing.T) {
	service, _, sqlDB := newBaselineService(t)
	ctx := t.Context()

	// Stand in for the schema that already exists on the adopted database.
	for _, ddl := range []string{
		"CREATE TABLE test_users (id INT PRIMARY KEY)",
		"CREATE TABLE test_posts (id INT PRIMARY KEY)",
	} {
		if _, err := sqlDB.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("seed schema: %v", err)
		}
	}

	if _, err := service.Baseline(ctx, "002_posts"); err != nil {
		t.Fatalf("Baseline: %v", err)
	}

	result, err := service.Up(ctx)
	if err != nil {
		t.Fatalf("Up after baseline: %v", err)
	}

	if len(result.Applied) != 1 || result.Applied[0].ID != "003_comments" {
		t.Fatalf("Up applied %v, want only 003_comments", result.Applied)
	}
}
