//go:build integration

package migrate_test

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// writeMigration writes a migration as the <id>.up.sql / <id>.down.sql pair.
func writeMigration(t *testing.T, dir, id, up, down string) {
	t.Helper()

	halves := map[string]string{
		id + ".up.sql":   up,
		id + ".down.sql": down,
	}
	for name, content := range halves {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}
}

func getAppliedIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.Query("SELECT id FROM migrations ORDER BY seq")
	if err != nil {
		t.Fatalf("query applied migrations: %v", err)
	}
	defer rows.Close()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}

	return ids
}

func TestLibrary_PlanOnEmptyDB(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	analysis, err := migrate.Plan(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir))
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if len(analysis.ToApply) != 1 || analysis.ToApply[0] != "001_init" {
		t.Errorf("expected to apply 001_init, got: %v", analysis.ToApply)
	}

	if len(analysis.ToRollback) != 0 {
		t.Errorf("expected 0 rollback migrations, got: %v", analysis.ToRollback)
	}

	if len(analysis.Interleaved) != 0 {
		t.Errorf("expected 0 interleaved migrations, got: %v", analysis.Interleaved)
	}

	// Plan must not have created the bookkeeping table: it needs no DDL rights.
	var exists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT to_regclass('migrations') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("failed to check for the migrations table: %v", err)
	}

	if exists {
		t.Error("Plan created the migrations table; it must not write anything")
	}
}

func TestLibrary_ReconcileStrict(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// Apply 001 and 002
	tmpDir1 := t.TempDir()
	writeMigration(t, tmpDir1, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir1, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir1), migrate.WithRollback()); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	// Switch to folder containing ONLY 001
	tmpDir2 := t.TempDir()
	writeMigration(t, tmpDir2, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	// Strict mode (no WithRollback()) must reject with ErrDivergence.
	_, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir2))
	if err == nil {
		t.Fatal("expected error due to divergence, but got nil")
	}

	if !errors.Is(err, migrate.ErrDivergence) {
		t.Errorf("expected errors.Is(err, ErrDivergence)=true, got err=%v", err)
	}

	// With WithRollback() it must succeed and roll back 002.
	result, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir2), migrate.WithRollback())
	if err != nil {
		t.Fatalf("Reconcile with rollback allowed failed: %v", err)
	}

	if len(result.RolledBack) != 1 || result.RolledBack[0] != "002_posts" {
		t.Errorf("expected rolled back migration 002_posts.sql, got %v", result.RolledBack)
	}

	applied := getAppliedIDs(t, dbConn.GetDB())
	if len(applied) != 1 || applied[0] != "001_init" {
		t.Errorf("expected DB to have only 001_init.sql, got %v", applied)
	}
}

func TestLibrary_ReconcileInterleavedStrict(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// Apply 001 and 003 (skipping 002 intentionally).
	tmpDir1 := t.TempDir()
	writeMigration(t, tmpDir1, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir1, "003_comments", "CREATE TABLE t3 (id INT);", "DROP TABLE t3;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir1), migrate.WithRollback()); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	// Now add 002 to the file set. Reconcile must detect interleaved apply.
	tmpDir2 := t.TempDir()
	writeMigration(t, tmpDir2, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir2, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")
	writeMigration(t, tmpDir2, "003_comments", "CREATE TABLE t3 (id INT);", "DROP TABLE t3;")

	// Without WithInterleaved() must reject.
	_, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir2), migrate.WithRollback())
	if err == nil {
		t.Fatal("expected ErrInterleaved, got nil")
	}

	if !errors.Is(err, migrate.ErrInterleaved) {
		t.Errorf("expected errors.Is(err, ErrInterleaved)=true, got err=%v", err)
	}

	// Verify nothing was applied for 002 yet (rejected before execution).
	applied := getAppliedIDs(t, dbConn.GetDB())
	if len(applied) != 2 {
		t.Errorf("expected DB unchanged after rejection, got %v", applied)
	}

	// With WithInterleaved() must succeed and report 002 in the Interleaved field.
	result, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir2), migrate.WithRollback(), migrate.WithInterleaved())
	if err != nil {
		t.Fatalf("Reconcile with AllowInterleaved=true failed: %v", err)
	}

	if len(result.Interleaved) != 1 || result.Interleaved[0] != "002_posts" {
		t.Errorf("expected Interleaved=[002_posts.sql], got %v", result.Interleaved)
	}

	if len(result.Applied) != 1 || result.Applied[0] != "002_posts" {
		t.Errorf("expected Applied=[002_posts.sql], got %v", result.Applied)
	}

	applied = getAppliedIDs(t, dbConn.GetDB())
	if len(applied) != 3 {
		t.Errorf("expected 3 applied migrations, got %v", applied)
	}
}

