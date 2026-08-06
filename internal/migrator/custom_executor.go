package migrator

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eidon-go/pg-migrate/internal/sqlconn"
)

// bookkeepingTimeout bounds the writes that must happen after a notransaction
// script has already changed the schema. They run detached from the caller's
// context, so they need a bound of their own.
const bookkeepingTimeout = 10 * time.Second

// AppliedMigration represents a migration record in the database.
type AppliedMigration struct {
	ID        string
	AppliedAt time.Time
	Error     *string // NULL if migration succeeded, error message if failed

	// DownScript is the stored rollback script. Reported so that resolving a
	// failed migration by hand does not require reverse-engineering the
	// bookkeeping table's shape in raw SQL.
	DownScript string
}

// CustomExecutor executes migrations and stores scripts in the database.
type CustomExecutor struct {
	db    *sql.DB
	table tableRef
}

// scanState tracks lexical context while scanning a SQL script for
// statement boundaries.
type scanState int

const (
	stateNormal         scanState = iota
	stateInSingleQuote            // '...'  ('' is an escaped quote)
	stateInDoubleQuote            // "..."  ("" is an escaped quote)
	stateInDollarQuote            // $$...$$ or $tag$...$tag$
	stateInBlockComment           // /* ... */ (nestable)
)

// isIdentifierByte reports whether c can appear inside an unquoted identifier.
// Bytes of multibyte runes count, which errs towards treating something as part
// of an identifier rather than as a standalone token.
func isIdentifierByte(c byte) bool {
	switch {
	case c == '_',
		c >= 'a' && c <= 'z',
		c >= 'A' && c <= 'Z',
		c >= '0' && c <= '9',
		c >= utf8.RuneSelf:
		return true
	default:
		return false
	}
}

// isEscapeStringPrefix reports whether the quote at line[i] opens an E'...'
// escape string, in which a backslash escapes the following character. Plain
// '...' literals treat backslash as data (standard_conforming_strings, on by
// default since Postgres 9.1), so only E-strings need the extra handling.
//
// The E must be a token of its own: an identifier ending in "e", as in
// WHERE name'..., is not a prefix.
func isEscapeStringPrefix(line string, i int) bool {
	if i == 0 {
		return false
	}

	if c := line[i-1]; c != 'E' && c != 'e' {
		return false
	}

	if i == 1 {
		return true
	}

	return !isIdentifierByte(line[i-2])
}

// isDollarTagChar reports whether r is valid inside a dollar-quote tag. Tags
// are identifier-like: letters/underscore, plus digits after the first
// character — a leading digit (as in the parameter placeholder "$1") is
// therefore never a valid tag start.
func isDollarTagChar(r rune, first bool) bool {
	if r == '_' || unicode.IsLetter(r) {
		return true
	}

	return !first && unicode.IsDigit(r)
}

// matchDollarTag attempts to match a dollar-quote delimiter "$tag$" (tag may
// be empty, as in bare "$$") starting at line[i] (line[i] must be '$').
// Returns the full delimiter text and true on success, or "", false if what
// follows isn't a valid tag (e.g. a parameter placeholder like "$1").
func matchDollarTag(line string, i int) (string, bool) {
	j := i + 1
	first := true

	for j < len(line) {
		r, size := utf8.DecodeRuneInString(line[j:])
		if r == '$' {
			return line[i : j+size], true
		}

		if !isDollarTagChar(r, first) {
			return "", false
		}

		first = false
		j += size
	}

	return "", false
}

