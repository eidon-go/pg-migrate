# Troubleshooting

## A migration is recorded as failed

**Symptom:** every operation returns `ErrFailedMigrations` and refuses to do
anything.

This only happens to a `notransaction` migration that died partway. Its earlier
statements are committed, so the schema matches no migration — and the library
will not guess its way out of that.

### 1. Find out what happened

```bash
migrate status --json | jq '.[] | select(.failed)'
```

```json
{
  "id": "0012_users_email_index",
  "applied_at": "2026-08-06T10:14:22Z",
  "error": "statement 2 failed: canceling statement due to statement timeout",
  "failed": true,
  "down_script": "-- +migrate notransaction\nDROP INDEX CONCURRENTLY IF EXISTS idx_users_email;\n"
}
```

The `error` says which statement died. The `down_script` is what a rollback
*would* have run — read it, because it tells you what the migration intended to
undo.

### 2. Bring the schema to a known state, by hand

Work out how far the script got and decide where you want to be — fully applied,
or fully undone. Then get there with `psql`. There is no shortcut here, and that
is deliberate: only you know what state the migration left behind.

For the example above:

```sql
-- The CONCURRENTLY build failed partway; drop the invalid index.
DROP INDEX CONCURRENTLY IF EXISTS idx_users_email;
```

### 3. Clear the row

```bash
migrate forget 0012_users_email_index
```

This touches only the ledger. Normal operation resumes on the next run.

!!! danger "Never retry automatically"

    `ErrFailedMigrations` is terminal, not transient. A pipeline that retries it
    will loop forever, and a pipeline that "recovers" by rolling back will
    destroy the successful migrations that came after.

## ErrPoolTooSmall

```
the pool allows only one open connection
```

The advisory lock pins one connection for the whole run. With a
single-connection pool, every other query would wait forever on a lock that
cannot be released.

```go
db.SetMaxOpenConns(4) // or 0 for unlimited; anything >= 2
```

## ErrLockTimeout

Another migration is in progress. This one **is** safe to retry.

```go
migrate.WithLockTimeout(5 * time.Minute) // or 0 to wait indefinitely
```

If nothing else should be migrating, look for a stranded lock:

```sql
SELECT pid, granted, objid, query, state
FROM pg_locks
JOIN pg_stat_activity USING (pid)
WHERE locktype = 'advisory';
```

A session holding it with `state = 'idle'` is a process that died without
releasing. Terminating that backend releases the lock:

```sql
SELECT pg_terminate_backend(<pid>);
```

## ErrEmptySource

```
migration source is empty
```

The source has no migrations but the database has some — which would mean rolling
the schema back to nothing. Almost always one of:

- **`embed.FS` without `fs.Sub`.** The embedded FS keeps the directory in the
  path, so the library sees one directory entry and zero migrations.

    ```go
    //go:embed migrations/*.sql
    var migrationsFS embed.FS

    fsys, err := fs.Sub(migrationsFS, "migrations") // ← this
    ```

- **A wrong path that happens to exist.** `os.DirFS("migration")` instead of
  `os.DirFS("migrations")`.

If the empty source is genuinely intended, pass `WithAllowEmptySource()`.

## ErrDivergence

The database has migrations your files do not. Normal when deploying an older
branch.

- Deploying older code on purpose? Use `Up` — it leaves extras alone.
- Really want them gone? `Reconcile` with `WithRollback()`.
- Neither? Someone applied a migration from a branch that never merged. Find out
  which before doing anything: `migrate plan --json | jq .to_rollback`.

## ErrInterleaved

Your files contain a migration whose ID sorts **before** the latest applied one —
it would go in out of order. Two developers branching from the same point and
merging in the other order.

Harmless when the migrations are independent, broken when the later one assumed
the earlier one ran. Decide, then pass `WithInterleaved()` if it is fine.

## ErrUnrecorded

The schema changed but the bookkeeping row could not be written to match. The
database and the ledger now disagree.

This needs a human. Compare the actual schema against what the migration
intended, then either finish the job and let the row be written on a retry, or
undo the change by hand. `migrate status` shows what the ledger currently
believes.

## Editing a migration file changes nothing

Working as designed. Rollback scripts are read from the database, and an applied
migration's row is never rewritten from the file.

To change applied schema, write a new migration. To fix a migration that was just
applied in development, roll it back first (`migrate down 1`), then edit and
re-apply.

## Migrations run but the table is not found

The bookkeeping table is unqualified by default and resolved through the
connection's `search_path`. If your application and your migration runner connect
with different roles, they may resolve it to different schemas.

Pin it explicitly:

```go
migrate.WithSchema("public")
```

## Getting more detail

```bash
migrate up --log-level debug --log-format json
```

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level: slog.LevelDebug,
}))

migrate.Up(ctx, db, fsys, migrate.WithLogger(logger))
```

Still stuck? [Open an issue](https://github.com/eidon-go/pg-migrate/issues/new/choose)
with the output of `migrate status --json` and the migration pair that
reproduces it.
