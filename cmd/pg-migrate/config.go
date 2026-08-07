package main

import (
	"fmt"
	"os"
	"time"

	"github.com/eidon-go/pg-migrate/internal/db"
)

// defaultLockTimeout is deliberately longer than the library's own 5s default.
// A CLI run is normally part of a deployment, where a busy lock means another
// instance is mid-migration and waiting it out beats failing the rollout.
const defaultLockTimeout = 5 * time.Minute

type Config struct {
	MigrationPath string
	Postgres      db.PostgresConfig
	LockTimeout   time.Duration
}

func NewConfig() (Config, error) {
	lockTimeout := defaultLockTimeout

	if raw := os.Getenv("MIGRATION_LOCK_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf(
				"invalid MIGRATION_LOCK_TIMEOUT %q: expected a duration such as 30s or 5m: %w", raw, err)
		}

		if parsed <= 0 {
			return Config{}, fmt.Errorf("invalid MIGRATION_LOCK_TIMEOUT %q: must be positive", raw)
		}

		lockTimeout = parsed
	}

	return Config{
		MigrationPath: os.Getenv("MIGRATION_PATH"),
		LockTimeout:   lockTimeout,
		Postgres: db.PostgresConfig{
			// DATABASE_URL takes precedence over the discrete variables, matching
			// what hosting platforms hand out.
			DSN:      firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("POSTGRES_DSN")),
			Host:     os.Getenv("POSTGRES_HOST"),
			Port:     os.Getenv("POSTGRES_PORT"),
			User:     os.Getenv("POSTGRES_USER"),
			Password: os.Getenv("POSTGRES_PASSWORD"),
			DBName:   os.Getenv("POSTGRES_DB"),
			SSLMode:  os.Getenv("POSTGRES_SSLMODE"),
		},
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}