// splitSQLStatements splits a SQL script into individual statements. It is a
// single-pass scanner aware of single- and double-quoted literals, dollar
// quoting ($$...$$ / $tag$...$tag$, used for PL/pgSQL function bodies), and
// line/block comments, so a semicolon inside any of those is never mistaken
// for a statement boundary.
//
// "-- +migrate StatementBegin" / "StatementEnd" markers (recognized only
// outside any open quote/comment) remain a manual override: everything
// between them becomes a single statement regardless of what the scanner
// would otherwise infer.
//
// Example:
//
//	-- +migrate StatementBegin
//	CREATE FUNCTION test() AS $$
//	BEGIN
//	    SELECT 1; -- this semicolon is ignored
//	END;
//	$$ LANGUAGE plpgsql;
//	-- +migrate StatementEnd
func splitSQLStatements(script string) ([]string, error) {
	var (
		statements []string
		buf        strings.Builder
	)

	state := stateNormal
	dollarTag := ""
	blockCommentDepth := 0
	inStatementBlock := false
	escapeString := false // the open '...' literal is an E-string

	lines := strings.Split(script, "\n")
	for lineIdx, line := range lines {
		isLastLine := lineIdx == len(lines)-1

		// Markers are matched against the whole trimmed line, never as a substring:
		// a matching line is consumed, so a loose match would silently delete the
		// SQL sharing that line — including a mere mention inside a string literal.
		if !inStatementBlock && state == stateNormal && strings.TrimSpace(line) == statementBeginLine {
			// Anything buffered before the marker is a statement of its own; it
			// must not be swallowed into the block.
			statements = appendStatement(statements, &buf)
			inStatementBlock = true

			continue
		}

		if inStatementBlock && state == stateNormal && strings.TrimSpace(line) == statementEndLine {
			statements = appendStatement(statements, &buf)
			inStatementBlock = false

			continue
		}

		if inStatementBlock {
			buf.WriteString(line)
			buf.WriteByte('\n')

			continue
		}

		segStart := 0
		i := 0

	scanLine:
		for i < len(line) {
			switch state {
			case stateNormal:
				switch {
				case line[i] == '\'':
					state = stateInSingleQuote
					escapeString = isEscapeStringPrefix(line, i)
				case line[i] == '"':
					state = stateInDoubleQuote
				case line[i] == '$':
					if tag, ok := matchDollarTag(line, i); ok {
						state = stateInDollarQuote
						dollarTag = tag
						i += len(tag)

						continue scanLine
					}
				case line[i] == '-' && i+1 < len(line) && line[i+1] == '-':
					break scanLine // rest of the line is a line comment
				case line[i] == '/' && i+1 < len(line) && line[i+1] == '*':
					state = stateInBlockComment
					blockCommentDepth = 1
					i += 2

					continue scanLine
				case line[i] == ';':
					buf.WriteString(line[segStart : i+1])
					statements = appendStatement(statements, &buf)
					segStart = i + 1
				}
			case stateInSingleQuote:
				// In an E-string a backslash escapes whatever follows, including a
				// quote: E'\'' is one literal quote, not an empty string followed
				// by an unterminated one.
				if escapeString && line[i] == '\\' {
					i += 2

					continue scanLine
				}

				if line[i] == '\'' {
					if i+1 < len(line) && line[i+1] == '\'' {
						i += 2

						continue scanLine
					}

					state = stateNormal
					escapeString = false
				}
			case stateInDoubleQuote:
				if line[i] == '"' {
					if i+1 < len(line) && line[i+1] == '"' {
						i += 2

						continue scanLine
					}

					state = stateNormal
				}
			case stateInDollarQuote:
				if line[i] == '$' && strings.HasPrefix(line[i:], dollarTag) {
					i += len(dollarTag)
					state = stateNormal
					dollarTag = ""

					continue scanLine
				}
			case stateInBlockComment:
				switch {
				case line[i] == '*' && i+1 < len(line) && line[i+1] == '/':
					blockCommentDepth--
					i += 2

					if blockCommentDepth == 0 {
						state = stateNormal
					}

					continue scanLine
				case line[i] == '/' && i+1 < len(line) && line[i+1] == '*':
					blockCommentDepth++
					i += 2

					continue scanLine
				}
			}

			i++
		}

		buf.WriteString(line[segStart:])

		if !isLastLine {
			buf.WriteByte('\n')
		}
	}

	if inStatementBlock {
		// Silently treating the rest of the file as one statement would hide a typo
		// in the closing marker and change what runs.
		return nil, fmt.Errorf("%q is never closed by %q", statementBeginLine, statementEndLine)
	}

	// Add any remaining content as a statement
	return appendStatement(statements, &buf), nil
}

// ValidateStatementBlocks reports whether a script's statement-block markers are
// well formed. It exists so that the file source can reject a broken script at
// parse time, rather than letting the problem surface mid-migration.
func ValidateStatementBlocks(script string) error {
	_, err := splitSQLStatements(script)

	return err
}

// isEmptyStatement reports whether s carries no SQL to execute: blank, nothing
// but statement terminators, or nothing but comments.
func isEmptyStatement(s string) bool {
	if strings.Trim(s, "; \t\r\n") == "" {
		return true
	}

	return isOnlyComments(s)
}

