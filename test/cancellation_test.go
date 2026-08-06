//go:build integration

package migrate_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// A notransaction script that is killed by the caller's deadline has already
// committed its earlier statements. The bookkeeping row must be written anyway —
// if it is lost, the next run re-applies a migration that already half-ran and
// reports success.
func TestCancellation_NoTransactionRecordsDespiteDeadline(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	// The first statement commits, the second outlives the deadline.
	writeMigration(t, tmpDir, "001_slow",
		"-- +migrate notransaction\n"+
			"CREATE TABLE ledger (id INT);\n"+
			"INSERT INTO ledger VALUES (1);\n"+
			"SELECT pg_sleep(5);",
		"DROP TABLE IF EXISTS ledger;")

	deadlineCtx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()

	if _, err := migrate.Up(deadlineCtx, dbConn.GetDB(), os.DirFS(tmpDir)); err == nil {
		t.Fatal("expected the migration to fail on the deadline")
	}

	// The row must exist and be marked failed.
	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 1 {
		t.Fatalf("the interrupted migration was not recorded at all, got %v; "+
			"a retry would silently apply it a second time", applied)
	}

	if !applied[0].Failed {
		t.Error("the interrupted migration must be recorded as failed")
	}

	// And a retry must refuse rather than re-run the committed statements.
	_, err = migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir))
	if !errors.Is(err, migrate.ErrFailedMigrations) {
		t.Errorf("retry after an interrupted migration must refuse, got: %v", err)
	}

	// Proof the first statement really did commit: exactly one row, not two.
	var rows int
	if err := dbConn.GetDB().QueryRowContext(t.Context(), "SELECT count(*) FROM ledger").Scan(&rows); err != nil {
		t.Fatalf("failed to count ledger rows: %v", err)
	}

	if rows != 1 {
		t.Errorf("ledger has %d rows, want 1 (2 means the migration was applied twice)", rows)
	}
}

// A deadline that expires while waiting for the advisory lock must surface as a
// failure to acquire, with nothing applied.
func TestCancellation_DeadlineWhileWaitingForLock(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	holder := dbtest.MustConnect(t, cfg)
	defer holder.Close()

	waiter := dbtest.MustConnect(t, cfg)
	defer waiter.Close()

	if err := holder.AcquireLock(t.Context()); err != nil {
		t.Fatalf("failed to take the lock: %v", err)
	}
	defer func() { _ = holder.ReleaseLock(t.Context()) }()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	// Comfortably longer than the setup that precedes the lock wait, so the
	// deadline lands where the test means it to even on a loaded machine, and far
	// shorter than the lock timeout, so it is the context that ends the wait.
	deadlineCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := migrate.Up(deadlineCtx, waiter.GetDB(), os.DirFS(tmpDir),
		migrate.WithLockTimeout(5*time.Minute))
	if err == nil {
		t.Fatal("expected Up to fail while another process holds the lock")
	}

	applied, err := migrate.Status(t.Context(), waiter.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("nothing may have been applied, got %v", applied)
	}
}

// An already-cancelled context must stop the run before it touches anything.
func TestCancellation_PreCancelledContext(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := migrate.Up(cancelledCtx, dbConn.GetDB(), os.DirFS(tmpDir)); err == nil {
		t.Fatal("expected Up to fail on an already-cancelled context")
	}

	var exists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT to_regclass('t1') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("failed to check for t1: %v", err)
	}

	if exists {
		t.Error("t1 was created despite a cancelled context")
	}
}

// A transactional migration cut off by a deadline must leave nothing behind:
// no table, no bookkeeping row.
func TestCancellation_TransactionalMigrationLeavesNothing(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_slow",
		"CREATE TABLE slow_tx (id INT);\nSELECT pg_sleep(5);",
		"DROP TABLE slow_tx;")

	deadlineCtx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()

	if _, err := migrate.Up(deadlineCtx, dbConn.GetDB(), os.DirFS(tmpDir)); err == nil {
		t.Fatal("expected the migration to fail on the deadline")
	}

	var exists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT to_regclass('slow_tx') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("failed to check for slow_tx: %v", err)
	}

	if exists {
		t.Error("slow_tx survived: the transaction should have rolled back")
	}

	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("a rolled-back transaction must leave no bookkeeping row, got %v", applied)
	}
}
