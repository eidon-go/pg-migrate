//go:build integration

package migrator_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/db"
	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// mockLocker is a no-op locker for testing.
type mockLocker struct{}

func (m *mockLocker) AcquireLock(ctx context.Context) error { return nil }
func (m *mockLocker) ReleaseLock(ctx context.Context) error { return nil }

func setupIntegrationTest(
	t *testing.T,
	migrationsPath string,
) (*migrator.Service, *db.Postgres) {
	t.Helper()

	// Set up an isolated test database
	cfg := dbtest.SetupTestPostgres(t)
	postgres := dbtest.MustConnect(t, cfg)

	// Create source with migrations
	src := source.NewFS(os.DirFS(migrationsPath))

	// Create executor and service
	executor := newTestExecutor(t, postgres.GetDB())
	locker := &mockLocker{}
	svc := migrator.NewService(src, executor, locker, nil)

	return svc, postgres
}

func getMigrationIDs(t *testing.T, sqlDB *sql.DB) []string {
	t.Helper()

	rows, err := sqlDB.QueryContext(t.Context(), "SELECT id FROM migrations ORDER BY seq")
	if err != nil {
		// Table may not exist
		return []string{}
	}
	defer func() {
		_ = rows.Close()
	}()

	var ids []string

	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			t.Fatalf("Failed to scan migration ID: %v", scanErr)
		}

		ids = append(ids, id)
	}

	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatalf("rows iteration: %v", rowsErr)
	}

	return ids
}

// TestIntegration_FullMigrationCycle checks full migration cycle
// with real SQL files.
func TestIntegration_FullMigrationCycle(t *testing.T) {
	t.Parallel()

	migrationsPath := filepath.Join("testdata", "migrations")
	svc, postgres := setupIntegrationTest(t, migrationsPath)

	// 1. Apply all 5 migrations
	if _, err := svc.Up(t.Context()); err != nil {
		t.Fatalf("Failed to apply migrations: %v", err)
	}

	// Check that all tables are created
	tables := []string{"users", "posts", "comments", "sessions", "tags", "post_tags"}
	for _, table := range tables {
		if !integrationTableExists(t, postgres.GetDB(), table) {
			t.Errorf("Table %s was not created", table)
		}
	}

	// Check that migrations table has 5 records
	ids := getMigrationIDs(t, postgres.GetDB())
	if len(ids) != 5 {
		t.Errorf("Expected 5 migrations in DB, got %d", len(ids))
	}

	// 2. Repeated Up() call should not change anything (idempotency)
	if _, err := svc.Up(t.Context()); err != nil {
		t.Fatalf("Second Up() failed: %v", err)
	}

	ids2 := getMigrationIDs(t, postgres.GetDB())
	if len(ids2) != 5 {
		t.Errorf("After second Up(), expected 5 migrations, got %d", len(ids2))
	}
}

