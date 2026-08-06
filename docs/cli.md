# CLI

```bash
go install github.com/eidon-go/pg-migrate/cmd/pg-migrate@latest
```

Or download a static binary from the
[releases page](https://github.com/eidon-go/pg-migrate/releases). It is built
with `CGO_ENABLED=0`, so it runs on `scratch` and `alpine` images unchanged.

## Commands

### new

```bash
pg-migrate new add_users_table
pg-migrate new "backfill user emails"
pg-migrate new add_email_index --notransaction
pg-migrate new drop_legacy_table --irreversible
```

Creates a `.up.sql`/`.down.sql` pair named `<utc-timestamp>_<slug>`, in
`--migration-path`. Along with `validate`, one of the two commands that never
touch the database.

The name is lowercased and anything that is not an ASCII letter or digit becomes
a single underscore, so `"backfill user emails"` and `backfill-user-emails` both
yield `backfill_user_emails`. The directory is created if missing; an existing
file is never overwritten.

| Flag | Effect |
|---|---|
| `--notransaction` | Writes the `notransaction` directive into **both** halves — an index built `CONCURRENTLY` is dropped concurrently too. |
| `--irreversible` | The down half becomes the `irreversible` directive and nothing else. |

Passing both marks the up half `notransaction` and leaves the down half
`irreversible`: there is no rollback script, so how it would have run does not
arise.

!!! note "Why the irreversible file has no comments"

    The loader rejects a script marked `irreversible` that still has a body —
    and a comment counts as a body. The generated file is therefore exactly one
    line. Adding a friendly explanation underneath would make the migration fail
    to load.

### validate

```bash
pg-migrate validate
pg-migrate validate --json
```

Parses every migration in `--migration-path` and reports what it found. **Needs
no database and no credentials**, so it belongs in a pre-commit hook and in CI:

```
  20260115104500_create_sessions
  20260210091500_sessions_expiry_index  [notransaction]

2 migration(s) in ./migrations: OK
```

It catches what would otherwise surface halfway through a deployment — a missing
`.down.sql`, a misspelled directive, an unclosed `StatementBegin` block, an empty
rollback that forgot the `irreversible` marker. Exits non-zero on the first
problem.

`[notransaction]` and `[irreversible]` are called out because they are the two
properties that change what a deployment risks: one gives up atomicity, the other
gives up rollback.

An empty directory is not an error — a project that has not written its first
migration is valid. It is `reconcile` that treats an empty source as suspicious,
because there it would mean rolling the schema back to nothing.

### up

```bash
pg-migrate up
```

Applies pending migrations forward. Extras in the database are left alone and
reported. The safe default for a deployment.

### reconcile

```bash
pg-migrate reconcile
pg-migrate reconcile -r    # --allow-rollback: roll back migrations missing from files
pg-migrate reconcile -i    # --allow-interleaved: apply out-of-order migrations
```

Makes the database match the files. Without `-r` it refuses when that would
require rolling something back.

### down

```bash
pg-migrate down 2      # the last two, most recent first
pg-migrate down all    # everything
```

Uses the rollback scripts stored in the database, so it works even when the
migration files are not present.

### plan

```bash
pg-migrate plan
pg-migrate plan --json
```

What a `reconcile` would do. Takes no lock, changes nothing, needs no DDL
privileges. The JSON includes `blocked` and `blocked_reason` — a plan listing
rollbacks does not mean the command would succeed.

### status

```bash
pg-migrate status
pg-migrate status --json
```

What is recorded as applied, in application order. For a failed migration this is
where you find the recorded error and the stored rollback script.

### baseline

```bash
pg-migrate baseline 20260115103000_create_users
```

Records every migration **up to and including** that ID as applied, without
running any of them. This is how you adopt pg-migrate on a database whose schema
already exists:

1. Write migrations describing the current schema.
2. `pg-migrate baseline <last-of-them>` — the ledger learns what is already there.
3. `pg-migrate up` — applies only what comes after.

Two guards keep it honest. It refuses unless the bookkeeping table is **empty**,
because adoption happens once; and the ID must exist in the source, so the
recorded set is the one you named.

!!! warning "Rollback of a baselined migration runs against a schema this tool never built"

    The `.down.sql` is stored exactly as a normal apply would store it, so a
    later `down` executes that script — against a schema somebody else created.
    Whether it fits is something only you can know.

### forget

```bash
pg-migrate forget 20260210091500_email_index
```

Drops a bookkeeping row **without running its rollback**. The escape hatch after
you have repaired the schema by hand.

## Configuration

Environment variables, overridable by flags.

### Connection

| Variable | Flag | Notes |
|---|---|---|
| `DATABASE_URL` | `--dsn` | Takes precedence over everything below. |
| `POSTGRES_DSN` | `--dsn` | Used when `DATABASE_URL` is unset. |
| `POSTGRES_HOST` | — | |
| `POSTGRES_PORT` | — | |
| `POSTGRES_USER` | — | |
| `POSTGRES_PASSWORD` | — | |
| `POSTGRES_DB` | — | |
| `POSTGRES_SSLMODE` | `--sslmode` | libpq `sslmode`. Managed Postgres usually needs `require`. |

Both DSN forms are accepted:

```bash
export DATABASE_URL="postgres://user:pass@host:5432/db?sslmode=require"
export DATABASE_URL="host=db.internal user=app dbname=app sslmode=require"
```

Unset discrete fields are omitted rather than sent empty, so the driver's
defaults and the standard `PG*` environment variables still apply.

### Behaviour

| Variable | Flag | Default |
|---|---|---|
| `MIGRATION_PATH` | `--migration-path` | — |
| `MIGRATION_LOCK_TIMEOUT` | `--lock-timeout` | `5m` |
| — | `--table` | `migrations` |
| — | `--schema` | the `search_path` |
| — | `--lock-id` | built-in constant |
| — | `--log-level` | `info` (`debug\|info\|warn\|error`) |
| — | `--log-format` | `text` (`text\|json`) |

The CLI's 5-minute lock default is deliberately longer than the library's 5s: a
CLI run is normally part of a deployment, where a busy lock means another
instance is mid-migration and waiting it out beats failing the rollout.

## In a container

```dockerfile
FROM scratch
COPY pg-migrate /pg-migrate
ENTRYPOINT ["/pg-migrate"]
```

```bash
docker run --rm \
  -e DATABASE_URL="postgres://user:pass@db:5432/app?sslmode=disable" \
  -e MIGRATION_PATH=/migrations \
  -v "$PWD/migrations:/migrations:ro" \
  ghcr.io/eidon-go/pg-migrate:latest up
```

## Exit codes

`0` on success, non-zero on failure. A failure caused by
`ErrFailedMigrations` is **terminal** — a pipeline should stop and surface it
rather than retry. See
[Troubleshooting](troubleshooting.md#a-migration-is-recorded-as-failed).

Use `--log-format json` when the output is going to a log aggregator.
