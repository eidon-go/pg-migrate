// Package migrate is a small, dependency-light PostgreSQL migration library
// driven by a directory (or fs.FS) of SQL files and an advisory-lock-protected
// reconciliation algorithm.
//
// # Entry points
//
// Reconcile, Up, Down and DownAll change the database and return a *Result
// describing what they actually did — on failure, the prefix that completed.
// Plan and Status only read: Plan returns an *Analysis of what a Reconcile would
// do (including whether it would refuse), and Status lists what is recorded as
// applied. Neither takes the lock, and neither needs DDL privileges. Forget is
// the manual escape hatch described under "Failed migrations stop everything",
// and Baseline adopts a database whose schema already exists by recording
// migrations as applied without running them.
//
// # Migration files
//
// Each migration is a pair of files sharing a base name, which becomes the
// migration ID:
//
//	migrations/
//	  20260115103000_create_users.up.sql
//	  20260115103000_create_users.down.sql
//
// IDs are compared lexicographically, never numerically. The CLI's "new"
// command therefore names them with a fixed-width UTC timestamp; a zero-padded
// sequence works as long as it never outgrows its width, since the first ID that
// does sorts before everything already applied. Both halves are required — a
// missing .down.sql is an error, not an empty rollback.
//
// Each file is plain SQL. It may begin with a single directive line, which must
// be the very first line:
//
//	-- +migrate notransaction
//	CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
//
// There are two directives. "notransaction" runs the script
// statement-by-statement outside a transaction, as required by statements
// PostgreSQL refuses inside one; note that such a script failing partway leaves
// the earlier statements committed. "irreversible" belongs in a .down.sql that is
// deliberately empty:
//
//	-- +migrate irreversible
//
// A rollback that would touch such a migration is refused with ErrIrreversible
// before anything runs. This exists so that a migration which genuinely cannot be
// undone — a dropped table, a destructive backfill — is distinguishable from a
// rollback script somebody forgot to write; an empty .down.sql without the
// directive is an error.
//
// Unknown, misspelled or wrongly-cased directives are rejected rather than
// ignored, as are directive lines below the first line. Files that are not .sql
// are ignored; a .sql file that is neither *.up.sql nor *.down.sql is an error.
// Subdirectories are not traversed.
//
// # Failed migrations stop everything
//
// A "notransaction" script that dies partway records its error in the
// bookkeeping table. While such a row exists, Reconcile, Up and Down all refuse
// to do anything and return ErrFailedMigrations.
//
// This is deliberate. The schema is then in a state no migration describes:
// running the rollback script means running it against a state it was never
// written for, and rolling back what came after means destroying successful
// work. Both are guesses. Resolving it is a manual step — Status reports the
// recorded error and the stored rollback script, and once the schema is back to a
// known state, Forget clears the row so normal operation resumes.
//
// Statements of a "notransaction" script all run on one dedicated connection, so
// session state they set (SET statement_timeout = 0 before CREATE INDEX
// CONCURRENTLY, a temporary table) applies to the statements that follow, and the
// session is reset before the connection returns to the caller's pool.
//
// A "notransaction" script is split into statements before being run, by a
// scanner that understands single- and double-quoted literals, E'...' escape
// strings, dollar quoting ($$...$$ and $tag$...$tag$) and line and block
// comments, so a semicolon inside any of those is not a boundary. It assumes
// standard_conforming_strings is on, which is the default; U&'...' literals are
// not treated specially.
//
// Where a boundary still cannot be inferred, it can be forced:
//
//	-- +migrate StatementBegin
//	...
//	-- +migrate StatementEnd
//
// # Rollback scripts come from the database
//
// Applying a migration stores both scripts in the bookkeeping table, and
// rollback executes the stored .down.sql rather than the file. This is
// deliberate: rolling back has to work when the checked-out branch no longer
// contains the migration at all, as when deploying an older branch or moving
// from a feature branch back to the mainline. A consequence is that editing the
// file of an already-applied migration has no effect on anything.
//
// # The bookkeeping table
//
// State lives in a table named "migrations" by default; use WithTableName and
// WithSchema to change that. By default it is unqualified and resolved through
// the connection's search_path.
//
// Migrations are ordered by a monotonic sequence column rather than by
// applied_at. Wall-clock time is not a reliable order — timestamps can tie, and
// they move backwards across a DST transition or when sessions disagree about
// the time zone — and that order determines which migrations "the last N" and
// automatic recovery act on.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/eidon-go/pg-migrate/internal/db"
	"github.com/eidon-go/pg-migrate/internal/migrator"
	"github.com/eidon-go/pg-migrate/internal/source"
)