// TestIntegration_BranchSwitch simulates switching between branches
// where one branch has more migrations than another.
func TestIntegration_BranchSwitch(t *testing.T) {
	t.Parallel()

	migrationsPath := filepath.Join("testdata", "migrations")
	svc, postgres := setupIntegrationTest(t, migrationsPath)

	// 1. Apply all 5 migrations (ветка feature)
	if _, err := svc.Up(t.Context()); err != nil {
		t.Fatalf("Failed to apply migrations: %v", err)
	}

	if !integrationTableExists(t, postgres.GetDB(), "tags") {
		t.Fatal("tags table should exist after applying all migrations")
	}

	// 2. Simulate switching to main branch with only 3 migrations
	// Create temporary directory with ALL migrations (git preserves history)
	// but use only first 3
	tempDir := t.TempDir()

	// Copy ALL migrations, then remove 4 and 5 to simulate switching to a branch
	// that never had them. Rollback must still work: the Down scripts come from
	// the database, not from these files.
	copyMigrations(t, migrationsPath, tempDir, testdataIDs...)
	removeMigrations(t, tempDir, "004_create_sessions", "005_create_tags")

	// Create new source and service with 3 migrations
	src := source.NewFS(os.DirFS(tempDir))
	executor := newTestExecutor(t, postgres.GetDB())
	locker := &mockLocker{}
	svc2 := migrator.NewService(src, executor, locker, nil)

	// 3. Apply Reconcile(..., true) - should rollback 2 extra migrations
	// KEY IMPROVEMENT: Rollback should work even without files because Down scripts are stored in DB
	_, err := svc2.Reconcile(t.Context(), migrator.RunOptions{AllowRollback: true, AllowInterleaved: true})
	if err != nil {
		t.Fatalf("Expected rollback to succeed with Down scripts from DB, got error: %v", err)
	}

	// Verify that tags and sessions tables were dropped
	if integrationTableExists(t, postgres.GetDB(), "tags") {
		t.Error("tags table should have been dropped during rollback")
	}

	if integrationTableExists(t, postgres.GetDB(), "sessions") {
		t.Error("sessions table should have been dropped during rollback")
	}

	// Verify that only first 3 tables remain
	if !integrationTableExists(t, postgres.GetDB(), "users") {
		t.Error("users table should still exist")
	}

	if !integrationTableExists(t, postgres.GetDB(), "posts") {
		t.Error("posts table should still exist")
	}

	if !integrationTableExists(t, postgres.GetDB(), "comments") {
		t.Error("comments table should still exist")
	}
}

// TestIntegration_MissingMigrationInMiddle checks case when
// migration is missing in the middle of sequence.
func TestIntegration_MissingMigrationInMiddle(t *testing.T) {
	t.Parallel()

	migrationsPath := filepath.Join("testdata", "migrations")

	// 1. First apply migrations 1,2,4 (skip 3)
	tempDir1 := t.TempDir()

	// Skip 003_create_comments.
	copyMigrations(t, migrationsPath, tempDir1,
		"001_create_users", "002_create_posts", "004_create_sessions")

	src1 := source.NewFS(os.DirFS(tempDir1))

	// Set up an isolated test database for this test
	cfg := dbtest.SetupTestPostgres(t)
	postgres := dbtest.MustConnect(t, cfg)

	executor := newTestExecutor(t, postgres.GetDB())
	locker := &mockLocker{}
	svc1 := migrator.NewService(src1, executor, locker, nil)

	if _, err := svc1.Up(t.Context()); err != nil {
		t.Fatalf("Failed to apply initial migrations: %v", err)
	}

	// DB: [1,2,4], check
	ids := getMigrationIDs(t, postgres.GetDB())

	expected := []string{"001_create_users", "002_create_posts", "004_create_sessions"}
	if len(ids) != len(expected) {
		t.Fatalf("Expected %d migrations, got %d", len(expected), len(ids))
	}

	// 2. Now add migration 3 and apply
	tempDir2 := t.TempDir()

	copyMigrations(t, migrationsPath, tempDir2,
		"001_create_users", "002_create_posts",
		"003_create_comments", // Added
		"004_create_sessions")

	src2 := source.NewFS(os.DirFS(tempDir2))
	svc2 := migrator.NewService(src2, executor, locker, nil)

	// Up with set-difference semantics: appends missing 003 without rolling back
	// anything. 003 is recorded as interleaved (it sorts before 004 which was
	// already applied) and ends up at the tail of the applied order.
	if _, err := svc2.Up(t.Context()); err != nil {
		t.Fatalf("Failed to fix missing migration: %v", err)
	}

	// Verify the applied order: 003 was applied AFTER 004, even though its ID sorts before.
	ids = getMigrationIDs(t, postgres.GetDB())

	expected = []string{
		"001_create_users",
		"002_create_posts",
		"004_create_sessions",
		"003_create_comments", // applied last (interleaved)
	}
	if len(ids) != len(expected) {
		t.Fatalf("Expected %d migrations, got %d (%v)", len(expected), len(ids), ids)
	}

	for i, id := range ids {
		if id != expected[i] {
			t.Errorf("Migration %d: expected %s, got %s", i, expected[i], id)
		}
	}

	// Check that comments table is created
	if !integrationTableExists(t, postgres.GetDB(), "comments") {
		t.Error("comments table should be created")
	}
}

