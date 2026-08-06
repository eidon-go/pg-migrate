# pg-migrate

[![CI](https://github.com/eidon-go/pg-migrate/actions/workflows/ci.yml/badge.svg)](https://github.com/eidon-go/pg-migrate/actions/workflows/ci.yml)
[![CodeQL](https://github.com/eidon-go/pg-migrate/actions/workflows/codeql.yml/badge.svg)](https://github.com/eidon-go/pg-migrate/actions/workflows/codeql.yml)
[![codecov](https://codecov.io/gh/eidon-go/pg-migrate/branch/main/graph/badge.svg)](https://codecov.io/gh/eidon-go/pg-migrate)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/eidon-go/pg-migrate/badge)](https://scorecard.dev/viewer/?uri=github.com/eidon-go/pg-migrate)
[![Go Reference](https://pkg.go.dev/badge/github.com/eidon-go/pg-migrate.svg)](https://pkg.go.dev/github.com/eidon-go/pg-migrate)
[![Go Report Card](https://goreportcard.com/badge/github.com/eidon-go/pg-migrate)](https://goreportcard.com/report/github.com/eidon-go/pg-migrate)
[![Release](https://img.shields.io/github/v/release/eidon-go/pg-migrate?sort=semver)](https://github.com/eidon-go/pg-migrate/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/eidon-go/pg-migrate)](go.mod)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-12%20%E2%80%93%2018-336791?logo=postgresql&logoColor=white)](https://github.com/eidon-go/pg-migrate/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A small, dependency-light PostgreSQL migration library and CLI, driven by a
directory of SQL files and an advisory-lock-protected reconciliation algorithm.

📖 **[Documentation](https://eidon-go.github.io/pg-migrate/)**

## Why another migration tool

Three design decisions set it apart:

- **Rollback scripts come from the database, not the filesystem.** Applying a
  migration stores both scripts in the bookkeeping table, and rollback runs the
  stored one. Rolling back works when the checked-out branch no longer contains
  the migration at all — deploying an older branch, moving off a feature branch.
- **Failed migrations stop everything, deliberately.** A `notransaction` script
  that dies partway leaves the schema in a state no migration describes.
  Guessing how to recover from that is how data gets destroyed, so the library
  refuses to act and hands the decision to a human.
- **Ordering is a monotonic sequence, not `applied_at`.** Wall-clock timestamps
  tie, and they move backwards across a DST transition or when sessions disagree
  about the time zone. That order decides which migrations "the last N" acts on.

## Install

As a library:

```bash
go get github.com/eidon-go/pg-migrate
```

As a CLI:

```bash
go install github.com/eidon-go/pg-migrate/cmd/pg-migrate@latest
```

Or grab a static binary from the [releases page](https://github.com/eidon-go/pg-migrate/releases)
(linux/darwin, amd64/arm64).

## Quick start

```go
package main

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log"

	migrate "github.com/eidon-go/pg-migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	ctx := context.Background()

	db, err := sql.Open("pgx", "postgres://user:pass@localhost:5432/app?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// The advisory lock pins one connection for the whole run, so the pool
	// needs at least two.
	db.SetMaxOpenConns(4)

	// embed.FS keeps the directory in the path; fs.Sub strips it.
	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		log.Fatal(err)
	}

	result, err := migrate.Up(ctx, db, fsys)
	if err != nil {
		log.Fatalf("migrate: %v (applied %v)", err, result.Applied)
	}

	log.Printf("applied: %v", result.Applied)
}
```

More in [`examples/`](examples/).

## Migration files

```bash
pg-migrate new create_users
```

Each migration is a pair of files sharing a base name, which becomes the ID:

```
migrations/
  20260115103000_create_users.up.sql
  20260115103000_create_users.down.sql
  20260116084500_add_email_index.up.sql
  20260116084500_add_email_index.down.sql
```

IDs are compared **lexicographically, never numerically**, which is why `new`
prefixes them with a fixed-width UTC timestamp: sequence numbers collide between
branches and break once they outgrow their padding. Hand-named files work too —
see [Why timestamps](https://eidon-go.github.io/pg-migrate/migrations/#why-timestamps).

Both halves are required — a missing `.down.sql` is an error, not an empty
rollback.

### Directives

A file may begin with a single directive line, which must be the very first line:

```sql
-- +migrate notransaction
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
```

| Directive | Meaning |
|---|---|
| `notransaction` | Run statement-by-statement outside a transaction, for statements PostgreSQL refuses inside one. A partial failure leaves earlier statements committed. |
| `irreversible` | Belongs in a deliberately empty `.down.sql`. A rollback that would touch it is refused with `ErrIrreversible` before anything runs. |

Unknown, misspelled or wrongly-cased directives are rejected rather than ignored.

Where a statement boundary cannot be inferred, force it:

```sql
-- +migrate StatementBegin
CREATE FUNCTION f() RETURNS void AS $$
BEGIN
    SELECT 1; -- this semicolon is not a boundary
END;
$$ LANGUAGE plpgsql;
-- +migrate StatementEnd
```

## API

| Function | Effect |
|---|---|
| `Up` | Applies pending migrations forward. Extras in the DB are left alone. |
| `Reconcile` | Makes the database match the files, rolling back extras when permitted. |
| `Down` / `DownAll` | Rolls back the last N / every applied migration. |
| `Plan` | Reports what a `Reconcile` would do, including whether it would refuse. Reads only. |
| `Status` | Lists what is recorded as applied. Reads only. |
| `Baseline` | Records migrations as applied without running them, to adopt an existing database. |
| `Forget` | Drops a bookkeeping row without running its rollback — the manual escape hatch. |

Options: `WithRollback`, `WithInterleaved`, `WithLockTimeout`, `WithLockID`,
`WithAllowEmptySource`, `WithTableName`, `WithSchema`, `WithLogger`.

Sentinel errors for `errors.Is`: `ErrDivergence`, `ErrInterleaved`,
`ErrFailedMigrations`, `ErrIrreversible`, `ErrEmptySource`, `ErrUnrecorded`,
`ErrLockTimeout`, `ErrPoolTooSmall`, `ErrAlreadyRecorded`, `ErrBaselineNotFound`.

Full reference on [pkg.go.dev](https://pkg.go.dev/github.com/eidon-go/pg-migrate).

## CLI

```bash
pg-migrate new add_users_table     # scaffold a timestamped up/down pair
pg-migrate validate                # check the files; no database needed
pg-migrate up                      # apply pending migrations
pg-migrate reconcile               # make the DB match the files
pg-migrate reconcile -r            # ...allowing rollback of extras
pg-migrate down 2                  # roll back the last two
pg-migrate down all                # roll back everything
pg-migrate plan --json             # what reconcile would do, no changes
pg-migrate status --json           # what is recorded as applied
pg-migrate baseline 20260115103000_create_users  # adopt an existing database
pg-migrate forget 20260210091500_bad_index   # drop a row without running its rollback
```

Configuration comes from environment variables, overridable by flags:

| Variable | Flag | Meaning |
|---|---|---|
| `DATABASE_URL` / `POSTGRES_DSN` | `--dsn` | Connection string; takes precedence over the discrete variables. |
| `POSTGRES_HOST` `POSTGRES_PORT` `POSTGRES_USER` `POSTGRES_PASSWORD` `POSTGRES_DB` | — | Discrete connection fields. |
| `POSTGRES_SSLMODE` | `--sslmode` | libpq `sslmode`. |
| `MIGRATION_PATH` | `--migration-path` | Directory holding the `.sql` files. |
| `MIGRATION_LOCK_TIMEOUT` | `--lock-timeout` | Advisory lock wait; CLI default is `5m`. |
| — | `--table` `--schema` | Bookkeeping table name and schema. |
| — | `--lock-id` | Advisory lock ID. |
| — | `--log-level` `--log-format` | `debug\|info\|warn\|error`, `text\|json`. |

## Requirements

- Go 1.25+
- PostgreSQL 12+
- A `*sql.DB` allowing **at least 2 open connections** — the advisory lock pins
  one for the whole run. `SetMaxOpenConns(1)` returns `ErrPoolTooSmall`.

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).
Commits follow [Conventional Commits](https://www.conventionalcommits.org/);
the changelog is generated from them.

## License

[MIT](LICENSE)
