package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound reports a session id that is not in the index.
var ErrNotFound = errors.New("session not found")

// Summary is one row of the session list.
//
// Everything the list renders comes from the sessions table, so drawing the
// pane never touches a session file. Preview and Reply are stored at index
// time for exactly this reason: parsing 1100 files to render a list would cost
// more than every other part of a request combined.
type Summary struct {
	ID        string
	Project   string
	CWD       string
	Path      string
	Host      string
	StartedAt time.Time
	NumMsgs   int

	// Title is the generated summary, empty until that pass runs.
	Title   string
	Preview string
	Reply   string
}

// Heading is what the list shows in bold: the generated title when there is
// one, and the opening message when there is not, so a row is never blank.
func (s Summary) Heading() string {
	if s.Title != "" {
		return s.Title
	}
	if s.Preview != "" {
		return s.Preview
	}
	return "(empty session)"
}

// Subtitle is the third line. It avoids repeating whatever Heading used.
func (s Summary) Subtitle() string {
	if s.Title == "" {
		return s.Reply
	}
	return s.Preview
}

// SummaryColumns returns the select list ScanSummary expects, with each
// column qualified by alias. Exported so search can return the same row type
// the list pane already renders, rather than a parallel one that has to be
// kept in step with it.
func SummaryColumns(alias string) string {
	if alias != "" {
		alias += "."
	}
	return alias + "id, " + alias + "project, " + alias + "cwd, " + alias + "path, " +
		alias + "host, " + alias + "started_at, " + alias + "n_msgs, " +
		"COALESCE(" + alias + "title, ''), " + alias + "preview, " + alias + "reply"
}

var summaryColumns = SummaryColumns("")

// ScanSummary reads a row selected with SummaryColumns.
func ScanSummary(rows interface{ Scan(...any) error }) (Summary, error) {
	return scanSummary(rows)
}

// scanSummary reads the SummaryColumns, after any columns selected ahead of
// them into lead.
func scanSummary(rows interface{ Scan(...any) error }, lead ...any) (Summary, error) {
	var s Summary
	var startedAt string
	err := rows.Scan(append(lead, &s.ID, &s.Project, &s.CWD, &s.Path, &s.Host, &startedAt,
		&s.NumMsgs, &s.Title, &s.Preview, &s.Reply)...)
	if err != nil {
		return s, err
	}
	// Timestamps are stored as RFC3339 text. A session written by a client with
	// a different format should not take the whole list down with it.
	s.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	return s, nil
}

// ListSessions returns sessions newest first.
func (d *DB) ListSessions(ctx context.Context, limit, offset int) ([]Summary, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+summaryColumns+` FROM sessions
		 ORDER BY started_at DESC, id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Summary
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Session returns one session's metadata.
func (d *DB) Session(ctx context.Context, id string) (Summary, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT `+summaryColumns+` FROM sessions WHERE id = ?`, id)
	s, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%q: %w", id, ErrNotFound)
	}
	return s, err
}

// LastArrival is the newest seq in arrivals, or 0 when nothing has arrived
// since the table was created. A page records it before listing sessions, so
// that one indexed in between turns up in both rather than in neither.
func (d *DB) LastArrival(ctx context.Context) (int64, error) {
	var seq int64
	err := d.sql.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM arrivals`).Scan(&seq)
	return seq, err
}

// ArrivedSince returns sessions that entered the index after seq, in the order
// they arrived, at most limit of them, and the seq of the last one returned --
// seq itself when there are none, so the caller can always carry it forward.
func (d *DB) ArrivedSince(ctx context.Context, seq int64, limit int) ([]Summary, int64, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT a.seq, `+SummaryColumns("s")+`
		 FROM arrivals a JOIN sessions s ON s.id = a.session_id
		 WHERE a.seq > ? ORDER BY a.seq LIMIT ?`, seq, limit)
	if err != nil {
		return nil, seq, err
	}
	defer rows.Close()

	var out []Summary
	last := seq
	for rows.Next() {
		s, err := scanSummary(rows, &last)
		if err != nil {
			return nil, seq, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, seq, err
	}
	return out, last, nil
}

// CountSessions reports how many sessions are indexed.
func (d *DB) CountSessions(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}
