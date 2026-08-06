package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eidon-go/pg-migrate/internal/sqlconn"
)

const (
	// MigrationLockID is the constant used for migration advisory lock
	// Using a large prime number to avoid conflicts.
	defaultLockID int64 = 2718281828459045235

	defaultLockTimeout = 5 * time.Second

	// unlockTimeout bounds how long ReleaseLock waits for pg_advisory_unlock.
	// Releasing must not be skipped: (*sql.Conn).Close returns the connection to
	// the pool rather than terminating the session, so a session-level advisory
	// lock survives it and would be held by a pooled connection indefinitely.
	unlockTimeout = 5 * time.Second

	// cleanupTimeout bounds the best-effort statements that make the dedicated
	// connection safe to hand back to the pool.
	cleanupTimeout = 5 * time.Second

	// lockTimeoutErrCode is SQLSTATE 55P03 lock_not_available, which Postgres
	// raises when lock_timeout elapses while pg_advisory_lock waits. Because the
	// server aborts the statement itself, this code means the lock was
	// definitively NOT granted — unlike a client-side cancellation, which leaves
	// it unknown whether the grant landed.
	lockTimeoutErrCode = "55P03"
)

type PostgresConfig struct {
	// DSN, when set, is used verbatim and every discrete field below is ignored.
	// Both the URL form ("postgres://user:pass@host/db?sslmode=require") and the
	// keyword/value form ("host=... dbname=...") are accepted, since pgx parses
	// either.
	DSN string

	Host     string
	Port     string
	User     string
	Password string
	DBName   string

	// SSLMode maps to libpq's sslmode. Empty leaves it unset, so the driver's
	// own default applies ("prefer": TLS when the server offers it, plaintext
	// otherwise). Managed Postgres generally needs "require" or stricter.
	SSLMode string

	// LockTimeout bounds the wait for the advisory lock. Zero is ambiguous on its
	// own, so LockTimeoutSet says whether it was configured: unset means the
	// default, an explicit zero means wait indefinitely (Postgres' lock_timeout=0).
	LockTimeout    time.Duration
	LockTimeoutSet bool

	LockID int64
}

// GetDataSource returns the connection string.
//
// A DSN set explicitly is returned as-is. Otherwise a keyword/value string is
// built from the discrete fields, quoting values per libpq's rules: a value is
// wrapped in single quotes (with internal \ and ' backslash-escaped) when it
// contains whitespace, a single quote or a backslash.
//
// Empty fields are omitted rather than emitted as empty values, so that the
// driver's defaults and the standard PG* environment variables still apply to
// anything not configured here. Emitting them bare would be unsafe in any case:
// libpq's parser swallows the next key=value pair as the value of an empty key.
func (c PostgresConfig) GetDataSource() string {
	if c.DSN != "" {
		return c.DSN
	}

	fields := []struct{ key, value string }{
		{"host", c.Host},
		{"port", c.Port},
		{"user", c.User},
		{"password", c.Password},
		{"dbname", c.DBName},
		{"sslmode", c.SSLMode},
	}

	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.value == "" {
			continue
		}

		parts = append(parts, f.key+"="+quoteDSNValue(f.value))
	}

	return strings.Join(parts, " ")
}