// Sentinel errors. Use errors.Is to detect.
var (
	// ErrDivergence is returned by Reconcile without WithRollback() when the
	// database has applied migrations that are missing from the file source.
	ErrDivergence = migrator.ErrDivergence

	// ErrInterleaved is returned by Reconcile without WithInterleaved() when
	// the file source contains migrations lexicographically smaller than the
	// latest migration applied in the database (out-of-order apply).
	ErrInterleaved = migrator.ErrInterleaved

	// ErrFailedMigrations is returned by Reconcile, Up and Down when the
	// bookkeeping table contains a migration recorded as failed. Nothing is
	// applied or rolled back while such a row exists, and nothing was changed.
	//
	// This is terminal, not transient: resolving it is a manual step, so a
	// deployment pipeline should stop rather than retry.
	ErrFailedMigrations = migrator.ErrFailedMigrations

	// ErrIrreversible is returned when a rollback would touch a migration whose
	// .down.sql is marked with the "irreversible" directive. The operation is
	// refused before it starts, so nothing was rolled back.
	ErrIrreversible = migrator.ErrIrreversible

	// ErrEmptySource is returned by Reconcile when the source has no migrations at
	// all but the database has some, which would mean rolling the schema back to
	// nothing. Nothing was changed. Usually a wrong path or an embed.FS that needs
	// fs.Sub; pass WithAllowEmptySource() if it really is intended.
	ErrEmptySource = migrator.ErrEmptySource

	// ErrUnrecorded is returned when a "notransaction" script changed the schema
	// but its bookkeeping row could not be written to match, leaving the database
	// and the ledger disagreeing. Reconcile them by hand; Forget clears a row once
	// the schema is back to a known state.
	ErrUnrecorded = migrator.ErrUnrecorded

	// ErrLockTimeout is returned when the advisory lock could not be taken within
	// the configured timeout, which means another migration is in progress.
	ErrLockTimeout = db.ErrLockTimeout

	// ErrPoolTooSmall is returned when the given *sql.DB allows only one open
	// connection. The advisory lock pins a connection for the whole run, so at
	// least two are required: call SetMaxOpenConns(0) or >= 2.
	ErrPoolTooSmall = db.ErrPoolTooSmall

	// ErrAlreadyRecorded is returned by Baseline when the bookkeeping table
	// already holds rows. Adoption happens once, on an empty ledger; the
	// restriction is what stops Baseline from doubling as a way to mark a
	// migration applied without running it.
	ErrAlreadyRecorded = migrator.ErrAlreadyRecorded

	// ErrBaselineNotFound is returned by Baseline when the given ID is not among
	// the migrations in fsys.
	ErrBaselineNotFound = migrator.ErrBaselineNotFound
)

// Option configures Reconcile/Up/Down/Plan. New options are added over time;
// callers only need to pass the ones relevant to them.
type Option func(*config)

type config struct {
	logger           *slog.Logger
	table            migrator.TableConfig
	lockTimeout      time.Duration
	lockID           int64
	allowRollback    bool
	allowInterleaved bool
	allowEmptySource bool
	// lockTimeoutSet distinguishes "not configured" from an explicit zero, which
	// means "wait indefinitely".
	lockTimeoutSet bool
}

// WithRollback permits Reconcile to roll back migrations that are applied in
// the database but missing from the file source. Has no effect on Up, Down,
// or Plan.
func WithRollback() Option {
	return func(c *config) {
		c.allowRollback = true
	}
}

// WithInterleaved permits Reconcile to apply migrations whose IDs are
// lexicographically smaller than the latest applied migration (out-of-order).
// Has no effect on Up, Down, or Plan.
func WithInterleaved() Option {
	return func(c *config) {
		c.allowInterleaved = true
	}
}

// WithLockTimeout bounds how long to wait when acquiring the advisory lock.
// Default: 5 seconds.
//
// Zero means wait indefinitely, matching Postgres' own lock_timeout = 0. A
// negative duration is rejected.
func WithLockTimeout(d time.Duration) Option {
	return func(c *config) {
		c.lockTimeout = d
		c.lockTimeoutSet = true
	}
}

