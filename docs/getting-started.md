# Getting started

## Install

=== "Library"

    ```bash
    go get github.com/eidon-go/pg-migrate
    ```

=== "CLI (go install)"

    ```bash
    go install github.com/eidon-go/pg-migrate/cmd/migrate@latest
    ```

=== "CLI (binary)"

    Download a static binary from the
    [releases page](https://github.com/eidon-go/pg-migrate/releases) —
    linux/darwin/windows, amd64/arm64.

    ```bash
    tar -xzf pg-migrate_0.1.0_linux_amd64.tar.gz
    sudo mv migrate /usr/local/bin/
    ```

## Write your first migration

```bash
export MIGRATION_PATH=./migrations
migrate new create_users
```

```
migrations/20260115103000_create_users.up.sql
migrations/20260115103000_create_users.down.sql
```

Migrations come in pairs sharing a base name, which becomes the migration ID.
Fill both halves in:

```sql title="migrations/20260115103000_create_users.up.sql"
CREATE TABLE users (
    id    BIGSERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE
);
```

```sql title="migrations/20260115103000_create_users.down.sql"
DROP TABLE users;
```

!!! note "Why the timestamp"

    IDs are compared **lexicographically**, never numerically — `0002` sorts
    before `0010`, but `2` sorts after `10`. A fixed-width UTC timestamp keeps
    order and collisions from ever becoming your problem. Naming files by hand
    works too; see [Why timestamps](migrations.md#why-timestamps).

## Run it from Go

```go
package main

import (
	"context"
	"database/sql"
	"log"
	"os"

	migrate "github.com/eidon-go/pg-migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	ctx := context.Background()

	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// The advisory lock pins one connection for the whole run.
	db.SetMaxOpenConns(4)

	result, err := migrate.Up(ctx, db, os.DirFS("migrations"))
	if err != nil {
		log.Fatalf("migrate: %v (applied %v)", err, result.Applied)
	}

	log.Printf("applied %d migrations: %v", len(result.Applied), result.Applied)
}
```

!!! danger "At least two connections"

    `SetMaxOpenConns(1)` returns `ErrPoolTooSmall`. The lock holds one
    connection for the whole run; with a single-connection pool every other
    query would wait forever on a lock that is never released.

## Run it from the CLI

```bash
export DATABASE_URL="postgres://user:pass@localhost:5432/app?sslmode=disable"
export MIGRATION_PATH=./migrations

migrate up
```

Look before you leap:

```bash
migrate plan --json     # what a reconcile would do; changes nothing
migrate status --json   # what is recorded as applied
```

## Choosing between Up and Reconcile

This is the decision that matters most, and it comes down to what should happen
to migrations that are in the database but missing from your files.

| | `Up` | `Reconcile` |
|---|---|---|
| Applies pending migrations | ✅ | ✅ |
| Extras present in the DB but not in files | Left alone, reported in `ExtrasLeft` | Refused with `ErrDivergence`, or rolled back with `WithRollback()` |
| Typical use | Production deploys | Development, CI, ephemeral environments |

`Up` is the conservative default: it never removes anything, so a deploy from an
older branch cannot silently drop schema that a newer release added.

`Reconcile` makes the database match the files exactly. Combined with
`WithRollback()` it will undo migrations the files no longer contain — useful
when switching branches locally, alarming in production.

```go
// Development: make the database match the branch, whatever that takes.
_, err := migrate.Reconcile(ctx, db, fsys, migrate.WithRollback())
```

## Embedding migrations in the binary

Shipping a single binary with no `migrations/` directory alongside it:

```go
import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// embed.FS keeps the directory in the path — fs.Sub strips it, otherwise the
// library sees one directory entry and no migrations at all.
fsys, err := fs.Sub(migrationsFS, "migrations")
if err != nil {
	return err
}

result, err := migrate.Up(ctx, db, fsys)
```

!!! tip "An empty source is caught for you"

    Forgetting `fs.Sub` is a common mistake, and it looks exactly like "roll
    everything back". `Reconcile` refuses with `ErrEmptySource` rather than
    acting on it. Pass `WithAllowEmptySource()` if you really mean it.

## Next steps

- [Migration files](migrations.md) — directives, `CREATE INDEX CONCURRENTLY`,
  statement splitting.
- [Concepts](concepts.md) — how locking and reconciliation actually work.
- [Recipes](recipes.md) — Kubernetes, testing, CI.
