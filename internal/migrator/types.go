package migrator

import "time"

// Migration represents a database migration with Up and Down scripts.
type Migration struct {
	AppliedAt time.Time // When the migration was applied to the database
	// ID is the shared base name of the migration's file pair, without the
	// .up.sql / .down.sql suffix (e.g. "001_create_users" for
	// 001_create_users.up.sql and 001_create_users.down.sql). Compared
	// lexicographically, so numeric prefixes must be zero-padded to a
	// consistent width.
	ID string

	// UpScript and DownScript hold the file contents verbatim, including the
	// optional "-- +migrate <directive>" header line. Both are stored in the
	// database, and DownScript is read back from there at rollback time rather
	// than from the file source, so that rollback works when the checked-out
	// branch no longer contains the migration. Directives are therefore always
	// parsed out of the script text itself (see ParseDirectiveHeader) rather
	// than carried alongside it.
	//
	// Note: a "notransaction" Up script executes statement-by-statement outside a
	// transaction, so a failure partway through leaves the schema partially
	// migrated, with the earlier statements already committed. Nothing tries to
	// undo that automatically — the failure is recorded and every later run
	// refuses until a human resolves it — but a DownScript written to be
	// idempotent ("DROP ... IF EXISTS" and similar) is what makes that resolution
	// straightforward. Not enforced by the type system.
	UpScript   string
	DownScript string
}
