package migrator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

type source interface {
	GetMigrations() ([]*Migration, error)
}

type migrationExecutor interface {
	EnsureMigrationsTable(ctx context.Context) error
	TableExists(ctx context.Context) (bool, error)
	ApplyMigration(ctx context.Context, migration *Migration) error
	RecordApplied(ctx context.Context, migrations []*Migration) error
	RollbackMigration(ctx context.Context, id string) error
	GetAppliedMigrations(ctx context.Context) ([]AppliedMigration, error)
	DeleteRecord(ctx context.Context, id string) (bool, error)
	EnsureReversible(ctx context.Context, ids []string) error
	ExecutePlan(ctx context.Context, plan *MigrationPlan, allMigrations []*Migration) (*ExecutionResult, error)
}

type locker interface {
	AcquireLock(ctx context.Context) error
	ReleaseLock(ctx context.Context) error
}

// RunOptions are Reconcile's switches. Up and Rollback take none: with recovery
// gone there is nothing left for them to vary, and a parameter they ignore would
// only invite callers to pass one and wonder why it did nothing.
type RunOptions struct {
	// AllowRollback lets Reconcile roll back migrations present in the database
	// but missing from the source.
	AllowRollback bool

	// AllowInterleaved lets Reconcile apply migrations that sort before the
	// latest migration that stays applied.
	AllowInterleaved bool

	// AllowEmptySource permits a Reconcile whose source has no migrations at all
	// to roll the database back to nothing. Without it that case is refused, since
	// it is far more often a misconfigured path than an intent to drop everything.
	AllowEmptySource bool
}

// ReconcileAnalysis is a plan together with whether Reconcile would actually
// execute it. BlockedBy is the error Reconcile would return instead of running,
// or nil.
type ReconcileAnalysis struct {
	Plan      *MigrationPlan
	BlockedBy error
}

// Result records what a run actually did, as opposed to what it planned to do.
// On error the slices hold the prefix that completed before the failure.
type Result struct {
	Applied     []*Migration // actually applied, in execution order
	RolledBack  []*Migration // actually rolled back, in execution order
	ExtrasLeft  []*Migration // Up only: extras in the DB deliberately left alone
	Interleaved []*Migration // subset of Applied that went in out of ID order
}

type Service struct {
	source   source
	executor migrationExecutor
	locker   locker
	logger   *slog.Logger
}

// NewService constructs a Service. A nil logger defaults to slog.Default().
func NewService(source source, executor migrationExecutor, locker locker, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}

	return &Service{
		source:   source,
		executor: executor,
		locker:   locker,
		logger:   logger,
	}
}

// Up applies pending migrations forward without rolling back extras.
//
// The returned Result reflects what was executed. On error it holds the prefix
// that completed before the failure.
func (s *Service) Up(ctx context.Context) (*Result, error) {
	result := &Result{}

	err := s.withLock(ctx, func() error {
		if err := s.executor.EnsureMigrationsTable(ctx); err != nil {
			return fmt.Errorf("ensure migrations table: %w", err)
		}

		if err := s.refuseIfFailed(ctx); err != nil {
			return err
		}

		fromFiles, fromDB, err := s.GetMigrations(ctx)
		if err != nil {
			return fmt.Errorf("get migrations: %w", err)
		}

		// Up leaves extras applied, so they still count for interleaving.
		plan := BuildPlan(fromFiles, fromDB, false)
		result.ExtrasLeft = plan.ToRollback

		if len(plan.ToRollback) > 0 {
			s.logger.WarnContext(ctx, "Database has extra migrations not present in files (these will not be rolled back in 'up' mode)",
				"extra_migrations", joinIDs(plan.ToRollback))
		}

		if len(plan.Interleaved) > 0 {
			s.logger.WarnContext(ctx, "Detected interleaved (out-of-order) migrations to apply",
				"interleaved_migrations", joinIDs(plan.Interleaved))
		}

		// Execution plan strips ToRollback - Up never rolls back extras.
		executed, execErr := s.executor.ExecutePlan(ctx, &MigrationPlan{ToApply: plan.ToApply}, fromFiles)
		s.record(result, executed, plan)

		if execErr != nil {
			return fmt.Errorf("execute plan: %w", execErr)
		}

		return nil
	})

	return result, err
}