// WithLockID overrides the pg_advisory_lock key. Use this when multiple
// independent migration namespaces share a database.
// Default: 2718281828459045235.
func WithLockID(id int64) Option {
	return func(c *config) {
		c.lockID = id
	}
}

// WithAllowEmptySource permits Reconcile to roll the database back to nothing
// when the file source contains no migrations at all.
//
// Without it that case is refused with ErrEmptySource, because it is far more
// often a wrong path or an embed.FS that needed fs.Sub than an intent to drop
// every table. Set it only where "no migrations means no schema" is genuinely
// what you mean — a test harness tearing a database down, say.
func WithAllowEmptySource() Option {
	return func(c *config) {
		c.allowEmptySource = true
	}
}

// WithTableName overrides the name of the bookkeeping table.
// Default: "migrations".
//
// The name is quoted, so it is case-sensitive and may contain any character;
// it must be a usable Postgres identifier (non-empty, at most 63 bytes).
func WithTableName(name string) Option {
	return func(c *config) {
		c.table.Name = name
	}
}

// WithSchema qualifies the bookkeeping table with a schema.
//
// By default the table is unqualified and resolved through the connection's
// search_path, which keeps per-tenant search_path setups working. Set this when
// the table must live in one specific schema regardless of search_path. The
// schema is not created — it must already exist.
func WithSchema(schema string) Option {
	return func(c *config) {
		c.table.Schema = schema
	}
}

// WithLogger overrides the *slog.Logger used for operational logs (lock
// acquisition, recovery, plan execution). Defaults to slog.Default(), so if
// the caller has configured slog's global default, logs already land there
// without needing this option — use it to scope/attribute logs (e.g.
// logger.With("component", "migrate")) or silence them (a logger backed by
// a discarding handler).
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) {
		c.logger = logger
	}
}

func resolveOptions(opts []Option) (*config, error) {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}

	if cfg.lockTimeout < 0 {
		return nil, fmt.Errorf("migrate: lock timeout must not be negative, got %s", cfg.lockTimeout)
	}

	return cfg, nil
}

// Analysis is what a Reconcile would do, in the future tense. It is returned by
// Plan, which executes nothing.
//
// Blocked reports whether a Reconcile called with the same options would refuse
// to run, and BlockedReason says why. Without those, a plan listing migrations
// under ToRollback reads as "this will happen" when the very same call would in
// fact stop with ErrDivergence.
type Analysis struct {
	BlockedReason string   `json:"blocked_reason,omitempty"` // why; empty unless Blocked
	ToApply       []string `json:"to_apply"`                 // would be applied, in the order they would run
	ToRollback    []string `json:"to_rollback"`              // would be rolled back, in the order they would run
	Interleaved   []string `json:"interleaved"`              // subset of ToApply that sorts before the latest migration staying applied

	Blocked bool `json:"blocked"` // a Reconcile with these options would return an error instead of executing
}

// Result is what a run actually did, in the past tense. It is returned by Up,
// Reconcile and Down.
//
// On error the slices hold the prefix that completed before the failure, so a
// partially applied run reports exactly how far it got. The result is never nil
// when the error came from executing rather than from validating arguments.
type Result struct {
	Applied     []string `json:"applied"`     // actually applied, in execution order
	RolledBack  []string `json:"rolled_back"` // actually rolled back, in execution order
	ExtrasLeft  []string `json:"extras_left"` // Up only: present in the database, missing from fsys, deliberately left alone
	Interleaved []string `json:"interleaved"` // subset of Applied that went in out of ID order
}

// AppliedMigration is one row of the bookkeeping table, as reported by Status.
type AppliedMigration struct {
	AppliedAt time.Time `json:"applied_at"`      // when it was applied
	ID        string    `json:"id"`              // migration ID
	Error     string    `json:"error,omitempty"` // the recorded error; empty unless Failed

	// DownScript is the rollback script as stored at apply time. Reported because
	// resolving a failed migration by hand starts with reading it, and it would
	// otherwise mean querying the bookkeeping table directly.
	DownScript string `json:"down_script"`
	Failed     bool   `json:"failed"` // true when the migration was recorded with an error
}

// migrationIDs extracts the IDs from a slice of internal migrations.
func migrationIDs(ms []*migrator.Migration) []string {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}

	return ids
}

