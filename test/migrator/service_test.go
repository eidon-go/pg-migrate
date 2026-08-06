//go:build integration

package migrator_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/db"
	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// mockLockerForServiceTest is a no-op locker for testing.
type mockLockerForServiceTest struct{}

func (m *mockLockerForServiceTest) AcquireLock(ctx context.Context) error { return nil }
func (m *mockLockerForServiceTest) ReleaseLock(ctx context.Context) error { return nil }

func setupTestDB(t *testing.T) *db.Postgres {
	t.Helper()

	cfg := dbtest.SetupTestPostgres(t)
	pg := dbtest.MustConnect(t, cfg)

	return pg
}

func cleanupMigrations(t *testing.T, pg *db.Postgres) {
	t.Helper()

	// Drop migrations table if exists
	_, err := pg.GetDB().Exec("DROP TABLE IF EXISTS migrations")
	if err != nil {
		t.Fatalf("Failed to drop migrations table: %v", err)
	}

	// Drop all test tables
	tables := []string{"test_users", "test_posts", "test_comments"}
	for _, table := range tables {
		_, err := pg.GetDB().Exec("DROP TABLE IF EXISTS " + table)
		if err != nil {
			t.Logf("Failed to drop table %s: %v", table, err)
		}
	}
}

//nolint:paralleltest // sequential subtests share a single test database by design
func TestGetMigrations(t *testing.T) {
	pg := setupTestDB(t)

	// Create temporary directory with test migrations
	tmpDir := t.TempDir()

	// Create test migration files
	writeMigrations(t, tmpDir, basicMigrations())

	// Create source from temporary directory
	src := source.NewFS(os.DirFS(tmpDir))

	// Clean up migrations table before test
	cleanupMigrations(t, pg)

	// Create service
	executor := newTestExecutor(t, pg.GetDB())
	service := migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

	t.Run("empty database", func(t *testing.T) {
		// Ensure table exists first
		if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
			t.Fatalf("Failed to ensure migrations table: %v", err)
		}

		fromFiles, fromDB, err := service.GetMigrations(t.Context())
		if err != nil {
			t.Fatalf("GetMigrations failed: %v", err)
		}

		// Should have 3 migrations from files
		if len(fromFiles) != 3 {
			t.Errorf("Expected 3 migrations from files, got %d", len(fromFiles))
		}

		// Should have 0 migrations from DB (empty database)
		if len(fromDB) != 0 {
			t.Errorf("Expected 0 migrations from DB, got %d", len(fromDB))
		}
	})

	t.Run("after applying migrations", func(t *testing.T) {
		// Ensure table exists
		if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
			t.Fatalf("Failed to ensure migrations table: %v", err)
		}

		// Apply all migrations
		if _, err := service.Up(t.Context()); err != nil {
			t.Fatalf("Failed to apply migrations: %v", err)
		}

		fromFiles, fromDB, err := service.GetMigrations(t.Context())
		if err != nil {
			t.Fatalf("GetMigrations failed: %v", err)
		}

		// Should have 3 migrations from files
		if len(fromFiles) != 3 {
			t.Errorf("Expected 3 migrations from files, got %d", len(fromFiles))
		}

		// Should have 3 migrations from DB
		if len(fromDB) != 3 {
			t.Errorf("Expected 3 migrations from DB, got %d", len(fromDB))
		}

		// Verify migration IDs match
		for i := 0; i < len(fromFiles) && i < len(fromDB); i++ {
			if fromFiles[i].ID != fromDB[i].ID {
				t.Errorf("Migration %d: files=%s, db=%s", i, fromFiles[i].ID, fromDB[i].ID)
			}
		}
	})
}