// appendStatement flushes buf as a statement, dropping it when there is nothing
// to execute. Used at every statement boundary so that stray semicolons and
// trailing comments never reach the database as statements of their own.
func appendStatement(statements []string, buf *strings.Builder) []string {
	text := strings.TrimSpace(buf.String())
	buf.Reset()

	if isEmptyStatement(text) {
		return statements
	}

	return append(statements, text)
}

// isOnlyComments checks if a string contains only comments and whitespace.
func isOnlyComments(s string) bool {
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "--") {
			return false
		}
	}

	return true
}

// NewCustomExecutor creates a CustomExecutor that keeps its bookkeeping in the
// table named by table. It fails if the table or schema name is not a usable
// Postgres identifier.
func NewCustomExecutor(db *sql.DB, table TableConfig) (*CustomExecutor, error) {
	ref, err := table.resolve()
	if err != nil {
		return nil, err
	}

	return &CustomExecutor{
		db:    db,
		table: ref,
	}, nil
}

// EnsureMigrationsTable creates the bookkeeping table if it doesn't exist.
//
// seq is the ordering key rather than applied_at. Wall-clock time is not a
// reliable order: applied_at can tie, and it moves backwards across a DST
// transition or when sessions disagree about the time zone — which would make
// rollback, "the last N migrations" and recovery operate on the wrong set.
func (e *CustomExecutor) EnsureMigrationsTable(ctx context.Context) error {
	query := e.query(`
		CREATE TABLE IF NOT EXISTS %s (
			seq BIGINT GENERATED ALWAYS AS IDENTITY,
			id VARCHAR(255) PRIMARY KEY,
			up_script TEXT NOT NULL,
			down_script TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			error TEXT NULL
		)
	`)

	if _, err := e.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("create migrations table %s: %w", e.table, err)
	}

	return nil
}

// TableExists reports whether the bookkeeping table is present, so that
// read-only operations need no DDL privileges.
//
// to_regclass resolves the same way a statement would — honouring search_path
// for an unqualified name — and yields NULL instead of raising when the table is
// absent.
func (e *CustomExecutor) TableExists(ctx context.Context) (bool, error) {
	var exists bool
	if err := e.db.QueryRowContext(ctx,
		"SELECT to_regclass($1) IS NOT NULL", string(e.table),
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check for migrations table %s: %w", e.table, err)
	}

	return exists, nil
}

// ApplyMigration applies a migration and stores scripts in the database.
func (e *CustomExecutor) ApplyMigration(ctx context.Context, migration *Migration) error {
	// Directives are read from the script itself rather than from a separate
	// field: the script is what gets stored in the database and what rollback
	// later reads back, so it is the single source of truth for both paths.
	directives, upSQL, err := ParseDirectiveHeader(migration.UpScript)
	if err != nil {
		return fmt.Errorf("parse up script for %s: %w", migration.ID, err)
	}

	noTransaction := directives[DirectiveNoTransaction]

	if noTransaction {
		return e.applyWithoutTransaction(ctx, migration, upSQL)
	}

	// Execute with transaction: the script and its bookkeeping row commit or fail
	// together, so error is always NULL here.
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, upSQL); execErr != nil {
		return fmt.Errorf("execute up script: %w", execErr)
	}

	insert := e.query("INSERT INTO %s (id, up_script, down_script, error) VALUES ($1, $2, $3, NULL)")
	if _, insErr := tx.ExecContext(ctx,
		insert, migration.ID, migration.UpScript, migration.DownScript,
	); insErr != nil {
		return fmt.Errorf("insert migration record: %w", insErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("commit transaction: %w", commitErr)
	}

	return nil
}

