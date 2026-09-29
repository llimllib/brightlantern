package index

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/llimllib/spireweb/internal/session"
)

// archiveMessages stores a session's messages verbatim, and returns how many
// rows it added and how many the session now has archived.
//
// Incremental in the only way that matters: pi appends, so a session that
// gained one message inserts one row rather than rewriting four hundred. The
// stored count is the starting point, which is correct exactly as long as
// sessions are append-only -- the same assumption chunk reuse and the title
// cache already rest on.
//
// A file with *fewer* messages than the archive is one of two things, told
// apart by its last message:
//
//   - **A prefix of the archive**, when that message matches the row at its
//     position. The archive holds more than the file: a longer copy merged in
//     from another machine, or a file that was truncated. Either way the
//     extra rows are the archive doing its job, and dropping them would make
//     it only as durable as the file.
//   - **A rewrite**, when it does not. The extra rows describe a conversation
//     the file no longer contains, and are dropped rather than left behind to
//     claim messages the session does not have.
//
// A file rewritten to a different conversation of the same length keeps the
// old messages, which merge reports as divergence rather than silently
// resolving.
func archiveMessages(ctx context.Context, tx *sql.Tx, s *session.Session) (added, total int, err error) {
	var have int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = ?`, s.ID).Scan(&have); err != nil {
		return 0, 0, err
	}

	if have > len(s.Messages) {
		prefix, err := isArchivePrefix(ctx, tx, s)
		if err != nil {
			return 0, 0, err
		}
		if prefix {
			return 0, have, nil
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE session_id = ? AND idx >= ?`,
			s.ID, len(s.Messages)); err != nil {
			return 0, 0, err
		}
		have = len(s.Messages)
	}
	if have == len(s.Messages) {
		return 0, have, nil
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO messages(session_id, idx, role, at, content)
		 VALUES(?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	n := 0
	for i := have; i < len(s.Messages); i++ {
		m := s.Messages[i]
		if len(m.Raw) == 0 {
			// Parsed without raw bytes: archiving a re-marshalled struct would
			// store a lossy copy, which is worse than storing nothing.
			continue
		}
		var at string
		if !m.At.IsZero() {
			at = m.At.UTC().Format(time.RFC3339Nano)
		}
		if _, err := stmt.ExecContext(ctx, s.ID, i, m.Role, at, string(m.Raw)); err != nil {
			return n, 0, err
		}
		n++
	}
	return n, have + n, nil
}

// isArchivePrefix reports whether a session's messages are the start of what
// is already archived for it. Only the last message is compared: sessions are
// append-only, so a file that shares its last message with the archive shares
// everything before it, and comparing one row keeps the check to one lookup
// however long the session is.
func isArchivePrefix(ctx context.Context, tx *sql.Tx, s *session.Session) (bool, error) {
	if len(s.Messages) == 0 {
		return true, nil
	}
	last := s.Messages[len(s.Messages)-1]
	if len(last.Raw) == 0 {
		return false, nil
	}
	var stored string
	err := tx.QueryRowContext(ctx,
		`SELECT content FROM messages WHERE session_id = ? AND idx = ?`,
		s.ID, len(s.Messages)-1).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stored == string(last.Raw), nil
}

// NeedsArchive reports whether the next build will backfill the archive, so a
// caller can say so before it happens.
//
// Exported because the growth is large and automatic: the server indexes in
// the background, so an index built before this table existed gains ~400MB
// within seconds of starting the server, and a progress bar reading "indexing"
// does not convey that.
func (d *DB) NeedsArchive() (bool, error) { return d.needsArchive() }

// needsArchive reports whether any indexed session has no archived messages.
//
// The same shape as needsVectors, and for the same reason: the skip test
// compares mtime and size, so an index built before this table existed would
// pass over every unchanged file forever and the archive would stay empty
// however many times indexing ran.
func (d *DB) needsArchive() (bool, error) {
	var missing bool
	err := d.sql.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM sessions s
		WHERE s.n_msgs > 0
		  AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id))`).Scan(&missing)
	return missing, err
}

// CountMessages reports how many messages are archived.
func (d *DB) CountMessages(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n)
	return n, err
}
