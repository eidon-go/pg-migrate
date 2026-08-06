//go:build integration

package migrator_test

import (
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

func TestCustomExecutor_CustomTableName(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor, err := migrator.NewCustomExecutor(postgres.GetDB(),
		migrator.TableConfig{Name: "schema_migrations"})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	if !integrationTableExists(t, postgres.GetDB(), "schema_migrations") {
		t.Error("schema_migrations table was not created")
	}

	if integrationTableExists(t, postgres.GetDB(), "migrations") {
		t.Error("the default migrations table must not be created when a name is configured")
	}

	// The whole apply/rollback cycle must use the configured table.
	m := &migrator.Migration{
		ID:         "001_custom_table",
		UpScript:   "CREATE TABLE t_custom (id INT);",
		DownScript: "DROP TABLE t_custom;",
	}
	if err := executor.ApplyMigration(t.Context(), m); err != nil {
		t.Fatalf("failed to apply: %v", err)
	}

	var count int
	if err := postgres.GetDB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM schema_migrations WHERE id = $1", m.ID).Scan(&count); err != nil {
		t.Fatalf("failed to read schema_migrations: %v", err)
	}

	if count != 1 {
		t.Errorf("expected the record in schema_migrations, got %d rows", count)
	}

	if err := executor.RollbackMigration(t.Context(), m.ID); err != nil {
		t.Fatalf("failed to rollback: %v", err)
	}

	if integrationTableExists(t, postgres.GetDB(), "t_custom") {
		t.Error("t_custom should have been dropped")
	}
}

func TestCustomExecutor_CustomSchema(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	if _, err := postgres.GetDB().ExecContext(t.Context(), "CREATE SCHEMA bookkeeping"); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	executor, err := migrator.NewCustomExecutor(postgres.GetDB(),
		migrator.TableConfig{Schema: "bookkeeping", Name: "migrations"})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	var schema string
	if err := postgres.GetDB().QueryRowContext(t.Context(),
		"SELECT table_schema FROM information_schema.tables WHERE table_name = 'migrations'",
	).Scan(&schema); err != nil {
		t.Fatalf("failed to locate the migrations table: %v", err)
	}

	if schema != "bookkeeping" {
		t.Errorf("migrations table is in schema %q, want %q", schema, "bookkeeping")
	}
}

// A missing schema must fail loudly rather than silently landing the table
// somewhere on the search_path.
func TestCustomExecutor_MissingSchemaIsAnError(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor, err := migrator.NewCustomExecutor(postgres.GetDB(),
		migrator.TableConfig{Schema: "does_not_exist"})
	if err != nil {
		t.Fatalf("failed to create executor: %v", err)
	}

	if err := executor.EnsureMigrationsTable(t.Context()); err == nil {
		t.Error("expected an error for a schema that does not exist, got nil")
	}
}

// Ordering must follow the monotonic sequence, not applied_at. Rewriting the
// timestamps to be deliberately out of order must not change the reported order,
// because that order drives reverse-order rollback and recovery.
func TestCustomExecutor_OrderIsBySequenceNotTimestamp(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor := newTestExecutor(t, postgres.GetDB())
	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	applied := []string{"001_first", "002_second", "003_third"}
	for _, id := range applied {
		m := &migrator.Migration{
			ID:         id,
			UpScript:   "SELECT 1;",
			DownScript: "SELECT 1;",
		}
		if err := executor.ApplyMigration(t.Context(), m); err != nil {
			t.Fatalf("failed to apply %s: %v", id, err)
		}
	}

	// Scramble applied_at so timestamp order is the exact reverse of the real one.
	if _, err := postgres.GetDB().ExecContext(t.Context(), `
		UPDATE migrations SET applied_at = CASE id
			WHEN '001_first'  THEN NOW() + INTERVAL '2 hours'
			WHEN '002_second' THEN NOW() + INTERVAL '1 hour'
			WHEN '003_third'  THEN NOW()
		END`); err != nil {
		t.Fatalf("failed to scramble applied_at: %v", err)
	}

	records, err := executor.GetAppliedMigrations(t.Context())
	if err != nil {
		t.Fatalf("failed to get applied migrations: %v", err)
	}

	if len(records) != len(applied) {
		t.Fatalf("got %d records, want %d", len(records), len(applied))
	}

	for i, want := range applied {
		if records[i].ID != want {
			t.Errorf("record %d = %s, want %s (order must follow seq, not applied_at)",
				i, records[i].ID, want)
		}
	}
}

// applied_at must be timestamptz, so that a stored instant means the same thing
// regardless of the session time zone that wrote or reads it.
func TestCustomExecutor_AppliedAtIsTimestamptz(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	postgres := dbtest.MustConnect(t, cfg)
	defer postgres.Close()

	executor := newTestExecutor(t, postgres.GetDB())
	if err := executor.EnsureMigrationsTable(t.Context()); err != nil {
		t.Fatalf("failed to ensure table: %v", err)
	}

	var dataType string
	if err := postgres.GetDB().QueryRowContext(t.Context(), `
		SELECT data_type FROM information_schema.columns
		WHERE table_name = 'migrations' AND column_name = 'applied_at'`).Scan(&dataType); err != nil {
		t.Fatalf("failed to read column type: %v", err)
	}

	if dataType != "timestamp with time zone" {
		t.Errorf("applied_at is %q, want %q", dataType, "timestamp with time zone")
	}
}