func TestExecutePlan(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	testMigrations := basicMigrations()
	writeMigrations(t, tmpDir, testMigrations)

	t.Run("empty plan", func(t *testing.T) {
		t.Parallel()

		pg := setupTestDB(t)

		src := source.NewFS(os.DirFS(tmpDir))
		executor := newTestExecutor(t, pg.GetDB())
		_ = migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

		allMigrations, err := src.GetMigrations()
		if err != nil {
			t.Fatalf("Failed to find migrations: %v", err)
		}

		plan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{},
			ToApply:    []*migrator.Migration{},
		}

		_, err = runPlan(t, executor, plan, allMigrations)
		if err != nil {
			t.Errorf("ExecutePlan with empty plan failed: %v", err)
		}
	})

	t.Run("only apply", func(t *testing.T) {
		t.Parallel()

		pg := setupTestDB(t)

		src := source.NewFS(os.DirFS(tmpDir))
		executor := newTestExecutor(t, pg.GetDB())
		_ = migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

		allMigrations, err := src.GetMigrations()
		if err != nil {
			t.Fatalf("Failed to find migrations: %v", err)
		}

		plan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{},
			ToApply:    allMigrations,
		}

		_, err = runPlan(t, executor, plan, allMigrations)
		if err != nil {
			t.Errorf("ExecutePlan failed: %v", err)
		}

		// Verify tables were created
		if !tableExists(t, pg.GetDB(), "test_users") {
			t.Error("test_users table should exist")
		}

		if !tableExists(t, pg.GetDB(), "test_posts") {
			t.Error("test_posts table should exist")
		}

		if !tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should exist")
		}
	})

	t.Run("only rollback", func(t *testing.T) {
		t.Parallel()

		pg := setupTestDB(t)

		src := source.NewFS(os.DirFS(tmpDir))
		executor := newTestExecutor(t, pg.GetDB())
		_ = migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

		allMigrations, err := src.GetMigrations()
		if err != nil {
			t.Fatalf("Failed to find migrations: %v", err)
		}

		// First apply all migrations
		applyPlan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{},
			ToApply:    allMigrations,
		}
		if _, applyErr := runPlan(t, executor, applyPlan, allMigrations); applyErr != nil {
			t.Fatalf("Failed to apply migrations: %v", applyErr)
		}

		// Now rollback the last one
		rollbackPlan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{allMigrations[2]}, // 003_comments.sql
			ToApply:    []*migrator.Migration{},
		}

		if _, err := runPlan(t, executor, rollbackPlan, allMigrations); err != nil {
			t.Errorf("ExecutePlan rollback failed: %v", err)
		}

		// Verify comments table was dropped
		if tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should be dropped")
		}

		// But other tables should still exist
		if !tableExists(t, pg.GetDB(), "test_users") {
			t.Error("test_users table should still exist")
		}

		if !tableExists(t, pg.GetDB(), "test_posts") {
			t.Error("test_posts table should still exist")
		}
	})

	t.Run("rollback and apply", func(t *testing.T) {
		t.Parallel()

		pg := setupTestDB(t)

		src := source.NewFS(os.DirFS(tmpDir))
		executor := newTestExecutor(t, pg.GetDB())
		_ = migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

		allMigrations, err := src.GetMigrations()
		if err != nil {
			t.Fatalf("Failed to find migrations: %v", err)
		}

		// First apply all 3 migrations
		applyPlan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{},
			ToApply:    allMigrations,
		}
		if _, applyErr := runPlan(t, executor, applyPlan, allMigrations); applyErr != nil {
			t.Fatalf("Failed to apply initial migrations: %v", applyErr)
		}

		// Verify initial state
		count := getMigrationCount(t, pg.GetDB())
		if count != 3 {
			t.Errorf("Expected 3 migrations applied, got %d", count)
		}

		// Now rollback last migration only
		plan := &migrator.MigrationPlan{
			ToRollback: []*migrator.Migration{allMigrations[2]}, // 003
			ToApply:    []*migrator.Migration{},
		}

		_, err = runPlan(t, executor, plan, allMigrations)
		if err != nil {
			t.Errorf("ExecutePlan rollback failed: %v", err)
		}

		// Verify final state - should have 2 migrations
		count = getMigrationCount(t, pg.GetDB())
		if count != 2 {
			t.Errorf("Expected 2 migrations after rollback, got %d", count)
		}

		// Verify tables
		if !tableExists(t, pg.GetDB(), "test_users") {
			t.Error("test_users table should exist")
		}

		if !tableExists(t, pg.GetDB(), "test_posts") {
			t.Error("test_posts table should exist")
		}

		if tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should be dropped")
		}
	})
}

