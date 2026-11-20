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

## Kubernetes

### Ship the migrations in the application image

Put the CLI and the migration files into the image the application already
runs from. The migrations are then versioned with the code that expects them,
and rolling the deployment back rolls the file set back with it.

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/app ./cmd/app

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=ghcr.io/eidon-go/pg-migrate:0.2.0 /pg-migrate /usr/local/bin/pg-migrate
COPY --from=build /out/app /app
COPY migrations /migrations
ENV MIGRATION_PATH=/migrations
ENTRYPOINT ["/app"]
```

`COPY --from` takes the binary for the platform being built, so a multi-arch
build needs no per-architecture download. Image tags carry no `v` prefix.

Avoid shipping migrations in a ConfigMap: it is capped at 1 MiB, the directory
only ever grows, and it is versioned separately from the application.

### A pre-deploy Job (Helm, Argo CD)

When the deployment goes through Helm or Argo CD, run the migrations as a hook
Job. It runs once per release instead of once per pod, a failure stops the
release before the Deployment changes — the old pods keep serving — and a long
migration does not count against the pods' startup probes.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: app-migrations
  annotations:
    # Helm
    helm.sh/hook: pre-install,pre-upgrade
    helm.sh/hook-delete-policy: before-hook-creation
    # Argo CD (it also maps the Helm hooks above, but says so explicitly)
    argocd.argoproj.io/hook: PreSync
    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation
spec:
  # ErrFailedMigrations is terminal: a retry cannot fix it.
  backoffLimit: 0
  activeDeadlineSeconds: 1800
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: ghcr.io/acme/app:1.2.3   # the application image
          command: ["/usr/local/bin/pg-migrate", "up", "--log-format", "json"]
          env:
            - name: DATABASE_URL
              valueFrom:
                # A role with DDL rights; the application can run with less.
                secretKeyRef: { name: app-db-migrator, key: url }
```

!!! note "Helm hooks and secrets"

    A `pre-install` hook runs before the chart's other resources exist, so a
    Secret created by the same chart is not there yet on the first install.
    Reference a Secret managed outside the chart, or make the Secret itself a
    hook with a lower `helm.sh/hook-weight`.

### Or an init container

Without a hook mechanism, an init container on the application's Deployment
does the job. Every pod runs it, but the advisory lock queues them: the first
applies the migrations and the rest find nothing to do.

```yaml
initContainers:
  - name: migrate
    image: ghcr.io/acme/app:1.2.3
    command: ["/usr/local/bin/pg-migrate", "up", "--log-format", "json"]
    env:
      - name: DATABASE_URL
        valueFrom:
          secretKeyRef: { name: app-db-migrator, key: url }
      # A busy lock means another pod is mid-migration; waiting beats failing.
      - name: MIGRATION_LOCK_TIMEOUT
        value: "10m"
```

!!! warning "Do not retry ErrFailedMigrations"

    Kubernetes restarts a failed init container, so `ErrFailedMigrations` turns
    into a CrashLoopBackOff that no restart will ever fix — the state needs a
    human. Alert on the Deployment exceeding `progressDeadlineSeconds`, or use
    the Job above. The container's last exit code tells the cases apart: `3`
    needs a human, `4` is a busy lock that a restart will get past (see
    [Exit codes](cli.md#exit-codes)).

### Things that hold either way

- **Use `up`, never `reconcile -r`, in a deployment.** Rolling the deployment
  back runs the *older* image's command. `up` leaves the newer migrations in
  place and reports them as `extras_left`; `reconcile -r` would roll them back,
  dropping whatever they created.
- **Write migrations that the previous release can live with.** During a
  rolling update the old pods keep serving against the new schema. Add first,
  remove in a later release (expand/contract).
- **Leave room for a clean shutdown.** On SIGTERM the CLI cancels the running
  statement, records a partially applied `notransaction` script as failed and
  releases the lock, each step under its own timeout — about 20 seconds at
  worst. The default `terminationGracePeriodSeconds` of 30 covers it.
- **Gate the release on a plan.** `pg-migrate plan --json` reports whether a
  reconcile would refuse and what it would roll back, without taking the lock;
  see [`examples/deploy-gate`](https://github.com/eidon-go/pg-migrate/tree/main/examples/deploy-gate).

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

## Adopt an existing database

The schema has been there for years; this tool has not. Describe the current
schema as migrations, then tell the ledger they are already applied:

```bash
# 1. Write migrations matching what the database already has.
pg-migrate new create_users
pg-migrate new add_orders
# ... fill them in with the DDL that produced the current schema

# 2. Check they parse before touching anything.
pg-migrate validate

# 3. Record them as applied, without running them.
pg-migrate baseline 20260115104500_add_orders

# 4. From here on, normal operation.
pg-migrate up
```

The baselined migrations are never executed — their tables already exist, so
running them would fail. What matters is that their **rollback scripts are
stored**, so a later `down` has something to run.

!!! tip "Verify before you trust it"

    Get the baseline migrations right by generating them from the live schema
    (`pg_dump --schema-only`), then confirm on a scratch copy that applying them
    from empty reproduces production. A baseline is a claim about what the schema
    is; nothing checks that claim for you.

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
