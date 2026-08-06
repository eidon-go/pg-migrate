//go:build integration

package migrator_test

import (
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

// TestCustomExecutor tests the custom executor with real database.
//
//nolint:paralleltest // sequential subtests share a single test database by design
func TestCustomExecutor(t *testing.T) {
	// Setup isolated test database
	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor := newTestExecutor(t, postgres.GetDB())

	// Test 1: EnsureMigrationsTable
	t.Run("EnsureMigrationsTable", func(t *testing.T) {
		err := executor.EnsureMigrationsTable(t.Context())
		if err != nil {
			t.Fatalf("Failed to ensure migrations table: %v", err)
		}

		// Verify table exists
		var exists bool

		query := `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'migrations'
		)`

		err = postgres.GetDB().QueryRow(query).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table existence: %v", err)
		}

		if !exists {
			t.Error("Migrations table was not created")
		}
	})

	// Test 2: ApplyMigration
	t.Run("ApplyMigration", func(t *testing.T) {
		// Scripts now include markers with directives
		upScript := "CREATE TABLE test_users (id SERIAL PRIMARY KEY, name VARCHAR(255))"
		downScript := "DROP TABLE test_users"

		migration := &migrator.Migration{
			ID:         "001_create_test_users",
			UpScript:   upScript,
			DownScript: downScript,
		}

		err := executor.ApplyMigration(t.Context(), migration)
		if err != nil {
			t.Fatalf("Failed to apply migration: %v", err)
		}

		// Verify table was created
		var exists bool

		query := `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'test_users'
		)`

		err = postgres.GetDB().QueryRow(query).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table existence: %v", err)
		}

		if !exists {
			t.Error("Table test_users was not created")
		}

		// Verify migration was recorded with full scripts (including markers)
		var id, storedUpScript, storedDownScript string

		err = postgres.GetDB().QueryRow("SELECT id, up_script, down_script FROM migrations WHERE id = $1", "001_create_test_users").
			Scan(&id, &storedUpScript, &storedDownScript)
		if err != nil {
			t.Fatalf("Failed to get migration record: %v", err)
		}

		if id != "001_create_test_users" {
			t.Errorf("Expected id '001_create_test_users', got '%s'", id)
		}

		if storedUpScript != upScript {
			t.Errorf("Stored up_script doesn't match. Expected:\n%s\nGot:\n%s", upScript, storedUpScript)
		}

		if storedDownScript != downScript {
			t.Errorf("Stored down_script doesn't match. Expected:\n%s\nGot:\n%s", downScript, storedDownScript)
		}
	})

	// Test 3: GetAppliedMigrations
	t.Run("GetAppliedMigrations", func(t *testing.T) {
		migrations, err := executor.GetAppliedMigrations(t.Context())
		if err != nil {
			t.Fatalf("Failed to get applied migrations: %v", err)
		}

		if len(migrations) != 1 {
			t.Errorf("Expected 1 migration, got %d", len(migrations))
		}

		if len(migrations) > 0 && migrations[0].ID != "001_create_test_users" {
			t.Errorf("Expected migration id '001_create_test_users', got '%s'", migrations[0].ID)
		}
	})

	// Test 4: RollbackMigration
	t.Run("RollbackMigration", func(t *testing.T) {
		err := executor.RollbackMigration(t.Context(), "001_create_test_users")
		if err != nil {
			t.Fatalf("Failed to rollback migration: %v", err)
		}

		// Verify table was dropped
		var exists bool

		query := `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'test_users'
		)`

		err = postgres.GetDB().QueryRow(query).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table existence: %v", err)
		}

		if exists {
			t.Error("Table test_users should have been dropped")
		}

		// Verify migration record was deleted
		var count int

		err = postgres.GetDB().QueryRow("SELECT COUNT(*) FROM migrations WHERE id = $1", "001_create_test_users").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to count migrations: %v", err)
		}

		if count != 0 {
			t.Error("Migration record should have been deleted")
		}
	})

	// Test 5: RollbackMigration - non-existent migration
	t.Run("RollbackMigration_NotFound", func(t *testing.T) {
		err := executor.RollbackMigration(t.Context(), "999_nonexistent")
		if err == nil {
			t.Error("Expected error when rolling back non-existent migration")
		}
	})

	// Test 6: ApplyMigration with notransaction directive
	t.Run("ApplyMigration_NoTransaction", func(t *testing.T) {
		// Note: CREATE DATABASE cannot run in a transaction in PostgreSQL
		// So we'll use a different example that demonstrates the no-transaction behavior
		upScript := "-- +migrate notransaction\nCREATE TABLE test_notx (id SERIAL PRIMARY KEY)"
		downScript := "DROP TABLE test_notx"

		migration := &migrator.Migration{
			ID:         "002_test_notransaction",
			UpScript:   upScript,
			DownScript: downScript,
		}

		err := executor.ApplyMigration(t.Context(), migration)
		if err != nil {
			t.Fatalf("Failed to apply migration with notransaction: %v", err)
		}

		// Verify table was created
		var exists bool

		query := `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'test_notx'
		)`

		err = postgres.GetDB().QueryRow(query).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table existence: %v", err)
		}

		if !exists {
			t.Error("Table test_notx was not created")
		}

		// Verify migration was recorded with full script (directives are in the script itself)
		var id, storedUpScript string

		err = postgres.GetDB().QueryRow(
			"SELECT id, up_script FROM migrations WHERE id = $1",
			"002_test_notransaction",
		).Scan(&id, &storedUpScript)
		if err != nil {
			t.Fatalf("Failed to get migration record: %v", err)
		}
		// Verify the script includes the notransaction directive
		if storedUpScript != upScript {
			t.Errorf("Stored up_script doesn't match. Expected:\n%s\nGot:\n%s", upScript, storedUpScript)
		}
	})

	// Test 7: RollbackMigration with notransaction directive
	t.Run("RollbackMigration_NoTransaction", func(t *testing.T) {
		// First apply a migration with down notransaction
		upScript := "CREATE TABLE test_down_notx (id SERIAL PRIMARY KEY)"
		downScript := "-- +migrate notransaction\nDROP TABLE test_down_notx"

		migration := &migrator.Migration{
			ID:         "003_test_down_notransaction",
			UpScript:   upScript,
			DownScript: downScript,
		}

		err := executor.ApplyMigration(t.Context(), migration)
		if err != nil {
			t.Fatalf("Failed to apply migration: %v", err)
		}

		// Now rollback with notransaction
		err = executor.RollbackMigration(t.Context(), "003_test_down_notransaction")
		if err != nil {
			t.Fatalf("Failed to rollback migration with notransaction: %v", err)
		}

		// Verify table was dropped
		var exists bool

		query := `SELECT EXISTS (
			SELECT FROM information_schema.tables
			WHERE table_schema = 'public'
			AND table_name = 'test_down_notx'
		)`

		err = postgres.GetDB().QueryRow(query).Scan(&exists)
		if err != nil {
			t.Fatalf("Failed to check table existence: %v", err)
		}

		if exists {
			t.Error("Table test_down_notx should have been dropped")
		}

		// Verify migration record was deleted
		var count int

		err = postgres.GetDB().QueryRow(
			"SELECT COUNT(*) FROM migrations WHERE id = $1",
			"003_test_down_notransaction",
		).Scan(&count)
		if err != nil {
			t.Fatalf("Failed to count migrations: %v", err)
		}

		if count != 0 {
			t.Error("Migration record should have been deleted")
		}
	})
}

