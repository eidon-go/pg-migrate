//go:build integration

package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/eidon-go/pg-migrate/internal/db"
	"github.com/eidon-go/pg-migrate/internal/dbconn"
)

const (
	pingRetries        = 10
	pingBackoff        = 300 * time.Millisecond
	randomNameNumBytes = 8
	dropTimeout        = 10 * time.Second
)

// pingWithRetry tolerates the brief window right after the shared postgres
// container reports healthy where the port isn't reliably accepting
// connections yet.
func pingWithRetry(t *testing.T, sqlDB *sql.DB) error {
	t.Helper()

	var err error
	for range pingRetries {
		if err = sqlDB.PingContext(t.Context()); err == nil {
			return nil
		}

		time.Sleep(pingBackoff)
	}

	return err
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}

// adminConfig returns connection details for the shared PostgreSQL instance
// used to create/drop per-test databases: docker-compose locally, a CI
// service container in the pipeline.
func adminConfig() db.PostgresConfig {
	return db.PostgresConfig{
		Host:     envOrDefault("TEST_POSTGRES_HOST", "localhost"),
		Port:     envOrDefault("TEST_POSTGRES_PORT", "5433"),
		User:     envOrDefault("TEST_POSTGRES_USER", "postgres"),
		Password: envOrDefault("TEST_POSTGRES_PASSWORD", "postgres"),
		DBName:   envOrDefault("TEST_POSTGRES_ADMIN_DB", "postgres"),
	}
}

func randomDBName(t *testing.T) string {
	t.Helper()

	buf := make([]byte, randomNameNumBytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("failed to generate random database name: %v", err)
	}

	return "test_" + hex.EncodeToString(buf)
}

func quoteIdent(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// SetupTestPostgres creates a fresh, isolated database on the shared test
// PostgreSQL instance and returns its connection config. Start the instance
// locally with `docker compose up -d postgres` (or `make compose-up`); CI
// provides it as a service container. The database is dropped automatically
// via t.Cleanup.
func SetupTestPostgres(t *testing.T) db.PostgresConfig {
	t.Helper()

	admin := adminConfig()

	adminDB, err := dbconn.OpenSQLDB(admin)
	if err != nil {
		t.Fatalf("failed to open admin connection: %v", err)
	}
	defer func() {
		_ = adminDB.Close()
	}()

	if err := pingWithRetry(t, adminDB); err != nil {
		t.Fatalf(
			"failed to reach test postgres at %s:%s (start it with `make compose-up`): %v",
			admin.Host, admin.Port, err,
		)
	}

	dbName := randomDBName(t)
	if _, err := adminDB.ExecContext(t.Context(), "CREATE DATABASE "+quoteIdent(dbName)); err != nil {
		t.Fatalf("failed to create test database %s: %v", dbName, err)
	}

	t.Cleanup(func() {
		dropTestDatabase(t, admin, dbName)
	})

	cfg := admin
	cfg.DBName = dbName

	return cfg
}

func dropTestDatabase(t *testing.T, admin db.PostgresConfig, dbName string) {
	t.Helper()

	cleanupDB, err := dbconn.OpenSQLDB(admin)
	if err != nil {
		t.Logf("failed to open admin connection for cleanup of %s: %v", dbName, err)

		return
	}
	defer func() {
		_ = cleanupDB.Close()
	}()

	// t.Context() is already canceled by the time Cleanup funcs run, so use
	// a fresh context bounded by its own timeout instead.
	ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
	defer cancel()

	// Terminate any lingering connections so DROP DATABASE doesn't fail.
	_, _ = cleanupDB.ExecContext(
		ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()",
		dbName,
	)

	if _, err := cleanupDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(dbName)); err != nil {
		t.Logf("failed to drop test database %s: %v", dbName, err)
	}
}

// MustConnect creates a connection to PostgreSQL or fails the test.
func MustConnect(t *testing.T, cfg db.PostgresConfig) *db.Postgres {
	t.Helper()

	pg, err := dbconn.NewPostgres(cfg)
	if err != nil {
		t.Fatalf("Failed to connect to postgres: %v", err)
	}

	// Verify connection
	if err := pg.GetDB().PingContext(t.Context()); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	return pg
}