// TestIntegration_DataPreservation checks that data is preserved
// during rollback and re-apply of migrations (for tables that are not rolled back).
func TestIntegration_DataPreservation(t *testing.T) {
	t.Parallel()

	migrationsPath := filepath.Join("testdata", "migrations")
	_, postgres := setupIntegrationTest(t, migrationsPath)

	// 1. Apply first 3 migrations
	tempDir := t.TempDir()
	copyMigrations(t, migrationsPath, tempDir, testdataIDs[:3]...)

	src := source.NewFS(os.DirFS(tempDir))
	executor := newTestExecutor(t, postgres.GetDB())
	locker := &mockLocker{}
	svc := migrator.NewService(src, executor, locker, nil)

	if _, err := svc.Up(t.Context()); err != nil {
		t.Fatalf("Failed to apply migrations: %v", err)
	}

	// 2. Insert test data
	_, err := postgres.GetDB().Exec(`
		INSERT INTO users (username, email) VALUES ('john', 'john@example.com');
		INSERT INTO users (username, email) VALUES ('jane', 'jane@example.com');
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// 3. Switch to all 5 migrations
	svc2, _ := setupIntegrationTest(t, migrationsPath)

	if _, upErr := svc2.Up(t.Context()); upErr != nil {
		t.Fatalf("Failed to apply additional migrations: %v", upErr)
	}

	// 4. Check that data in users is preserved
	var count int

	err = postgres.GetDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count users: %v", err)
	}

	if count != 2 {
		t.Errorf("Expected 2 users, got %d (data was lost)", count)
	}

	// Check specific records
	var username, email string

	err = postgres.GetDB().QueryRow("SELECT username, email FROM users WHERE username = 'john'").Scan(&username, &email)
	if err != nil {
		t.Errorf("Failed to find john user: %v", err)
	}

	if username != "john" || email != "john@example.com" {
		t.Errorf("User data corrupted: got %s, %s", username, email)
	}
}

// TestIntegration_LargeMigrationSet checks working with large number of migrations.
func TestIntegration_LargeMigrationSet(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	// Create 20 migrations
	for i := 1; i <= 20; i++ {
		migrationID := formatMigrationID(i)
		upScript := formatCreateTable(i)
		downScript := formatDropTable(i)

		writeMigrationPair(t, tempDir, migrationID, upScript, downScript)
	}

	svc, postgres := setupIntegrationTest(t, tempDir)

	// Apply all 20 migrations
	if _, err := svc.Up(t.Context()); err != nil {
		t.Fatalf("Failed to apply 20 migrations: %v", err)
	}

	// Check that all 20 migrations are applied
	ids := getMigrationIDs(t, postgres.GetDB())
	if len(ids) != 20 {
		t.Errorf("Expected 20 migrations, got %d", len(ids))
	}

	// Check that all 20 tables are created
	for i := 1; i <= 20; i++ {
		tableName := formatTableName(i)
		if !integrationTableExists(t, postgres.GetDB(), tableName) {
			t.Errorf("Table %s was not created", tableName)
		}
	}
}

func formatMigrationID(i int) string {
	return fmt.Sprintf("%03d_migration", i)
}

func formatTableName(i int) string {
	return fmt.Sprintf("table_%d", i)
}

func formatCreateTable(i int) string {
	return fmt.Sprintf("CREATE TABLE %s (id SERIAL PRIMARY KEY, data TEXT);", formatTableName(i))
}

func formatDropTable(i int) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s;", formatTableName(i))
}

func integrationTableExists(t *testing.T, sqlDB *sql.DB, tableName string) bool {
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