// TestErrorTracking tests error tracking functionality.
//
//nolint:paralleltest // sequential subtests share a single test database by design
func TestErrorTracking(t *testing.T) {
	// Setup isolated test database
	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor := newTestExecutor(t, postgres.GetDB())

	// Ensure migrations table exists
	err := executor.EnsureMigrationsTable(t.Context())
	if err != nil {
		t.Fatalf("Failed to ensure migrations table: %v", err)
	}

	// Test 1: Successful migration should have error = NULL
	t.Run("SuccessfulMigration_ErrorIsNull", func(t *testing.T) {
		upScript := "CREATE TABLE test_success (id SERIAL PRIMARY KEY)"
		downScript := "DROP TABLE test_success"

		migration := &migrator.Migration{
			ID:         "001_test_success",
			UpScript:   upScript,
			DownScript: downScript,
		}

		err := executor.ApplyMigration(t.Context(), migration)
		if err != nil {
			t.Fatalf("Failed to apply migration: %v", err)
		}

		// Check that error column is NULL
		migrations, err := executor.GetAppliedMigrations(t.Context())
		if err != nil {
			t.Fatalf("Failed to get applied migrations: %v", err)
		}

		if len(migrations) != 1 {
			t.Fatalf("Expected 1 migration, got %d", len(migrations))
		}

		if migrations[0].Error != nil {
			t.Errorf("Expected error to be nil for successful migration, got: %v", *migrations[0].Error)
		}
	})

	// Test 2: Failed migration in notransaction mode should record partial error
	t.Run("PartialFailure_NoTransaction", func(t *testing.T) {
		// Create a migration with multiple statements where one fails
		upScript := `-- +migrate notransaction
CREATE TABLE test_partial (id SERIAL PRIMARY KEY);
CREATE TABLE test_partial (id SERIAL PRIMARY KEY);`
		downScript := "DROP TABLE IF EXISTS test_partial"

		migration := &migrator.Migration{
			ID:         "002_test_partial_failure",
			UpScript:   upScript,
			DownScript: downScript,
		}

		// This should fail on second statement (table already exists)
		err := executor.ApplyMigration(t.Context(), migration)
		if err == nil {
			t.Fatal("Expected error for duplicate table creation, got nil")
		}

		// Check that migration was recorded with error message
		migrations, err := executor.GetAppliedMigrations(t.Context())
		if err != nil {
			t.Fatalf("Failed to get applied migrations: %v", err)
		}

		// Find our migration
		var found bool

		for _, m := range migrations {
			if m.ID == "002_test_partial_failure" {
				found = true

				if m.Error == nil {
					t.Error("Expected error message to be recorded, got nil")
				} else if !contains(*m.Error, "already exists") && !contains(*m.Error, "duplicate") {
					// Verify error message contains PostgreSQL error about duplicate table
					t.Errorf("Expected error to contain 'already exists' or 'duplicate', got: %s", *m.Error)
				}

				break
			}
		}

		if !found {
			t.Error("Migration with error was not recorded in database")
		}
	})

	// Test 3: Failed migration in transaction mode should not be recorded
	t.Run("Failure_WithTransaction_NotRecorded", func(t *testing.T) {
		// Create a migration that will fail
		upScript := `CREATE TABLE test_fail (id SERIAL PRIMARY KEY);
CREATE TABLE test_fail (id SERIAL PRIMARY KEY);`
		downScript := "DROP TABLE IF EXISTS test_fail"

		migration := &migrator.Migration{
			ID:         "003_test_failure_tx",
			UpScript:   upScript,
			DownScript: downScript,
		}

		// This should fail
		err := executor.ApplyMigration(t.Context(), migration)
		if err == nil {
			t.Fatal("Expected error for duplicate table creation, got nil")
		}

		// Check that migration was NOT recorded (transaction rolled back)
		migrations, err := executor.GetAppliedMigrations(t.Context())
		if err != nil {
			t.Fatalf("Failed to get applied migrations: %v", err)
		}

		// Verify migration was not recorded
		for _, m := range migrations {
			if m.ID == "003_test_failure_tx" {
				t.Error("Failed transactional migration should not be recorded in database")
			}
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