// Reconcile synchronizes the database with the target state.
//
// Without AllowRollback it returns ErrDivergence when the database has
// migrations that the source does not. Without AllowInterleaved it returns
// ErrInterleaved when the source has migrations that sort before the latest one
// that stays applied.
//
// The returned Result reflects what was executed; on error it holds the prefix
// that completed.
func (s *Service) Reconcile(ctx context.Context, opts RunOptions) (*Result, error) {
	result := &Result{}

	err := s.withLock(ctx, func() error {
		if err := s.executor.EnsureMigrationsTable(ctx); err != nil {
			return fmt.Errorf("ensure migrations table: %w", err)
		}

		if err := s.refuseIfFailed(ctx); err != nil {
			return err
		}

		fromFiles, fromDB, err := s.GetMigrations(ctx)
		if err != nil {
			return fmt.Errorf("get migrations: %w", err)
		}

		// Reconcile rolls extras back before applying anything, so they must not
		// count for interleaving. Passing true unconditionally is safe: the
		// divergence check below rejects the plan before the interleaving check
		// ever runs when extras exist and AllowRollback is false.
		plan := BuildPlan(fromFiles, fromDB, true)

		if blocked := blockedBy(plan, fromFiles, opts); blocked != nil {
			return blocked
		}

		if len(plan.Interleaved) > 0 {
			s.logger.WarnContext(ctx, "Applying interleaved (out-of-order) migrations",
				"interleaved_migrations", joinIDs(plan.Interleaved))
		}

		executed, execErr := s.executor.ExecutePlan(ctx, plan, fromFiles)
		s.record(result, executed, plan)

		if execErr != nil {
			return fmt.Errorf("execute plan: %w", execErr)
		}

		return nil
	})

	return result, err
}

// Rollback rolls back applied migrations in reverse order.
//
// count <= 0 rolls back all applied migrations; count > 0 rolls back the last N.
//
// The returned Result lists what was actually rolled back.
func (s *Service) Rollback(ctx context.Context, count int) (*Result, error) {
	result := &Result{}

	err := s.withLock(ctx, func() error {
		if err := s.executor.EnsureMigrationsTable(ctx); err != nil {
			return fmt.Errorf("ensure migrations table: %w", err)
		}

		if err := s.refuseIfFailed(ctx); err != nil {
			return err
		}

		appliedMigrations, err := s.executor.GetAppliedMigrations(ctx)
		if err != nil {
			return fmt.Errorf("get applied migrations: %w", err)
		}

		if len(appliedMigrations) == 0 {
			s.logger.InfoContext(ctx, "no applied migrations found")

			return nil
		}

		rollbackCount := count
		if count <= 0 || count > len(appliedMigrations) {
			rollbackCount = len(appliedMigrations)
		}

		// appliedMigrations is in application order, so the last N are the tail.
		candidates := appliedMigrations[len(appliedMigrations)-rollbackCount:]

		ids := make([]string, 0, len(candidates))
		for _, record := range candidates {
			ids = append(ids, record.ID)
		}

		if err := s.executor.EnsureReversible(ctx, ids); err != nil {
			return fmt.Errorf("ensure reversible: %w", err)
		}

		for _, record := range slices.Backward(candidates) {
			if err := s.executor.RollbackMigration(ctx, record.ID); err != nil {
				return fmt.Errorf("rollback %s failed: %w", record.ID, err)
			}

			result.RolledBack = append(result.RolledBack, &Migration{
				ID:        record.ID,
				AppliedAt: record.AppliedAt,
			})
		}

		return nil
	})

	return result, err
}

// joinIDs joins migration IDs as a comma-separated string for log/error context.
func joinIDs(ms []*Migration) string {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}

	return strings.Join(ids, ", ")
}

// BuildReconcilePlan returns the plan Reconcile would execute, without acquiring
// the lock or writing anything.
//
// It does not create the bookkeeping table: a missing table simply means nothing
// has been applied yet, so planning needs no DDL privileges.
func (s *Service) BuildReconcilePlan(ctx context.Context, opts RunOptions) (*ReconcileAnalysis, error) {
	fromFiles, fromDB, err := s.GetMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("get migrations: %w", err)
	}

	plan := BuildPlan(fromFiles, fromDB, true)

	return &ReconcileAnalysis{
		Plan:      plan,
		BlockedBy: blockedBy(plan, fromFiles, opts),
	}, nil
}