// RecordApplied writes bookkeeping rows for migrations whose effect is already
// present in the schema, without executing anything.
//
// This exists for adopting the tool on a database that predates it. The scripts
// are stored exactly as a normal apply would store them, so a later rollback
// reads back the same text and behaves identically.
//
// All rows go in one transaction: a partial baseline would leave the ledger
// describing a prefix of the schema, which is the state this whole operation
// exists to avoid.
func (e *CustomExecutor) RecordApplied(ctx context.Context, migrations []*Migration) error {
	if len(migrations) == 0 {
		return nil
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	insert := e.query("INSERT INTO %s (id, up_script, down_script, error) VALUES ($1, $2, $3, NULL)")

	for _, migration := range migrations {
		if _, execErr := tx.ExecContext(ctx,
			insert, migration.ID, migration.UpScript, migration.DownScript,
		); execErr != nil {
			return fmt.Errorf("record %s as applied: %w", migration.ID, execErr)
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("commit transaction: %w", commitErr)
	}

	return nil
}

// RollbackMigration rolls back a migration using the down_script from the
// database. The script is read from the database rather than from the file
// source on purpose: rollback must work when the checked-out branch no longer
// contains the migration file at all.
func (e *CustomExecutor) RollbackMigration(ctx context.Context, id string) error {
	// First, get the down script from database (includes the directive header)
	var downScript string

	selectDown := e.query("SELECT down_script FROM %s WHERE id = $1")

	err := e.db.QueryRowContext(ctx, selectDown, id).Scan(&downScript)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("migration %s not found in database", id)
	}

	if err != nil {
		return fmt.Errorf("get down script for %s: %w", id, err)
	}

	// Parse directives from the stored script
	directives, downSQL, err := ParseDirectiveHeader(downScript)
	if err != nil {
		return fmt.Errorf("parse stored down script for %s: %w", id, err)
	}

	downNoTransaction := directives[DirectiveNoTransaction]

	if downNoTransaction {
		return e.rollbackWithoutTransaction(ctx, id, downSQL)
	}

	// Execute with transaction: the script and the removal of its bookkeeping row
	// commit or fail together.
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, execErr := tx.ExecContext(ctx, downSQL); execErr != nil {
		return fmt.Errorf("execute down script: %w", execErr)
	}

	if _, execErr := tx.ExecContext(ctx, e.deleteQuery(), id); execErr != nil {
		return fmt.Errorf("delete migration record: %w", execErr)
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("commit transaction: %w", commitErr)
	}

	return nil
}

// errNoPinnedConn marks the case where the dedicated connection could not be
// obtained at all, so the script never ran and there is nothing to record.
var errNoPinnedConn = errors.New("no dedicated connection")

// runStatements executes statements in order on conn, stopping at the first
// failure and reporting which statement it was.
func runStatements(ctx context.Context, conn *sql.Conn, statements []string) error {
	for i, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d failed: %w", i+1, err)
		}
	}

	return nil
}

// DeleteRecord removes a migration's bookkeeping row, reporting whether there was
// one. It does not run the rollback script; that is the caller's decision.
func (e *CustomExecutor) DeleteRecord(ctx context.Context, id string) (bool, error) {
	result, err := e.db.ExecContext(ctx, e.deleteQuery(), id)
	if err != nil {
		return false, fmt.Errorf("delete migration record: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count deleted rows: %w", err)
	}

	return affected > 0, nil
}

// GetAppliedMigrations returns the applied migrations in the order they were
// applied.
//
// Ordering is by seq, not applied_at: the caller relies on this order to roll
// back in reverse, and wall-clock time can tie or move backwards.
func (e *CustomExecutor) GetAppliedMigrations(ctx context.Context) ([]AppliedMigration, error) {
	query := e.query("SELECT id, applied_at, error, down_script FROM %s ORDER BY seq")

	rows, err := e.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query migrations: %w", err)
	}
	defer rows.Close()

	var migrations []AppliedMigration

	for rows.Next() {
		var (
			m        AppliedMigration
			errorMsg sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.AppliedAt, &errorMsg, &m.DownScript); err != nil {
			return nil, fmt.Errorf("scan migration: %w", err)
		}

		if errorMsg.Valid {
			m.Error = &errorMsg.String
		}

		migrations = append(migrations, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return migrations, nil
}

// ExecutionResult records what ExecutePlan actually did. On error it holds the
// prefix that completed before the failure, so callers can report facts rather
// than intentions.
type ExecutionResult struct {
	Applied    []*Migration
	RolledBack []*Migration
}

// migrationIDs extracts the IDs from a slice of migrations.
func migrationIDs(ms []*Migration) []string {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}

	return ids
}

