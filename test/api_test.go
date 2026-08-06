//go:build integration

package migrate_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	migrate "github.com/eidon-go/pg-migrate"
	"github.com/eidon-go/pg-migrate/test/dbtest"
)

func TestLibrary_StatusOnFreshDatabase(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status on a never-migrated database failed: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("expected no applied migrations, got %v", applied)
	}

	// Status must not create the bookkeeping table either.
	var exists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT to_regclass('migrations') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("failed to check for the migrations table: %v", err)
	}

	if exists {
		t.Error("Status created the migrations table; it must only read")
	}
}

func TestLibrary_StatusReportsAppliedOrder(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	want := []string{"001_init", "002_posts"}
	if len(applied) != len(want) {
		t.Fatalf("got %d applied migrations, want %d", len(applied), len(want))
	}

	for i, id := range want {
		if applied[i].ID != id {
			t.Errorf("applied[%d].ID = %q, want %q", i, applied[i].ID, id)
		}

		if applied[i].Failed {
			t.Errorf("applied[%d] is marked failed: %s", i, applied[i].Error)
		}

		if applied[i].AppliedAt.IsZero() {
			t.Errorf("applied[%d].AppliedAt is zero", i)
		}
	}
}

// A notransaction migration that fails partway records an error row. Status must
// surface it, and every subsequent operation must refuse to run.
func TestLibrary_FailedMigrationStopsEverything(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	// The second statement fails, so the first is already committed.
	writeMigration(t, tmpDir, "001_partial",
		"-- +migrate notransaction\nCREATE TABLE t1 (id INT);\nCREATE TABLE t1 (id INT);",
		"DROP TABLE IF EXISTS t1;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err == nil {
		t.Fatal("expected the partial migration to fail")
	}

	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 1 {
		t.Fatalf("expected 1 recorded migration, got %v", applied)
	}

	if !applied[0].Failed {
		t.Error("expected the migration to be recorded as failed")
	}

	if applied[0].Error == "" {
		t.Error("expected a recorded error message")
	}

	// Up, Reconcile and Down must all refuse while that row exists.
	operations := map[string]func() error{
		"Up": func() error {
			_, upErr := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir))

			return upErr
		},
		"Reconcile": func() error {
			_, rErr := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir), migrate.WithRollback())

			return rErr
		},
		"Down": func() error {
			_, dErr := migrate.DownAll(t.Context(), dbConn.GetDB())

			return dErr
		},
	}
	for name, operation := range operations {
		if opErr := operation(); !errors.Is(opErr, migrate.ErrFailedMigrations) {
			t.Errorf("%s: expected errors.Is(err, ErrFailedMigrations), got: %v", name, opErr)
		}
	}

	// And the ledger is untouched throughout.
	applied, err = migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 1 || !applied[0].Failed {
		t.Errorf("state must be untouched, got %v", applied)
	}

	// Status keeps working — it is how an operator sees what happened.
	if applied[0].ID != "001_partial" {
		t.Errorf("Status reported %q, want 001_partial", applied[0].ID)
	}
}

// An irreversible migration applies normally but refuses to be rolled back, and
// the refusal happens before anything is executed.
func TestLibrary_IrreversibleMigration(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_keep", "CREATE TABLE keep_me (id INT);", "DROP TABLE keep_me;")
	writeMigration(t, tmpDir, "002_destructive",
		"DROP TABLE keep_me;",
		"-- +migrate irreversible")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("applying an irreversible migration should work: %v", err)
	}

	// Rolling back everything must be refused up front.
	result, err := migrate.DownAll(t.Context(), dbConn.GetDB())
	if !errors.Is(err, migrate.ErrIrreversible) {
		t.Fatalf("expected errors.Is(err, ErrIrreversible), got: %v", err)
	}

	if !strings.Contains(err.Error(), "002_destructive") {
		t.Errorf("error should name the irreversible migration, got: %v", err)
	}

	if result != nil && len(result.RolledBack) != 0 {
		t.Errorf("nothing may be rolled back, got %v", result.RolledBack)
	}

	// Both rows are still there: the refusal came before any execution, so the
	// reversible migration underneath was not touched either.
	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 2 {
		t.Errorf("expected both migrations still applied, got %v", applied)
	}

	// A Reconcile that would roll it back is refused the same way.
	emptyDir := t.TempDir()
	writeMigration(t, emptyDir, "001_keep", "CREATE TABLE keep_me (id INT);", "DROP TABLE keep_me;")

	if _, err := migrate.Reconcile(t.Context(), dbConn.GetDB(), os.DirFS(emptyDir),
		migrate.WithRollback()); !errors.Is(err, migrate.ErrIrreversible) {
		t.Errorf("expected Reconcile to refuse with ErrIrreversible, got: %v", err)
	}
}

