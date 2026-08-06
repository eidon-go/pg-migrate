package migrator

import (
	"strings"
	"testing"
)

// FuzzSplitSQLStatements exercises the statement scanner, which is the most
// state-heavy parser in the project: quoted literals, E-strings, dollar quoting
// with arbitrary tags, nestable block comments and manual statement markers all
// interact in one pass. A wrong boundary here silently changes what runs against
// the database.
func FuzzSplitSQLStatements(f *testing.F) {
	seeds := []string{
		"",
		";",
		";;;",
		"SELECT 1;",
		"SELECT 1; SELECT 2;",
		"SELECT 1", // no trailing semicolon
		"-- just a comment",
		"/* block */ SELECT 1;",
		"/* nested /* deeper */ still */ SELECT 1;",
		"/* unterminated",
		"SELECT ';';",
		"SELECT '';",
		"SELECT '''';",
		`SELECT E'\';';`,
		`SELECT e'\\';`,
		`SELECT "col;umn" FROM t;`,
		`SELECT """";`,
		"SELECT $$ body; with semi $$;",
		"SELECT $tag$ body; $tag$;",
		"SELECT $$ unterminated;",
		"SELECT $1;", // parameter placeholder, not a dollar tag
		"SELECT $_x$ a; $_x$;",
		"CREATE FUNCTION f() AS $$ BEGIN SELECT 1; END; $$ LANGUAGE plpgsql;",
		statementBeginLine + "\nSELECT 1; SELECT 2;\n" + statementEndLine,
		statementBeginLine + "\nSELECT 1;",     // never closed
		statementEndLine + "\nSELECT 1;",       // stray end marker
		"SELECT '" + statementBeginLine + "';", // marker inside a literal
		"SELECT 1; -- trailing\nSELECT 2;",
		"SELECT 1;\n\n\n;\n;",
		"SELECT 'ünïcodé';",
		"SELECT $ünï$ x; $ünï$;",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, script string) {
		statements, err := splitSQLStatements(script)
		if err != nil {
			// The only reported failure is an unclosed statement block; nothing
			// may be returned alongside it.
			if statements != nil {
				t.Fatalf("statements returned alongside error: %q", statements)
			}

			return
		}

		total := 0

		for i, stmt := range statements {
			// appendStatement is supposed to drop anything with nothing to run,
			// so an empty or comment-only statement escaping here would mean the
			// database is asked to execute nothing at all.
			if strings.TrimSpace(stmt) == "" {
				t.Fatalf("statement %d is blank", i)
			}

			if isEmptyStatement(stmt) {
				t.Fatalf("statement %d has nothing to execute: %q", i, stmt)
			}

			total += len(stmt)
		}

		// Statements are cut from the input, never synthesised. Producing more
		// bytes than were read would mean the scanner duplicated a segment —
		// which, for SQL, means running something twice.
		if total > len(script) {
			t.Fatalf("output grew: %d bytes of statements from %d bytes of script (%q)",
				total, len(script), statements)
		}
	})
}

// FuzzParseDirectiveHeader checks the directive parser, whose whole purpose is
// to reject anything it does not understand: a directive that silently fails to
// apply would run a "notransaction" script inside a transaction, or treat a
// missing rollback as an intentional one.
func FuzzParseDirectiveHeader(f *testing.F) {
	seeds := []string{
		"",
		"SELECT 1;",
		"-- +migrate notransaction\nSELECT 1;",
		"-- +migrate irreversible",
		"-- +migrate notransaction irreversible",
		"-- +migrate",               // names nothing
		"-- +migrate NoTransaction", // wrong case
		"-- +migrate unknown",       // unknown name
		"--+migrate notransaction",  // comment token fused
		"-- +migrateNotADirective",
		"SELECT 1;\n-- +migrate notransaction", // directive below line 1
		"-- +migrate Up",                       // legacy section marker
		"-- +migrate Down",
		statementBeginLine + "\nSELECT 1;\n" + statementEndLine,
		"   -- +migrate notransaction   \nSELECT 1;",
		"-- +migrate notransaction\r\nSELECT 1;",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, script string) {
		directives, body, err := ParseDirectiveHeader(script)
		if err != nil {
			return
		}

		// Only known directives may come back, or a caller switching on the map
		// would silently ignore something the author wrote.
		for name := range directives {
			if !knownDirectives[name] {
				t.Fatalf("unknown directive %q accepted", name)
			}
		}

		// The body is the script minus at most its first line — never rewritten,
		// since it is the SQL that will be executed verbatim.
		if !strings.HasSuffix(script, body) {
			t.Fatalf("body is not a suffix of the script:\nscript=%q\nbody=%q", script, body)
		}

		if len(directives) == 0 && body != script {
			t.Fatalf("body changed with no directive parsed:\nscript=%q\nbody=%q", script, body)
		}
	})
}

// FuzzQuoteIdentifier is the security-relevant one: table and schema names
// cannot be passed as bind parameters, so this quoting is the only thing
// standing between a caller-supplied name and string interpolation into SQL.
func FuzzQuoteIdentifier(f *testing.F) {
	seeds := []string{
		"",
		"migrations",
		"Migrations",
		`with"quote`,
		`""`,
		`"; DROP TABLE users; --`,
		"with space",
		"ünïcodé",
		"\x00",
		"tab\there",
		"new\nline",
		strings.Repeat("a", 63),
		strings.Repeat("a", 64),
		strings.Repeat("ü", 40),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		quoted, err := quoteIdentifier(name)
		if err != nil {
			if quoted != "" {
				t.Fatalf("value returned alongside error: %q", quoted)
			}

			return
		}

		if len(quoted) < 2 || !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) {
			t.Fatalf("not delimited by double quotes: %q", quoted)
		}

		// The interior must contain no lone double quote: every one has to be
		// doubled, or the identifier would terminate early and the remainder
		// would be parsed as SQL.
		interior := quoted[1 : len(quoted)-1]
		for i := 0; i < len(interior); i++ {
			if interior[i] != '"' {
				continue
			}

			if i+1 >= len(interior) || interior[i+1] != '"' {
				t.Fatalf("lone double quote at %d in %q (from %q)", i, quoted, name)
			}

			i++ // skip the pair
		}

		// A NUL would truncate the identifier inside the server's C string
		// handling, so it must never reach the wire.
		if strings.ContainsRune(quoted, 0) {
			t.Fatalf("NUL byte survived quoting: %q", name)
		}
	})
}
