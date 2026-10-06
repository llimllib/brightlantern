package search

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// rrf is one ranker's contribution at a 1-based rank, under DefaultFusion.
func rrf(rank int) float64 { return 1 / (60 + float64(rank)) }

func TestSessionScoreCountsBreadth(t *testing.T) {
	// The case from #42: fifteen good passages lose to one slightly better
	// one if only the best chunk counts.
	broad := []float64{rrf(12)}
	for r := 13; r < 27; r++ {
		broad = append(broad, rrf(r))
	}
	once := []float64{rrf(1)}
	if sessionScore(broad) <= sessionScore(once) {
		t.Errorf("fifteen chunks from rank 12 = %f, one chunk at rank 1 = %f; want breadth to win",
			sessionScore(broad), sessionScore(once))
	}
}

func TestSessionScoreDoesNotRewardWeakMatchesForTheirNumber(t *testing.T) {
	// Twenty passing mentions, all near the bottom of a ranker's 100. Not
	// much further up is where breadth starts to win: RRF is flat enough that
	// rank 60 scores half of rank 1, and the decayed tail is worth up to the
	// best chunk again.
	weak := []float64{}
	for r := 80; r < 100; r++ {
		weak = append(weak, rrf(r))
	}
	strong := []float64{rrf(1)}
	if sessionScore(weak) >= sessionScore(strong) {
		t.Errorf("twenty weak chunks = %f, one at rank 1 = %f; want the strong one to win",
			sessionScore(weak), sessionScore(strong))
	}
	if got, limit := sessionScore(weak), 2*weak[0]; got >= limit {
		t.Errorf("score %f reached twice the best chunk, %f", got, limit)
	}
}

func TestSessionScoreOfOneChunkIsThatChunk(t *testing.T) {
	if got := sessionScore([]float64{rrf(3)}); got != rrf(3) {
		t.Errorf("got %f, want %f", got, rrf(3))
	}
}

// Through SearchSessions, so that the roll-up is the one doing the ranking:
// "broad" is never the best chunk but holds most of the others.
func TestSearchSessionsRanksBreadthAboveOneBestChunk(t *testing.T) {
	db := indexFixture(t, map[string][]string{
		"once":  {strings.Repeat("x", 700)},
		"broad": {strings.Repeat("a", 700), strings.Repeat("b", 700), strings.Repeat("c", 700)},
	})
	order := &scripted{db: db, order: []string{"once", "broad", "broad", "broad"}}
	engine := &Engine{Rankers: []Ranker{order}, Fusion: DefaultFusion()}

	res, err := engine.SearchSessions(context.Background(), db, Query{Text: "anything"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range res {
		got = append(got, r.ID)
	}
	if strings.Join(got, ",") != "broad,once" {
		t.Errorf("results = %v, want broad first", got)
	}
	if res[0].NumChunks != 3 {
		t.Errorf("broad matched %d chunks, want 3", res[0].NumChunks)
	}
}

// scripted ranks chunks in a fixed order of sessions: each entry takes that
// session's next unused chunk.
type scripted struct {
	db    *sql.DB
	order []string
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Rank(ctx context.Context, _ Query, _ int) ([]ChunkID, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id FROM chunks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySession := map[string][]ChunkID{}
	for rows.Next() {
		var id ChunkID
		var sid string
		if err := rows.Scan(&id, &sid); err != nil {
			return nil, err
		}
		bySession[sid] = append(bySession[sid], id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ChunkID
	for _, sid := range s.order {
		out = append(out, bySession[sid][0])
		bySession[sid] = bySession[sid][1:]
	}
	return out, nil
}
