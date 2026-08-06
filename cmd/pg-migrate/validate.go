package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
)

// validatedMigration is one migration as the loader sees it, before any
// database is involved.
type validatedMigration struct {
	ID string `json:"id"`

	// NoTransaction and Irreversible are surfaced because they are the two
	// properties that change what a deployment risks: one gives up atomicity,
	// the other gives up rollback.
	NoTransaction bool `json:"notransaction"`
	Irreversible  bool `json:"irreversible"`
}

type validationReport struct {
	Path       string               `json:"path"`
	Migrations []validatedMigration `json:"migrations"`
	Count      int                  `json:"count"`
}

// validateCommand builds the "validate" subcommand.
//
// It reads and parses the migration files and reports what it found, without
// opening a database connection. The point is to fail in CI or a pre-commit
// hook rather than partway through a deployment: a misspelled directive or an
// unclosed statement block is otherwise discovered when the pod is already
// starting.
func validateCommand(resolvePath func() (string, error)) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check migration files without connecting to a database",
		Long: "Parse every migration in --migration-path and report problems.\n\n" +
			"Checks that both halves of each pair exist, that directive lines are\n" +
			"recognised and correctly placed, that statement blocks are closed, and\n" +
			"that no two IDs differ only in letter case.\n\n" +
			"Needs no database and no credentials.",
		Example: "  pg-migrate validate\n" +
			"  pg-migrate validate --json --migration-path ./db/migrations",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolvePath()
			if err != nil {
				return err
			}

			report, err := validateMigrations(path)
			if err != nil {
				return err
			}

			return writeValidationReport(cmd.OutOrStdout(), report, jsonOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the report as JSON")

	return cmd
}

// validateMigrations loads the source and describes what it holds. Loading is
// the validation: the file source rejects a malformed pair rather than
// returning it, so anything that comes back is known good.
func validateMigrations(path string) (*validationReport, error) {
	migrations, err := source.NewFS(os.DirFS(path)).GetMigrations()
	if err != nil {
		return nil, fmt.Errorf("migration path %s: %w", path, err)
	}

	report := &validationReport{
		Path:       path,
		Count:      len(migrations),
		Migrations: make([]validatedMigration, 0, len(migrations)),
	}

	for _, migration := range migrations {
		// Already parsed once by the loader, so an error here is impossible;
		// ignoring it keeps the reporting path free of a branch that cannot be
		// reached or tested.
		upDirectives, _, _ := migrator.ParseDirectiveHeader(migration.UpScript)
		downDirectives, _, _ := migrator.ParseDirectiveHeader(migration.DownScript)

		report.Migrations = append(report.Migrations, validatedMigration{
			ID:            migration.ID,
			NoTransaction: upDirectives[migrator.DirectiveNoTransaction],
			Irreversible:  downDirectives[migrator.DirectiveIrreversible],
		})
	}

	return report, nil
}

func writeValidationReport(out io.Writer, report *validationReport, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("encode report: %w", err)
		}

		return nil
	}

	// An empty directory is not an error here — validate reports what is there,
	// and "nothing" is a valid answer for a project that has not started yet.
	// It is Reconcile that treats an empty source as suspicious, because there
	// it would mean rolling the schema back to nothing.
	if report.Count == 0 {
		fmt.Fprintf(out, "no migrations found in %s\n", report.Path)

		return nil
	}

	for _, migration := range report.Migrations {
		fmt.Fprintf(out, "  %s%s\n", migration.ID, annotate(migration))
	}

	fmt.Fprintf(out, "\n%d migration(s) in %s: OK\n", report.Count, report.Path)

	return nil
}

// annotate renders the properties worth seeing at a glance: one costs
// atomicity, the other costs the ability to roll back.
func annotate(migration validatedMigration) string {
	switch {
	case migration.NoTransaction && migration.Irreversible:
		return "  [notransaction, irreversible]"
	case migration.NoTransaction:
		return "  [notransaction]"
	case migration.Irreversible:
		return "  [irreversible]"
	default:
		return ""
	}
}
