package main

import (
	"strings"
	"testing"
	"time"
)

// NewConfig reads process-wide state, so these cannot run in parallel with each
// other — t.Setenv enforces that by failing a test that also calls t.Parallel.

//nolint:paralleltest // t.Setenv panics in a test that also calls t.Parallel
func TestNewConfigDefaults(t *testing.T) {
	clearMigrationEnv(t)

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	// The CLI default is deliberately longer than the library's 5s: a CLI run is
	// usually part of a deployment, where a busy lock means another instance is
	// mid-migration and waiting beats failing the rollout.
	if cfg.LockTimeout != defaultLockTimeout {
		t.Errorf("LockTimeout = %s, want %s", cfg.LockTimeout, defaultLockTimeout)
	}

	if cfg.MigrationPath != "" {
		t.Errorf("MigrationPath = %q, want empty", cfg.MigrationPath)
	}
}

func TestNewConfigLockTimeout(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr string
	}{
		{name: "duration", raw: "30s", want: 30 * time.Second},
		{name: "minutes", raw: "10m", want: 10 * time.Minute},
		{name: "not a duration", raw: "soon", wantErr: "expected a duration"},
		{name: "bare number", raw: "30", wantErr: "expected a duration"},
		// Zero means "wait forever" in the library, but as a CLI default it is
		// far more likely to be an unset variable than an intent to hang.
		{name: "zero", raw: "0s", wantErr: "must be positive"},
		{name: "negative", raw: "-5s", wantErr: "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMigrationEnv(t)
			t.Setenv("MIGRATION_LOCK_TIMEOUT", tt.raw)

			cfg, err := NewConfig()

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("NewConfig(%q) succeeded, want error", tt.raw)
				}

				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not mention %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("NewConfig(%q): %v", tt.raw, err)
			}

			if cfg.LockTimeout != tt.want {
				t.Errorf("LockTimeout = %s, want %s", cfg.LockTimeout, tt.want)
			}
		})
	}
}

// DATABASE_URL wins over POSTGRES_DSN because that is what hosting platforms
// hand out, and both being set is normal rather than a mistake.
func TestNewConfigDSNPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		databaseURL string
		postgresDSN string
		want        string
	}{
		{name: "database url only", databaseURL: "postgres://a", want: "postgres://a"},
		{name: "postgres dsn only", postgresDSN: "postgres://b", want: "postgres://b"},
		{name: "database url wins", databaseURL: "postgres://a", postgresDSN: "postgres://b", want: "postgres://a"},
		{name: "neither", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearMigrationEnv(t)
			t.Setenv("DATABASE_URL", tt.databaseURL)
			t.Setenv("POSTGRES_DSN", tt.postgresDSN)

			cfg, err := NewConfig()
			if err != nil {
				t.Fatalf("NewConfig: %v", err)
			}

			if cfg.Postgres.DSN != tt.want {
				t.Errorf("DSN = %q, want %q", cfg.Postgres.DSN, tt.want)
			}
		})
	}
}

func TestNewConfigDiscreteFields(t *testing.T) {
	clearMigrationEnv(t)

	t.Setenv("POSTGRES_HOST", "db.internal")
	t.Setenv("POSTGRES_PORT", "5433")
	t.Setenv("POSTGRES_USER", "app")
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("POSTGRES_DB", "appdb")
	t.Setenv("POSTGRES_SSLMODE", "require")
	t.Setenv("MIGRATION_PATH", "./db/migrations")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	for _, check := range []struct{ name, got, want string }{
		{"Host", cfg.Postgres.Host, "db.internal"},
		{"Port", cfg.Postgres.Port, "5433"},
		{"User", cfg.Postgres.User, "app"},
		{"Password", cfg.Postgres.Password, "secret"},
		{"DBName", cfg.Postgres.DBName, "appdb"},
		{"SSLMode", cfg.Postgres.SSLMode, "require"},
		{"MigrationPath", cfg.MigrationPath, "./db/migrations"},
	} {
		if check.got != check.want {
			t.Errorf("%s = %q, want %q", check.name, check.got, check.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "first wins", values: []string{"a", "b"}, want: "a"},
		{name: "skips empty", values: []string{"", "b"}, want: "b"},
		{name: "all empty", values: []string{"", ""}, want: ""},
		{name: "none", values: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := firstNonEmpty(tt.values...); got != tt.want {
				t.Errorf("firstNonEmpty(%q) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}

// clearMigrationEnv unsets everything NewConfig reads, so a variable in the
// developer's shell cannot change what a test observes. t.Setenv restores the
// previous value when the test ends.
func clearMigrationEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"MIGRATION_LOCK_TIMEOUT", "MIGRATION_PATH",
		"DATABASE_URL", "POSTGRES_DSN", "POSTGRES_HOST", "POSTGRES_PORT",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "POSTGRES_SSLMODE",
	} {
		t.Setenv(key, "")
	}
}
