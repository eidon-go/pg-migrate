package source

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/eidon-go/pg-migrate/internal/migrator"
)

const (
	upSuffix   = migrator.UpFileSuffix
	downSuffix = migrator.DownFileSuffix
	sqlExt     = ".sql"
)

// FS reads migration file pairs from an fs.FS.
type FS struct {
	fsys fs.FS
}

// NewFS creates a new FS source that reads migrations from the specified fs.FS.
func NewFS(fsys fs.FS) *FS {
	return &FS{
		fsys: fsys,
	}
}

// GetMigrations reads and parses all migrations from the source fs.FS.
//
// A migration is a pair of files, <id>.up.sql and <id>.down.sql, and its ID is
// the shared base name. Both halves are required: a missing rollback script is
// an error, because an empty one would let automatic recovery and
// branch-switch rollback "succeed" while doing nothing and still drop the
// ledger row, leaving the schema silently drifted.
//
// Only the top-level directory is read; subdirectories are not traversed.
// Files that are not .sql are ignored, but a .sql file that is neither
// *.up.sql nor *.down.sql is an error rather than a silent skip — in a
// migrations directory the only acceptable outcomes for a .sql file are "it
// runs" and "you were told why it did not".
func (f *FS) GetMigrations() ([]*migrator.Migration, error) {
	entries, err := fs.ReadDir(f.fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
	}

	ups, downs, err := collectPairs(entries)
	if err != nil {
		return nil, err
	}

	if len(ups) == 0 && len(downs) == 0 {
		if err := f.explainEmptyRoot(entries); err != nil {
			return nil, err
		}
	}

	migrations := make([]*migrator.Migration, 0, len(ups))
	for _, id := range sortedIDs(ups, downs) {
		upName, hasUp := ups[id]
		downName, hasDown := downs[id]

		switch {
		case !hasUp:
			return nil, fmt.Errorf(
				"migration %q: found %s but no %s", id, downName, id+upSuffix)
		case !hasDown:
			return nil, fmt.Errorf(
				"migration %q: found %s but no %s; every migration needs a rollback script",
				id, upName, id+downSuffix)
		}

		upContent, err := fs.ReadFile(f.fsys, upName)
		if err != nil {
			return nil, fmt.Errorf("read file %s: %w", upName, err)
		}

		downContent, err := fs.ReadFile(f.fsys, downName)
		if err != nil {
			return nil, fmt.Errorf("read file %s: %w", downName, err)
		}

		migration, err := ParseMigrationPair(id, upContent, downContent)
		if err != nil {
			return nil, err
		}

		migrations = append(migrations, migration)
	}

	return migrations, nil
}

// explainEmptyRoot turns the commonest configuration mistake into a specific
// error instead of an empty result.
//
// Nothing at the top level but migrations one level down means the source is
// rooted a directory too high — typically an embed.FS used without fs.Sub, where
// the only entry is the "migrations" directory itself. Silently reporting zero
// migrations from that is dangerous: Up would do nothing and Reconcile would
// treat the whole schema as removable.
//
// An empty root with no migrations underneath either is legitimate (a project
// that has not written its first migration) and is left alone.
func (f *FS) explainEmptyRoot(entries []fs.DirEntry) error {
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		nested, err := fs.ReadDir(f.fsys, entry.Name())
		if err != nil {
			continue // an unreadable subdirectory is not our problem here
		}

		for _, candidate := range nested {
			if !candidate.IsDir() && strings.HasSuffix(candidate.Name(), upSuffix) {
				return fmt.Errorf(
					"no migrations at the source root, but %s/ contains %s: the source is rooted one "+
						"level too high (with go:embed, wrap it in fs.Sub(embedFS, %q))",
					entry.Name(), candidate.Name(), entry.Name())
			}
		}
	}

	return nil
}

// collectPairs indexes directory entries by migration ID, one map per half.
func collectPairs(entries []fs.DirEntry) (ups, downs map[string]string, _ error) {
	ups = make(map[string]string)
	downs = make(map[string]string)

	// Lowercased ID -> the ID as first seen, to reject names that would collide
	// on a case-insensitive filesystem and so behave differently on macOS or
	// Windows than on Linux.
	seenFolded := make(map[string]string)

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), sqlExt) {
			continue
		}

		var (
			id   string
			half map[string]string
		)

		switch {
		case strings.HasSuffix(name, upSuffix):
			id, half = strings.TrimSuffix(name, upSuffix), ups
		case strings.HasSuffix(name, downSuffix):
			id, half = strings.TrimSuffix(name, downSuffix), downs
		default:
			return nil, nil, fmt.Errorf(
				"migration file %s: name must end in %s or %s (lowercase)", name, upSuffix, downSuffix)
		}

		if id == "" {
			return nil, nil, fmt.Errorf("migration file %s: migration ID is empty", name)
		}

		if previous, exists := half[id]; exists {
			return nil, nil, fmt.Errorf(
				"migration %q: duplicate files %s and %s", id, previous, name)
		}

		half[id] = name

		if previous, exists := seenFolded[strings.ToLower(id)]; exists && previous != id {
			return nil, nil, fmt.Errorf(
				"migration IDs %q and %q differ only in letter case, which collides on case-insensitive filesystems",
				previous, id)
		}

		seenFolded[strings.ToLower(id)] = id
	}

	return ups, downs, nil
}

// sortedIDs returns every ID seen in either half, sorted, so that migrations
// are applied in lexicographic order and errors are reported deterministically.
func sortedIDs(ups, downs map[string]string) []string {
	ids := make([]string, 0, len(ups))
	for id := range ups {
		ids = append(ids, id)
	}

	for id := range downs {
		if _, paired := ups[id]; !paired {
			ids = append(ids, id)
		}
	}

	slices.Sort(ids)

	return ids
}
