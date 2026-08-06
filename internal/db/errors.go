package db

import "errors"

// ErrPoolTooSmall is returned by AcquireLock when the underlying *sql.DB is
// configured with SetMaxOpenConns(1). The advisory lock pins a dedicated
// connection for the whole migration run, so at least 2 concurrent
// connections are required or every other query would block forever waiting
// for a connection that can never free up.
var ErrPoolTooSmall = errors.New(
	"sql.DB allows only 1 open connection: the advisory lock pins a dedicated " +
		"connection for the whole migration run, so at least 2 concurrent connections " +
		"are required (call SetMaxOpenConns(0) or >= 2)",
)

// ErrLockAlreadyAcquired is returned by AcquireLock when called on a
// *Postgres that already holds the advisory lock.
var ErrLockAlreadyAcquired = errors.New("lock already acquired")

// ErrLockNotAcquired is returned by ReleaseLock when called on a *Postgres
// that does not currently hold the advisory lock.
var ErrLockNotAcquired = errors.New("lock not acquired")

// ErrLockTimeout is returned by AcquireLock when the configured lock timeout
// elapsed while waiting, which means another process holds the migration lock.
// Postgres aborts the waiting statement itself, so this reliably means the lock
// was not granted.
var ErrLockTimeout = errors.New("timed out waiting for the migration advisory lock")

// ErrLockNotHeld is returned by ReleaseLock when pg_advisory_unlock reports the
// lock was not held by this session, meaning something released it out of band.
var ErrLockNotHeld = errors.New("advisory lock was not held at release time")
