package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/source"
)

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "already a slug", input: "add_users_table", want: "add_users_table"},
		{name: "spaces", input: "add users table", want: "add_users_table"},
		{name: "dashes", input: "add-users-table", want: "add_users_table"},
		{name: "uppercase folded", input: "AddUsersTable", want: "adduserstable"},
		{name: "mixed separators", input: "add - users  table", want: "add_users_table"},
		{name: "leading and trailing junk", input: "  --add users--  ", want: "add_users"},
		{name: "dots removed", input: "add.users.table", want: "add_users_table"},
		{name: "digits kept", input: "backfill v2 emails", want: "backfill_v2_emails"},
		{name: "non-ascii folded", input: "добавить users", want: "users"},
		{name: "empty", input: "", wantErr: true},
		{name: "only separators", input: "---", wantErr: true},
		{name: "only non-ascii", input: "миграция", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := slugify(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("slugify(%q) = %q, want error", tt.input, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("slugify(%q): %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSlugifyIsBoundedAndSafe(t *testing.T) {
	t.Parallel()

	slug, err := slugify(strings.Repeat("very long name ", 40))
	if err != nil {
		t.Fatalf("slugify: %v", err)
	}

	if len(slug) > maxSlugLength {
		t.Errorf("slug is %d bytes, over the %d limit", len(slug), maxSlugLength)
	}

	// A trailing underscore would produce IDs like "20260806..._add_", which is
	// merely ugly — but a dot would break the .up.sql suffix parsing outright.
	if strings.HasSuffix(slug, "_") || strings.Contains(slug, ".") {
		t.Errorf("unsafe slug %q", slug)
	}
}

// TestWriteMigrationPairIsLoadable is the test that matters: the scaffolded
// files must satisfy the loader, or `new` would hand the user a pair that the
// very next `up` rejects.
func TestWriteMigrationPairIsLoadable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	created, err := writeMigrationPair(dir, "20260806120000_add_users", "add_users")
	if err != nil {
		t.Fatalf("writeMigrationPair: %v", err)
	}

	if len(created) != 2 {
		t.Fatalf("created %d files, want 2", len(created))
	}

	migrations, err := source.NewFS(os.DirFS(dir)).GetMigrations()
	if err != nil {
		t.Fatalf("the generated pair does not load: %v", err)
	}

	if len(migrations) != 1 {
		t.Fatalf("loaded %d migrations, want 1", len(migrations))
	}

	if got := migrations[0].ID; got != "20260806120000_add_users" {
		t.Errorf("ID = %q, want %q", got, "20260806120000_add_users")
	}
}

func TestWriteMigrationPairRefusesToOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if _, err := writeMigrationPair(dir, "20260806120000_add_users", "add_users"); err != nil {
		t.Fatalf("first write: %v", err)
	}

	if _, err := writeMigrationPair(dir, "20260806120000_add_users", "add_users"); err == nil {
		t.Fatal("second write succeeded, want an error")
	}
}

// A half-written pair is worse than none: the loader rejects an .up.sql without
// its .down.sql, so every later command would fail until someone noticed.
func TestWriteMigrationPairCleansUpAfterPartialFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	id := "20260806120000_add_users"

	// Occupy the down half so that creating it fails after the up half is written.
	if err := os.WriteFile(filepath.Join(dir, id+".down.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatalf("seed down file: %v", err)
	}

	if _, err := writeMigrationPair(dir, id, "add_users"); err == nil {
		t.Fatal("writeMigrationPair succeeded, want an error")
	}

	if _, err := os.Stat(filepath.Join(dir, id+".up.sql")); !os.IsNotExist(err) {
		t.Errorf("the up half was left behind: %v", err)
	}
}

func TestWriteMigrationPairCreatesMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "db", "migrations")

	if _, err := writeMigrationPair(dir, "20260806120000_init", "init"); err != nil {
		t.Fatalf("writeMigrationPair: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("directory not created: %v", err)
	}
}
