# pg-migrate

A small, dependency-light PostgreSQL migration library and CLI for Go, driven by
a directory of SQL files and an advisory-lock-protected reconciliation algorithm.

```bash
go get github.com/eidon-go/pg-migrate
```

```go
result, err := migrate.Up(ctx, db, os.DirFS("migrations"))
```

## What makes it different

Most migration tools agree on the basics — a table of applied versions, a folder
of SQL files, a lock. Three decisions here are worth knowing about before you
adopt it, because they are the ones you will notice.

### Rollback scripts come from the database

Applying a migration stores **both** scripts in the bookkeeping table, and a
rollback runs the stored `.down.sql` rather than the file on disk.

This is what makes rollback work when the checked-out branch no longer contains
the migration at all — deploying an older release, moving off a feature branch,
rolling back a deployment whose code is already gone. The trade-off: editing the
file of an already-applied migration has no effect on anything.

### Failed migrations stop everything

A `notransaction` script that dies partway records its error in the bookkeeping
table. While that row exists, `Up`, `Down` and `Reconcile` all refuse to act and
return `ErrFailedMigrations`.

That refusal is the feature. The schema is in a state no migration describes:
running the rollback means running it against a state it was never written for,
and rolling back what came after means destroying successful work. Both are
guesses. Resolving it is a human decision — see
[Troubleshooting](troubleshooting.md#a-migration-is-recorded-as-failed).

### Ordering is a sequence, not a timestamp

Migrations are ordered by a monotonic sequence column, never by `applied_at`.
Wall-clock timestamps tie, and they move backwards across a DST transition or
when sessions disagree about the time zone. That order is what "roll back the
last two" acts on, so getting it wrong is not cosmetic.

## Where to go next

<div class="grid cards" markdown>

- **[Getting started](getting-started.md)** — install, first migration, wiring it
  into an application.
- **[Migration files](migrations.md)** — naming, directives, statement
  splitting.
- **[Concepts](concepts.md)** — the locking model, reconciliation, the
  bookkeeping table.
- **[Library API](api.md)** — every entry point and option.
- **[CLI](cli.md)** — commands, flags, environment variables.
- **[Recipes](recipes.md)** — embed.FS, Kubernetes init containers, testing.
- **[Database privileges](permissions.md)** — what a dedicated migration role
  actually needs.
- **[Troubleshooting](troubleshooting.md)** — what each error means and how to
  resolve it.

</div>

## Requirements

- Go 1.25+
- PostgreSQL 12+
- A `*sql.DB` allowing at least **2** open connections — the advisory lock pins
  one for the whole run.

## License

[MIT](https://github.com/eidon-go/pg-migrate/blob/main/LICENSE)