// blockedBy returns the guard a Reconcile would trip on, in the order Reconcile
// checks them, or nil if it would run. Reconcile uses it too, so the two cannot
// drift into disagreeing about what is allowed.
func blockedBy(plan *MigrationPlan, fromFiles []*Migration, opts RunOptions) error {
	switch {
	// An empty source is indistinguishable from "roll everything back", and the
	// ways to get one by accident are mundane: an embed.FS used without fs.Sub
	// yields zero migrations because its only entry is a directory, and so does a
	// mistyped path that happens to exist. Checked before divergence because it is
	// the more specific — and more alarming — diagnosis of the same situation.
	case len(fromFiles) == 0 && len(plan.ToRollback) > 0 && !opts.AllowEmptySource:
		return fmt.Errorf(
			"%w: the source contains no migrations, which would roll back all %d applied "+
				"migration(s); check the path or the embed.FS root (go:embed usually needs fs.Sub)",
			ErrEmptySource, len(plan.ToRollback))

	case len(plan.ToRollback) > 0 && !opts.AllowRollback:
		return fmt.Errorf("%w: %s", ErrDivergence, joinIDs(plan.ToRollback))

	case len(plan.Interleaved) > 0 && !opts.AllowInterleaved:
		return fmt.Errorf("%w: %s", ErrInterleaved, joinIDs(plan.Interleaved))

	default:
		return nil
	}
}

// AppliedMigrations returns the bookkeeping rows in application order. A missing
// bookkeeping table yields an empty list rather than an error, so callers can
// ask about a database that has never been migrated.
func (s *Service) AppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	exists, err := s.executor.TableExists(ctx)
	if err != nil {
		return nil, fmt.Errorf("check migrations table: %w", err)
	}

	if !exists {
		return nil, nil
	}

	applied, err := s.executor.GetAppliedMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("get applied migrations: %w", err)
	}

	return applied, nil
}

// Forget removes a migration's bookkeeping row without running its rollback
// script.
//
// It is the escape hatch from ErrFailedMigrations: once an operator has brought
// the schema to a known state by hand, this is how they tell the library to stop
// refusing. It deliberately touches nothing but the ledger — the schema is the
// operator's business, since only they know what state they put it in.
func (s *Service) Forget(ctx context.Context, id string) error {
	return s.withLock(ctx, func() error {
		exists, err := s.executor.TableExists(ctx)
		if err != nil {
			return fmt.Errorf("check migrations table: %w", err)
		}

		if !exists {
			return fmt.Errorf("forget %s: no bookkeeping table exists", id)
		}

		forgotten, err := s.executor.DeleteRecord(ctx, id)
		if err != nil {
			return fmt.Errorf("forget %s: %w", id, err)
		}

		if !forgotten {
			return fmt.Errorf("forget %s: no such migration is recorded", id)
		}

		s.logger.WarnContext(ctx, "Removed a migration's bookkeeping row without running its rollback",
			"id", id)

		return nil
	})
}

// Baseline records every migration up to and including throughID as applied,
// without executing any of them.
//
// It exists for one situation: adopting this tool on a database whose schema
// already exists. The migrations describing that schema cannot be run — the
// tables are already there — but the ledger has to know about them, or the first
// Up would try to create everything from scratch.
//
// Two guards keep this from becoming a way to skip migrations. The bookkeeping
// table must be empty, since adoption happens once; and throughID must name a
// migration that actually exists, so the recorded set is the one the caller
// asked for rather than a silently different prefix.
//
// The scripts are stored exactly as a normal apply would store them, so a later
// rollback of a baselined migration reads back the same text and behaves the
// same way.
func (s *Service) Baseline(ctx context.Context, throughID string) (*Result, error) {
	result := &Result{}

	err := s.withLock(ctx, func() error {
		if err := s.executor.EnsureMigrationsTable(ctx); err != nil {
			return fmt.Errorf("ensure migrations table: %w", err)
		}

		applied, err := s.executor.GetAppliedMigrations(ctx)
		if err != nil {
			return fmt.Errorf("get applied migrations: %w", err)
		}

		if len(applied) > 0 {
			return fmt.Errorf(
				"%w: %d row(s), the most recent being %q; baseline is for adopting this tool on a "+
					"database it has never managed",
				ErrAlreadyRecorded, len(applied), applied[len(applied)-1].ID)
		}

		fromFiles, err := s.source.GetMigrations()
		if err != nil {
			return fmt.Errorf("get migrations from files: %w", err)
		}

		toRecord, err := migrationsThrough(fromFiles, throughID)
		if err != nil {
			return err
		}

		if err := s.executor.RecordApplied(ctx, toRecord); err != nil {
			return fmt.Errorf("record baseline: %w", err)
		}

		result.Applied = toRecord

		s.logger.WarnContext(ctx, "Recorded migrations as applied without running them",
			"count", len(toRecord), "through", throughID)

		return nil
	})

	return result, err
}

