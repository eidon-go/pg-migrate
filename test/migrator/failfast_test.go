//go:build integration

package migrator_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
)

// mockLockerForFailFast is a no-op locker for testing.
type mockLockerForFailFast struct{}

func (m *mockLockerForFailFast) AcquireLock(context.Context) error { return nil }
func (m *mockLockerForFailFast) ReleaseLock(context.Context) error { return nil }

// recordFailure applies a notransaction migration whose second statement fails,
// which is the only way a failed row can end up in the bookkeeping table: a
// transactional failure rolls its own row back.
func recordFailure(t *testing.T, executor *migrator.CustomExecutor, id, table string) {
	t.Helper()

	m := &migrator.Migration{
		ID: id,
		UpScript: "-- +migrate notransaction\n" +
			"CREATE TABLE " + table + " (id INT);\n" +
			"CREATE TABLE " + table + " (id INT);",
		DownScript: "DROP TABLE IF EXISTS " + table + ";",
	}
	if err := executor.ApplyMigration(t.Context(), m); err == nil {
		t.Fatal("expected the second statement to fail")
	}

	applied, err := executor.GetAppliedMigrations(t.Context())
	if err != nil {
		t.Fatalf("failed to read applied migrations: %v", err)
	}

	for _, record := range applied {
		if record.ID == id && record.Error != nil {
			return
		}
	}

	t.Fatalf("migration %s was not recorded as failed", id)
}

// Every entry point must refuse to touch the database while a failed migration is
// recorded — including Rollback, and including when the failed migration is not
// one the operation would otherwise involve.
func TestFailFast_AllEntryPointsRefuse(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	writeMigrations(t, tmpDir, basicMigrations())

	operations := map[string]func(*migrator.Service) error{
		"Up": func(svc *migrator.Service) error {
			_, err := svc.Up(t.Context())

			return err
		},
		"Reconcile": func(svc *migrator.Service) error {
			_, err := svc.Reconcile(t.Context(),
				migrator.RunOptions{AllowRollback: true, AllowInterleaved: true})

			return err
		},
		"Rollback": func(svc *migrator.Service) error {
			_, err := svc.Rollback(t.Context(), 1)

			return err
		},
	}

	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pg := setupTestDB(t)

			executor := newTestExecutor(t, pg.GetDB())
			if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
				t.Fatalf("failed to ensure table: %v", err)
			}

			recordFailure(t, executor, "001_broken", "ff_"+strings.ToLower(name))

			svc := migrator.NewService(
				source.NewFS(os.DirFS(tmpDir)), executor, &mockLockerForFailFast{}, nil)

			err := operation(svc)
			if !errors.Is(err, migrator.ErrFailedMigrations) {
				t.Fatalf("expected errors.Is(err, ErrFailedMigrations), got: %v", err)
			}

			if !strings.Contains(err.Error(), "001_broken") {
				t.Errorf("error should name the failed migration, got: %v", err)
			}

			// Nothing may have changed: the failed row is still the only row.
			applied, err := executor.GetAppliedMigrations(t.Context())
			if err != nil {
				t.Fatalf("failed to read applied migrations: %v", err)
			}

			if len(applied) != 1 || applied[0].ID != "001_broken" || applied[0].Error == nil {
				t.Errorf("state must be untouched, got %d rows: %+v", len(applied), applied)
			}
		})
	}
}

// A failed migration blocks operations even when successful ones exist around it:
// the check is about the ledger as a whole, not about the operation's own targets.
func TestFailFast_BlocksEvenWhenUnrelated(t *testing.T) {
	t.Parallel()

	pg := setupTestDB(t)

	executor := newTestExecutor(t, pg.GetDB())
	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	// A successful migration first, then a failed one.
	ok := &migrator.Migration{
		ID:         "001_ok",
		UpScript:   "CREATE TABLE ff_ok (id INT);",
		DownScript: "DROP TABLE ff_ok;",
	}
	if err := executor.ApplyMigration(t.Context(), ok); err != nil {
		t.Fatalf("failed to apply the good migration: %v", err)
	}

	recordFailure(t, executor, "002_broken", "ff_unrelated")

	tmpDir := t.TempDir()
	writeMigrations(t, tmpDir, basicMigrations())
	svc := migrator.NewService(
		source.NewFS(os.DirFS(tmpDir)), executor, &mockLockerForFailFast{}, nil)

	// Rolling back only the newest would not even reach 001_ok, but the run is
	// still refused.
	_, err := svc.Rollback(t.Context(), 1)
	if !errors.Is(err, migrator.ErrFailedMigrations) {
		t.Fatalf("expected errors.Is(err, ErrFailedMigrations), got: %v", err)
	}

	if !integrationTableExists(t, pg.GetDB(), "ff_ok") {
		t.Error("the successful migration's table must be untouched")
	}
}

// Once the failed row is resolved by hand, operations resume.
func TestFailFast_ResumesAfterManualCleanup(t *testing.T) {
	t.Parallel()

	pg := setupTestDB(t)

	executor := newTestExecutor(t, pg.GetDB())
	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	recordFailure(t, executor, "001_broken", "ff_cleanup")

	// What an operator would do: bring the schema to a known state, drop the row.
	if _, err := pg.GetDB().ExecContext(t.Context(), "DROP TABLE IF EXISTS ff_cleanup"); err != nil {
		t.Fatalf("failed to clean up the schema: %v", err)
	}

	if _, err := pg.GetDB().ExecContext(t.Context(),
		"DELETE FROM migrations WHERE id = $1", "001_broken"); err != nil {
		t.Fatalf("failed to delete the failed row: %v", err)
	}

	tmpDir := t.TempDir()
	writeMigrations(t, tmpDir, basicMigrations())
	svc := migrator.NewService(
		source.NewFS(os.DirFS(tmpDir)), executor, &mockLockerForFailFast{}, nil)

	if _, err := svc.Rollback(t.Context(), 1); err != nil {
		t.Fatalf("operations should resume after manual cleanup, got: %v", err)
	}
}
