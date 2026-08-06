package migrator_test

import (
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

// Statement block markers, spelled out here rather than referenced from the
// package so this test can live outside it.
const (
	statementBeginLine = "-- +migrate StatementBegin"
	statementEndLine   = "-- +migrate StatementEnd"
)

func TestParseDirectiveHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		script   string
		wantErr  string // substring; empty means success
		wantDirs []string
		wantBody string
	}{
		{
			name:     "no header",
			script:   "CREATE TABLE t (id INT);",
			wantBody: "CREATE TABLE t (id INT);",
		},
		{
			name:     "notransaction header",
			script:   "-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			wantDirs: []string{"notransaction"},
			wantBody: "CREATE INDEX CONCURRENTLY i ON t(x);",
		},
		{
			name:     "header with surrounding whitespace",
			script:   "   -- +migrate    notransaction   \nSELECT 1;",
			wantDirs: []string{"notransaction"},
			wantBody: "SELECT 1;",
		},
		{
			name:     "statement begin on first line is body, not header",
			script:   statementBeginLine + "\nSELECT 1;\n" + statementEndLine,
			wantBody: statementBeginLine + "\nSELECT 1;\n" + statementEndLine,
		},
		{
			name:     "comment mentioning migrate is body",
			script:   "-- read the +migrate notes\nSELECT 1;",
			wantBody: "-- read the +migrate notes\nSELECT 1;",
		},
		{
			name:     "marker-looking text inside a literal is body",
			script:   "INSERT INTO t VALUES ('-- +migrate notransaction');",
			wantBody: "INSERT INTO t VALUES ('-- +migrate notransaction');",
		},
		{
			name:     "single line script without newline",
			script:   "SELECT 1;",
			wantBody: "SELECT 1;",
		},
		{
			name:     "empty script",
			script:   "",
			wantBody: "",
		},

		// --- rejections ---
		{
			name:    "unknown directive",
			script:  "-- +migrate wat\nSELECT 1;",
			wantErr: `unknown directive "wat"`,
		},
		{
			name:    "wrong case",
			script:  "-- +migrate NOTRANSACTION\nSELECT 1;",
			wantErr: "case-sensitive",
		},
		{
			name:    "legacy Up marker names the new layout",
			script:  "-- +migrate Up\nSELECT 1;",
			wantErr: ".up.sql",
		},
		{
			name:    "legacy Down marker names the new layout",
			script:  "-- +migrate Down\nSELECT 1;",
			wantErr: ".down.sql",
		},
		{
			name:    "fused comment token",
			script:  "--+migrate notransaction\nSELECT 1;",
			wantErr: "malformed directive line",
		},
		{
			name:    "misspelled token",
			script:  "-- +migrat notransaction\nSELECT 1;",
			wantErr: "malformed directive line",
		},
		{
			name:    "bare directive line",
			script:  "-- +migrate\nSELECT 1;",
			wantErr: "names no directive",
		},
		{
			name:    "directive on second line",
			script:  "SELECT 1;\n-- +migrate notransaction\nSELECT 2;",
			wantErr: "line 2",
		},
		{
			name:    "directive after a header",
			script:  "-- +migrate notransaction\nSELECT 1;\n-- +migrate notransaction\nSELECT 2;",
			wantErr: "line 3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dirs, body, err := migrator.ParseDirectiveHeader(tt.script)

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

			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}

			if len(dirs) != len(tt.wantDirs) {
				t.Fatalf("directives = %v, want %v", dirs, tt.wantDirs)
			}

			for _, name := range tt.wantDirs {
				if !dirs[name] {
					t.Errorf("directive %q missing from %v", name, dirs)
				}
			}
		})
	}
}