func toAnalysis(plan *migrator.MigrationPlan, blockedBy error) *Analysis {
	analysis := &Analysis{
		ToApply:     migrationIDs(plan.ToApply),
		ToRollback:  migrationIDs(plan.ToRollback),
		Interleaved: migrationIDs(plan.Interleaved),
	}
	if blockedBy != nil {
		analysis.Blocked = true
		analysis.BlockedReason = blockedBy.Error()
	}

	return analysis
}

func toResult(result *migrator.Result) *Result {
	if result == nil {
		return nil
	}

	return &Result{
		Applied:     migrationIDs(result.Applied),
		RolledBack:  migrationIDs(result.RolledBack),
		ExtrasLeft:  migrationIDs(result.ExtrasLeft),
		Interleaved: migrationIDs(result.Interleaved),
	}
}

func (c *config) runOptions() migrator.RunOptions {
	return migrator.RunOptions{
		AllowRollback:    c.allowRollback,
		AllowInterleaved: c.allowInterleaved,
		AllowEmptySource: c.allowEmptySource,
	}
}

func newService(sqlDB *sql.DB, fsys fs.FS, opts []Option) (*migrator.Service, *config, error) {
	// Guarded rather than left to panic deep inside io/fs: a library returning an
	// error beats a segfault in someone's deployment job.
	if sqlDB == nil {
		return nil, nil, errors.New("migrate: sqlDB must not be nil")
	}

	if fsys == nil {
		return nil, nil, errors.New("migrate: fsys must not be nil")
	}

	cfg, err := resolveOptions(opts)
	if err != nil {
		return nil, nil, err
	}

	executor, err := migrator.NewCustomExecutor(sqlDB, cfg.table)
	if err != nil {
		return nil, nil, err
	}

	pg := db.NewPostgresFromDB(sqlDB, db.PostgresConfig{
		LockTimeout:    cfg.lockTimeout,
		LockTimeoutSet: cfg.lockTimeoutSet,
		LockID:         cfg.lockID,
	})
	svc := migrator.NewService(
		source.NewFS(fsys),
		executor,
		pg,
		cfg.logger,
	)

	return svc, cfg, nil
}

// Reconcile synchronizes the database with the migration files in fsys.
//
// Without WithRollback() it returns ErrDivergence if the DB has migrations
// missing from fsys. Without WithInterleaved() it returns ErrInterleaved if
// fsys contains migrations whose IDs sort before the latest migration that
// stays applied.
//
// For go:embed, wrap the embed.FS with fs.Sub(embedFS, "migrations").
//
// On error the returned Result describes what did complete before the failure;
// the database may be partially updated.
func Reconcile(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Result, error) {
	svc, cfg, err := newService(sqlDB, fsys, opts)
	if err != nil {
		return nil, err
	}

	result, err := svc.Reconcile(ctx, cfg.runOptions())

	return toResult(result), err
}

// Up applies pending migrations forward. Migrations in the DB that are missing
// from fsys are NOT rolled back; their IDs are reported in the Result's
// ExtrasLeft field as a warning list. Out-of-order (interleaved) migrations are
// applied with a warning. WithRollback and WithInterleaved have no effect here.
//
// For go:embed, wrap the embed.FS with fs.Sub(embedFS, "migrations").
//
// On error the returned Result describes what did complete before the failure.
func Up(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Result, error) {
	svc, _, err := newService(sqlDB, fsys, opts)
	if err != nil {
		return nil, err
	}

	result, err := svc.Up(ctx)

	return toResult(result), err
}

// Down rolls back the last count applied migrations, most recent first.
//
// count must be positive. Rolling back everything is DownAll, spelled out
// separately on purpose: this used to treat count <= 0 as "all", which made the
// zero value of an int — an unset config field, an unchecked parse — mean
// "destroy the entire schema".
//
// No fsys is needed: rollback scripts come from the database, which is what lets
// Down work when the checked-out branch no longer contains the migrations.
//
// On error the returned Result lists what was rolled back before the failure.
func Down(ctx context.Context, sqlDB *sql.DB, count int, opts ...Option) (*Result, error) {
	if count <= 0 {
		return nil, fmt.Errorf(
			"migrate: Down count must be positive, got %d; use DownAll to roll back everything", count)
	}

	return rollback(ctx, sqlDB, count, opts)
}

// DownAll rolls back every applied migration, most recent first, leaving the
// database with no migrations applied.
//
// On error the returned Result lists what was rolled back before the failure.
func DownAll(ctx context.Context, sqlDB *sql.DB, opts ...Option) (*Result, error) {
	return rollback(ctx, sqlDB, allMigrations, opts)
}

