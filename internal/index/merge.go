package index

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/llimllib/spireweb/internal/session"
)

// MergeOptions configures a Merge.
type MergeOptions struct {
	// Embedder, when set, embeds the chunks of merged sessions. Without one
	// they are indexed lexically, and gain vectors on the next build that has
	// a model, through the same backfill any lexical index gets.
	Embedder   Embedder
	ChunkChars int // 0 uses session.MaxChunkChars

	// OnProgress is called as merged sessions are indexed, which is the slow
	// half when there is an embedder.
	OnProgress func(done, total int)
}

// MergeReport says what a merge did, and what it declined to do.
type MergeReport struct {
	Sessions int // sessions in the other index
	Added    int // not here before
	Extended int // longer there, so replaced here
	Same     int // the same or shorter there: nothing to take
	Titles   int // sessions that gained a title from the other index
	Messages int // message rows copied
	Chunks   int // chunks written while indexing what was merged

	// Unarchived counts sessions the other index has no messages for -- it
	// was built before the archive existed -- and so has nothing to give.
	Unarchived int

	// Diverged lists sessions whose shared messages differ between the two
	// indexes. Sessions are append-only, so this means the assumption broke
	// somewhere, and neither copy is taken: picking one would be silently
	// resolving something nobody has looked at.
	Diverged []Divergence

	// Conflicts lists sessions whose path is claimed here by a different
	// session. sessions.path is unique, and neither row can be dropped without
	// losing its archive.
	Conflicts []string
}

// Divergence is one session whose two copies disagree.
type Divergence struct {
	ID   string
	Rows int // messages at the same position with different bytes
}

