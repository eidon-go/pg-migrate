//go:build integration

package migrate_test

import (
	"database/sql"
	"errors"
	"os"
	"testing"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// relationExists reports whether a relation is visible on the search_path.
func relationExists(t *testing.T, sqlDB *sql.DB, name string) bool {
	t.Helper()

	var exists bool
	if err := sqlDB.QueryRowContext(t.Context(),
		"SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatalf("failed to check for relation %s: %v", name, err)
	}

	return exists
}

// Statements of one notransaction script share a session, so session-scoped
// state a script sets applies to the statements that follow it. A temporary
// table is the sharpest test: it exists only for its own session.
func TestNoTransaction_StatementsShareOneSession(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// Force every pooled connection to be a fresh one, so statements landing on
	// different connections would be guaranteed rather than merely likely.
	dbConn.GetDB().SetMaxIdleConns(0)

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_session",
		"-- +migrate notransaction\n"+
			"CREATE TEMP TABLE session_probe (id INT);\n"+
			"INSERT INTO session_probe VALUES (1);\n"+
			"CREATE TABLE session_proof (id INT);",
		"DROP TABLE IF EXISTS session_proof;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("statements of one notransaction script must share a session: %v", err)
	}

	if !relationExists(t, dbConn.GetDB(), "session_proof") {
		t.Error("the script did not run to completion")
	}
}

// Whatever a notransaction script sets on its session must not survive into the
// caller's pool: this library borrows the application's *sql.DB.
func TestNoTransaction_SessionStateDoesNotLeakIntoPool(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// One reusable connection, so any leak is guaranteed to be observed.
	dbConn.GetDB().SetMaxIdleConns(1)
	dbConn.GetDB().SetMaxOpenConns(2)

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_guc",
		"-- +migrate notransaction\n"+
			"SET statement_timeout = '1234ms';\n"+
			"CREATE TABLE guc_probe (id INT);",
		"DROP TABLE IF EXISTS guc_probe;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	// Sample repeatedly: a leak would show up on whichever connection was reused.
	for i := range 5 {
		var timeout string
		if err := dbConn.GetDB().QueryRowContext(t.Context(), "SHOW statement_timeout").Scan(&timeout); err != nil {
			t.Fatalf("failed to read statement_timeout: %v", err)
		}

		if timeout == "1234ms" {
			t.Fatalf("sample %d: the migration's statement_timeout leaked into the caller's pool", i)
		}
	}
}

// A notransaction rollback that dies partway has already dropped some of what it
// meant to drop. That must be recorded, or the next run reports the database as
// in sync while the schema has quietly drifted.
func TestNoTransaction_PartialRollbackIsRecorded(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	// The down script's second statement fails, so the first has already run.
	writeMigration(t, tmpDir, "001_partial_down",
		"CREATE TABLE drift_a (id INT);\nCREATE TABLE drift_b (id INT);",
		"-- +migrate notransaction\n"+
			"DROP TABLE drift_a;\n"+
			"DROP TABLE drift_missing;\n"+
			"DROP TABLE drift_b;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	if _, err := migrate.DownAll(t.Context(), dbConn.GetDB()); err == nil {
		t.Fatal("expected the partial rollback to fail")
	}

	// drift_a is gone, drift_b is not: the schema is between two states.
	if relationExists(t, dbConn.GetDB(), "drift_a") {
		t.Error("drift_a should already have been dropped")
	}

	if !relationExists(t, dbConn.GetDB(), "drift_b") {
		t.Error("drift_b should still exist")
	}

	// The row must say so.
	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 1 {
		t.Fatalf("expected the migration still recorded, got %v", applied)
	}

	if !applied[0].Failed {
		t.Error("a partially-failed rollback must be recorded as failed, " +
			"otherwise the next run reports the database as in sync")
	}

	// And everything now refuses, instead of declaring success.
	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); !errors.Is(err, migrate.ErrFailedMigrations) {
		t.Errorf("expected ErrFailedMigrations after a partial rollback, got: %v", err)
	}
}

// Forget is the way out: it clears the row without running anything, so normal
// operation resumes once the operator has fixed the schema.
func TestNoTransaction_ForgetClearsTheWedge(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_wedge",
		"-- +migrate notransaction\nCREATE TABLE wedge (id INT);\nCREATE TABLE wedge (id INT);",
		"DROP TABLE IF EXISTS wedge;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err == nil {
		t.Fatal("expected the migration to fail")
	}

	// Status hands the operator what they need, including the rollback script.
	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 1 || !applied[0].Failed {
		t.Fatalf("expected one failed migration, got %v", applied)
	}

	if applied[0].DownScript == "" {
		t.Error("Status must report the stored rollback script; " +
			"resolving this by hand starts with reading it")
	}

	// What an operator does: fix the schema, then clear the row.
	if _, dropErr := dbConn.GetDB().ExecContext(t.Context(), "DROP TABLE IF EXISTS wedge"); dropErr != nil {
		t.Fatalf("failed to clean up: %v", dropErr)
	}

	if forgetErr := migrate.Forget(t.Context(), dbConn.GetDB(), "001_wedge"); forgetErr != nil {
		t.Fatalf("Forget failed: %v", forgetErr)
	}

	applied, err = migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("Forget should have cleared the row, got %v", applied)
	}

	// Forgetting something that is not there is an error, not a silent no-op.
	if err := migrate.Forget(t.Context(), dbConn.GetDB(), "001_wedge"); err == nil {
		t.Error("expected Forget to report an unknown migration")
	}
}
