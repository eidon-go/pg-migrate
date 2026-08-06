package migrator_test

import (
	"slices"
	"testing"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

func TestBuildPlan_OnlyNewMigrations(t *testing.T) {
	t.Parallel()

	// DB: [1,2,3], Files: [1,2,3,4,5]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
		{ID: "004_users.sql"},
		{ID: "005_sessions.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if len(plan.ToRollback) != 0 {
		t.Errorf("Expected 0 migrations to rollback, got %d", len(plan.ToRollback))
	}

	if len(plan.ToApply) != 2 {
		t.Errorf("Expected 2 migrations to apply, got %d", len(plan.ToApply))
	}

	// Check order
	if len(plan.ToApply) >= 2 {
		if plan.ToApply[0].ID != "004_users.sql" {
			t.Errorf("Expected first migration to apply: 004_users.sql, got %s", plan.ToApply[0].ID)
		}

		if plan.ToApply[1].ID != "005_sessions.sql" {
			t.Errorf("Expected second migration to apply: 005_sessions.sql, got %s", plan.ToApply[1].ID)
		}
	}
}

func TestBuildPlan_OnlyRollback(t *testing.T) {
	t.Parallel()

	// DB: [1,2,3,4,5], Files: [1,2,3]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
		{ID: "004_users.sql"},
		{ID: "005_sessions.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if len(plan.ToRollback) != 2 {
		t.Errorf("Expected 2 migrations to rollback, got %d", len(plan.ToRollback))
	}

	if len(plan.ToApply) != 0 {
		t.Errorf("Expected 0 migrations to apply, got %d", len(plan.ToApply))
	}

	// Check rollback order (should be in reverse order)
	if len(plan.ToRollback) >= 2 {
		if plan.ToRollback[0].ID != "005_sessions.sql" {
			t.Errorf("Expected first migration to rollback: 005_sessions.sql, got %s", plan.ToRollback[0].ID)
		}

		if plan.ToRollback[1].ID != "004_users.sql" {
			t.Errorf("Expected second migration to rollback: 004_users.sql, got %s", plan.ToRollback[1].ID)
		}
	}
}

func TestBuildPlan_MissingInMiddle(t *testing.T) {
	t.Parallel()

	// DB: [1,2,4], Files: [1,2,3,4]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "004_users.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
		{ID: "004_users.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	// Should just apply 003, no rollbacks needed
	if len(plan.ToRollback) != 0 {
		t.Errorf("Expected 0 migrations to rollback, got %d", len(plan.ToRollback))
	}

	if len(plan.ToApply) != 1 {
		t.Errorf("Expected 1 migration to apply, got %d", len(plan.ToApply))
	}

	// Check order
	if len(plan.ToApply) >= 1 {
		if plan.ToApply[0].ID != "003_comments.sql" {
			t.Errorf("Expected first to apply: 003_comments.sql, got %s", plan.ToApply[0].ID)
		}
	}
}

func TestBuildPlan_ExtraInMiddle(t *testing.T) {
	t.Parallel()

	// DB: [1,2,3,4], Files: [1,2,4]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
		{ID: "004_users.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "004_users.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	// Should rollback 003, no applies needed
	if len(plan.ToRollback) != 1 {
		t.Errorf("Expected 1 migration to rollback, got %d", len(plan.ToRollback))
	}

	if len(plan.ToApply) != 0 {
		t.Errorf("Expected 0 migrations to apply, got %d", len(plan.ToApply))
	}

	// Check rollback order
	if len(plan.ToRollback) >= 1 {
		if plan.ToRollback[0].ID != "003_comments.sql" {
			t.Errorf("Expected rollback: 003_comments.sql, got %s", plan.ToRollback[0].ID)
		}
	}
}

func TestBuildPlan_NoChanges(t *testing.T) {
	t.Parallel()

	// DB: [1,2,3], Files: [1,2,3]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if len(plan.ToRollback) != 0 {
		t.Errorf("Expected 0 migrations to rollback, got %d", len(plan.ToRollback))
	}

	if len(plan.ToApply) != 0 {
		t.Errorf("Expected 0 migrations to apply, got %d", len(plan.ToApply))
	}
}

func TestBuildPlan_RollbackOrder(t *testing.T) {
	t.Parallel()

	// Check that rollbacks are in reverse order
	// DB: [1,2,3,4,5,6], Files: [1,2,3]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
		{ID: "004_users.sql"},
		{ID: "005_sessions.sql"},
		{ID: "006_tokens.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if len(plan.ToRollback) != 3 {
		t.Errorf("Expected 3 migrations to rollback, got %d", len(plan.ToRollback))
	}

	// Check that order is reversed: 6, 5, 4
	expected := []string{"006_tokens.sql", "005_sessions.sql", "004_users.sql"}
	for i, m := range plan.ToRollback {
		if m.ID != expected[i] {
			t.Errorf("Expected rollback[%d] = %s, got %s", i, expected[i], m.ID)
		}
	}
}

func TestBuildPlan_Interleaved(t *testing.T) {
	t.Parallel()

	// DB: [001_init.sql, 003_comments.sql]
	// Files: [001_init.sql, 002_posts.sql, 003_comments.sql]
	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "003_comments.sql"},
	}

	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "003_comments.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	// Should apply 002_posts.sql
	if len(plan.ToApply) != 1 {
		t.Fatalf("Expected 1 migration to apply, got %d", len(plan.ToApply))
	}

	if plan.ToApply[0].ID != "002_posts.sql" {
		t.Errorf("Expected to apply 002_posts.sql, got %s", plan.ToApply[0].ID)
	}

	// Should detect 002_posts.sql as interleaved (since 003_comments.sql is already applied)
	if len(plan.Interleaved) != 1 {
		t.Fatalf("Expected 1 interleaved migration, got %d", len(plan.Interleaved))
	}

	if plan.Interleaved[0].ID != "002_posts.sql" {
		t.Errorf("Expected interleaved to be 002_posts.sql, got %s", plan.Interleaved[0].ID)
	}
}