// migrationsThrough returns the prefix of fromFiles up to and including
// throughID. fromFiles is sorted by ID, so the prefix is exactly what an Up
// would have applied first.
func migrationsThrough(fromFiles []*Migration, throughID string) ([]*Migration, error) {
	for i, migration := range fromFiles {
		if migration.ID == throughID {
			return fromFiles[:i+1], nil
		}
	}

	return nil, fmt.Errorf("%w: %q", ErrBaselineNotFound, throughID)
}

// GetMigrations reads migrations from files and DB. fromFiles is sorted by ID;
// fromDB preserves the order the migrations were applied in, which is what the
// reverse-order rollback logic depends on.
func (s *Service) GetMigrations(ctx context.Context) (fromFiles, fromDB []*Migration, _ error) {
	// Get migrations from files
	fromFiles, err := s.source.GetMigrations()
	if err != nil {
		return nil, nil, fmt.Errorf("get migrations from files: %w", err)
	}

	// Get migration records from DB
	appliedMigrations, err := s.AppliedMigrations(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Convert AppliedMigration to Migration for uniformity. The order comes from
	// the executor, which returns rows by their monotonic sequence; re-sorting by
	// AppliedAt here would reintroduce the wall-clock ordering problem it exists
	// to avoid.
	fromDB = make([]*Migration, 0, len(appliedMigrations))
	for _, record := range appliedMigrations {
		fromDB = append(fromDB, &Migration{
			ID:        record.ID,
			AppliedAt: record.AppliedAt,
		})
	}

	return fromFiles, fromDB, nil
}

// withLock executes a function with lock acquisition and release.
func (s *Service) withLock(ctx context.Context, fn func() error) error {
	s.logger.InfoContext(ctx, "Acquiring migration lock...")

	if err := s.locker.AcquireLock(ctx); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer func() {
		if err := s.locker.ReleaseLock(ctx); err != nil {
			s.logger.WarnContext(ctx, "Failed to release lock", "error", err)
		}
	}()

	s.logger.InfoContext(ctx, "Lock acquired successfully")

	return fn()
}

// record folds an execution outcome into result. executed may be nil when
// ExecutePlan failed before doing anything.
func (s *Service) record(result *Result, executed *ExecutionResult, plan *MigrationPlan) {
	if executed == nil {
		return
	}

	result.Applied = executed.Applied
	result.RolledBack = executed.RolledBack

	// Interleaved is only meaningful for migrations that actually went in.
	appliedIDs := make(map[string]bool, len(executed.Applied))
	for _, m := range executed.Applied {
		appliedIDs[m.ID] = true
	}

	for _, m := range plan.Interleaved {
		if appliedIDs[m.ID] {
			result.Interleaved = append(result.Interleaved, m)
		}
	}
}

// refuseIfFailed stops the run when any recorded migration failed, whether or
// not it is one this operation would touch.
//
// This is deliberately fail-fast rather than self-healing. A failed migration can
// only come from a "notransaction" script that died partway, which leaves the
// schema in a state no migration describes. Rolling it back automatically means
// running a script against a state it was never written for, and rolling back
// what came after it means destroying successful work — both guesses, made
// without asking. Refusing costs a human a few minutes; guessing wrong costs
// data.
func (s *Service) refuseIfFailed(ctx context.Context) error {
	applied, err := s.executor.GetAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("get applied migrations: %w", err)
	}

	var failed []string

	for _, m := range applied {
		if m.Error != nil {
			failed = append(failed, m.ID)
			s.logger.ErrorContext(ctx, "Migration recorded as failed", "id", m.ID, "error", *m.Error)
		}
	}

	if len(failed) == 0 {
		return nil
	}

	return fmt.Errorf(
		"%w: %s; the schema is in a state no migration describes, so nothing will be applied or "+
			"rolled back until this is resolved by hand: inspect the recorded error and the stored "+
			"down_script, bring the schema to a known state, then delete the row(s)",
		ErrFailedMigrations, strings.Join(failed, ", "),
	)
}
