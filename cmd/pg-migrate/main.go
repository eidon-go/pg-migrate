package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/internal/dbconn"
)

// version is set via ldflags during build.
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("Command execution failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := NewConfig()
	if err != nil {
		return err
	}

	var (
		logLevel  string
		logFormat string
	)

	rootCmd := &cobra.Command{
		Use:           "pg-migrate",
		Short:         "Database migration tool",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return setupLogging(logLevel, logFormat)
		},
	}

	// Global / Common flags
	var (
		lockTimeoutStr string
		lockID         int64
		migrationPath  string
		tableName      string
		tableSchema    string
		dsn            string
		sslMode        string
		jsonOutput     bool
	)

	rootCmd.PersistentFlags().StringVar(&lockTimeoutStr, "lock-timeout", "",
		"Timeout for acquiring advisory lock (e.g. 5s, 5m). Overrides MIGRATION_LOCK_TIMEOUT.")
	rootCmd.PersistentFlags().Int64Var(&lockID, "lock-id", 0,
		"Custom pg_advisory_lock ID")
	rootCmd.PersistentFlags().StringVar(&migrationPath, "migration-path", "",
		"Path to migration files. Overrides MIGRATION_PATH.")
	rootCmd.PersistentFlags().StringVar(&tableName, "table", "",
		`Bookkeeping table name (default "migrations")`)
	rootCmd.PersistentFlags().StringVar(&tableSchema, "schema", "",
		"Schema for the bookkeeping table (default: resolved via search_path)")
	rootCmd.PersistentFlags().StringVar(&dsn, "dsn", "",
		"Postgres connection string, URL or keyword/value form. Overrides DATABASE_URL and the discrete POSTGRES_* variables.")
	rootCmd.PersistentFlags().StringVar(&sslMode, "sslmode", "",
		"Postgres sslmode: disable, prefer, require, verify-ca, verify-full. "+
			"Overrides POSTGRES_SSLMODE; unset means the driver default (prefer).")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info",
		"Log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "text",
		"Log format: text or json")

	type optsOverrides struct {
		allowRollback    bool
		allowInterleaved bool
	}

	// buildOptions assembles the library options from flags and environment.
	// Commands that read migration files call resolvePath separately, so that
	// the ones that don't — down, status — do not demand a path they never use.
	buildOptions := func(overrides optsOverrides) ([]migrate.Option, error) {
		timeout := cfg.LockTimeout

		if lockTimeoutStr != "" {
			parsed, err := time.ParseDuration(lockTimeoutStr)
			if err != nil {
				return nil, fmt.Errorf("invalid --lock-timeout %q: %w", lockTimeoutStr, err)
			}

			timeout = parsed
		}

		opts := []migrate.Option{migrate.WithLockTimeout(timeout)}
		if lockID != 0 {
			opts = append(opts, migrate.WithLockID(lockID))
		}

		if tableName != "" {
			opts = append(opts, migrate.WithTableName(tableName))
		}

		if tableSchema != "" {
			opts = append(opts, migrate.WithSchema(tableSchema))
		}

		if overrides.allowRollback {
			opts = append(opts, migrate.WithRollback())
		}

		if overrides.allowInterleaved {
			opts = append(opts, migrate.WithInterleaved())
		}

		return opts, nil
	}

	resolvePath := func() (string, error) {
		path := cfg.MigrationPath
		if migrationPath != "" {
			path = migrationPath
		}

		if path == "" {
			return "", errors.New(
				"migration path is required (use --migration-path flag or MIGRATION_PATH env var)",
			)
		}

		return path, nil
	}

	connectDB := func(ctx context.Context) (*sql.DB, error) {
		pgCfg := cfg.Postgres
		if dsn != "" {
			pgCfg.DSN = dsn
		}

		if sslMode != "" {
			pgCfg.SSLMode = sslMode
		}

		postgresDB, err := dbconn.OpenSQLDB(pgCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to open database connection: %w", err)
		}

		if err := postgresDB.PingContext(ctx); err != nil {
			postgresDB.Close()

			return nil, fmt.Errorf("failed to connect to database (ping): %w", err)
		}

		return postgresDB, nil
	}

	// addCLIHint wraps sentinel errors from the library with CLI flag suggestions.
	addCLIHint := func(err error) error {
		switch {
		case errors.Is(err, migrate.ErrDivergence):
			return fmt.Errorf("%w (re-run with --allow-rollback to roll back missing migrations)", err)
		case errors.Is(err, migrate.ErrInterleaved):
			return fmt.Errorf("%w (re-run with --allow-interleaved to apply out-of-order migrations)", err)
		case errors.Is(err, migrate.ErrFailedMigrations):
			return fmt.Errorf("%w (see `migrate status` for the recorded error)", err)
		}

		return err
	}

	// 1. reconcile command
	var (
		allowRollback    bool
		allowInterleaved bool
	)

	reconcileCmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile database schema with migration files",
		Long:  `Reconcile database schema with migration files. Outputs operational logs to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := buildOptions(optsOverrides{
				allowRollback:    allowRollback,
				allowInterleaved: allowInterleaved,
			})
			if err != nil {
				return err
			}

			path, err := resolvePath()
			if err != nil {
				return err
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			slog.Info("Running reconcile", "path", path,
				"allow_rollback", allowRollback,
				"allow_interleaved", allowInterleaved)

			result, err := migrate.Reconcile(cmd.Context(), dbConn, os.DirFS(path), opts...)
			if err != nil {
				return addCLIHint(err)
			}

			slog.Info("Reconcile completed successfully",
				"applied", len(result.Applied),
				"rolled_back", len(result.RolledBack),
				"interleaved", len(result.Interleaved))

			return nil
		},
	}
	reconcileCmd.Flags().BoolVarP(&allowRollback, "allow-rollback", "r", false,
		"Allow automatic rollback of migrations missing from files")
	reconcileCmd.Flags().BoolVarP(&allowInterleaved, "allow-interleaved", "i", false,
		"Allow applying out-of-order (interleaved) migrations")
	rootCmd.AddCommand(reconcileCmd)

	// 2. up command
	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations forward",
		Long:  `Apply pending migrations forward. Outputs operational logs to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := buildOptions(optsOverrides{})
			if err != nil {
				return err
			}

			path, err := resolvePath()
			if err != nil {
				return err
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			slog.Info("Running up", "path", path)

			result, err := migrate.Up(cmd.Context(), dbConn, os.DirFS(path), opts...)
			if err != nil {
				return addCLIHint(err)
			}

			slog.Info("Migrations applied forward successfully",
				"applied", len(result.Applied),
				"extras_left", len(result.ExtrasLeft),
				"interleaved", len(result.Interleaved))

			return nil
		},
	}
	rootCmd.AddCommand(upCmd)

	// 3. down command
	downCmd := &cobra.Command{
		Use:   "down [count|all]",
		Short: "Rollback applied migrations",
		Long: `Rollback applied migrations. Rollback scripts are read from the database, ` +
			`so no migration path is needed. Outputs operational logs to stderr.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOptions(optsOverrides{})
			if err != nil {
				return err
			}

			countArg := args[0]
			rollbackAll := countArg == "all"

			var count int

			if rollbackAll {
				slog.Info("Running down (rollback all migrations)")
			} else {
				count, err = strconv.Atoi(countArg)
				if err != nil || count <= 0 {
					return fmt.Errorf("invalid count argument: %s (must be positive integer or 'all')", countArg)
				}

				slog.Info("Running down", "count", count)
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			var result *migrate.Result
			if rollbackAll {
				result, err = migrate.DownAll(cmd.Context(), dbConn, opts...)
			} else {
				result, err = migrate.Down(cmd.Context(), dbConn, count, opts...)
			}

			if err != nil {
				return addCLIHint(err)
			}

			slog.Info("Migrations rolled back successfully", "rolled_back", len(result.RolledBack))

			return nil
		},
	}
	rootCmd.AddCommand(downCmd)

	// 4. plan command
	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Show migrations that would be applied and rolled back",
		Long: `Show migrations that would be applied and rolled back. ` +
			`This command outputs directly to stdout, while other commands output logs to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := buildOptions(optsOverrides{})
			if err != nil {
				return err
			}

			path, err := resolvePath()
			if err != nil {
				return err
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			analysis, err := migrate.Plan(cmd.Context(), dbConn, os.DirFS(path), opts...)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(analysis)
			}

			fmt.Println("Migration execution plan:")

			if len(analysis.ToRollback) > 0 {
				fmt.Println("To Rollback:")

				for _, m := range analysis.ToRollback {
					fmt.Printf("  - %s (DOWN)\n", m)
				}
			} else {
				fmt.Println("To Rollback: None")
			}

			if len(analysis.ToApply) > 0 {
				fmt.Println("To Apply:")

				for _, m := range analysis.ToApply {
					fmt.Printf("  - %s (UP)\n", m)
				}
			} else {
				fmt.Println("To Apply: None")
			}

			if len(analysis.Interleaved) > 0 {
				fmt.Println("Interleaved (Out-of-Order):")

				for _, m := range analysis.Interleaved {
					fmt.Printf("  - %s (Interleaved)\n", m)
				}
			}

			return nil
		},
	}
	planCmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the plan as JSON")
	rootCmd.AddCommand(planCmd)

	// 5. status command
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show migrations recorded in the database",
		Long: `Show migrations recorded in the database, in the order they were applied. ` +
			`Reads only the database — no migration path, no lock. ` +
			`Outputs to stdout, while other commands output logs to stderr.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := buildOptions(optsOverrides{})
			if err != nil {
				return err
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			applied, err := migrate.Status(cmd.Context(), dbConn, opts...)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(applied)
			}

			if len(applied) == 0 {
				fmt.Println("No migrations applied.")

				return nil
			}

			fmt.Printf("Applied migrations (%d):\n", len(applied))

			for _, appliedMigration := range applied {
				if appliedMigration.Failed {
					fmt.Printf(
						"  ✗ %s  %s  FAILED: %s\n",
						appliedMigration.ID,
						appliedMigration.AppliedAt.Format(time.RFC3339),
						appliedMigration.Error,
					)

					continue
				}

				fmt.Printf("  ✓ %s  %s\n", appliedMigration.ID, appliedMigration.AppliedAt.Format(time.RFC3339))
			}

			return nil
		},
	}
	statusCmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the status as JSON")
	rootCmd.AddCommand(statusCmd)

	// 6. forget command
	forgetCmd := &cobra.Command{
		Use:   "forget <migration-id>",
		Short: "Drop a migration's bookkeeping row without running its rollback",
		Long: `Drop a migration's bookkeeping row without running its rollback script.

