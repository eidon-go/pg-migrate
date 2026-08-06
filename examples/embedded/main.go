// Command embedded ships its migrations inside the binary with embed.FS, so
// there is no migrations/ directory to deploy alongside it.
//
//	export DATABASE_URL="postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"
//	go run ./examples/embedded
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	migrate "github.com/eidon-go/pg-migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const tableName = "example_embedded_migrations"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func run(ctx context.Context) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(4)

	// embed.FS keeps the directory in every path, so the library would see a
	// single directory entry and zero migrations. fs.Sub strips it.
	//
	// Forgetting this is the usual cause of ErrEmptySource — which the library
	// refuses to act on precisely because it is indistinguishable from
	// "roll everything back".
	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("sub filesystem: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	result, err := migrate.Up(ctx, db, fsys,
		migrate.WithTableName(tableName),
		migrate.WithLogger(logger),
	)
	if err != nil {
		// Result is nil only when the call was rejected before anything ran
		// (bad options, a nil *sql.DB); otherwise it holds the prefix that
		// completed before the failure.
		var applied []string
		if result != nil {
			applied = result.Applied
		}

		// Distinguish the terminal failure from the retryable ones: a caller
		// that retries ErrFailedMigrations will loop forever.
		switch {
		case errors.Is(err, migrate.ErrFailedMigrations):
			return fmt.Errorf("manual intervention required, do not retry: %w", err)

		case errors.Is(err, migrate.ErrLockTimeout):
			return fmt.Errorf("another migration is in progress, retry later: %w", err)

		default:
			return fmt.Errorf("apply migrations (applied %v): %w", applied, err)
		}
	}

	fmt.Printf("applied: %v\n", result.Applied)

	return nil
}