func quoteDSNValue(s string) string {
	if !strings.ContainsAny(s, " '\\") {
		return s
	}

	var b strings.Builder
	b.WriteByte('\'')

	for _, r := range s {
		if r == '\'' || r == '\\' {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	b.WriteByte('\'')

	return b.String()
}

type Postgres struct {
	db             *sql.DB
	lockConn       *sql.Conn
	lockTimeout    time.Duration
	lockID         int64
	lockMu         sync.Mutex
	isLockAcquired bool
}

// NewPostgresFromDB wraps an already-open *sql.DB (opened by the caller with
// whatever driver they choose) in a *Postgres. See internal/dbconn for a
// pgx-backed constructor that opens the connection itself.
func NewPostgresFromDB(db *sql.DB, cfg PostgresConfig) *Postgres {
	lockTimeout := cfg.LockTimeout
	if !cfg.LockTimeoutSet && lockTimeout == 0 {
		lockTimeout = defaultLockTimeout
	}

	lockID := cfg.LockID
	if lockID == 0 {
		lockID = defaultLockID
	}

	return &Postgres{
		db:             db,
		lockTimeout:    lockTimeout,
		lockMu:         sync.Mutex{},
		lockID:         lockID,
		isLockAcquired: false,
	}
}

func (p *Postgres) GetDB() *sql.DB {
	return p.db
}

func (p *Postgres) Close() error {
	if p.db != nil {
		if err := p.db.Close(); err != nil {
			return fmt.Errorf("close database: %w", err)
		}
	}

	return nil
}

// AcquireLock acquires the advisory lock on a dedicated connection, waiting at
// most the configured lock timeout.
//
// The timeout is enforced by the server via lock_timeout rather than by
// cancelling the query from the client. That distinction matters: a cancelled
// query may already have been granted the lock, and since the connection then
// goes back to the pool rather than being closed, the lock would be stranded on
// a pooled connection with nothing tracking it. A server-side abort is
// unambiguous — the lock was not granted.
//
// lock_timeout is set with transaction scope, so it reverts on COMMIT and never
// leaks onto the pooled connection. The advisory lock is session-scoped and
// outlives that transaction.
func (p *Postgres) AcquireLock(ctx context.Context) error {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()

	if p.isLockAcquired {
		return ErrLockAlreadyAcquired
	}

	// The lock connection is pinned for the whole migration run while
	// CustomExecutor keeps issuing queries through the same *sql.DB. With
	// only 1 allowed connection, the lock would permanently own the only
	// slot and every other query would hang forever with no way to time
	// out. Fail fast instead.
	if p.db.Stats().MaxOpenConnections == 1 {
		return ErrPoolTooSmall
	}

	// Retrieve a dedicated connection from pool
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get dedicated connection for lock: %w", err)
	}

	if err := p.lockOnConn(ctx, conn); err != nil {
		// The lock may or may not have been granted (a cancelled query leaves
		// that unknown), so scrub the connection before it rejoins the pool.
		releaseAllAndClose(ctx, conn)

		return err
	}

	p.lockConn = conn
	p.isLockAcquired = true

	return nil
}

// ReleaseLock releases the advisory lock and returns the dedicated connection to
// the pool.
//
// The release is not optional. (*sql.Conn).Close only returns the connection to
// the pool — the session stays alive, so a session-level advisory lock survives
// it. If the lock cannot be confirmed released, the connection is destroyed
// rather than reused, since Postgres does release the lock when the session
// actually ends.
func (p *Postgres) ReleaseLock(ctx context.Context) error {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()

	if !p.isLockAcquired || p.lockConn == nil {
		return ErrLockNotAcquired
	}

	conn := p.lockConn
	p.lockConn = nil
	p.isLockAcquired = false

	// Use a non-cancelled context so unlock still runs when the caller's
	// context was cancelled by the time we hit the deferred ReleaseLock.
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()

	var unlocked bool

	err := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", p.lockID).Scan(&unlocked)

	switch {
	case err != nil:
		// Unknown whether the lock is still held; do not hand this connection
		// back to the pool holding it.
		sqlconn.Destroy(conn)

		return fmt.Errorf("release advisory lock: %w", err)

	case !unlocked:
		// Postgres reports we did not hold it. Nothing to strand, but the state
		// disagrees with ours, so report it.
		_ = conn.Close()

		return ErrLockNotHeld

	default:
		if err := conn.Close(); err != nil {
			return fmt.Errorf("return lock connection to pool: %w", err)
		}

		return nil
	}
}

// lockOnConn takes the advisory lock on conn under a transaction-scoped
// lock_timeout.
func (p *Postgres) lockOnConn(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lock transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// SET does not accept bind parameters; set_config with is_local=true is the
	// parameterized equivalent of SET LOCAL.
	milliseconds := strconv.FormatInt(p.lockTimeout.Milliseconds(), 10)
	if _, err := tx.ExecContext(ctx, "SELECT set_config('lock_timeout', $1, true)", milliseconds); err != nil {
		return fmt.Errorf("set lock timeout: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_lock($1)", p.lockID); err != nil {
		if isLockTimeout(err) {
			return fmt.Errorf("%w after %s: another migration process is holding it",
				ErrLockTimeout, p.lockTimeout)
		}
		// lock_timeout = 0 waits forever, so only the caller's context can end the
		// wait; say so rather than reporting a bare cancellation.
		if p.lockTimeout == 0 && ctx.Err() != nil {
			return fmt.Errorf("acquire advisory lock: waiting indefinitely (lock timeout 0) "+
				"until the context ended: %w", err)
		}

		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	// Committing keeps the session-level advisory lock and drops the
	// transaction-scoped lock_timeout.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lock transaction: %w", err)
	}

	return nil
}

// releaseAllAndClose drops every session-level advisory lock on conn before
// returning it to the pool, for paths where it is unknown whether a lock was
// granted. pg_advisory_unlock_all is safe when nothing is held.
//
// Cancellation is stripped from ctx: this runs on the failure path, where the
// caller's context is often already cancelled, and skipping the cleanup is what
// strands a lock on a pooled connection.
func releaseAllAndClose(ctx context.Context, conn *sql.Conn) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if _, err := conn.ExecContext(cleanupCtx, "SELECT pg_advisory_unlock_all()"); err != nil {
		// Could not prove the connection is clean, so do not reuse it.
		sqlconn.Destroy(conn)

		return
	}

	_ = conn.Close()
}

// isLockTimeout reports whether err is Postgres' lock_not_available, raised when
// lock_timeout elapses. Matched on the SQLSTATE text so that no driver-specific
// error type is needed here — internal/db stays driver-agnostic.
func isLockTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), lockTimeoutErrCode)
}
