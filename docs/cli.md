# CLI

```bash
go install github.com/eidon-go/pg-migrate/cmd/migrate@latest
```

Or download a static binary from the
[releases page](https://github.com/eidon-go/pg-migrate/releases). It is built
with `CGO_ENABLED=0`, so it runs on `scratch` and `alpine` images unchanged.

## Commands

### up

```bash
migrate up
```

Applies pending migrations forward. Extras in the database are left alone and
reported. The safe default for a deployment.

### reconcile

```bash
migrate reconcile
migrate reconcile -r    # --allow-rollback: roll back migrations missing from files
migrate reconcile -i    # --allow-interleaved: apply out-of-order migrations
```

Makes the database match the files. Without `-r` it refuses when that would
require rolling something back.

### down

```bash
migrate down 2      # the last two, most recent first
migrate down all    # everything
```

Uses the rollback scripts stored in the database, so it works even when the
migration files are not present.

### plan

```bash
migrate plan
migrate plan --json
```

What a `reconcile` would do. Takes no lock, changes nothing, needs no DDL
privileges. The JSON includes `blocked` and `blocked_reason` — a plan listing
rollbacks does not mean the command would succeed.

### status

```bash
migrate status
migrate status --json
```

What is recorded as applied, in application order. For a failed migration this is
where you find the recorded error and the stored rollback script.

### forget

```bash
migrate forget 0007_email_index
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
COPY migrate /migrate
ENTRYPOINT ["/migrate"]
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