// EnsureReversible reports an error naming every migration among ids whose
// stored rollback script is marked irreversible.
//
// It is a pre-flight check: refusing the whole operation up front is the point,
// since discovering an irreversible migration halfway through a rollback would
// leave the schema between two intended states.
func (e *CustomExecutor) EnsureReversible(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	selectDown := e.query("SELECT down_script FROM %s WHERE id = $1")

	var irreversible []string

	for _, id := range ids {
		var downScript string

		err := e.db.QueryRowContext(ctx, selectDown, id).Scan(&downScript)
		if errors.Is(err, sql.ErrNoRows) {
			// Not applied, so not a rollback candidate after all.
			continue
		}

		if err != nil {
			return fmt.Errorf("get down script for %s: %w", id, err)
		}

		directives, _, err := ParseDirectiveHeader(downScript)
		if err != nil {
			return fmt.Errorf("parse stored down script for %s: %w", id, err)
		}

		if directives[DirectiveIrreversible] {
			irreversible = append(irreversible, id)
		}
	}

	if len(irreversible) > 0 {
		return fmt.Errorf("%w: %s; undo the change by hand, then remove the row(s) from %s",
			ErrIrreversible, strings.Join(irreversible, ", "), e.table)
	}

	return nil
}

// ExecutePlan executes a migration plan, rolling back before applying.
//
// The returned result is never nil: on failure it describes how far execution
// got.
func (e *CustomExecutor) ExecutePlan(
	ctx context.Context, plan *MigrationPlan, allMigrations []*Migration,
) (*ExecutionResult, error) {
	result := &ExecutionResult{}

	// If plan is empty - nothing to do
	if len(plan.ToRollback) == 0 && len(plan.ToApply) == 0 {
		return result, nil
	}

	if err := e.EnsureReversible(ctx, migrationIDs(plan.ToRollback)); err != nil {
		return result, err
	}

	// Execute rollbacks (Down) - scripts are read from database
	for _, migration := range plan.ToRollback {
		if err := e.RollbackMigration(ctx, migration.ID); err != nil {
			return result, fmt.Errorf("rollback %s failed: %w", migration.ID, err)
		}

		result.RolledBack = append(result.RolledBack, migration)
	}

	// Execute migrations (Up) - read from files and store in database
	// Create map for fast lookup
	allMigrationsMap := make(map[string]*Migration)
	for _, m := range allMigrations {
		allMigrationsMap[m.ID] = m
	}

	for _, migration := range plan.ToApply {
		fullMigration, exists := allMigrationsMap[migration.ID]
		if !exists {
			return result, fmt.Errorf("migration %s not found in files", migration.ID)
		}

		if err := e.ApplyMigration(ctx, fullMigration); err != nil {
			return result, fmt.Errorf("apply %s failed: %w", migration.ID, err)
		}

		result.Applied = append(result.Applied, fullMigration)
	}

	return result, nil
}

// query interpolates the table reference into a statement template.
//
// The reference was validated and quoted at construction, which is what makes
// this safe; table and schema names cannot be passed as bind parameters.
func (e *CustomExecutor) query(template string) string {
	return fmt.Sprintf(template, e.table)
}

// deleteQuery removes a migration's bookkeeping row. Used from both the
// transactional and non-transactional rollback paths.
func (e *CustomExecutor) deleteQuery() string {
	return e.query("DELETE FROM %s WHERE id = $1")
}

// applyWithoutTransaction runs a "notransaction" up script statement by statement
// and records the outcome.
//
// Every statement runs on one dedicated connection, so that session state a
// script sets (SET statement_timeout = 0 before CREATE INDEX CONCURRENTLY, a
// temporary table, a prepared statement) applies to the statements that follow
// and does not survive into the caller's pool afterwards.
//
// The bookkeeping row is written even when a statement failed, because the
// earlier statements are already committed and the schema no longer matches any
// migration. That write must survive cancellation of ctx — losing it is worse
// than anything it could report, since the next run would then re-apply a
// migration that already half-ran.
func (e *CustomExecutor) applyWithoutTransaction(ctx context.Context, migration *Migration, upSQL string) error {
	statements, err := splitSQLStatements(upSQL)
	if err != nil {
		return fmt.Errorf("split up script for %s: %w", migration.ID, err)
	}

	executionError := e.onPinnedConn(ctx, func(conn *sql.Conn) error {
		return runStatements(ctx, conn, statements)
	})
	if errors.Is(executionError, errNoPinnedConn) {
		return executionError
	}

	var errorMsg sql.NullString
	if executionError != nil {
		errorMsg = sql.NullString{String: executionError.Error(), Valid: true}
	}

	// Detached from ctx: see the doc comment. Bounded by its own timeout so a
	// wedged connection cannot hang the caller forever.
	//
	// Written through the pool rather than on the pinned connection: if ctx died
	// mid-statement, the driver cancelled the query on that connection and it may
	// no longer be usable — which is precisely the case where losing this row
	// matters most.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()

	insert := e.query("INSERT INTO %s (id, up_script, down_script, error) VALUES ($1, $2, $3, $4)")
	if _, insErr := e.db.ExecContext(recordCtx,
		insert, migration.ID, migration.UpScript, migration.DownScript, errorMsg,
	); insErr != nil {
		// Both errors matter: one says what the schema did, the other says the
		// ledger does not know about it.
		return errors.Join(executionError, fmt.Errorf(
			"%w: migration %s ran outside a transaction but its bookkeeping row could not be written: %w",
			ErrUnrecorded, migration.ID, insErr,
		))
	}

	return executionError
}

