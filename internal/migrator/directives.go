package migrator

import (
	"fmt"
	"slices"
	"strings"
)

// File suffixes of a migration pair. Defined here because Migration.ID is
// derived from them and because directive errors need to name them; the source
// package reads them rather than repeating the strings.
const (
	UpFileSuffix   = ".up.sql"
	DownFileSuffix = ".down.sql"
)

// Directive lines look like "-- +migrate <name> [<name>...]" and are only
// recognized on the first line of a migration script.
const (
	commentToken   = "--"
	migrateToken   = "+migrate"
	directiveStart = commentToken + " " + migrateToken

	// migrateTokenPrefix is the shortest prefix that identifies an attempt to
	// write migrateToken, used to catch typos such as "+migrat" or "+migrates"
	// instead of letting them pass as ordinary comments.
	migrateTokenPrefix = "+migrat"
)

// Statement block markers share the directive prefix but are not directives:
// they delimit a manual statement boundary inside a script body, so they must
// never be treated as a header or validated against the whitelist.
const (
	statementBeginMarker = "StatementBegin"
	statementEndMarker   = "StatementEnd"

	statementBeginLine = directiveStart + " " + statementBeginMarker
	statementEndLine   = directiveStart + " " + statementEndMarker
)

// directiveNoTransaction runs the script statement-by-statement outside a
// transaction. Required for statements Postgres refuses inside one, such as
// CREATE INDEX CONCURRENTLY.
const directiveNoTransaction = "notransaction"

// DirectiveIrreversible marks a .down.sql as deliberately empty: the migration
// cannot be undone, so any attempt to roll it back must fail rather than
// silently do nothing. It exists so that "I decided this cannot be rolled back"
// is distinguishable from "I forgot to write the rollback".
const DirectiveIrreversible = "irreversible"

// Section markers from the superseded single-file format. They are recognized
// only to produce a useful error: anyone carrying files over from that format,
// or copying an example from sql-migrate or goose, lands here first.
const (
	legacyUpMarker   = "Up"
	legacyDownMarker = "Down"
)

// knownDirectives is the whitelist. An unrecognized name is a parse error
// rather than a silent no-op: a typo'd or wrong-case directive would otherwise
// leave the migration running under semantics its author did not intend — for
// example inside a transaction when it must not be.
var knownDirectives = map[string]bool{
	directiveNoTransaction: true,
	DirectiveIrreversible:  true,
}

// DirectiveHeaderFor renders the header line that sets the named directive, so
// that error messages quote the exact text a user has to write.
func DirectiveHeaderFor(name string) string {
	return directiveStart + " " + name
}

// directiveNames returns the directive names on line if it is a well-formed
// directive line that is not a statement block marker. ok is false for any
// other line, including the statement markers.
func directiveNames(line string) (names []string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || fields[0] != commentToken || fields[1] != migrateToken {
		return nil, false
	}

	if len(fields) > 2 && (fields[2] == statementBeginMarker || fields[2] == statementEndMarker) {
		return nil, false
	}

	return fields[2:], true
}

// looksLikeDirective reports whether line is a comment that was probably meant
// to be a directive but is malformed — "--+migrate notransaction" (no space)
// or "-- +migrates notransaction" (misspelled). Deliberately narrow: an
// ordinary comment that merely mentions +migrate must not trip it.
func looksLikeDirective(line string) bool {
	fields := strings.Fields(strings.TrimSpace(line))
	switch {
	case len(fields) == 0:
		return false
	case strings.HasPrefix(fields[0], commentToken+migrateTokenPrefix):
		return true // comment token fused to the directive
	case fields[0] == commentToken && len(fields) > 1:
		return strings.HasPrefix(fields[1], migrateTokenPrefix)
	default:
		return false
	}
}

// isStatementMarkerLine reports whether line is a statement block marker, which
// may legitimately be the first line of a script that has no directive header.
func isStatementMarkerLine(line string) bool {
	trimmed := strings.TrimSpace(line)

	return strings.HasPrefix(trimmed, statementBeginLine) || strings.HasPrefix(trimmed, statementEndLine)
}

// ParseDirectiveHeader splits a migration script into its optional directive
// header and its SQL body.
//
// The header, if present, must be the very first line of the script:
//
//	-- +migrate notransaction
//	CREATE INDEX CONCURRENTLY idx ON t(x);
//
// A directive line anywhere other than the first line is an error rather than
// being ignored, and so is an unknown or malformed directive — a directive that
// silently fails to apply is the failure mode this validation exists to
// prevent.
func ParseDirectiveHeader(script string) (directives map[string]bool, body string, err error) {
	directives = make(map[string]bool)

	head, rest, _ := strings.Cut(script, "\n")

	// bodyLine1 is the script line number that the body starts on, so that
	// error messages point at the real line in the file.
	bodyLine1 := 1

	names, isDirective := directiveNames(head)
	switch {
	case isDirective:
		if len(names) == 0 {
			return nil, "", fmt.Errorf("directive line %q names no directive", strings.TrimSpace(head))
		}

		for _, name := range names {
			switch {
			case knownDirectives[name]:
				directives[name] = true
			case name == legacyUpMarker || name == legacyDownMarker:
				return nil, "", fmt.Errorf(
					"%q is a section marker from the single-file format, which this version no longer uses: "+
						"put the Up SQL in <id>%s and the Down SQL in <id>%s, and drop the marker line",
					name, UpFileSuffix, DownFileSuffix,
				)
			default:
				return nil, "", fmt.Errorf(
					"unknown directive %q (known: %s); directives are case-sensitive",
					name, strings.Join(sortedKnownDirectives(), ", "),
				)
			}
		}

		body = rest
		bodyLine1 = 2

	case looksLikeDirective(head) && !isStatementMarkerLine(head):
		return nil, "", fmt.Errorf(
			"malformed directive line %q: expected %q followed by directive names",
			strings.TrimSpace(head), directiveStart,
		)

	default:
		body = script
	}

	if err := checkNoLateDirectives(body, bodyLine1); err != nil {
		return nil, "", err
	}

	return directives, body, nil
}

// checkNoLateDirectives rejects directive lines below the first line of the
// script, which Postgres would read as plain comments and silently ignore.
func checkNoLateDirectives(body string, bodyLine1 int) error {
	for i, line := range strings.Split(body, "\n") {
		if _, ok := directiveNames(line); ok {
			return fmt.Errorf(
				"directive line %q found on line %d: directives are only recognized on the first line of a script",
				strings.TrimSpace(line), bodyLine1+i,
			)
		}
	}

	return nil
}

func sortedKnownDirectives() []string {
	names := make([]string, 0, len(knownDirectives))
	for name := range knownDirectives {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}
