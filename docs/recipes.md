# Recipes

## Migrate on application startup

The common pattern for a service that owns its schema. Keep it separate from the
request path and give it its own timeout.

```go
func runMigrations(ctx context.Context, db *sql.DB, fsys fs.FS, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	result, err := migrate.Up(ctx, db, fsys, migrate.WithLogger(logger))
	if err != nil {
		// The prefix that completed is the useful part of a failure log. Result
		// is nil only when the call was rejected before anything ran.
		var applied []string
		if result != nil {
			applied = result.Applied
		}

		logger.ErrorContext(ctx, "migration failed",
			"applied", applied,
			"error", err)

		return fmt.Errorf("run migrations: %w", err)
	}

	logger.InfoContext(ctx, "migrations applied",
		"count", len(result.Applied),
		"applied", result.Applied,
		"extras_left", result.ExtrasLeft)

	return nil
}
```

## Kubernetes init container

Running migrations in an init container keeps them out of the application
process, so N replicas do not all try to migrate. The advisory lock makes it safe
even if they do.

```yaml
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: migrate
          image: ghcr.io/eidon-go/pg-migrate:v0.1.0
          args: ["up"]
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef: { name: app-db, key: url }
            - name: MIGRATION_PATH
              value: /migrations
            # Longer than the pod's startup budget: a busy lock means another
            # rollout is mid-migration, and waiting beats failing.
            - name: MIGRATION_LOCK_TIMEOUT
              value: "10m"
          volumeMounts:
            - { name: migrations, mountPath: /migrations, readOnly: true }
      containers:
        - name: app
          image: ghcr.io/acme/app:v1.2.3
      volumes:
        - name: migrations
          configMap: { name: app-migrations }
```

!!! warning "Do not retry ErrFailedMigrations"

    Kubernetes restarts a failed init container by default. When the failure is
    `ErrFailedMigrations`, restarting will never help — the state needs a human.
    Consider `restartPolicy: Never` on a migration `Job` instead, and alert on it.

## Testing against a real database

The library's own integration tests use this shape: each test creates and drops
its own database, so they run in parallel without interfering.

```go
func TestUserRepository(t *testing.T) {
	t.Parallel()

	db := newTestDB(t) // creates a fresh database, drops it in t.Cleanup

	// Migrate the schema under test.
	if _, err := migrate.Up(t.Context(), db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	repo := NewUserRepository(db)
	// ...
}
```

For a rollback test, `DownAll` needs no files at all:

```go
func TestMigrationsAreReversible(t *testing.T) {
	t.Parallel()

	db := newTestDB(t)
	ctx := t.Context()

	if _, err := migrate.Up(ctx, db, migrationsFS); err != nil {
		t.Fatalf("up: %v", err)
	}

	// Uses the scripts stored in the database, not the files.
	if _, err := migrate.DownAll(ctx, db); err != nil {
		t.Fatalf("down all: %v", err)
	}
}
```

## Catch broken migrations before they reach CI

`validate` needs no database, so it is cheap enough for a pre-commit hook:

```yaml title=".pre-commit-config.yaml"
repos:
  - repo: local
    hooks:
      - id: pg-migrate-validate
        name: validate migrations
        entry: pg-migrate validate
        language: system
        files: ^migrations/.*\.sql$
        pass_filenames: false
```

Or as a step in CI, before anything that needs credentials:

```yaml
- name: Validate migrations
  run: go run github.com/eidon-go/pg-migrate/cmd/pg-migrate@latest validate
  env:
    MIGRATION_PATH: ./migrations
```

A misspelled `-- +migrate notransction` is otherwise found when the deployment
is already running.

## Gate a deployment on the plan

`Plan` needs no DDL privileges and takes no lock, so it is safe to run from CI
against production with a read-only role.

```go
analysis, err := migrate.Plan(ctx, db, fsys)
if err != nil {
	return err
}

if analysis.Blocked {
	return fmt.Errorf("deployment would be refused: %s", analysis.BlockedReason)
}

if len(analysis.ToRollback) > 0 {
	return fmt.Errorf("refusing to deploy: would roll back %v", analysis.ToRollback)
}
```

From a shell:

```bash
pg-migrate plan --json | jq -e '.blocked == false and (.to_rollback | length) == 0'
```

## Zero-downtime index creation

`CREATE INDEX CONCURRENTLY` cannot run in a transaction, and it can take hours on
a large table.

```sql title="20260401093000_users_email_index.up.sql"
-- +migrate notransaction
SET statement_timeout = 0;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);
```

```sql title="20260401093000_users_email_index.down.sql"
-- +migrate notransaction
DROP INDEX CONCURRENTLY IF EXISTS idx_users_email;
```

Both statements run on the same dedicated connection, so `SET statement_timeout`
applies to the `CREATE INDEX` that follows. The `IF NOT EXISTS` matters: a failed
`CONCURRENTLY` build leaves an invalid index behind, and you want the retry to be
able to proceed after you drop it.

Give the run a generous lock timeout — the advisory lock is held for the whole
thing:

```go
migrate.Up(ctx, db, fsys, migrate.WithLockTimeout(0)) // wait indefinitely
```

## Separate schema for the bookkeeping table

Keeping migration state out of `public`:

```go
migrate.Up(ctx, db, fsys,
	migrate.WithSchema("infra"),
	migrate.WithTableName("schema_migrations"),
)
```

The schema must already exist — the library creates its table, not your schema.

## Two tools, one database

If something else already uses advisory locks on the same database, give this one
its own ID so the two do not block each other:

```go
migrate.Up(ctx, db, fsys, migrate.WithLockID(9182736450))
```

Every instance migrating the same database must use the **same** ID — that is
what makes the lock mutual.