// rollbackWithoutTransaction runs a "notransaction" down script statement by
// statement on one dedicated connection, mirroring applyWithoutTransaction.
//
// A partial failure here is recorded on the migration's row rather than left
// silent. Without that, a rollback that dropped half of what it meant to drop
// would leave a row that still looks healthy, and the next run would report the
// database as in sync while the schema had quietly drifted.
func (e *CustomExecutor) rollbackWithoutTransaction(ctx context.Context, id, downSQL string) error {
	statements, err := splitSQLStatements(downSQL)
	if err != nil {
		return fmt.Errorf("split down script for %s: %w", id, err)
	}

	execErr := e.onPinnedConn(ctx, func(conn *sql.Conn) error {
		return runStatements(ctx, conn, statements)
	})
	if errors.Is(execErr, errNoPinnedConn) {
		return execErr
	}

	if execErr != nil {
		if markErr := e.markFailed(ctx, id, execErr); markErr != nil {
			return errors.Join(execErr, markErr)
		}

		return fmt.Errorf("execute down script: %w", execErr)
	}

	// Detached from ctx and issued through the pool, for the same reasons as the
	// apply path: the script already ran, so the ledger must be brought into line
	// even if the caller is gone and the pinned connection is spent.
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()

	if _, err := e.db.ExecContext(deleteCtx, e.deleteQuery(), id); err != nil {
		return fmt.Errorf(
			"%w: migration %s was rolled back but its bookkeeping row could not be removed: %w",
			ErrUnrecorded, id, err)
	}

	return nil
}

// markFailed stamps the error column so that the fail-fast guard trips on the
// next run. Detached from ctx: the schema has already changed, so this has to be
// recorded even if the caller is going away.
func (e *CustomExecutor) markFailed(ctx context.Context, id string, cause error) error {
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()

	update := e.query("UPDATE %s SET error = $1 WHERE id = $2")
	if _, err := e.db.ExecContext(markCtx, update, cause.Error(), id); err != nil {
		return fmt.Errorf(
			"%w: migration %s failed to roll back and the failure could not be recorded either: %w",
			ErrUnrecorded, id, err)
	}

	return nil
}

// onPinnedConn runs fn on one connection dedicated to a whole notransaction
// script, and releases that connection before returning.
//
// Releasing before the caller records the outcome is deliberate, for two reasons.
// The bookkeeping write then runs with clean session state, so a migration that
// set a tiny statement_timeout cannot sabotage the record of its own failure. And
// it never needs a third connection alongside the advisory lock's, which would
// deadlock a pool limited to two.
func (e *CustomExecutor) onPinnedConn(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: get dedicated connection for notransaction script: %w",
			errNoPinnedConn, err)
	}
	defer e.releaseConn(conn)

	return fn(conn)
}

// releaseConn ends the session instead of returning the connection to the
// caller's pool.
//
// A migration script may set anything on its session — GUCs, temporary tables,
// prepared statements — and this library borrows the caller's *sql.DB, so letting
// any of that survive would contaminate the application's pool. Ending the session
// is the only reset that covers everything.
//
// Resetting in place with DISCARD ALL is not an option: it issues DEALLOCATE ALL,
// which invalidates the driver's own prepared-statement cache without telling it,
// and the next query on that connection then fails with SQLSTATE 26000.
//
// The cost is one re-established connection per notransaction migration, which
// are the exception rather than the rule.
func (e *CustomExecutor) releaseConn(conn *sql.Conn) {
	sqlconn.Destroy(conn)
}
