package migrator

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultTableName is the bookkeeping table used when none is configured.
const DefaultTableName = "migrations"

// maxIdentifierBytes is Postgres' NAMEDATALEN-1. Longer identifiers are
// silently truncated by the server, which would quietly point the library at a
// different table than the caller named, so they are rejected instead.
const maxIdentifierBytes = 63

// TableConfig names the bookkeeping table. The zero value means the default
// table name, unqualified.
type TableConfig struct {
	// Schema qualifies the table. Empty means the table is left unqualified and
	// resolved through the connection's search_path, which is the default so
	// that per-tenant search_path setups keep working.
	Schema string

	// Name is the table name. Empty means DefaultTableName.
	Name string
}

// tableRef is a validated, quoted table reference ready to interpolate into a
// statement.
type tableRef string

// resolve validates the configuration and renders the quoted reference.
//
// Identifiers are quoted, which makes them case-sensitive: a Name of "Migrations"
// refers to a table that must be spelled that way, not folded to lower case.
func (c TableConfig) resolve() (tableRef, error) {
	name := c.Name
	if name == "" {
		name = DefaultTableName
	}

	quotedName, err := quoteIdentifier(name)
	if err != nil {
		return "", fmt.Errorf("migrations table name: %w", err)
	}

	if c.Schema == "" {
		return tableRef(quotedName), nil
	}

	quotedSchema, err := quoteIdentifier(c.Schema)
	if err != nil {
		return "", fmt.Errorf("migrations table schema: %w", err)
	}

	return tableRef(quotedSchema + "." + quotedName), nil
}

// quoteIdentifier renders s as a quoted Postgres identifier. Quoting is what
// makes an arbitrary caller-supplied name safe to interpolate into a statement,
// since identifiers cannot be passed as bind parameters.
func quoteIdentifier(s string) (string, error) {
	switch {
	case s == "":
		return "", errors.New("must not be empty")
	case len(s) > maxIdentifierBytes:
		return "", fmt.Errorf("%q is %d bytes, over the Postgres limit of %d", s, len(s), maxIdentifierBytes)
	case strings.ContainsRune(s, 0):
		return "", fmt.Errorf("%q contains a NUL byte", s)
	}

	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`, nil
}
