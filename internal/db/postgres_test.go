package db_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/eidon-go/pg-migrate/internal/db"
)

func TestGetDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   db.PostgresConfig
		expected string
	}{
		{
			name: "full config",
			config: db.PostgresConfig{
				Host:     "localhost",
				Port:     "5432",
				User:     "postgres",
				Password: "secret",
				DBName:   "testdb",
			},
			// No sslmode: unset leaves the driver's default in effect.
			expected: "host=localhost port=5432 user=postgres password=secret dbname=testdb",
		},
		{
			name: "explicit sslmode",
			config: db.PostgresConfig{
				Host:    "db.example.com",
				DBName:  "production",
				SSLMode: "require",
			},
			expected: "host=db.example.com dbname=production sslmode=require",
		},
		{
			name: "with special characters in password",
			config: db.PostgresConfig{
				Host:     "db.example.com",
				Port:     "5433",
				User:     "admin",
				Password: "p@ssw0rd!",
				DBName:   "production",
			},
			expected: "host=db.example.com port=5433 user=admin password=p@ssw0rd! dbname=production",
		},
		{
			// Nothing configured means nothing specified, so the driver falls back
			// to its defaults and the standard PG* environment variables.
			name:     "empty config",
			config:   db.PostgresConfig{},
			expected: "",
		},
		{
			name: "partial config omits the rest",
			config: db.PostgresConfig{
				Host:   "localhost",
				DBName: "testdb",
			},
			expected: "host=localhost dbname=testdb",
		},
		{
			// A DSN wins outright; the discrete fields are not merged into it.
			name: "explicit DSN is used verbatim",
			config: db.PostgresConfig{
				DSN:  "postgres://u:p@example.com:5432/app?sslmode=verify-full",
				Host: "ignored",
			},
			expected: "postgres://u:p@example.com:5432/app?sslmode=verify-full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.config.GetDataSource()
			if got != tt.expected {
				t.Errorf("GetDataSource() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestGetDataSource_EscapesSpecialCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		password string
	}{
		{name: "space", password: "p@ss word"},
		{name: "single quote", password: "p@ss'word"},
		{name: "backslash", password: `p@ss\word`},
		{name: "quote and backslash", password: `p\a's's'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := db.PostgresConfig{
				Host:     "localhost",
				Port:     "5432",
				User:     "postgres",
				Password: tt.password,
				DBName:   "testdb",
			}

			dsn := cfg.GetDataSource()

			connConfig, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatalf("GetDataSource() produced an unparseable DSN %q: %v", dsn, err)
			}

			if connConfig.Password != tt.password {
				t.Errorf("round-tripped password = %q, want %q (dsn: %q)", connConfig.Password, tt.password, dsn)
			}
		})
	}
}