// Rolling back only as far as the reversible migrations is allowed: the check
// covers the candidates, not the whole ledger.
func TestLibrary_IrreversibleDoesNotBlockNewerRollbacks(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_destructive", "CREATE TABLE gone (id INT);", "-- +migrate irreversible")
	writeMigration(t, tmpDir, "002_normal", "CREATE TABLE fine (id INT);", "DROP TABLE fine;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	result, err := migrate.Down(t.Context(), dbConn.GetDB(), 1)
	if err != nil {
		t.Fatalf("rolling back only the reversible migration should work: %v", err)
	}

	if len(result.RolledBack) != 1 || result.RolledBack[0] != "002_normal" {
		t.Errorf("RolledBack = %v, want [002_normal]", result.RolledBack)
	}
}

// The Result must describe what actually ran, not what was planned: a run that
// dies on the second of three migrations reports one applied, not three.
func TestLibrary_ResultReportsActualProgress(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_ok", "CREATE TABLE ok1 (id INT);", "DROP TABLE ok1;")
	writeMigration(t, tmpDir, "002_broken", "CREATE TABLE (((;", "SELECT 1;")
	writeMigration(t, tmpDir, "003_never", "CREATE TABLE never (id INT);", "DROP TABLE never;")

	result, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir))
	if err == nil {
		t.Fatal("expected the broken migration to fail")
	}

	if result == nil {
		t.Fatal("expected a non-nil Result alongside the error")
	}

	if len(result.Applied) != 1 || result.Applied[0] != "001_ok" {
		t.Errorf("Applied = %v, want [001_ok]: only the first migration actually ran", result.Applied)
	}

	// And the database agrees.
	applied := getAppliedIDs(t, dbConn.GetDB())
	if len(applied) != 1 || applied[0] != "001_ok" {
		t.Errorf("database has %v, want [001_ok]", applied)
	}
}

// Down needs no fsys: rollback scripts come from the database, so it works from
// a checkout that no longer has the files.
func TestLibrary_DownWithoutFiles(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")
	writeMigration(t, tmpDir, "002_posts", "CREATE TABLE t2 (id INT);", "DROP TABLE t2;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("Up failed: %v", err)
	}

	// Delete the files entirely, then roll back.
	if err := os.RemoveAll(tmpDir); err != nil {
		t.Fatalf("failed to remove the migration directory: %v", err)
	}

	result, err := migrate.DownAll(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Down failed with no files present: %v", err)
	}

	want := []string{"002_posts", "001_init"} // reverse order
	if len(result.RolledBack) != len(want) {
		t.Fatalf("RolledBack = %v, want %v", result.RolledBack, want)
	}

	for i, id := range want {
		if result.RolledBack[i] != id {
			t.Errorf("RolledBack[%d] = %q, want %q", i, result.RolledBack[i], id)
		}
	}

	applied, err := migrate.Status(t.Context(), dbConn.GetDB())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("expected an empty ledger after rolling everything back, got %v", applied)
	}
}

// A notransaction migration is split into statements and run one by one,
// precisely so that CREATE INDEX CONCURRENTLY — which Postgres refuses inside a
// transaction — can work. An E-string earlier in the script must not desync the
// splitter, because a script sent as one statement is wrapped in an implicit
// transaction and the CONCURRENTLY step then fails.
func TestLibrary_NoTransactionWithEscapeString(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_concurrent",
		"-- +migrate notransaction\n"+
			"CREATE TABLE notes (id INT, body TEXT);\n"+
			`INSERT INTO notes VALUES (1, E'quote: \'; not a statement end');`+"\n"+
			"CREATE INDEX CONCURRENTLY idx_notes_id ON notes(id);",
		"DROP INDEX IF EXISTS idx_notes_id;\nDROP TABLE IF EXISTS notes;")

	if _, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir)); err != nil {
		t.Fatalf("notransaction migration with an E-string failed: %v", err)
	}

	// The index exists, so the statements really did run outside a transaction.
	var indexExists bool
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT to_regclass('idx_notes_id') IS NOT NULL").Scan(&indexExists); err != nil {
		t.Fatalf("failed to check for the index: %v", err)
	}

	if !indexExists {
		t.Error("idx_notes_id was not created")
	}

	// And the literal made it through intact.
	var body string
	if err := dbConn.GetDB().QueryRowContext(t.Context(),
		"SELECT body FROM notes WHERE id = 1").Scan(&body); err != nil {
		t.Fatalf("failed to read the inserted row: %v", err)
	}

	if want := "quote: '; not a statement end"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestLibrary_ErrPoolTooSmallIsDetectable(t *testing.T) {
	t.Parallel()

	cfg := dbtest.SetupTestPostgres(t)

	dbConn := dbtest.MustConnect(t, cfg)
	defer dbConn.Close()

	dbConn.GetDB().SetMaxOpenConns(1)

	tmpDir := t.TempDir()
	writeMigration(t, tmpDir, "001_init", "CREATE TABLE t1 (id INT);", "DROP TABLE t1;")

	_, err := migrate.Up(t.Context(), dbConn.GetDB(), os.DirFS(tmpDir))
	if !errors.Is(err, migrate.ErrPoolTooSmall) {
		t.Errorf("expected errors.Is(err, ErrPoolTooSmall), got: %v", err)
	}
}
