package source_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/eidon-go/pg-migrate/internal/source"
)

func mapFS(files map[string]string) fs.FS {
	fsys := make(fstest.MapFS, len(files))
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}

	return fsys
}

func TestFS_GetMigrations(t *testing.T) {
	t.Parallel()

	fsys := mapFS(map[string]string{
		"002_posts.up.sql":   "CREATE TABLE posts (id INT);",
		"002_posts.down.sql": "DROP TABLE posts;",
		"001_users.up.sql":   "CREATE TABLE users (id INT);",
		"001_users.down.sql": "DROP TABLE users;",
		// Non-SQL files are ignored silently.
		"README.md":  "# migrations",
		".gitkeep":   "",
		"notes.txt":  "scratch",
		"schema.png": "",
	})

	migrations, err := source.NewFS(fsys).GetMigrations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(migrations) != 2 {
		t.Fatalf("got %d migrations, want 2", len(migrations))
	}

	if migrations[0].ID != "001_users" || migrations[1].ID != "002_posts" {
		t.Errorf("IDs = [%s %s], want [001_users 002_posts] in lexicographic order",
			migrations[0].ID, migrations[1].ID)
	}

	if migrations[0].UpScript != "CREATE TABLE users (id INT);" {
		t.Errorf("UpScript = %q", migrations[0].UpScript)
	}

	if migrations[0].DownScript != "DROP TABLE users;" {
		t.Errorf("DownScript = %q", migrations[0].DownScript)
	}
}

func TestFS_GetMigrations_Empty(t *testing.T) {
	t.Parallel()

	migrations, err := source.NewFS(mapFS(map[string]string{"README.md": "nothing here"})).GetMigrations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(migrations) != 0 {
		t.Errorf("got %d migrations, want 0", len(migrations))
	}
}

func TestFS_GetMigrations_Rejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		{
			name: "missing down half",
			files: map[string]string{
				"001_users.up.sql": "CREATE TABLE users (id INT);",
			},
			wantErr: "no 001_users.down.sql",
		},
		{
			name: "orphan down half",
			files: map[string]string{
				"001_users.down.sql": "DROP TABLE users;",
			},
			wantErr: "no 001_users.up.sql",
		},
		{
			// A .sql file that will never run must say so rather than vanish.
			name: "sql file in the old single-file naming",
			files: map[string]string{
				"001_users.sql": "CREATE TABLE users (id INT);",
			},
			wantErr: "must end in .up.sql or .down.sql",
		},
		{
			name: "sql file with typo in suffix",
			files: map[string]string{
				"001_users.up.sqll":  "CREATE TABLE users (id INT);",
				"001_users.down.sql": "DROP TABLE users;",
			},
			// .sqll is not .sql at all, so it is ignored — and then the up half
			// is missing, which is reported.
			wantErr: "no 001_users.up.sql",
		},
		{
			name: "uppercase suffix is not silently skipped",
			files: map[string]string{
				"001_users.UP.SQL":   "CREATE TABLE users (id INT);",
				"001_users.down.sql": "DROP TABLE users;",
			},
			wantErr: "must end in .up.sql or .down.sql",
		},
		{
			name: "ids differing only in case",
			files: map[string]string{
				"001_Users.up.sql":   "CREATE TABLE users (id INT);",
				"001_Users.down.sql": "DROP TABLE users;",
				"001_users.up.sql":   "CREATE TABLE users2 (id INT);",
				"001_users.down.sql": "DROP TABLE users2;",
			},
			wantErr: "differ only in letter case",
		},
		{
			name: "empty migration id",
			files: map[string]string{
				".up.sql":   "CREATE TABLE t (id INT);",
				".down.sql": "DROP TABLE t;",
			},
			wantErr: "migration ID is empty",
		},
		{
			name: "invalid script inside a valid pair",
			files: map[string]string{
				"001_users.up.sql":   "-- +migrate Up\nCREATE TABLE users (id INT);",
				"001_users.down.sql": "DROP TABLE users;",
			},
			wantErr: "no longer uses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := source.NewFS(mapFS(tt.files)).GetMigrations()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Subdirectories are not traversed. Documented behaviour, asserted so it cannot
// change silently.
func TestFS_GetMigrations_IgnoresSubdirectories(t *testing.T) {
	t.Parallel()

	fsys := mapFS(map[string]string{
		"001_users.up.sql":          "CREATE TABLE users (id INT);",
		"001_users.down.sql":        "DROP TABLE users;",
		"nested/002_posts.up.sql":   "CREATE TABLE posts (id INT);",
		"nested/002_posts.down.sql": "DROP TABLE posts;",
	})

	migrations, err := source.NewFS(fsys).GetMigrations()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(migrations) != 1 || migrations[0].ID != "001_users" {
		t.Errorf("got %d migrations, want only 001_users from the top level", len(migrations))
	}
}

// Errors must be deterministic: with several broken migrations the message
// always names the lexicographically first one.
func TestFS_GetMigrations_DeterministicError(t *testing.T) {
	t.Parallel()

	fsys := mapFS(map[string]string{
		"001_a.up.sql": "CREATE TABLE a (id INT);",
		"002_b.up.sql": "CREATE TABLE b (id INT);",
		"003_c.up.sql": "CREATE TABLE c (id INT);",
	})

	for range 20 {
		_, err := source.NewFS(fsys).GetMigrations()
		if err == nil {
			t.Fatal("expected an error")
		}

		if !strings.Contains(err.Error(), "001_a") {
			t.Fatalf("error = %q, want it to name 001_a every time", err)
		}
	}
}