This is the escape hatch after a migration failed partway: the tool refuses to
act while a failed migration is recorded, and this clears the record once you
have brought the schema to a known state yourself. It changes nothing but the
bookkeeping table. Run 'migrate status' first to see the recorded error and the
stored rollback script.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := buildOptions(optsOverrides{})
			if err != nil {
				return err
			}

			dbConn, err := connectDB(cmd.Context())
			if err != nil {
				return err
			}
			defer dbConn.Close()

			id := args[0]
			if err := migrate.Forget(cmd.Context(), dbConn, id, opts...); err != nil {
				return err
			}

			slog.Warn("Removed the migration's bookkeeping row; its rollback script was NOT run", "id", id)

			return nil
		},
	}
	rootCmd.AddCommand(forgetCmd)

	// 7. new and validate — both work on files alone, never touching the database.
	rootCmd.AddCommand(newCommand(resolvePath))
	rootCmd.AddCommand(validateCommand(resolvePath))

	if err := rootCmd.Execute(); err != nil {
		return fmt.Errorf("execute command: %w", err)
	}

	return nil
}

// setupLogging installs the process-wide slog default, which is what the library
// logs through unless a caller passes WithLogger.
func setupLogging(level, format string) error {
	var slogLevel slog.Level

	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return fmt.Errorf("invalid --log-level %q: expected debug, info, warn or error", level)
	}

	// Logs go to stderr so that stdout carries only command output, which keeps
	// `plan --json` pipeable.
	opts := &slog.HandlerOptions{Level: slogLevel}

	var handler slog.Handler

	switch strings.ToLower(format) {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("invalid --log-format %q: expected text or json", format)
	}

	slog.SetDefault(slog.New(handler))

	return nil
}

// printJSON writes v to stdout as indented JSON.
func printJSON(v any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")

	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}

	return nil
}
