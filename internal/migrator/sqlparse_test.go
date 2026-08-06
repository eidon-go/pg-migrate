package migrator

import (
	"strings"
	"testing"
)

func TestSplitSQLStatements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		script   string
		expected []string
	}{
		{
			name: "simple statements",
			script: `CREATE TABLE users (id INT PRIMARY KEY);
INSERT INTO users VALUES (1);
SELECT * FROM users;`,
			expected: []string{
				"CREATE TABLE users (id INT PRIMARY KEY);",
				"INSERT INTO users VALUES (1);",
				"SELECT * FROM users;",
			},
		},
		{
			name: "statements with comments",
			script: `-- Create users table
CREATE TABLE users (id INT);
-- Insert data; this semicolon is in comment
INSERT INTO users VALUES (1);`,
			expected: []string{
				"-- Create users table\nCREATE TABLE users (id INT);",
				"-- Insert data; this semicolon is in comment\nINSERT INTO users VALUES (1);",
			},
		},
		{
			name: "StatementBegin/End block",
			script: `CREATE TABLE test (id INT);

-- +migrate StatementBegin
CREATE FUNCTION test_func() RETURNS void AS $$
BEGIN
    RAISE NOTICE 'Hello';
    INSERT INTO log VALUES (1);
END;
$$ LANGUAGE plpgsql;
-- +migrate StatementEnd

CREATE INDEX idx_test ON test(id);`,
			expected: []string{
				"CREATE TABLE test (id INT);",
				"CREATE FUNCTION test_func() RETURNS void AS $$\nBEGIN\n    RAISE NOTICE 'Hello';\n    INSERT INTO log VALUES (1);\nEND;\n$$ LANGUAGE plpgsql;",
				"CREATE INDEX idx_test ON test(id);",
			},
		},
		{
			name: "multiline statement",
			script: `CREATE TABLE users (
    id INT PRIMARY KEY,
    name VARCHAR(100),
    email VARCHAR(255)
);`,
			expected: []string{
				"CREATE TABLE users (\n    id INT PRIMARY KEY,\n    name VARCHAR(100),\n    email VARCHAR(255)\n);",
			},
		},
		{
			name: "empty lines between statements",
			script: `CREATE TABLE users (id INT);


INSERT INTO users VALUES (1);`,
			expected: []string{
				"CREATE TABLE users (id INT);",
				"INSERT INTO users VALUES (1);",
			},
		},
		{
			name: "complex PL/pgSQL function with multiple semicolons",
			script: `-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION process_order(order_id INT)
RETURNS void AS $$
DECLARE
    total DECIMAL;
    item RECORD;
BEGIN
    -- Calculate total
    SELECT SUM(price) INTO total FROM order_items WHERE order_id = order_id;

    -- Process each item
    FOR item IN SELECT * FROM order_items WHERE order_id = order_id LOOP
        UPDATE inventory SET qty = qty - item.qty WHERE id = item.product_id;
    END LOOP;

    -- Update order
    UPDATE orders SET total = total, status = 'processed' WHERE id = order_id;
END;
$$ LANGUAGE plpgsql;
-- +migrate StatementEnd`,
			expected: []string{
				"CREATE OR REPLACE FUNCTION process_order(order_id INT)\nRETURNS void AS $$\nDECLARE\n    total DECIMAL;\n    item RECORD;\nBEGIN\n    -- Calculate total\n    SELECT SUM(price) INTO total FROM order_items WHERE order_id = order_id;\n\n    -- Process each item\n    FOR item IN SELECT * FROM order_items WHERE order_id = order_id LOOP\n        UPDATE inventory SET qty = qty - item.qty WHERE id = item.product_id;\n    END LOOP;\n\n    -- Update order\n    UPDATE orders SET total = total, status = 'processed' WHERE id = order_id;\nEND;\n$$ LANGUAGE plpgsql;",
			},
		},
		{
			// A backslash escapes the next character inside E'...', so the quote
			// here is data and does not end the literal.
			name:   "E-string with escaped quote",
			script: `INSERT INTO t VALUES (E'\'');` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`INSERT INTO t VALUES (E'\'');`,
				"DROP TABLE t;",
			},
		},
		{
			name:   "E-string with escaped backslash",
			script: `INSERT INTO t VALUES (E'a\\b');` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`INSERT INTO t VALUES (E'a\\b');`,
				"DROP TABLE t;",
			},
		},
		{
			name:   "E-string hiding a semicolon",
			script: `INSERT INTO t VALUES (E'a\';b');` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`INSERT INTO t VALUES (E'a\';b');`,
				"DROP TABLE t;",
			},
		},
		{
			name:   "lowercase e prefix",
			script: `INSERT INTO t VALUES (e'\'');` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`INSERT INTO t VALUES (e'\'');`,
				"DROP TABLE t;",
			},
		},
		{
			// A keyword ending in "E" butted against a quote — LIKE'...' is valid
			// SQL — must not be read as an E-string prefix. The E has to be a
			// token of its own, so here the backslash stays data and the literal
			// ends at the next quote.
			name:   "keyword ending in E is not an E-string prefix",
			script: `SELECT id FROM t WHERE name LIKE'a\';` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`SELECT id FROM t WHERE name LIKE'a\';`,
				"DROP TABLE t;",
			},
		},
		{
			// standard_conforming_strings is on by default, so a backslash in a
			// plain literal is data. It must not swallow the closing quote.
			name:   "backslash in a plain literal is data",
			script: `INSERT INTO t VALUES ('C:\');` + "\n" + `DROP TABLE t;`,
			expected: []string{
				`INSERT INTO t VALUES ('C:\');`,
				"DROP TABLE t;",
			},
		},
		{
			// The unterminated statement before the marker is its own statement,
			// not part of the block.
			name: "pending text before StatementBegin",
			script: "CREATE TABLE t(id int)\n" +
				statementBeginLine + "\nSELECT 1;\n" + statementEndLine,
			expected: []string{
				"CREATE TABLE t(id int)",
				"SELECT 1;",
			},
		},
		{
			name: "comment before StatementBegin is dropped",
			script: "-- describes the function below\n" +
				statementBeginLine + "\nSELECT 1;\n" + statementEndLine,
			expected: []string{
				"SELECT 1;",
			},
		},
		{
			name:   "stray semicolon is not a statement",
			script: "CREATE TABLE t(id int);\n;\n",
			expected: []string{
				"CREATE TABLE t(id int);",
			},
		},
		{
			name:     "only semicolons",
			script:   ";;\n;",
			expected: []string{},
		},
		{
			// A marker is only a marker when it is the whole line. Matching it as a
			// substring would consume this line and delete the INSERT with it.
			name: "marker mentioned inside a string literal is data",
			script: "INSERT INTO notes VALUES ('" + statementBeginLine + "');\n" +
				"INSERT INTO notes VALUES ('second');",
			expected: []string{
				"INSERT INTO notes VALUES ('" + statementBeginLine + "');",
				"INSERT INTO notes VALUES ('second');",
			},
		},
		{
			// Not a marker (not the whole line), so it degrades to an ordinary
			// trailing comment and rides along with the next statement, the way
			// every other comment does. What matters is that SELECT 1 survives:
			// matching the marker loosely used to delete it.
			name:   "marker as a trailing comment does not eat the statement",
			script: "SELECT 1; " + statementBeginLine + "\nSELECT 2;",
			expected: []string{
				"SELECT 1;",
				statementBeginLine + "\nSELECT 2;",
			},
		},
		{
			name:   "indented marker still delimits",
			script: "   " + statementBeginLine + "\nSELECT 1;\n\t" + statementEndLine,
			expected: []string{
				"SELECT 1;",
			},
		},
		{
			name:     "empty script",
			script:   "",
			expected: []string{},
		},
		{
			name:     "only comments",
			script:   "-- just a comment\n-- another comment",
			expected: []string{},
		},
		{
			name: "semicolon inside single-quoted string spanning lines",
			script: `INSERT INTO t (note) VALUES ('line one;
line two');`,
			expected: []string{
				"INSERT INTO t (note) VALUES ('line one;\nline two');",
			},
		},
		{
			name:   "doubled single-quote escape containing a semicolon",
			script: `SELECT 'it''s; still one string';`,
			expected: []string{
				`SELECT 'it''s; still one string';`,
			},
		},
		{
			name: "dollar-quoted function body without StatementBegin/End",
			script: `CREATE FUNCTION f() RETURNS void AS $$
BEGIN
    RAISE NOTICE 'hi';
END;
$$ LANGUAGE plpgsql;`,
			expected: []string{
				"CREATE FUNCTION f() RETURNS void AS $$\nBEGIN\n    RAISE NOTICE 'hi';\nEND;\n$$ LANGUAGE plpgsql;",
			},
		},
		{
			name: "tagged dollar-quote not confused by nested untagged $$",
			script: `CREATE FUNCTION g() RETURNS text AS $tag$
SELECT 1; -- body contains a literal $$ marker: $$ nested $$
$tag$ LANGUAGE sql;`,
			expected: []string{
				"CREATE FUNCTION g() RETURNS text AS $tag$\nSELECT 1; -- body contains a literal $$ marker: $$ nested $$\n$tag$ LANGUAGE sql;",
			},
		},
		{
			name: "parameter placeholders not mistaken for dollar-quote tags",
			script: `SELECT * FROM t WHERE a = $1 AND b = $2;
CREATE FUNCTION f() RETURNS void AS $$
BEGIN
    NULL;
END;
$$ LANGUAGE plpgsql;`,
			expected: []string{
				"SELECT * FROM t WHERE a = $1 AND b = $2;",
				"CREATE FUNCTION f() RETURNS void AS $$\nBEGIN\n    NULL;\nEND;\n$$ LANGUAGE plpgsql;",
			},
		},
		{
			name:   "semicolon inside double-quoted identifier",
			script: `ALTER TABLE t RENAME COLUMN "old;name" TO "new;name";`,
			expected: []string{
				`ALTER TABLE t RENAME COLUMN "old;name" TO "new;name";`,
			},
		},
		{
			name:   "semicolon inside block comment",
			script: `SELECT 1 /* comment; with semicolon */;`,
			expected: []string{
				"SELECT 1 /* comment; with semicolon */;",
			},
		},
		{
			name: "semicolon inside multi-line block comment",
			script: `SELECT 1 /* start
   still comment; ignored
   end */;`,
			expected: []string{
				"SELECT 1 /* start\n   still comment; ignored\n   end */;",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := splitSQLStatements(tt.script)
			if err != nil {
				t.Fatalf("splitSQLStatements() unexpected error: %v", err)
			}

			if len(result) != len(tt.expected) {
				t.Errorf("splitSQLStatements() returned %d statements, want %d", len(result), len(tt.expected))
				t.Logf("Got: %#v", result)
				t.Logf("Want: %#v", tt.expected)

				return
			}

			for i := range result {
				if strings.TrimSpace(result[i]) != strings.TrimSpace(tt.expected[i]) {
					t.Errorf("Statement %d mismatch:\nGot:  %q\nWant: %q", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

// An unterminated block must be reported, not silently absorb the rest of the
// file — that would hide a typo in the closing marker and change what runs.
func TestSplitSQLStatements_UnterminatedBlock(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"never closed":       statementBeginLine + "\nSELECT 1;",
		"typo in the closer": statementBeginLine + "\nSELECT 1;\n-- +migrate StatementEndd",
		"closer as a comment tail": statementBeginLine + "\nSELECT 1;\nSELECT 2; " +
			statementEndLine,
	}

	for name, script := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := splitSQLStatements(script)
			if err == nil {
				t.Fatal("expected an error for an unterminated statement block, got nil")
			}

			if !strings.Contains(err.Error(), "never closed") {
				t.Errorf("error = %q, want it to say the block is never closed", err)
			}
		})
	}
}

func TestSplitSQLStatements_CreateIndexConcurrently(t *testing.T) {
	t.Parallel()

	// Test case specifically for CREATE INDEX CONCURRENTLY
	script := `DROP TABLE IF EXISTS activity;

CREATE TABLE activity (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    board_id uuid NOT NULL,
    user_id uuid NOT NULL
);

CREATE INDEX CONCURRENTLY idx_activity_user ON activity(user_id);
CREATE INDEX CONCURRENTLY idx_activity_board ON activity(board_id);
CREATE UNIQUE INDEX CONCURRENTLY idx_activity_dedup ON activity(board_id, user_id);`

	expected := []string{
		"DROP TABLE IF EXISTS activity;",
		"CREATE TABLE activity (\n    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),\n    board_id uuid NOT NULL,\n    user_id uuid NOT NULL\n);",
		"CREATE INDEX CONCURRENTLY idx_activity_user ON activity(user_id);",
		"CREATE INDEX CONCURRENTLY idx_activity_board ON activity(board_id);",
		"CREATE UNIQUE INDEX CONCURRENTLY idx_activity_dedup ON activity(board_id, user_id);",
	}

	result, err := splitSQLStatements(script)
	if err != nil {
		t.Fatalf("splitSQLStatements() unexpected error: %v", err)
	}

	if len(result) != len(expected) {
		t.Errorf("splitSQLStatements() returned %d statements, want %d", len(result), len(expected))

		for i, stmt := range result {
			t.Logf("Statement %d: %q", i, stmt)
		}

		return
	}

	for i := range result {
		if strings.TrimSpace(result[i]) != strings.TrimSpace(expected[i]) {
			t.Errorf("Statement %d mismatch:\nGot:  %q\nWant: %q", i, result[i], expected[i])
		}
	}
}