// Switching from one feature branch to another: the DB holds branch A's
// migration, the checked-out branch B has a migration that sorts before it.
// Reconcile restores the common-ancestor state and then applies branch B's
// migration, which is a plain forward apply — WithInterleaved() must NOT be
// required.
func TestLibrary_ReconcileBranchSwitchNeedsNoInterleaved(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// Common ancestor plus branch A's migration.
	branchA := t.TempDir()
	writeMigration(t, branchA, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, branchA, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")
	writeMigration(t, branchA, "20240101_a", "CREATE TABLE ta (id INT);", "DROP TABLE ta;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(branchA), migrate.WithRollback()); err != nil {
		t.Fatalf("Reconcile on branch A failed: %v", err)
	}

	// Branch B: A's migration is gone, and B adds one that sorts before it.
	branchB := t.TempDir()
	writeMigration(t, branchB, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, branchB, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")
	writeMigration(t, branchB, "20231201_b", "CREATE TABLE tb (id INT);", "DROP TABLE tb;")

	result, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(branchB), migrate.WithRollback())
	if err != nil {
		t.Fatalf("Reconcile on branch B must not require WithInterleaved(), got: %v", err)
	}

	if len(result.Interleaved) != 0 {
		t.Errorf("expected Interleaved=[], got %v", result.Interleaved)
	}

	if len(result.RolledBack) != 1 || result.RolledBack[0] != "20240101_a" {
		t.Errorf("expected RolledBack=[20240101_a.sql], got %v", result.RolledBack)
	}

	if len(result.Applied) != 1 || result.Applied[0] != "20231201_b" {
		t.Errorf("expected Applied=[20231201_b.sql], got %v", result.Applied)
	}

	applied := getAppliedIDs(t, dbConn.GetDB())
	if len(applied) != 3 {
		t.Fatalf("expected 3 applied migrations, got %v", applied)
	}

	if slices.Contains(applied, "20240101_a") {
		t.Errorf("branch A migration should have been rolled back, got %v", applied)
	}

	// Branch A's table must be gone and branch B's must exist.
	for table, wantExists := range map[string]bool{"ta": false, "tb": true} {
		var count int
		if err := dbConn.GetDB().QueryRowContext(t.Context(),
			"SELECT count(*) FROM information_schema.tables WHERE table_name = $1", table,
		).Scan(&count); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}

		if (count == 1) != wantExists {
			t.Errorf("table %s: exists=%v, want %v", table, count == 1, wantExists)
		}
	}
}

func TestLibrary_WithTableNameAndSchema(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	if _, err := dbConn.GetDB().ExecContext(t.Context(), "CREATE SCHEMA infra"); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	result, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir),
		migrate.WithSchema("infra"), migrate.WithTableName("applied_migrations"))
	if err != nil {
		t.Fatalf("Reconcile with a custom table failed: %v", err)
	}

	if len(result.Applied) != 1 {
		t.Errorf("expected 1 applied migration, got %v", result.Applied)
	}

	var count int
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM infra.applied_migrations").Scan(&count); err != nil {
		t.Fatalf("failed to read infra.applied_migrations: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 row in infra.applied_migrations, got %d", count)
	}

	// The default table must not have been touched.
	var defaultExists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(), `
		SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'migrations')`,
	).Scan(&defaultExists); err != nil {
		t.Fatalf("failed to check for the default table: %v", err)
	}

	if defaultExists {
		t.Error("the default migrations table must not be created when a custom one is configured")
	}
}

func TestLibrary_InvalidTableNameIsRejected(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	_, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir),
		migrate.WithTableName(strings.Repeat("x", 64)))
	if err == nil {
		t.Fatal("expected an error for an over-long table name, got nil")
	}

	if !strings.Contains(err.Error(), "over the Postgres limit") {
		t.Errorf("error = %q, want it to mention the identifier limit", err)
	}
}

func TestLibrary_UpWarnOnly(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	// Apply 001 and 002
	tmpDir1 := t.TempDir()
	writeMigration(t, tmpDir1, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir1, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir1), migrate.WithRollback()); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}

	// Switch to folder containing ONLY 001 and 003 (so 002 is extra in DB, and 003 is new)
	tmpDir2 := t.TempDir()
	writeMigration(t, tmpDir2, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir2, "003_comments", "CREATE TABLE t3 (id INT);", "DROP TABLE t3;")

	result, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir2))
	if err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	// Up must report both Applied (003) and ExtrasNotRolledBack (002, warning).
	if len(result.Applied) != 1 || result.Applied[0] != "003_comments" {
		t.Errorf("expected Applied=[003_comments.sql], got %v", result.Applied)
	}

	if len(result.ExtrasLeft) != 1 || result.ExtrasLeft[0] != "002_posts" {
		t.Errorf("expected ExtrasLeft=[002_posts] (warning), got %v", result.ExtrasLeft)
	}

	// All three must be applied in chronological order: 001, 002 (kept), 003 (just applied).
	applied := getAppliedIDs(t, dbConn.GetDB())

	expected := []string{"001_init", "002_posts", "003_comments"}
	if len(applied) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, applied)
	}

	for i, id := range applied {
		if id != expected[i] {
			t.Errorf("index %d: expected %s, got %s", i, expected[i], id)
		}
	}
}

