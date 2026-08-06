// Package dbconn opens *sql.DB connections backed by pgx. It is kept
// separate from internal/db so that library consumers who bring their own
// *sql.DB (via db.NewPostgresFromDB) never pull pgx into their build graph —
// only the CLI (cmd/migrate) and test helpers, which open their own
// connections, import this package.
package dbconn

import (
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/eidon-go/pg-migrate/internal/db"
)

// applicationName identifies migration-tool connections in pg_stat_activity,
// which matters when diagnosing a stuck advisory lock.
const applicationName = "migrate"

// OpenSQLDB opens a *sql.DB backed by pgx (binary protocol, server-side
// prepared statement caching by default) with application_name set so the
// connection is identifiable in pg_stat_activity.
func OpenSQLDB(cfg db.PostgresConfig) (*sql.DB, error) {
	connConfig, err := pgx.ParseConfig(cfg.GetDataSource())
	if err != nil {
		return nil, fmt.Errorf("parse postgres connection string: %w", err)
	}

	connConfig.RuntimeParams["application_name"] = applicationName

	return stdlib.OpenDB(*connConfig), nil
}

// NewPostgres opens a pgx-backed connection and wraps it in *db.Postgres.
func NewPostgres(cfg db.PostgresConfig) (*db.Postgres, error) {
	sqlDB, err := OpenSQLDB(cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}

	return db.NewPostgresFromDB(sqlDB, cfg), nil
}