// ids extracts migration IDs for comparison in assertions.
func ids(ms []*migrator.Migration) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}

	return out
}

func equalIDs(got []*migrator.Migration, want []string) bool {
	return slices.Equal(ids(got), want)
}

// Branch switch: the DB holds a migration from another feature branch that the
// current branch does not have, and the current branch adds one that sorts
// before it. With rollbackExtras=true the extra is gone before anything is
// applied, so this is a plain forward apply against the common ancestor and
// must NOT be reported as interleaved.
func TestBuildPlan_BranchSwitchIsNotInterleaved(t *testing.T) {
	t.Parallel()

	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "20240101_a.sql"}, // from branch A, absent from this branch
	}
	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "20231201_b.sql"}, // added by branch B, sorts before 20240101_a
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if !equalIDs(plan.ToRollback, []string{"20240101_a.sql"}) {
		t.Errorf("ToRollback = %v, want [20240101_a.sql]", ids(plan.ToRollback))
	}

	if !equalIDs(plan.ToApply, []string{"20231201_b.sql"}) {
		t.Errorf("ToApply = %v, want [20231201_b.sql]", ids(plan.ToApply))
	}

	if len(plan.Interleaved) != 0 {
		t.Errorf("Interleaved = %v, want empty: the extra it is compared against is rolled back first",
			ids(plan.Interleaved))
	}
}

// Same input as above, but with Up semantics: extras stay applied, so applying
// a migration that sorts before them really does run out of order and the
// warning must still fire.
func TestBuildPlan_BranchSwitchIsInterleavedWhenExtrasKept(t *testing.T) {
	t.Parallel()

	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "20240101_a.sql"},
	}
	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"},
		{ID: "20231201_b.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, false)

	if !equalIDs(plan.Interleaved, []string{"20231201_b.sql"}) {
		t.Errorf("Interleaved = %v, want [20231201_b.sql]: extras stay applied in Up mode",
			ids(plan.Interleaved))
	}
}

// Rolling an extra back must not excuse a migration that is out of order
// relative to a record which REMAINS applied.
func TestBuildPlan_InterleavedStillDetectedAlongsideExtras(t *testing.T) {
	t.Parallel()

	fromDB := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "003_comments.sql"},
		{ID: "005_extra.sql"}, // extra, will be rolled back
	}
	fromFiles := []*migrator.Migration{
		{ID: "001_init.sql"},
		{ID: "002_posts.sql"}, // out of order relative to 003, which stays
		{ID: "003_comments.sql"},
	}

	plan := migrator.BuildPlan(fromFiles, fromDB, true)

	if !equalIDs(plan.ToRollback, []string{"005_extra.sql"}) {
		t.Errorf("ToRollback = %v, want [005_extra.sql]", ids(plan.ToRollback))
	}

	if !equalIDs(plan.Interleaved, []string{"002_posts.sql"}) {
		t.Errorf("Interleaved = %v, want [002_posts.sql]: 003_comments.sql remains applied",
			ids(plan.Interleaved))
	}
}
