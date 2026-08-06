# Library API

Full generated reference lives on
[pkg.go.dev](https://pkg.go.dev/github.com/eidon-go/pg-migrate). This page is the
guided version.

```go
import migrate "github.com/eidon-go/pg-migrate"
```

## Entry points

Every function takes a `*sql.DB` you opened yourself, with whatever driver you
prefer. The library never opens a connection on your behalf.

### Up

```go
func Up(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Result, error)
```

Applies pending migrations forward. Migrations present in the database but
missing from `fsys` are **left alone** and reported in `Result.ExtrasLeft`.

The conservative choice for production: deploying an older branch cannot silently
drop schema a newer release added.

### Reconcile

```go
func Reconcile(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Result, error)
```

Makes the database match the files. Refuses with `ErrDivergence` when that would
require removing something, unless `WithRollback()` is passed.

### Down / DownAll

```go
func Down(ctx context.Context, sqlDB *sql.DB, count int, opts ...Option) (*Result, error)
func DownAll(ctx context.Context, sqlDB *sql.DB, opts ...Option) (*Result, error)
```

Rolls back the last `count` (or every) applied migration, most recent first,
using the scripts stored in the database. No `fs.FS` needed — the files are
irrelevant here.

Refused with `ErrIrreversible` before anything runs if any candidate is marked
irreversible.

### Plan

```go
func Plan(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Analysis, error)
```

What a `Reconcile` with the same options **would** do. Takes no lock, writes
nothing, needs no DDL privileges — a missing bookkeeping table just means nothing
has been applied yet.

`Analysis.Blocked` reports whether that `Reconcile` would refuse, and
`BlockedReason` says why. Without those, a plan listing migrations under
`ToRollback` would read as "this will happen" when the same call would in fact
stop with `ErrDivergence`.

### Status

```go
func Status(ctx context.Context, sqlDB *sql.DB, opts ...Option) ([]AppliedMigration, error)
```

The bookkeeping rows in application order. Read-only. Returns an empty slice for
a database that has never been migrated.

### Forget

```go
func Forget(ctx context.Context, sqlDB *sql.DB, id string, opts ...Option) error
```

Removes a migration's bookkeeping row **without running its rollback script**.

The escape hatch from `ErrFailedMigrations`: once you have brought the schema to
a known state by hand, this tells the library to stop refusing. It touches
nothing but the ledger — the schema is your business, since only you know what
state you put it in.

## Options

| Option | Effect |
|---|---|
| `WithRollback()` | Let `Reconcile` roll back migrations missing from the source. |
| `WithInterleaved()` | Allow applying migrations that sort before the latest applied one. |
| `WithLockTimeout(d)` | Bound the advisory lock wait. `0` waits indefinitely. Default 5s. |
| `WithLockID(id)` | Change the advisory lock ID, to isolate from another tool. |
| `WithAllowEmptySource()` | Permit reconciling from a source with no migrations. |
| `WithTableName(name)` | Bookkeeping table name. Default `migrations`. |
| `WithSchema(schema)` | Schema for the bookkeeping table. Default: the `search_path`. |
| `WithLogger(l)` | A `*slog.Logger`. Defaults to `slog.Default()`. |

```go
result, err := migrate.Reconcile(ctx, db, fsys,
	migrate.WithRollback(),
	migrate.WithSchema("infra"),
	migrate.WithTableName("schema_migrations"),
	migrate.WithLockTimeout(2*time.Minute),
	migrate.WithLogger(logger),
)
```

## Result types

```go
type Result struct {
	Applied     []string // actually applied, in execution order
	RolledBack  []string // actually rolled back, in execution order
	ExtrasLeft  []string // Up only: in the DB, missing from fsys, left alone
	Interleaved []string // subset of Applied that went in out of ID order
}
```

!!! important "Read the Result even on error"

    On failure the slices hold the prefix that **completed** before the failure.
    A partially applied run reports exactly how far it got, which is what you
    want in a deployment log.

    The pointer is `nil` only when the call was rejected before anything ran — a
    nil `*sql.DB`, a nil `fs.FS`, an invalid table name. Guard for it:

    ```go
    result, err := migrate.Up(ctx, db, fsys)
    if err != nil {
        var applied []string
        if result != nil {
            applied = result.Applied
        }

        log.Printf("migrate failed after applying %v: %v", applied, err)

        return err
    }
    ```

```go
type Analysis struct {
	BlockedReason string   // why; empty unless Blocked
	ToApply       []string
	ToRollback    []string
	Interleaved   []string
	Blocked       bool     // a Reconcile with these options would refuse
}

type AppliedMigration struct {
	AppliedAt  time.Time
	ID         string
	Error      string // the recorded error; empty unless Failed
	DownScript string // the rollback script as stored at apply time
	Failed     bool
}
```

All three carry JSON tags — the CLI's `--json` output is these types verbatim.

## Sentinel errors

Match with `errors.Is`. Every one of these means **nothing was changed** unless
noted.

| Error | Meaning |
|---|---|
| `ErrDivergence` | DB has migrations missing from the files. Pass `WithRollback()` or fix the source. |
| `ErrInterleaved` | Files contain IDs sorting before the latest applied. Pass `WithInterleaved()` if intended. |
| `ErrFailedMigrations` | A migration is recorded as failed. **Terminal — do not retry.** |
| `ErrIrreversible` | A rollback would touch a migration marked irreversible. |
| `ErrEmptySource` | Source empty, database not. Usually a missing `fs.Sub`. |
| `ErrUnrecorded` | The schema changed but the ledger could not be updated. **Needs a human.** |
| `ErrLockTimeout` | Another migration is in progress. Safe to retry. |
| `ErrPoolTooSmall` | `SetMaxOpenConns(1)`. Use `0` or `>= 2`. |

```go
switch {
case errors.Is(err, migrate.ErrFailedMigrations):
	// Terminal. Stop the rollout and page someone.
	return fmt.Errorf("manual intervention required: %w", err)

case errors.Is(err, migrate.ErrLockTimeout):
	// Another instance is migrating. Retrying is reasonable.
	return retryLater(err)

case err != nil:
	return err
}
```
