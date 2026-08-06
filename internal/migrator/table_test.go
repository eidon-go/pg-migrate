package migrator_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

func TestNewCustomExecutor_TableValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		table   migrator.TableConfig
		wantErr string // substring; empty means the config is accepted
	}{
		{
			name:  "zero value uses the default table",
			table: migrator.TableConfig{},
		},
		{
			name:  "custom name",
			table: migrator.TableConfig{Name: "schema_migrations"},
		},
		{
			name:  "schema qualified",
			table: migrator.TableConfig{Schema: "app", Name: "migrations"},
		},
		{
			name:  "schema with default name",
			table: migrator.TableConfig{Schema: "app"},
		},
		{
			// Quoting is what makes arbitrary names safe, so they are allowed
			// rather than restricted to a character class.
			name:  "name needing quoting",
			table: migrator.TableConfig{Name: `weird "name"`},
		},
		{
			name:    "name at the length limit",
			table:   migrator.TableConfig{Name: strings.Repeat("a", 63)},
			wantErr: "",
		},
		{
			name:    "name over the length limit",
			table:   migrator.TableConfig{Name: strings.Repeat("a", 64)},
			wantErr: "over the Postgres limit",
		},
		{
			name:    "schema over the length limit",
			table:   migrator.TableConfig{Schema: strings.Repeat("s", 64)},
			wantErr: "over the Postgres limit",
		},
		{
			name:    "name with NUL byte",
			table:   migrator.TableConfig{Name: "migra\x00tions"},
			wantErr: "NUL byte",
		},
		{
			// An explicitly empty schema means "unqualified", but an explicitly
			// empty name would be a mistake worth reporting. It cannot be
			// distinguished from unset, so it falls back to the default instead.
			name:  "empty name falls back to the default",
			table: migrator.TableConfig{Name: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			executor, err := migrator.NewCustomExecutor(&sql.DB{}, tt.table)

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

			if executor == nil {
				t.Error("expected an executor, got nil")
			}
		})
	}
}
