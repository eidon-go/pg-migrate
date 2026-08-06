package source_test

import (
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/source"
)

func TestParseMigrationPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		up       string
		down     string
		wantErr  string // substring; empty means success
		wantUp   string
		wantDown string
	}{
		{
			name:     "plain pair",
			up:       "CREATE TABLE t (id INT);\n",
			down:     "DROP TABLE t;\n",
			wantUp:   "CREATE TABLE t (id INT);",
			wantDown: "DROP TABLE t;",
		},
		{
			name:     "directive header on up",
			up:       "-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			down:     "DROP INDEX IF EXISTS i;",
			wantUp:   "-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			wantDown: "DROP INDEX IF EXISTS i;",
		},
		{
			name:     "directive header on down only",
			up:       "CREATE TABLE t (id INT);",
			down:     "-- +migrate notransaction\nDROP INDEX CONCURRENTLY IF EXISTS i;",
			wantUp:   "CREATE TABLE t (id INT);",
			wantDown: "-- +migrate notransaction\nDROP INDEX CONCURRENTLY IF EXISTS i;",
		},
		{
			// The bug the format change exists to kill: a marker-looking string
			// inside a literal must be preserved verbatim, not split on.
			name:     "marker text inside string literal is just data",
			up:       "INSERT INTO audit(note) VALUES ('-- +migrate Down');\nCREATE TABLE t (id INT);",
			down:     "DROP TABLE t;",
			wantUp:   "INSERT INTO audit(note) VALUES ('-- +migrate Down');\nCREATE TABLE t (id INT);",
			wantDown: "DROP TABLE t;",
		},
		{
			name:     "statement block marker on first line is not a directive",
			up:       "-- +migrate StatementBegin\nCREATE FUNCTION f() RETURNS void AS $$ BEGIN END; $$ LANGUAGE plpgsql;\n-- +migrate StatementEnd",
			down:     "DROP FUNCTION f();",
			wantUp:   "-- +migrate StatementBegin\nCREATE FUNCTION f() RETURNS void AS $$ BEGIN END; $$ LANGUAGE plpgsql;\n-- +migrate StatementEnd",
			wantDown: "DROP FUNCTION f();",
		},
		{
			name:     "ordinary comment mentioning migrate is allowed",
			up:       "-- see the +migrate docs for details\nCREATE TABLE t (id INT);",
			down:     "DROP TABLE t;",
			wantUp:   "-- see the +migrate docs for details\nCREATE TABLE t (id INT);",
			wantDown: "DROP TABLE t;",
		},
		{
			name:     "leading blank lines still allow a header",
			up:       "\n\n-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			down:     "DROP INDEX IF EXISTS i;",
			wantUp:   "-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			wantDown: "DROP INDEX IF EXISTS i;",
		},

		{
			name:     "irreversible down needs no SQL",
			up:       "DROP TABLE legacy;",
			down:     "-- +migrate irreversible",
			wantUp:   "DROP TABLE legacy;",
			wantDown: "-- +migrate irreversible",
		},

		// --- rejections ---
		{
			name:    "empty up",
			up:      "",
			down:    "DROP TABLE t;",
			wantErr: "contains no SQL",
		},
		{
			// The message has to point at the two valid ways out, or the author
			// will just write "SELECT 1;" and lose the distinction.
			name:    "empty down names the irreversible directive",
			up:      "CREATE TABLE t (id INT);",
			down:    "",
			wantErr: `state that there is none with a first line of "-- +migrate irreversible"`,
		},
		{
			name:    "whitespace-only down",
			up:      "CREATE TABLE t (id INT);",
			down:    "   \n\t\n",
			wantErr: "contains no SQL",
		},
		{
			name:    "irreversible with SQL is contradictory",
			up:      "DROP TABLE legacy;",
			down:    "-- +migrate irreversible\nCREATE TABLE legacy (id INT);",
			wantErr: "either reversible or not",
		},
		{
			name:    "irreversible in the up file",
			up:      "-- +migrate irreversible\nDROP TABLE legacy;",
			down:    "SELECT 1;",
			wantErr: "only belongs in a .down.sql file",
		},
		{
			name:    "header with no SQL after it",
			up:      "-- +migrate notransaction\n",
			down:    "DROP TABLE t;",
			wantErr: "contains no SQL",
		},
		{
			name:    "unknown directive",
			up:      "-- +migrate concurrently\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: `unknown directive "concurrently"`,
		},
		{
			name:    "wrong case directive",
			up:      "-- +migrate NoTransaction\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: "case-sensitive",
		},
		{
			name:    "old format Up marker",
			up:      "-- +migrate Up\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: "no longer uses",
		},
		{
			name:    "old format Down marker in down file",
			up:      "CREATE TABLE t (id INT);",
			down:    "-- +migrate Down\nDROP TABLE t;",
			wantErr: "no longer uses",
		},
		{
			name:    "malformed directive without space",
			up:      "--+migrate notransaction\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: "malformed directive line",
		},
		{
			name:    "misspelled migrate token",
			up:      "-- +migrates notransaction\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: "malformed directive line",
		},
		{
			name:    "bare directive line names nothing",
			up:      "-- +migrate\nCREATE TABLE t (id INT);",
			down:    "DROP TABLE t;",
			wantErr: "names no directive",
		},
		{
			name:    "directive below the first line",
			up:      "CREATE TABLE t (id INT);\n-- +migrate notransaction\nCREATE INDEX i ON t(x);",
			down:    "DROP TABLE t;",
			wantErr: "only recognized on the first line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, err := source.ParseMigrationPair("001_test", []byte(tt.up), []byte(tt.down))

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}

				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if m.ID != "001_test" {
				t.Errorf("ID = %q, want 001_test", m.ID)
			}

			if m.UpScript != tt.wantUp {
				t.Errorf("UpScript = %q, want %q", m.UpScript, tt.wantUp)
			}

			if m.DownScript != tt.wantDown {
				t.Errorf("DownScript = %q, want %q", m.DownScript, tt.wantDown)
			}
		})
	}
}

// The error naming the offending file matters: with two files per migration,
// "which one" is the first thing the reader needs.
func TestParseMigrationPair_ErrorNamesTheFile(t *testing.T) {
	t.Parallel()

	_, err := source.ParseMigrationPair("003_widgets", []byte("CREATE TABLE t (id INT);"), []byte(""))
	if err == nil {
		t.Fatal("expected an error for an empty down script")
	}

	if !strings.Contains(err.Error(), "003_widgets.down.sql") {
		t.Errorf("error = %q, want it to name 003_widgets.down.sql", err)
	}
}
