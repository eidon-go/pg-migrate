//go:build integration

package migrate_test

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

//go:embed testdata/nomigrations
var emptyEmbed embed.FS

// A project that has not written its first migration yet must start normally.
// This is the case that decides whether the empty-source guard is a safety net or
// an outage: a service calling Up on boot cannot be brought down by an empty
// migrations directory.
func TestEmptySource_FreshProjectStartsFine(t *testing.T) {
	t.Parallel()

	sources := map[string]func(t *testing.T) fs.FS{
		"empty directory": func(t *testing.T) fs.FS {
			t.Helper()

			return os.DirFS(t.TempDir())
		},
		"directory with only non-SQL files": func(t *testing.T) fs.FS {
			t.Helper()

			dir := t.TempDir()
			for _, name := range []string{"README.md", ".gitkeep"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
					t.Fatalf("failed to write %s: %v", name, err)
				}
			}

			return os.DirFS(dir)
		},
		"directory with an unrelated subdirectory": func(t *testing.T) fs.FS {
			t.Helper()

			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
				t.Fatalf("failed to create subdirectory: %v", err)
			}

			return os.DirFS(dir)
		},
		"embedded directory with no migrations": func(t *testing.T) fs.FS {
			t.Helper()

			sub, err := fs.Sub(emptyEmbed, "testdata/nomigrations")
			if err != nil {
				t.Fatalf("fs.Sub failed: %v", err)
			}

			return sub
		},
	}

	for name, makeSource := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := dbtest.SetupTestPostgres(t)

			dbConn := dbtest.MustConnect(t, cfg)
			defer dbConn.Close()

			fsys := makeSource(t)

			// Every entry point must be a clean no-op, not an error. Up in
			// particular: it is what a service calls on boot.
			result, err := migrate.Up(t.Context(), dbConn.GetDB(), fsys)
			if err != nil {
				t.Fatalf("Up on an empty source must succeed, got: %v", err)
			}

			if len(result.Applied) != 0 {
				t.Errorf("expected nothing applied, got %v", result.Applied)
			}

			if _, strictErr := migrate.Reconcile(t.Context(), dbConn.GetDB(), fsys); strictErr != nil {
				t.Errorf("Reconcile on an empty source and empty database must succeed, got: %v", strictErr)
			}

			if _, rollbackErr := migrate.Reconcile(t.Context(), dbConn.GetDB(), fsys,
				migrate.WithRollback()); rollbackErr != nil {
				t.Errorf("Reconcile with rollback allowed must also succeed, got: %v", rollbackErr)
			}

			analysis, err := migrate.Plan(t.Context(), dbConn.GetDB(), fsys)
			if err != nil {
				t.Errorf("Plan on an empty source must succeed, got: %v", err)
			} else if analysis.Blocked {
				t.Errorf("Plan must not report a block, got %q", analysis.BlockedReason)
			}

			applied, err := migrate.Status(t.Context(), dbConn.GetDB())
			if err != nil {
				t.Errorf("Status must succeed, got: %v", err)
			}

			if len(applied) != 0 {
				t.Errorf("expected an empty ledger, got %v", applied)
			}
		})
	}
}

// The same empty source becomes a refusal only once the database has something to
// lose. That is the whole point of the guard: it protects applied migrations, not
// the act of starting up.
func TestEmptySource_RefusesOnlyWhenThereIsSomethingToRollBack(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	populated := t.TempDir()
	writeMigration(t, populated, "001_init", "CREATE TABLE keep_me (id INT);", "DROP TABLE keep_me;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(populated)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	empty := os.DirFS(t.TempDir())

	// Up still does nothing and still succeeds: it never rolls anything back, so
	// an empty source cannot cost anything.
	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), empty); err != nil {
		t.Errorf("Up must stay a no-op even with migrations applied, got: %v", err)
	}

	if !relationExists(t, dbConn.GetDB(), "keep_me") {
		t.Fatal("Up must not have dropped anything")
	}

	// Reconcile with rollback allowed is where the schema would be destroyed.
	_, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), empty, migrate.WithRollback())
	if !errors.Is(err, migrate.ErrEmptySource) {
		t.Fatalf("expected errors.Is(err, ErrEmptySource), got: %v", err)
	}

	if !relationExists(t, dbConn.GetDB(), "keep_me") {
		t.Error("nothing may have been rolled back")
	}

	// Plan says so up front rather than describing a rollback that would not run.
	analysis, err := migrate.Plan(t.Context(), dbConn.GetDB(), empty, migrate.WithRollback())
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	if !analysis.Blocked {
		t.Error("Plan must report that Reconcile would refuse")
	}

	// And it remains possible on purpose, for a test harness tearing a database
	// down.
	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), empty,
		migrate.WithRollback(), migrate.WithAllowEmptySource()); err != nil {
		t.Fatalf("WithAllowEmptySource must permit it, got: %v", err)
	}

	if relationExists(t, dbConn.GetDB(), "keep_me") {
		t.Error("with the opt-in, the rollback should have happened")
	}
}

// A path that does not exist is a misconfiguration and must be reported as one,
// not silently treated as "no migrations".
func TestEmptySource_MissingDirectoryIsAnError(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	missing := os.DirFS(filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), missing); err == nil {
		t.Error("Up must report a nonexistent migration directory")
	}
}
