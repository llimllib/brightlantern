package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattn/go-sqlite3"
)

// DefaultPath is where the index lives: $XDG_DATA_HOME/brightlantern/index.db,
// else ~/.local/share/brightlantern/index.db, beside the model.
//
// Not a cache directory, which is where it used to be. ~/Library/Caches is
// somewhere macOS may empty under disk pressure, and cleanup tools empty on
// sight. That was fine while the index was derived from the session files; the
// archive is not, and the titles in it cost an API call each (#60). XDG
// because the model and the settings file already follow it.
func DefaultPath() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "brightlantern", "index.db")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "brightlantern", "index.db")
	}
	return "brightlantern-index.db"
}

// LegacyPath is where versions up to v0.0.2 kept the index, or "" if there is
// no such directory on this machine.
func LegacyPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "spireweb", "index.db")
}

// Relocation says where the index is, after Relocate has looked.
type Relocation struct {
	// Path is the index to use.
	Path string

	// Moved is the old path, when the index was moved from it to Path.
	Moved string

	// Stayed explains why an index at an old path was used where it is
	// rather than moved; "" otherwise.
	Stayed string
}

// Relocate moves an index from legacy to dst when dst has none and legacy has
// one, so that upgrading neither rebuilds the index -- re-embedding every
// chunk and looking as though everything was lost -- nor leaves it somewhere
// it can be deleted.
//
// Moved only when nothing has it open. Renaming a database under an open
// connection splits it: that process keeps writing to the moved file through
// its descriptors, while anything opening the old path gets a new, empty one.
// So an index in use -- by a brightlantern serve still running from before the
// upgrade -- is used where it is, and moved on a later run.
//
// Nothing here is an error. Any failure -- a rename across volumes, say --
// leaves the index where it is, which is what every version so far has done.
// Copying is the alternative, and a second 500MB copy that silently diverges
// from the first is worse than a note.
func Relocate(dst, legacy string) Relocation {
	r := Relocation{Path: dst}
	if legacy == "" || legacy == dst || exists(dst) || !exists(legacy) {
		return r
	}
	if busy, err := inUse(legacy); err != nil {
		r.Path, r.Stayed = legacy, fmt.Sprintf("it could not be checked: %v", err)
		return r
	} else if busy {
		r.Path, r.Stayed = legacy, "it is open in another brightlantern"
		return r
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		r.Path, r.Stayed = legacy, fmt.Sprintf("it could not be moved: %v", err)
		return r
	}
	// -wal and -shm go with it: a crashed process can leave pages in the -wal
	// that were never checkpointed, and SQLite finds both by name. A failure
	// part way puts back what had moved, rather than leave the database and
	// its WAL in different directories.
	var moved []string
	for _, suffix := range indexFiles {
		err := os.Rename(legacy+suffix, dst+suffix)
		if err == nil {
			moved = append(moved, suffix)
			continue
		}
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if suffix == "" && exists(dst) {
			return r // another process moved it first
		}
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(dst+moved[i], legacy+moved[i])
		}
		r.Path, r.Stayed = legacy, fmt.Sprintf("it could not be moved: %v", err)
		return r
	}
	r.Moved = legacy
	return r
}

// inUse reports whether any connection, in any process, has the database open.
//
// In WAL mode every open connection holds a shared lock on the database file
// for as long as it is open, idle or not; that is how SQLite itself knows when
// the last one closes. So an exclusive lock that cannot be had right now means
// someone is there. Not the -wal file: Apple's SQLite keeps it after a clean
// close, so a database nobody has open can have one.
func inUse(path string) (bool, error) {
	db, err := sql.Open(DriverName, path+"?_busy_timeout=0")
	if err != nil {
		return false, err
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		return false, err
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, `PRAGMA locking_mode=EXCLUSIVE`); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		if isBusy(err) {
			return true, nil
		}
		return false, err
	}
	// The lock is taken by the first read, not by BEGIN, in WAL mode.
	var n int
	err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&n)
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	if isBusy(err) {
		return true, nil
	}
	return false, err
}

func isBusy(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