// allMigrations is the internal sentinel meaning "every applied migration".
const allMigrations = -1

func rollback(ctx context.Context, sqlDB *sql.DB, count int, opts []Option) (*Result, error) {
	svc, _, err := newService(sqlDB, emptyFS{}, opts)
	if err != nil {
		return nil, err
	}

	result, err := svc.Rollback(ctx, count)

	return toResult(result), err
}

// Plan computes what a Reconcile would do, without acquiring the advisory lock
// and without executing or writing anything.
//
// It needs no DDL privileges: a missing bookkeeping table simply means nothing
// has been applied yet.
//
// Because it takes no lock, a concurrent migration can invalidate the answer
// between planning and acting on it.
//
// For go:embed, wrap the embed.FS with fs.Sub(embedFS, "migrations").
func Plan(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, opts ...Option) (*Analysis, error) {
	svc, cfg, err := newService(sqlDB, fsys, opts)
	if err != nil {
		return nil, err
	}

	analysis, err := svc.BuildReconcilePlan(ctx, cfg.runOptions())
	if err != nil {
		return nil, err
	}

	return toAnalysis(analysis.Plan, analysis.BlockedBy), nil
}

// Forget removes a migration's bookkeeping row without running its rollback
// script, reporting the database as no longer having that migration applied.
//
// This is the escape hatch from ErrFailedMigrations. When a "notransaction"
// script has failed partway, the library refuses to act until a human resolves
// it; once the schema has been brought to a known state by hand, Forget clears
// the row so normal operation resumes. Use Status to read the recorded error and
// the stored rollback script first.
//
// It changes nothing but the ledger. Bringing the schema itself to a consistent
// state is the caller's responsibility — only they know what state they left it
// in.
func Forget(ctx context.Context, sqlDB *sql.DB, id string, opts ...Option) error {
	svc, _, err := newService(sqlDB, emptyFS{}, opts)
	if err != nil {
		return err
	}

	return svc.Forget(ctx, id)
}

// Baseline records every migration up to and including throughID as applied,
// without running any of them, and returns what it recorded.
//
// It is how this library is adopted on a database whose schema already exists.
// Write the migrations that describe the current schema, then baseline through
// the last of them: the ledger learns what is already there, and the next Up
// applies only what comes after.
//
//	// The schema already matches migrations 1..N; adopt without re-running them.
//	result, err := migrate.Baseline(ctx, db, fsys, "20260115103000_create_users")
//
// Two guards keep this from becoming a way to skip a migration. It returns
// ErrAlreadyRecorded unless the bookkeeping table is empty, since adoption
// happens once; and ErrBaselineNotFound if throughID is not in fsys, so the
// recorded set is the one named rather than a silently different prefix.
//
// The scripts are stored exactly as a normal apply stores them, so a later Down
// of a baselined migration runs the same text it would have run anyway. That is
// worth thinking about: the rollback will execute against a schema this library
// never built, and only the caller knows whether it fits.
func Baseline(ctx context.Context, sqlDB *sql.DB, fsys fs.FS, throughID string, opts ...Option) (*Result, error) {
	if throughID == "" {
		return nil, errors.New("migrate: Baseline requires the ID to baseline through")
	}

	svc, _, err := newService(sqlDB, fsys, opts)
	if err != nil {
		return nil, err
	}

	result, err := svc.Baseline(ctx, throughID)

	return toResult(result), err
}

// Status reports the migrations recorded in the bookkeeping table, in the order
// they were applied.
//
// It reads only the database — no fsys, no lock, no DDL. A database that has
// never been migrated yields an empty slice rather than an error.
func Status(ctx context.Context, sqlDB *sql.DB, opts ...Option) ([]AppliedMigration, error) {
	svc, _, err := newService(sqlDB, emptyFS{}, opts)
	if err != nil {
		return nil, err
	}

	applied, err := svc.AppliedMigrations(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]AppliedMigration, 0, len(applied))
	for _, m := range applied {
		record := AppliedMigration{
			ID:         m.ID,
			AppliedAt:  m.AppliedAt,
			DownScript: m.DownScript,
		}
		if m.Error != nil {
			record.Failed = true
			record.Error = *m.Error
		}

		out = append(out, record)
	}

	return out, nil
}

// emptyFS stands in for the file source in the operations that do not read one.
// The service always holds a source; these paths simply never consult it.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
