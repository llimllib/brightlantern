package search

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/llimllib/spireweb/internal/index"
)

// SessionResult is a search hit rolled up to the session that contains it.
//
// It embeds index.Summary so a result renders through exactly the same row
// template as a browsed session. The alternative -- a parallel struct with
// the same fields -- drifts the moment either side gains a column.
type SessionResult struct {
	index.Summary

	Score     float64
	NumChunks int            // matching chunks in this session
	BestChunk ChunkID        // highest-scoring chunk
	BestText  string         // its body, before excerpting
	Ranks     map[string]int // per-ranker position of BestChunk
}

// BreadthDecay is how much each further matching chunk counts relative to
// the one before it, in a session's score. See sessionScore.
const BreadthDecay = 0.5

// sessionScore rolls a session's chunk scores, best first, up into one: the
// best chunk, plus the second at half weight, the third at a quarter, and so
// on.
//
// Best-alone was the original rule, on the grounds that a session discussing
// something once, well, should beat one mentioning it three times in passing.
// On the real corpus it put the sessions with fifteen substantial passages
// about a subject below sessions with one: breadth is evidence of aboutness,
// and best-alone counted it for nothing.
//
// The decay keeps what was right about best-alone. Each chunk counts by its
// own score, so a session with many weak matches gains little, and the total
// can never exceed twice the best chunk however many there are. RRF scores are
// flat -- rank 60 is worth half of rank 1 -- so in practice a session whose
// best chunk is around 60th and that has many more behind it can pass a
// single chunk at rank 1. That is deliberate: dozens of matches in the top
// hundred are not passing mentions. A bonus on
// the *count* of chunks was measured and rejected for exactly that: with the
// words of a query OR-ed together, a session saying "limit" eight times
// climbed from 32nd to 8th on "rate limit". 0.7 did the same; 0.3 barely
// moved anything. The comparison is in #42.
func sessionScore(scores []float64) float64 {
	total, w := 0.0, 1.0
	for _, s := range scores {
		total += w * s
		w *= BreadthDecay
	}
	return total
}

// SearchSessions runs a query and returns results grouped by session, scored
// by sessionScore. Chunk count is reported separately and breaks ties.
func (e *Engine) SearchSessions(ctx context.Context, db *sql.DB, q Query, limit int) ([]SessionResult, error) {
	if db == nil {
		return nil, nil
	}
	hits, err := e.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	// A quoted phrase is a constraint, and the lexical ranker is not the only
	// source of candidates: the semantic ranker has no notion of a phrase and
	// will happily return neighbours of the query that contain none of its
	// words. Without this filter, quoting would visibly fail to do the one
	// thing it promises.
	qualified, err := sessionsMatching(ctx, db, strictQuery(q.Text))
	if err != nil {
		return nil, err
	}

	byChunk := make(map[ChunkID]Scored, len(hits))
	args := make([]any, 0, len(hits))
	for _, h := range hits {
		byChunk[h.ID] = h
		args = append(args, int64(h.ID))
	}

	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.body, `+index.SummaryColumns("s")+`
		FROM chunks c JOIN sessions s ON s.id = c.session_id
		WHERE c.id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySession := map[string]*SessionResult{}
	scores := map[string][]float64{}
	for rows.Next() {
		var chunkID ChunkID
		var body string
		// Scanning in two steps because the summary columns are produced by a
		// helper: read the two leading values, then hand the rest over.
		summary, err := scanWithPrefix(rows, &chunkID, &body)
		if err != nil {
			return nil, err
		}

		if qualified != nil && !qualified[summary.ID] {
			continue
		}

		cur, ok := bySession[summary.ID]
		if !ok {
			cur = &SessionResult{Summary: summary}
			bySession[summary.ID] = cur
		}
		cur.NumChunks++
		sc := byChunk[chunkID]
		scores[summary.ID] = append(scores[summary.ID], sc.Score)
		// Score is the best chunk's until the loop ends, then the session's.
		if sc.Score > cur.Score {
			cur.Score = sc.Score
			cur.BestChunk = chunkID
			cur.BestText = body
			cur.Ranks = sc.Ranks
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]SessionResult, 0, len(bySession))
	for id, r := range bySession {
		s := scores[id]
		sort.Sort(sort.Reverse(sort.Float64Slice(s)))
		r.Score = sessionScore(s)
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].NumChunks != out[j].NumChunks {
			return out[i].NumChunks > out[j].NumChunks
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sessionsMatching returns the sessions satisfying a strict FTS5 expression,
// or nil when there is no constraint to apply.
//
// A session qualifies when any one of its chunks matches, because the session
// is what the list shows. The alternative -- requiring one chunk to hold every
// phrase -- would make a two-phrase query depend on how an 800-character
// chunking happened to fall.
func sessionsMatching(ctx context.Context, db *sql.DB, strict string) (map[string]bool, error) {
	if strict == "" {
		return nil, nil // nil means "no constraint", which is not the same as none matching
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT c.session_id
		FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
		WHERE chunks_fts MATCH ?`, strict)
	if err != nil {
		return nil, fmt.Errorf("phrase filter: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// prefixScanner adapts a row so index.ScanSummary can read the trailing
// columns after some leading ones have been claimed.
type prefixScanner struct {
	row    interface{ Scan(...any) error }
	prefix []any
}

func (p prefixScanner) Scan(dest ...any) error {
	return p.row.Scan(append(append([]any{}, p.prefix...), dest...)...)
}

func scanWithPrefix(row interface{ Scan(...any) error }, prefix ...any) (index.Summary, error) {
	return index.ScanSummary(prefixScanner{row: row, prefix: prefix})
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
