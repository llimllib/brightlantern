package index

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/llimllib/brightlantern/internal/session"
)

// MetaMigrations counts the entries of migrations applied to this index.
// Absent means none, which is every index written before migrations existed.
const MetaMigrations = "migrations"

// migration takes an index from one layout to the next.
type migration struct {
	// What it does, for the note printed as it runs and for whoever reads
	// this list next.
	desc  string
	apply func(*sql.Tx) error
}

// migrations change the layout of an existing index, in order, forward only.
// Each runs once, in its own transaction with the count recorded under
// MetaMigrations, so a failure leaves the index at the last one that worked.
//
// This is how the layout changes now -- not by bumping SchemaVersion, which
// deleted the database and rebuilt it. That was right while everything in it
// was derived from the session files. The archive is not: messages holds
// sessions whose files are gone, and titles cost an API call each. A rebuild
// loses the first for good and pays for the second again.
//
// A migration sees the layout the previous one left, not the one schema
// describes: schema runs after them, so a CREATE INDEX there may name a column
// a migration has yet to add. SQLite's ALTER TABLE cannot do everything, and
// what it cannot is done the way SQLite documents -- create the new table, copy
// across, drop the old, rename -- which is still a migration, not a reset.
//
// Never edit or reorder an entry once released. Indexes in the wild record
// how many they have applied, and that number is only meaningful if the list
// it counts never changes underneath it.
var migrations = []migration{}

// SchemaVersion is frozen at 1 because the released binaries cannot be
// changed, and v0.0.1 and v0.0.2 delete any index whose schema_version is not
// theirs. Bumping it would make reinstalling an older version erase the
// archive. Counting migrations under a key those versions never read means an
// older binary opening a newer index gets a SQL error, or leaves derived data
// stale for the next --full -- both recoverable, which deletion is not.
//
// Downgrading is still unsupported; it is merely no longer destructive.

// NewerIndexError reports an index migrated by a newer brightlantern than this one.
//
// Never reset: everything in it may be newer than this binary understands,
// including archive rows this binary would not write back.
type NewerIndexError struct {
	Path    string
	Applied int
	Known   int
}

func (e *NewerIndexError) Error() string {
	return fmt.Sprintf("index at %s was written by a newer brightlantern (%d migrations; this "+
		"build knows %d). Upgrade brightlantern, or point --db somewhere else", e.Path, e.Applied, e.Known)
}

// StaleIndexError reports an index whose schema_version is not SchemaVersion.
//
// Nothing released has written another value, so this is a development index
// or a damaged one. It is a distinct type so OpenOrReset can recover from it.
type StaleIndexError struct {
	Path  string
	Found string
	Want  int
}

func (e *StaleIndexError) Error() string {
	return fmt.Sprintf("index at %s has schema version %s, this build expects %d",
		e.Path, e.Found, e.Want)
}

func (d *DB) init() error {
	existing, err := d.existing()
	if err != nil {
		return err
	}

	if !existing {
		if _, err := d.sql.Exec(schema); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		if err := d.addColumns(); err != nil {
			return err
		}
		if _, err := d.sql.Exec(views); err != nil {
			return fmt.Errorf("create views: %w", err)
		}
		for k, v := range map[string]string{
			MetaSchemaVersion: strconv.Itoa(SchemaVersion),
			MetaMigrations:    strconv.Itoa(len(migrations)),
			MetaChunkChars:    strconv.Itoa(session.MaxChunkChars),
		} {
			if err := d.SetMeta(k, v); err != nil {
				return err
			}
		}
		return nil
	}

	// Every check that can refuse comes before anything is written: an index
	// this build declines to open is left exactly as it was found.
	if got, err := d.Meta(MetaSchemaVersion); err != nil {
		return err
	} else if got != strconv.Itoa(SchemaVersion) {
		return &StaleIndexError{Path: d.path, Found: got, Want: SchemaVersion}
	}
	applied, err := d.appliedMigrations()
	if err != nil {
		return err
	}
	if applied > len(migrations) {
		return &NewerIndexError{Path: d.path, Applied: applied, Known: len(migrations)}
	}

	// Before migrations: they predate them, and are idempotent. See addedColumns.
	if err := d.addColumns(); err != nil {
		return err
	}
	for i := applied; i < len(migrations); i++ {
		if err := d.applyMigration(i); err != nil {
			return err
		}
	}
	// After migrations, so that it only ever adds what is wholly new.
	if _, err := d.sql.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := d.sql.Exec(views); err != nil {
		return fmt.Errorf("create views: %w", err)
	}
	return nil
}

