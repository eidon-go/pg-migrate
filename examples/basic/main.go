// Command basic applies migrations from a directory on disk.
//
// Run it with a PostgreSQL available:
//
//	export DATABASE_URL="postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"
//	go run ./examples/basic
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	migrate "github.com/eidon-go/pg-migrate"
)

func main() {
	// os.Exit skips deferred calls, so the real work lives one frame down where
	// `defer stop()` is guaranteed to run.
	os.Exit(realMain())
}

func realMain() int {
	// Ctrl-C cancels the run. Bookkeeping writes are deliberately detached from
	// this context, so a cancelled run still records what it did.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx); err != nil {
		log.Printf("error: %v", err)

		return 1
	}

	return 0
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

	// The advisory lock pins one connection for the whole run, so a
	// single-connection pool would deadlock. The library rejects it outright
	// with ErrPoolTooSmall.
	db.SetMaxOpenConns(4)

	if pingErr := db.PingContext(ctx); pingErr != nil {
		return fmt.Errorf("ping database: %w", pingErr)
	}

	// Migrations live in a plain directory next to this file.
	fsys := os.DirFS("examples/basic/migrations")

	result, err := migrate.Up(ctx, db, fsys,
		migrate.WithTableName("example_basic_migrations"),
		migrate.WithLockTimeout(30*time.Second),
	)

	// Read the result even on error: it holds the prefix that completed before
	// the failure, which is the useful part of a deployment log.
	//
	// It is nil only when the call was rejected before anything ran (bad
	// options, a nil *sql.DB), so guard before reaching into it.
	if result != nil {
		if len(result.Applied) > 0 {
			fmt.Printf("applied: %v\n", result.Applied)
		}

		if len(result.ExtrasLeft) > 0 {
			fmt.Printf("left alone (in the database, missing from files): %v\n", result.ExtrasLeft)
		}
	}

	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if len(result.Applied) == 0 {
		fmt.Println("nothing to do — the database is up to date")
	}

	return reportStatus(ctx, db)
}

func reportStatus(ctx context.Context, db *sql.DB) error {
	// Status reads only: no lock, no DDL privileges needed.
	applied, err := migrate.Status(ctx, db,
		migrate.WithTableName("example_basic_migrations"))
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}

	fmt.Printf("\n%d migration(s) recorded:\n", len(applied))

	for _, m := range applied {
		state := "ok"
		if m.Failed {
			state = "FAILED: " + m.Error
		}

		fmt.Printf("  %-28s %s  %s\n", m.ID, m.AppliedAt.Format(time.RFC3339), state)
	}

	return nil
}
