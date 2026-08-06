package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
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
// very next `up` rejects. Every flag combination is covered, because each one
// produces different file contents.
func TestWriteMigrationPairIsLoadable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		opts             migrationTemplateOptions
		wantUpNoTx       bool
		wantDownNoTx     bool
		wantIrreversible bool
	}{
		{
			name: "plain",
		},
		{
			name:         "notransaction",
			opts:         migrationTemplateOptions{noTransaction: true},
			wantUpNoTx:   true,
			wantDownNoTx: true,
		},
		{
			name:             "irreversible",
			opts:             migrationTemplateOptions{irreversible: true},
			wantIrreversible: true,
		},
		{
			// irreversible wins for the down half: there is no rollback script,
			// so how it would have run is moot.
			name:             "both",
			opts:             migrationTemplateOptions{noTransaction: true, irreversible: true},
			wantUpNoTx:       true,
			wantIrreversible: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			id := "20260806120000_add_users"

			created, err := writeMigrationPair(dir, id, "add_users", tt.opts)
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

			migration := migrations[0]
			if migration.ID != id {
				t.Errorf("ID = %q, want %q", migration.ID, id)
			}

			upDirectives, _, err := migrator.ParseDirectiveHeader(migration.UpScript)
			if err != nil {
				t.Fatalf("up half does not parse: %v", err)
			}

			downDirectives, _, err := migrator.ParseDirectiveHeader(migration.DownScript)
			if err != nil {
				t.Fatalf("down half does not parse: %v", err)
			}

			if got := upDirectives[migrator.DirectiveNoTransaction]; got != tt.wantUpNoTx {
				t.Errorf("up notransaction = %v, want %v", got, tt.wantUpNoTx)
			}

			if got := downDirectives[migrator.DirectiveNoTransaction]; got != tt.wantDownNoTx {
				t.Errorf("down notransaction = %v, want %v", got, tt.wantDownNoTx)
			}

			if got := downDirectives[migrator.DirectiveIrreversible]; got != tt.wantIrreversible {
				t.Errorf("down irreversible = %v, want %v", got, tt.wantIrreversible)
			}
		})
	}
}

// The loader rejects an "irreversible" script that still has a body, and a
// comment counts as one — so the down half must carry the directive and nothing
// else. Easy to break by adding a friendly explanation below it.
func TestIrreversibleDownHasNoBody(t *testing.T) {
	t.Parallel()

	opts := migrationTemplateOptions{irreversible: true}

	_, body, err := migrator.ParseDirectiveHeader(strings.TrimSpace(opts.downContent("add_users")))
	if err != nil {
		t.Fatalf("down half does not parse: %v", err)
	}

	if strings.TrimSpace(body) != "" {
		t.Errorf("body must be empty, got %q", body)
	}
}

func TestWriteMigrationPairRefusesToOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if _, err := writeMigrationPair(dir, "20260806120000_add_users", "add_users", migrationTemplateOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	if _, err := writeMigrationPair(dir, "20260806120000_add_users", "add_users", migrationTemplateOptions{}); err == nil {
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

	if _, err := writeMigrationPair(dir, id, "add_users", migrationTemplateOptions{}); err == nil {
		t.Fatal("writeMigrationPair succeeded, want an error")
	}

	if _, err := os.Stat(filepath.Join(dir, id+".up.sql")); !os.IsNotExist(err) {
		t.Errorf("the up half was left behind: %v", err)
	}
}

func TestWriteMigrationPairCreatesMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "db", "migrations")

	if _, err := writeMigrationPair(dir, "20260806120000_init", "init", migrationTemplateOptions{}); err != nil {
		t.Fatalf("writeMigrationPair: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("directory not created: %v", err)
	}
}
