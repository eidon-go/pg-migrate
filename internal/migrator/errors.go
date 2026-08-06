package migrator

import "errors"

// ErrDivergence is returned when the database has migrations applied that are
// not present in the source files and rollback has not been explicitly allowed.
// The error wraps additional context with the list of diverging migration IDs.
var ErrDivergence = errors.New("divergence detected: database has migrations applied that are not present in files")

// ErrInterleaved is returned when the source files contain migrations that
// are lexicographically smaller than the latest migration applied in the
// database (out-of-order apply) and interleaving has not been explicitly allowed.
// The error wraps additional context with the list of interleaved migration IDs.
var ErrInterleaved = errors.New(
	"interleaved migrations detected: files contain migrations lexicographically smaller than the latest applied migration",
)

// ErrFailedMigrations is returned when the bookkeeping table contains a
// migration recorded as failed. Nothing is applied or rolled back while such a
// row exists: the schema is in a state nobody designed, so acting on it
// automatically could only compound the damage. The error names the migrations
// and what to do; resolving it is a manual step.
//
// This is a terminal state, not a transient failure — a deployment pipeline
// should stop and page a human rather than retry.
var ErrFailedMigrations = errors.New("database contains failed migrations")

// ErrEmptySource is returned by Reconcile when the file source contains no
// migrations at all but the database has some, which would mean rolling the
// schema back to nothing. Nothing was changed. Almost always a wrong path or an
// embed.FS that needs fs.Sub; if it really is intended, opt in explicitly.
var ErrEmptySource = errors.New("migration source is empty")

// ErrUnrecorded is returned when a "notransaction" script changed the schema but
// its bookkeeping row could not be written, updated or removed to match. The
// database and the ledger now disagree, and no automated run can safely continue:
// reconcile them by hand.
var ErrUnrecorded = errors.New("schema was changed but the bookkeeping row could not be updated to match")

// ErrIrreversible is returned when a rollback would touch a migration whose
// .down.sql is marked with the "irreversible" directive. Nothing is rolled back:
// the whole operation is refused before it starts, rather than discovering the
// problem partway through.
var ErrIrreversible = errors.New("migration is marked irreversible and cannot be rolled back")
