package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePair is a fixture helper: the two halves of a migration, verbatim.
func writePair(t *testing.T, dir, id, up, down string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, id+".up.sql"), []byte(up), 0o600); err != nil {
		t.Fatalf("write up: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, id+".down.sql"), []byte(down), 0o600); err != nil {
		t.Fatalf("write down: %v", err)
	}
}

func TestValidateMigrationsReportsDirectives(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writePair(t, dir, "20260115103000_create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY);",
		"DROP TABLE users;")
	writePair(t, dir, "20260116084500_add_index",
		"-- +migrate notransaction\nCREATE INDEX CONCURRENTLY i ON users(id);",
		"-- +migrate notransaction\nDROP INDEX CONCURRENTLY i;")
	writePair(t, dir, "20260117090000_drop_legacy",
		"DROP TABLE legacy;",
		"-- +migrate irreversible")

	report, err := validateMigrations(dir)
	if err != nil {
		t.Fatalf("validateMigrations: %v", err)
	}

	if report.Count != 3 {
		t.Fatalf("Count = %d, want 3", report.Count)
	}

	// Order is the order migrations would be applied in, so the report reads the
	// same way the deployment will run.
	want := []validatedMigration{
		{ID: "20260115103000_create_users"},
		{ID: "20260116084500_add_index", NoTransaction: true},
		{ID: "20260117090000_drop_legacy", Irreversible: true},
	}

	for i, expected := range want {
		got := report.Migrations[i]
		if got != expected {
			t.Errorf("migration %d = %+v, want %+v", i, got, expected)
		}
	}
}

func TestValidateMigrationsRejectsBrokenSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		wantErr string
	}{
		{
			name: "missing down half",
			setup: func(t *testing.T, dir string) {
				t.Helper()

				if err := os.WriteFile(filepath.Join(dir, "20260115103000_x.up.sql"),
					[]byte("SELECT 1;"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "down",
		},
		{
			name: "misspelled directive",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				writePair(t, dir, "20260115103000_x",
					"-- +migrate notransction\nSELECT 1;", "SELECT 1;")
			},
			wantErr: "unknown directive",
		},
		{
			name: "unclosed statement block",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				writePair(t, dir, "20260115103000_x",
					"-- +migrate StatementBegin\nSELECT 1;", "SELECT 1;")
			},
			wantErr: "StatementEnd",
		},
		{
			name: "empty down without the irreversible directive",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				writePair(t, dir, "20260115103000_x", "SELECT 1;", "")
			},
			wantErr: "irreversible",
		},
		// IDs differing only in letter case are rejected too, but that cannot be
		// set up through the filesystem on macOS or Windows, where the second
		// file simply overwrites the first. It is covered against an in-memory
		// FS in internal/source instead.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			tt.setup(t, dir)

			_, err := validateMigrations(dir)
			if err == nil {
				t.Fatal("validateMigrations succeeded, want an error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMigrationsOnEmptyDirectory(t *testing.T) {
	t.Parallel()

	// Not an error: a project that has not written its first migration is valid.
	// Reconcile is where an empty source is treated as suspicious, because there
	// it would mean rolling the schema back to nothing.
	report, err := validateMigrations(t.TempDir())
	if err != nil {
		t.Fatalf("validateMigrations: %v", err)
	}

	if report.Count != 0 {
		t.Errorf("Count = %d, want 0", report.Count)
	}
}

func TestWriteValidationReportJSON(t *testing.T) {
	t.Parallel()

	report := &validationReport{
		Path:  "./migrations",
		Count: 1,
		Migrations: []validatedMigration{
			{ID: "20260115103000_x", NoTransaction: true},
		},
	}

	var buf bytes.Buffer
	if err := writeValidationReport(&buf, report, true); err != nil {
		t.Fatalf("writeValidationReport: %v", err)
	}

	var decoded validationReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if decoded.Count != 1 || decoded.Migrations[0].ID != "20260115103000_x" {
		t.Errorf("round-trip lost data: %+v", decoded)
	}

	if !decoded.Migrations[0].NoTransaction {
		t.Error("NoTransaction did not survive the round trip")
	}
}

func TestWriteValidationReportText(t *testing.T) {
	t.Parallel()

	report := &validationReport{
		Path:  "./migrations",
		Count: 2,
		Migrations: []validatedMigration{
			{ID: "20260115103000_a"},
			{ID: "20260116084500_b", NoTransaction: true, Irreversible: true},
		},
	}

	var buf bytes.Buffer
	if err := writeValidationReport(&buf, report, false); err != nil {
		t.Fatalf("writeValidationReport: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"20260115103000_a",
		"20260116084500_b",
		"[notransaction, irreversible]",
		"2 migration(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}
