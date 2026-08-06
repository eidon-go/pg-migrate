package source

import (
	"fmt"
	"strings"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

// parseScript validates one migration script file and returns the content to
// store, verbatim except for surrounding whitespace.
//
// The directive header (if any) is deliberately kept: the stored script is what
// the rollback path reads back from the database, and it re-parses directives
// from that text.
//
// allowIrreversible is set for the .down.sql half, where a body may legitimately
// be absent if the migration is explicitly marked irreversible.
func parseScript(filename, content string, allowIrreversible bool) (string, error) {
	script := strings.TrimSpace(content)

	directives, body, err := migrator.ParseDirectiveHeader(script)
	if err != nil {
		return "", fmt.Errorf("migration file %s: %w", filename, err)
	}

	// Checked here so a malformed statement block is reported when the files are
	// read, not partway through a migration.
	if err := migrator.ValidateStatementBlocks(body); err != nil {
		return "", fmt.Errorf("migration file %s: %w", filename, err)
	}

	irreversible := directives[migrator.DirectiveIrreversible]
	hasSQL := strings.TrimSpace(body) != ""

	switch {
	case irreversible && !allowIrreversible:
		return "", fmt.Errorf(
			"migration file %s: the %q directive only belongs in a %s file",
			filename, migrator.DirectiveIrreversible, migrator.DownFileSuffix)

	case irreversible && hasSQL:
		return "", fmt.Errorf(
			"migration file %s: marked %q but still contains SQL; a migration is either reversible or not",
			filename, migrator.DirectiveIrreversible)

	case irreversible:
		return script, nil

	case !hasSQL && allowIrreversible:
		return "", fmt.Errorf(
			"migration file %s: contains no SQL; write the rollback, or state that there is none with "+
				"a first line of %q",
			filename, migrator.DirectiveHeaderFor(migrator.DirectiveIrreversible))

	case !hasSQL:
		return "", fmt.Errorf("migration file %s: contains no SQL", filename)

	default:
		return script, nil
	}
}

// ParseMigrationPair builds a Migration from the contents of an
// <id>.up.sql / <id>.down.sql file pair.
//
// Each file is plain SQL, optionally preceded by a single directive line:
//
//	-- +migrate notransaction
//	CREATE INDEX CONCURRENTLY idx_users_email ON users(email);
//
// A migration that cannot be undone says so in its .down.sql instead of leaving
// it empty:
//
//	-- +migrate irreversible
func ParseMigrationPair(id string, upContent, downContent []byte) (*migrator.Migration, error) {
	up, err := parseScript(id+upSuffix, string(upContent), false)
	if err != nil {
		return nil, err
	}

	down, err := parseScript(id+downSuffix, string(downContent), true)
	if err != nil {
		return nil, err
	}

	return &migrator.Migration{
		ID:         id,
		UpScript:   up,
		DownScript: down,
	}, nil
}