// existing reports whether this file already holds an index. One with a meta
// table but no schema_version is treated as new, as it always has been.
func (d *DB) existing() (bool, error) {
	var n int
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	v, err := d.Meta(MetaSchemaVersion)
	return v != "", err
}

func (d *DB) appliedMigrations() (int, error) {
	v, err := d.Meta(MetaMigrations)
	if err != nil || v == "" {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("index at %s: unreadable %s %q", d.path, MetaMigrations, v)
	}
	return n, nil
}

func (d *DB) applyMigration(i int) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := migrations[i].apply(tx); err != nil {
		return fmt.Errorf("migration %d (%s): %w", i+1, migrations[i].desc, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		MetaMigrations, strconv.Itoa(i+1)); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetReport says what OpenOrReset did with an index it could not open.
type ResetReport struct {
	// Found is the schema_version the old index carried.
	Found string

	// Kept is where the old index was moved, or "" if it was deleted. It is
	// moved whenever it holds anything a rebuild cannot recover.
	Kept string
}

// OpenOrReset opens the index, replacing it with an empty one if its
// schema_version is not SchemaVersion. The report is nil when nothing was
// replaced, so the caller can tell the user why indexing is about to run and
// where the old index went.
//
// A newer index is never replaced: see NewerIndexError.
func OpenOrReset(path, driver string) (*DB, *ResetReport, error) {
	db, err := Open(path, driver)
	if err == nil {
		return db, nil, nil
	}
	var stale *StaleIndexError
	if !errors.As(err, &stale) {
		return nil, nil, err
	}
	report := &ResetReport{Found: stale.Found}

	keep, err := holdsArchive(path)
	if err != nil {
		return nil, nil, err
	}
	if keep {
		report.Kept, err = moveAside(path, stale.Found)
	} else {
		err = removeIndex(path)
	}
	if err != nil {
		return nil, nil, err
	}
	db, err = Open(path, driver)
	if err != nil {
		return nil, nil, err
	}
	return db, report, nil
}

// holdsArchive reports whether an index contains anything a rebuild from the
// session files cannot recover: archived messages, or generated titles.
// Asked without Open, which is what refused this index in the first place.
func holdsArchive(path string) (bool, error) {
	db, err := sql.Open(DriverName, path+"?_query_only=true")
	if err != nil {
		return false, err
	}
	defer db.Close()
	for _, q := range []string{
		`SELECT EXISTS(SELECT 1 FROM messages)`,
		`SELECT EXISTS(SELECT 1 FROM sessions WHERE title IS NOT NULL)`,
	} {
		var has bool
		if err := db.QueryRow(q).Scan(&has); err != nil {
			continue // no such table or column: nothing of that kind to keep
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

// indexFiles are the files making up one index. -wal and -shm travel with the
// database, or SQLite may recover or lose content that belongs to it.
var indexFiles = []string{"", "-wal", "-shm"}

func removeIndex(path string) error {
	for _, suffix := range indexFiles {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale index %s%s: %w", path, suffix, err)
		}
	}
	return nil
}

// moveAside renames an index out of the way, keeping it whole, and returns
// where it went.
func moveAside(path, version string) (string, error) {
	dest := fmt.Sprintf("%s.schema-%s.%s", path, version, time.Now().Format("20060102-150405"))
	for _, suffix := range indexFiles {
		if err := os.Rename(path+suffix, dest+suffix); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("moving stale index aside to %s: %w", dest, err)
		}
	}
	return dest, nil
}
