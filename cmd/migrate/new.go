package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
)

// timestampLayout is the ID prefix format: 14 digits, always the same width, so
// lexicographic order — which is the only order this library uses — matches
// chronological order.
const timestampLayout = "20060102150405"

// maxSlugLength keeps the generated filename comfortably inside the limits of
// every filesystem once the timestamp and suffix are added.
const maxSlugLength = 100

const (
	// dirPerm and filePerm are the usual defaults, left to be narrowed by the
	// caller's umask. Migration scripts are source code, not secrets.
	dirPerm  os.FileMode = 0o755
	filePerm os.FileMode = 0o644
)

const upTemplate = `-- %s
--
-- Forward migration. Runs inside a transaction unless the first line of this
-- file is:
--
--   -- +migrate notransaction
--
-- which is required for statements PostgreSQL refuses inside one, such as
-- CREATE INDEX CONCURRENTLY.

`

const downTemplate = `-- Undo: %s
--
-- Rollback for the migration above. This script is stored in the database when
-- the migration is applied, and it is that stored copy which runs on rollback —
-- so it must undo the forward migration on its own.
--
-- If the change genuinely cannot be undone, replace everything in this file
-- with exactly:
--
--   -- +migrate irreversible

`

// newCommand builds the "new" subcommand, which scaffolds an up/down pair.
//
// resolvePath is shared with the commands that read migrations, so a single
// --migration-path (or MIGRATION_PATH) governs where files are written and
// where they are read from. It never touches the database.
func newCommand(resolvePath func() (string, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "new <name>",
		Short: "Create an up/down migration pair",
		Long: "Create a timestamped .up.sql/.down.sql pair.\n\n" +
			"The name is slugified and prefixed with a UTC timestamp, so that IDs sort\n" +
			"chronologically and two people working in parallel cannot collide.",
		Example: "  migrate new add_users_table\n" +
			"  migrate new \"backfill user emails\" --migration-path ./db/migrations",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolvePath()
			if err != nil {
				return err
			}

			slug, err := slugify(args[0])
			if err != nil {
				return err
			}

			// UTC, not local time: a team spread across time zones would
			// otherwise generate IDs that sort in an order nobody intended.
			id := time.Now().UTC().Format(timestampLayout) + "_" + slug

			created, err := writeMigrationPair(dir, id, slug)
			if err != nil {
				return err
			}

			for _, path := range created {
				fmt.Fprintln(cmd.OutOrStdout(), path)
			}

			return nil
		},
	}
}

// writeMigrationPair creates both halves, refusing to overwrite either. If the
// second file cannot be created the first is removed again: a lone .up.sql is
// rejected by the loader, so leaving one behind would break every later command.
func writeMigrationPair(dir, id, slug string) ([]string, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create migration directory %s: %w", dir, err)
	}

	upPath := filepath.Join(dir, id+".up.sql")
	downPath := filepath.Join(dir, id+".down.sql")

	if err := writeNewFile(upPath, fmt.Sprintf(upTemplate, slug)); err != nil {
		return nil, err
	}

	if err := writeNewFile(downPath, fmt.Sprintf(downTemplate, slug)); err != nil {
		_ = os.Remove(upPath)

		return nil, err
	}

	return []string{upPath, downPath}, nil
}

// writeNewFile creates path, failing if it already exists. O_EXCL rather than a
// stat-then-write: two `new` invocations in the same second must not have one
// silently clobber the other's file.
func writeNewFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists", path)
		}

		return fmt.Errorf("create %s: %w", path, err)
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// slugify reduces a human-written name to the safe subset of a migration ID:
// lowercase ASCII letters, digits and underscores.
//
// Lowercase because IDs differing only in case are rejected by the loader —
// they collide on case-insensitive filesystems. No dots, because the loader
// splits the filename on the .up.sql / .down.sql suffix.
func slugify(name string) (string, error) {
	var b strings.Builder

	lastUnderscore := true // leading separators are dropped

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)

			lastUnderscore = false

		// Everything else — spaces, dashes, dots, punctuation, non-ASCII — folds
		// to a single underscore rather than being dropped, so that "add users"
		// and "add-users" do not both become "addusers".
		case !lastUnderscore:
			b.WriteByte('_')

			lastUnderscore = true
		}
	}

	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		return "", fmt.Errorf(
			"migration name %q has no letters or digits to build an identifier from", name)
	}

	if len(slug) > maxSlugLength {
		slug = strings.Trim(slug[:maxSlugLength], "_")
	}

	return slug, nil
}