//nolint:paralleltest // sequential subtests share a single test database by design
func TestServiceUp_EndToEnd(t *testing.T) {
	pg := setupTestDB(t)

	tmpDir := t.TempDir()

	testMigrations := basicMigrations()
	writeMigrations(t, tmpDir, testMigrations)

	src := source.NewFS(os.DirFS(tmpDir))
	executor := newTestExecutor(t, pg.GetDB())
	service := migrator.NewService(src, executor, &mockLockerForServiceTest{}, nil)

	t.Run("empty database to full", func(t *testing.T) {
		cleanupMigrations(t, pg)

		// Apply all migrations
		if _, err := service.Up(t.Context()); err != nil {
			t.Fatalf("Up() failed: %v", err)
		}

		// Verify all tables exist
		if !tableExists(t, pg.GetDB(), "test_users") {
			t.Error("test_users table should exist")
		}

		if !tableExists(t, pg.GetDB(), "test_posts") {
			t.Error("test_posts table should exist")
		}

		if !tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should exist")
		}

		// Verify migrations table
		count := getMigrationCount(t, pg.GetDB())
		if count != 3 {
			t.Errorf("Expected 3 migrations in DB, got %d", count)
		}
	})

	t.Run("rollback scenario - rollback without file (stored in DB)", func(t *testing.T) {
		cleanupMigrations(t, pg)

		// Apply all 3 migrations
		if _, err := service.Up(t.Context()); err != nil {
			t.Fatalf("Initial Up() failed: %v", err)
		}

		// Verify test_comments table exists
		if !tableExists(t, pg.GetDB(), "test_comments") {
			t.Fatal("test_comments table should exist after applying all migrations")
		}

		// Now simulate a checkout that no longer contains the last migration.
		tmpDir2 := t.TempDir()
		writeMigrations(t, tmpDir2, without(testMigrations, "003_comments"))

		src2 := source.NewFS(os.DirFS(tmpDir2))
		service2 := migrator.NewService(src2, executor, &mockLockerForServiceTest{}, nil)

		// KEY TEST: Rollback should work even without the file because Down script is stored in DB
		_, err := service2.Reconcile(t.Context(), migrator.RunOptions{AllowRollback: true, AllowInterleaved: true})
		if err != nil {
			t.Fatalf("Up() should succeed even without migration file (Down script stored in DB): %v", err)
		}

		// Verify test_comments table was dropped
		if tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should have been dropped during rollback")
		}

		// Verify only 2 migrations remain in DB
		var count int

		err = pg.GetDB().QueryRow("SELECT COUNT(*) FROM migrations").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to count migrations: %v", err)
		}

		if count != 2 {
			t.Errorf("Expected 2 migrations in DB after rollback, got %d", count)
		}
	})

	t.Run("new migrations added", func(t *testing.T) {
		cleanupMigrations(t, pg)

		// Start with 2 migrations
		tmpDir2 := t.TempDir()
		writeMigrations(t, tmpDir2, without(testMigrations, "003_comments"))

		src2 := source.NewFS(os.DirFS(tmpDir2))
		service2 := migrator.NewService(src2, executor, &mockLockerForServiceTest{}, nil)

		if _, err := service2.Up(t.Context()); err != nil {
			t.Fatalf("First Up() failed: %v", err)
		}

		count := getMigrationCount(t, pg.GetDB())
		if count != 2 {
			t.Errorf("Expected 2 migrations, got %d", count)
		}

		// Add third migration
		third := testMigrations[2]
		writeMigrationPair(t, tmpDir2, third.id, third.up, third.down)

		// Apply new migration
		if _, err := service2.Up(t.Context()); err != nil {
			t.Fatalf("Second Up() failed: %v", err)
		}

		count = getMigrationCount(t, pg.GetDB())
		if count != 3 {
			t.Errorf("Expected 3 migrations after adding new one, got %d", count)
		}

		if !tableExists(t, pg.GetDB(), "test_comments") {
			t.Error("test_comments table should exist")
		}
	})
}

// Helper functions

func tableExists(t *testing.T, sqlDB *sql.DB, tableName string) bool {
	t.Helper()

	const query = `SELECT EXISTS (
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_name = $1
	)`

	var exists bool
	if err := sqlDB.QueryRowContext(t.Context(), query, tableName).Scan(&exists); err != nil {
		t.Fatalf("Failed to check table existence: %v", err)
	}

	return exists
}

func getMigrationCount(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()

	// Check if migrations table exists first
	var migrationsTableExists bool

	err := sqlDB.QueryRowContext(t.Context(), `
		SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'migrations'
		)
	`).Scan(&migrationsTableExists)
	if err != nil || !migrationsTableExists {
		return 0
	}

	var count int
	if err := sqlDB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM migrations").Scan(&count); err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}

	return count
}