// TestLibrary_WithLoggerOption proves WithLogger actually routes the
// library's operational logs to the supplied *slog.Logger instead of
// slog.Default().
func TestLibrary_WithLoggerOption(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	var buf bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&buf, nil))

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir), migrate.WithRollback(), migrate.WithLogger(logger)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if !strings.Contains(buf.String(), "Acquiring migration lock") {
		t.Errorf("expected the custom logger to receive operational logs, got: %q", buf.String())
	}
}

func TestLibrary_DownBoundaryConditions(t *testing.T) {
	t.Parallel()

	reapply := func(t *testing.T, dbConn *sql.DB, tmpDir string) {
		t.Helper()

		if _, err := migrate.Reconcile(t.Context(), dbConn, os.DirFS(tmpDir), migrate.WithRollback()); err != nil {
			t.Fatalf("Reconcile failed: %v", err)
		}
	}

	// Ways of asking for "everything". A non-positive count is not one of them
	// any more: it is rejected so that a zero-valued int cannot mean "drop the
	// whole schema".
	rollbackEverything := map[string]func(*testing.T, *sql.DB) (*migrate.Result, error){
		"DownAll": func(t *testing.T, sqlDB *sql.DB) (*migrate.Result, error) {
			t.Helper()

			return migrate.DownAll(t.Context(), sqlDB)
		},
		"count_over_len": func(t *testing.T, sqlDB *sql.DB) (*migrate.Result, error) {
			t.Helper()

			return migrate.Down(t.Context(), sqlDB, 10)
		},
	}

	for name, rollback := range rollbackEverything {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := dbtest.SetupTestPostgres(t)

			dbConn := dbtest.MustConnect(t, cfg)
			defer dbConn.Close()

			tmpDir := t.TempDir()
			writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
			writeMigration(t, tmpDir, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")
			writeMigration(t, tmpDir, "003_comments", "CREATE TABLE t3 (id INT);", "DROP TABLE t3;")

			reapply(t, dbConn.GetDB(), tmpDir)

			result, err := rollback(t, dbConn.GetDB())
			if err != nil {
				t.Fatalf("rollback failed: %v", err)
			}

			if len(result.RolledBack) != 3 {
				t.Errorf("expected 3 rollbacks, got %d", len(result.RolledBack))
			}

			applied := getAppliedIDs(t, dbConn.GetDB())
			if len(applied) != 0 {
				t.Errorf("expected all rolled back, got %v", applied)
			}
		})
	}

	// The zero value of an int must not be a way to destroy everything.
	for _, count := range []int{0, -5} {
		t.Run(fmt.Sprintf("count_%d_is_rejected", count), func(t *testing.T) {
			t.Parallel()

			cfg := dbtest.SetupTestPostgres(t)

			dbConn := dbtest.MustConnect(t, cfg)
			defer dbConn.Close()

			tmpDir := t.TempDir()
			writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
			reapply(t, dbConn.GetDB(), tmpDir)

			if _, err := migrate.Down(t.Context(), dbConn.GetDB(), count); err == nil {
				t.Fatalf("Down(%d) must be rejected, not treated as 'all'", count)
			}

			if applied := getAppliedIDs(t, dbConn.GetDB()); len(applied) != 1 {
				t.Errorf("nothing may have been rolled back, got %v", applied)
			}
		})
	}

	t.Run("partial_rollback_keeps_earliest", func(t *testing.T) {
		t.Parallel()

		cfg := dbtest.SetupTestPostgres(t)

		dbConn := dbtest.MustConnect(t, cfg)
		defer dbConn.Close()

		tmpDir := t.TempDir()
		writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
		writeMigration(t, tmpDir, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")
		writeMigration(t, tmpDir, "003_comments", "CREATE TABLE t3 (id INT);", "DROP TABLE t3;")

		reapply(t, dbConn.GetDB(), tmpDir)

		// Roll back only the last 2; 001 must remain applied.
		result, err := migrate.Down(t.Context(), dbConn.GetDB(), 2)
		if err != nil {
			t.Fatalf("Down failed: %v", err)
		}

		if len(result.RolledBack) != 2 {
			t.Errorf("expected 2 rollbacks, got %d", len(result.RolledBack))
		}

		applied := getAppliedIDs(t, dbConn.GetDB())
		if len(applied) != 1 || applied[0] != "001_init" {
			t.Errorf("expected only 001_init.sql remaining, got %v", applied)
		}
	})
}
