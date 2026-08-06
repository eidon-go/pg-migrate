// Command deploy-gate refuses a deployment whose migration plan looks unsafe,
// before anything is applied.
//
// Plan takes no advisory lock, writes nothing and needs no DDL privileges, so
// this is safe to run from CI against production with a read-only role.
//
//	export DATABASE_URL="postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"
//	go run ./examples/deploy-gate
//
// Exits non-zero when the deployment should not proceed.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	migrate "github.com/eidon-go/pg-migrate"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "deployment blocked: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("plan is safe — proceeding")
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

	db.SetMaxOpenConns(2)

	fsys := os.DirFS("examples/basic/migrations")

	analysis, err := migrate.Plan(ctx, db, fsys,
		migrate.WithTableName("example_basic_migrations"))
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}

	fmt.Printf("would apply:    %v\n", analysis.ToApply)
	fmt.Printf("would roll back: %v\n", analysis.ToRollback)

	// Blocked says a Reconcile with these options would refuse outright. Without
	// checking it, a plan listing rollbacks reads as "this will happen" when the
	// same call would in fact stop with an error.
	if analysis.Blocked {
		return fmt.Errorf("reconcile would refuse: %s", analysis.BlockedReason)
	}

	// A deployment that would undo schema is worth a human's attention even when
	// the library would allow it.
	if len(analysis.ToRollback) > 0 {
		return fmt.Errorf("plan would roll back %d migration(s): %v",
			len(analysis.ToRollback), analysis.ToRollback)
	}

	// Out-of-order migrations are fine when independent and broken when the later
	// one assumed the earlier one ran. Surface them rather than deciding here.
	if len(analysis.Interleaved) > 0 {
		return fmt.Errorf("plan contains out-of-order migrations: %v", analysis.Interleaved)
	}

	return nil
}