// Merge folds another index's archive into this one, the way atuin merges
// shell history.
//
// The union of both, keyed by session id. pi and Claude Code both mint UUIDs,
// so two machines never collide on one; the only overlap is the same session
// seen twice, and then the longer copy wins. Sessions are append-only, so the
// longer is a superset of the shorter and taking it loses nothing. Merging
// message by message would be cleverer and wrong: two divergent branches of
// one session are not a conversation that ever happened.
//
// Only sessions and messages cross over. Chunks and vectors are derived, and
// chunk ids are AUTOINCREMENT with chunks_vec keyed by them, so merging them
// would mean remapping every id and every vector. Instead each merged session
// is indexed here from its archived messages, which keeps derived data
// machine-local and lets a machine with no embedding model merge at all.
//
// Titles cross over with their session row, so a title paid for on one
// machine is not paid for again on the other: whenever the other copy wins,
// and also when it loses but this one has no title yet.
func Merge(ctx context.Context, d *DB, otherPath string, opts MergeOptions) (MergeReport, error) {
	var rep MergeReport

	if opts.Embedder != nil {
		if err := d.checkEmbedder(opts.Embedder, false); err != nil {
			return rep, err
		}
	}

	abs, err := filepath.Abs(otherPath)
	if err != nil {
		return rep, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return rep, err
	}
	if mine, err := os.Stat(d.path); err == nil && os.SameFile(fi, mine) {
		return rep, errors.New("cannot merge an index into itself")
	}

	// Attached read-only, through a URI so that mode=ro applies: this reads
	// someone else's archive and has no business being able to write it. The
	// URL type escapes whatever a path holds that a URI cannot.
	uri := (&url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}).String()
	if _, err := d.sql.ExecContext(ctx, `ATTACH DATABASE ? AS other`, uri); err != nil {
		return rep, fmt.Errorf("open %s: %w", otherPath, err)
	}
	attached := true
	detach := func() error {
		if !attached {
			return nil
		}
		attached = false
		_, err := d.sql.Exec(`DETACH DATABASE other`)
		return err
	}
	defer func() { _ = detach() }()

	if err := checkMergeable(ctx, d, otherPath); err != nil {
		return rep, err
	}

	plan, err := planMerge(ctx, d, &rep)
	if err != nil {
		return rep, err
	}
	if err := applyMerge(ctx, d, plan, &rep); err != nil {
		return rep, err
	}
	// Before indexing: DETACH cannot run while a statement is open, and
	// indexing is where a statement would be.
	if err := detach(); err != nil {
		return rep, err
	}

	// Indexed one session per transaction, like a build. A failure here leaves
	// the archive merged and some sessions unindexed, which a full build
	// repairs: it reindexes every session with no file from the archive.
	bopts := BuildOptions{ChunkChars: opts.ChunkChars, Embedder: opts.Embedder}
	for i, m := range plan {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		n, err := d.reindexMerged(ctx, m, bopts)
		if err != nil {
			return rep, fmt.Errorf("index merged session %s: %w", m.id, err)
		}
		rep.Chunks += n
		if opts.OnProgress != nil {
			opts.OnProgress(i+1, len(plan))
		}
	}

	if opts.Embedder != nil && len(plan) > 0 {
		if err := d.SetMeta(MetaEmbedModel, opts.Embedder.Name()); err != nil {
			return rep, err
		}
		if err := d.SetMeta(MetaEmbedDim, strconv.Itoa(opts.Embedder.Dim())); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// checkMergeable refuses an attached index this build cannot read the same
// way it reads its own.
func checkMergeable(ctx context.Context, d *DB, otherPath string) error {
	var tables int
	if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM other.sqlite_master
		WHERE type = 'table' AND name IN ('sessions', 'messages', 'meta')`).Scan(&tables); err != nil {
		return fmt.Errorf("%s is not a spireweb index: %w", otherPath, err)
	}
	if tables != 3 {
		return fmt.Errorf("%s is not a spireweb index with a message archive", otherPath)
	}
	var version string
	err := d.sql.QueryRowContext(ctx,
		`SELECT value FROM other.meta WHERE key = ?`, MetaSchemaVersion).Scan(&version)
	if err != nil {
		return fmt.Errorf("%s has no schema version: %w", otherPath, err)
	}
	if version != strconv.Itoa(SchemaVersion) {
		return fmt.Errorf("%s has schema version %s, this build expects %d",
			otherPath, version, SchemaVersion)
	}
	return nil
}

// Merge actions.
const (
	mergeAdd    = "add"
	mergeExtend = "extend"
	mergeTitle  = "title"
)

type mergeStep struct {
	id     string
	path   string // this index's, when it has the session: see reindexMerged
	action string
	ours   int // messages archived here before the merge

	// gainsTitle is whether this index has no title for the session and the
	// other one does, which every action takes.
	gainsTitle bool
}

// planMerge decides what happens to each of the other index's sessions,
// reading both but writing neither.
func planMerge(ctx context.Context, d *DB, rep *MergeReport) ([]mergeStep, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT o.id, COALESCE(s.path, o.path),
		       (SELECT COUNT(*) FROM other.messages m WHERE m.session_id = o.id),
		       s.id IS NOT NULL,
		       (SELECT COUNT(*) FROM main.messages m WHERE m.session_id = o.id),
		       COALESCE(s.title, '') = '' AND COALESCE(o.title, '') <> '',
		       (SELECT COUNT(*) FROM main.sessions p WHERE p.path = o.path AND p.id <> o.id)
		FROM other.sessions o
		LEFT JOIN main.sessions s ON s.id = o.id
		ORDER BY o.started_at, o.id`)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		mergeStep
		theirs   int
		present  bool
		conflict bool
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.path, &c.theirs, &c.present, &c.ours,
			&c.gainsTitle, &c.conflict); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var plan []mergeStep
	for _, c := range cands {
		rep.Sessions++
		switch {
		case c.theirs == 0:
			rep.Unarchived++
			continue
		case c.conflict:
			rep.Conflicts = append(rep.Conflicts, c.id)
			continue
		case !c.present:
			c.action = mergeAdd
			plan = append(plan, c.mergeStep)
			continue
		}

		// Compared in full rather than at one position. This is the check
		// that exists to catch the append-only assumption failing, so it
		// cannot lean on that assumption to take a shortcut.
		var differ int
		if err := d.sql.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM other.messages o
			JOIN main.messages m ON m.session_id = o.session_id AND m.idx = o.idx
			WHERE o.session_id = ? AND o.content <> m.content`, c.id).Scan(&differ); err != nil {
			return nil, err
		}
		switch {
		case differ > 0:
			rep.Diverged = append(rep.Diverged, Divergence{ID: c.id, Rows: differ})
		case c.theirs > c.ours:
			c.action = mergeExtend
			plan = append(plan, c.mergeStep)
		case c.gainsTitle:
			rep.Same++
			c.action = mergeTitle
			plan = append(plan, c.mergeStep)
		default:
			rep.Same++
		}
	}
	return plan, nil
}

// applyMerge copies the planned rows in one transaction, so a merge that
// fails partway leaves this index as it was.
func applyMerge(ctx context.Context, d *DB, plan []mergeStep, rep *MergeReport) error {
	if len(plan) == 0 {
		return nil
	}

	// title_key and title_msgs are added by migrate(), so an index last
	// opened by an older build may not have them. Those rows merge with the
	// columns empty, which is what migrate() would have given them.
	col := func(name string) (string, error) {
		var n int
		err := d.sql.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('sessions', 'other') WHERE name = ?`, name).Scan(&n)
		if err != nil || n == 0 {
			return "NULL", err
		}
		return "o." + name, nil
	}
	titleKey, err := col("title_key")
	if err != nil {
		return err
	}
	titleMsgs, err := col("title_msgs")
	if err != nil {
		return err
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The whole row, host and path included. They are machine-local, which is
	// exactly why they are worth keeping: "which laptop was this on" is a
	// question worth being able to answer.
	//
	// A title travels with its bookkeeping or not at all, so that title_key
	// always describes the title beside it. The other copy's title wins when
	// it has one; when it has none, this one's is kept rather than lost.
	upsertRow, err := tx.PrepareContext(ctx, `
		INSERT INTO main.sessions(id, path, host, cwd, project, started_at, mtime, size,
		                          n_msgs, preview, reply, title, title_key, title_msgs)
		SELECT o.id, o.path, o.host, o.cwd, o.project, o.started_at, o.mtime, o.size,
		       o.n_msgs, o.preview, o.reply, o.title, `+titleKey+`, `+titleMsgs+`
		FROM other.sessions o WHERE o.id = ?
		ON CONFLICT(id) DO UPDATE SET
			path = excluded.path, host = excluded.host, cwd = excluded.cwd,
			project = excluded.project, started_at = excluded.started_at,
			mtime = excluded.mtime, size = excluded.size, n_msgs = excluded.n_msgs,
			preview = excluded.preview, reply = excluded.reply,
			title = CASE WHEN excluded.title IS NULL THEN title ELSE excluded.title END,
			title_key = CASE WHEN excluded.title IS NULL THEN title_key ELSE excluded.title_key END,
			title_msgs = CASE WHEN excluded.title IS NULL THEN title_msgs ELSE excluded.title_msgs END`)
	if err != nil {
		return err
	}
	defer upsertRow.Close()

	// OR IGNORE for a session whose rows here have a gap, where a count is not
	// the same thing as the next free position.
	copyMsgs, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO main.messages(session_id, idx, role, at, content)
		SELECT session_id, idx, role, at, content FROM other.messages
		WHERE session_id = ? AND idx >= ?`)
	if err != nil {
		return err
	}
	defer copyMsgs.Close()

	takeTitle, err := tx.PrepareContext(ctx, `
		UPDATE main.sessions SET title = o.title, title_key = `+titleKey+`, title_msgs = `+titleMsgs+`
		FROM other.sessions o WHERE o.id = main.sessions.id AND main.sessions.id = ?`)
	if err != nil {
		return err
	}
	defer takeTitle.Close()

	for _, m := range plan {
		if m.action == mergeTitle {
			if _, err := takeTitle.ExecContext(ctx, m.id); err != nil {
				return fmt.Errorf("%s: %w", m.id, err)
			}
			rep.Titles++
			continue
		}

		if _, err := upsertRow.ExecContext(ctx, m.id); err != nil {
			return fmt.Errorf("%s: %w", m.id, err)
		}
		res, err := copyMsgs.ExecContext(ctx, m.id, m.ours)
		if err != nil {
			return fmt.Errorf("%s: %w", m.id, err)
		}
		n, _ := res.RowsAffected()
		rep.Messages += int(n)

		if m.gainsTitle {
			rep.Titles++
		}
		if m.action == mergeAdd {
			rep.Added++
		} else {
			rep.Extended++
		}
	}
	return tx.Commit()
}

// reindexMerged derives chunks for a session the merge changed.
//
// From the archive for a session the merge added or extended: that is where
// the merged messages are, and the whole of what the session now is. A session
// that only gained a title is read the usual way, file first, because its
// messages did not change and the file may be newer than the archive.
func (d *DB) reindexMerged(ctx context.Context, m mergeStep, opts BuildOptions) (int, error) {
	if m.action != mergeTitle {
		return d.reindexArchived(ctx, m.id, opts)
	}
	if opts.ChunkChars <= 0 {
		opts.ChunkChars = session.MaxChunkChars
	}
	s, err := d.LoadSession(ctx, m.id, m.path)
	if err != nil {
		return 0, err
	}
	n, _, err := d.upsertSession(ctx, s, opts)
	return n, err
}
