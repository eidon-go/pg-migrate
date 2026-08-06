//go:build integration

package migrator_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

// runPlan executes a plan against a bookkeeping table it ensures exists.
// ExecutePlan itself no longer creates the table — the Service does that once
// per run — so direct callers have to.
func runPlan(
	t *testing.T,
	executor *migrator.CustomExecutor,
	plan *migrator.MigrationPlan,
	all []*migrator.Migration,
) (*migrator.ExecutionResult, error) {
	t.Helper()

	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure migrations table: %v", err)
	}

	return executor.ExecutePlan(t.Context(), plan, all)
}

// newTestExecutor builds an executor against the default bookkeeping table.
func newTestExecutor(t *testing.T, sqlDB *sql.DB) *migrator.CustomExecutor {
	t.Helper()

	executor, err := migrator.NewCustomExecutor(sqlDB, migrator.TableConfig{})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	return executor
}

// testMigration is one migration's SQL, written out as the
// <id>.up.sql / <id>.down.sql pair the source package expects.
type testMigration struct {
	id   string
	up   string
	down string
}

// basicMigrations is the three-migration fixture the service tests share. It
// matches the tables cleanupMigrations drops.
func basicMigrations() []testMigration {
	return []testMigration{
		{"001_init", "CREATE TABLE test_users (id INT PRIMARY KEY);", "DROP TABLE test_users;"},
		{"002_posts", "CREATE TABLE test_posts (id INT PRIMARY KEY);", "DROP TABLE test_posts;"},
		{"003_comments", "CREATE TABLE test_comments (id INT PRIMARY KEY);", "DROP TABLE test_comments;"},
	}
}

// without returns the fixture minus the migrations with the given IDs, for
// simulating a checkout that does not contain them.
func without(ms []testMigration, ids ...string) []testMigration {
	kept := make([]testMigration, 0, len(ms))
	for _, m := range ms {
		if !slices.Contains(ids, m.id) {
			kept = append(kept, m)
		}
	}

	return kept
}

// writeMigrationPair writes one migration as its two files.
func writeMigrationPair(t *testing.T, dir, id, up, down string) {
	t.Helper()

	halves := []struct {
		suffix  string
		content string
	}{
		{migrator.UpFileSuffix, up},
		{migrator.DownFileSuffix, down},
	}
	for _, half := range halves {
		path := filepath.Join(dir, id+half.suffix)
		if err := os.WriteFile(path, []byte(half.content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}
}

// writeMigrations writes a whole fixture into dir.
func writeMigrations(t *testing.T, dir string, ms []testMigration) {
	t.Helper()

	for _, m := range ms {
		writeMigrationPair(t, dir, m.id, m.up, m.down)
	}
}

// copyMigrations copies both halves of each named migration from srcDir to
// dstDir, for tests that build a directory representing a particular checkout
// out of the testdata fixture.
func copyMigrations(t *testing.T, srcDir, dstDir string, ids ...string) {
	t.Helper()

	for _, id := range ids {
		for _, suffix := range []string{migrator.UpFileSuffix, migrator.DownFileSuffix} {
			data, err := os.ReadFile(filepath.Join(srcDir, id+suffix))
			if err != nil {
				t.Fatalf("failed to read %s%s: %v", id, suffix, err)
			}

			if err := os.WriteFile(filepath.Join(dstDir, id+suffix), data, 0o644); err != nil {
				t.Fatalf("failed to write %s%s: %v", id, suffix, err)
			}
		}
	}
}

// removeMigrations deletes both halves of each named migration, simulating a
// checkout that does not contain them.
func removeMigrations(t *testing.T, dir string, ids ...string) {
	t.Helper()

	for _, id := range ids {
		for _, suffix := range []string{migrator.UpFileSuffix, migrator.DownFileSuffix} {
			if err := os.Remove(filepath.Join(dir, id+suffix)); err != nil {
				t.Fatalf("failed to remove %s%s: %v", id, suffix, err)
			}
		}
	}
}

// testdataIDs are the migration IDs in test/migrator/testdata/migrations.
var testdataIDs = []string{
	"001_create_users",
	"002_create_posts",
	"003_create_comments",
	"004_create_sessions",
	"005_create_tags",
}
