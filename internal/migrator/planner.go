package migrator

import "slices"

// MigrationPlan represents a migration execution plan.
type MigrationPlan struct {
	ToRollback  []*Migration // migrations to rollback (in execution order - reverse)
	ToApply     []*Migration // migrations to apply (in execution order)
	Interleaved []*Migration // migrations to apply out of lexicographic order (see BuildPlan)
}

// BuildPlan builds a migration plan by comparing files and DB using set difference.
//
// rollbackExtras states whether the caller is going to execute ToRollback, and
// must match what it actually does, because it decides which applied
// migrations count when detecting interleaving:
//
//   - true (Reconcile): extras are rolled back before anything is applied, so a
//     migration is interleaved only relative to the records that REMAIN
//     applied. Switching to a branch that drops one migration and adds an
//     earlier-sorting one is then an ordinary forward apply against the common
//     ancestor, not an out-of-order apply.
//   - false (Up): extras stay applied, so they still count — applying a
//     migration that sorts before them genuinely does run out of order.
func BuildPlan(fromFiles, fromDB []*Migration, rollbackExtras bool) *MigrationPlan {
	plan := &MigrationPlan{
		ToRollback:  make([]*Migration, 0),
		ToApply:     make([]*Migration, 0),
		Interleaved: make([]*Migration, 0),
	}

	// Create sets for fast lookup
	filesMap := make(map[string]*Migration)
	for _, m := range fromFiles {
		filesMap[m.ID] = m
	}

	dbMap := make(map[string]*Migration)
	for _, m := range fromDB {
		dbMap[m.ID] = m
	}

	// Highest applied ID that will still be applied once this plan has run.
	// When the caller rolls extras back, they must not count: after they are
	// gone, a file migration sorting before them is no longer out of order.
	var maxAppliedID string

	for _, m := range fromDB {
		if rollbackExtras {
			if _, keptInFiles := filesMap[m.ID]; !keptInFiles {
				continue
			}
		}

		if m.ID > maxAppliedID {
			maxAppliedID = m.ID
		}
	}

	// ToRollback: migrations in DB but not in files.
	// fromDB is in the order the migrations were applied, so iterating in reverse
	// rolls back dependencies in the correct sequence.
	for _, v := range slices.Backward(fromDB) {
		dbMigration := v
		if _, exists := filesMap[dbMigration.ID]; !exists {
			plan.ToRollback = append(plan.ToRollback, dbMigration)
		}
	}

	// ToApply: migrations in files but not in DB.
	// Since fromFiles is sorted alphabetically, we iterate forward
	// to apply them in alphabetical order.
	for _, fileMigration := range fromFiles {
		if _, exists := dbMap[fileMigration.ID]; !exists {
			plan.ToApply = append(plan.ToApply, fileMigration)
			if maxAppliedID != "" && fileMigration.ID < maxAppliedID {
				plan.Interleaved = append(plan.Interleaved, fileMigration)
			}
		}
	}

	return plan
}
