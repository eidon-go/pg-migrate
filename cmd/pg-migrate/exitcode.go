package main

import (
	"errors"

	migrate "github.com/eidon-go/pg-migrate"
)

// Exit codes beyond the generic failure, so a deployment pipeline can tell
// "needs a human" from "try again later" without parsing log text. They are part
// of the CLI's interface: a code, once released, keeps its meaning.
//
// 2 is left unused, since shells and many tools already read it as a usage
// error.
const (
	exitFailure = 1

	// exitNeedsHuman: the database and the ledger are in a state no automated
	// run may act on. Retrying cannot help.
	exitNeedsHuman = 3

	// exitLockTimeout: another run holds the advisory lock. Transient; retrying
	// later is the right response.
	exitLockTimeout = 4

	// exitRefused: the run was refused before it changed anything, because going
	// ahead needs a flag or a decision the invocation did not carry.
	exitRefused = 5
)

// exitCode maps an error returned by run to the process exit status.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, migrate.ErrFailedMigrations),
		errors.Is(err, migrate.ErrUnrecorded):
		return exitNeedsHuman
	case errors.Is(err, migrate.ErrLockTimeout):
		return exitLockTimeout
	case errors.Is(err, migrate.ErrDivergence),
		errors.Is(err, migrate.ErrInterleaved),
		errors.Is(err, migrate.ErrEmptySource),
		errors.Is(err, migrate.ErrIrreversible),
		errors.Is(err, migrate.ErrAlreadyRecorded),
		errors.Is(err, migrate.ErrBaselineNotFound):
		return exitRefused
	default:
		return exitFailure
	}
}
