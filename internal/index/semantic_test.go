package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/llimllib/spireweb/internal/embed"
)

// An Embedder has to work against the reader pool, because that is where
// search queries get embedded. The pool sets query_only, which refuses writes
// to every attached database including temp -- so anything that registers the
// model lazily fails there, and semantic search would be available to the
// indexer and to nothing else.
func TestEmbedderWorksOnTheReaderPool(t *testing.T) {
	p := semanticPaths(t)

	path := filepath.Join(t.TempDir(), "i.db")
	db, err := Open(path, SemanticDriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	r, err := OpenReader(path, SemanticDriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	e, err := embed.New(r.SQL(), p)
	if err != nil {
		t.Fatalf("embedder over a read-only pool: %v", err)
	}
	blob, err := e.EmbedText(context.Background(), "how do I make this faster")
	if err != nil {
		t.Fatalf("embedding a query: %v", err)
	}
	if len(blob) != embed.Dim*4 {
		t.Fatalf("got %d bytes, want %d", len(blob), embed.Dim*4)
	}
}

// Vectors that are present but meaningless would pass every structural check
// in this package. This asserts the embeddings actually carry meaning: a query
// that shares no words with the target has to rank it above an unrelated
// session, which is the entire reason for having them.
func TestNearestNeighbourFindsParaphrase(t *testing.T) {
	semanticPaths(t)

	dir := writeCorpus(t, map[string][]string{
		"perf":  {userMsg("the dashboard takes twelve seconds to load and users complain")},
		"pets":  {userMsg("my dog will not stop barking at the mail carrier")},
		"pasta": {userMsg("the best ratio of egg yolk to cheese for carbonara")},
	})

	path := filepath.Join(t.TempDir(), "i.db")
	db, err := Open(path, SemanticDriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	e, err := embed.New(db.SQL(), embed.DefaultPaths())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureVectorTable(e.Dim()); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}, Embedder: e}); err != nil {
		t.Fatal(err)
	}

	// Shares no content word with the target chunk.
	q, err := e.EmbedText(context.Background(), "make the page render quicker")
	if err != nil {
		t.Fatal(err)
	}

	var got string
	err = db.SQL().QueryRow(`
		SELECT c.session_id FROM chunks_vec v
		JOIN chunks c ON c.id = v.rowid
		WHERE v.embedding MATCH ? AND k = 1`, q).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got != "perf" {
		t.Errorf("nearest neighbour = %q, want %q", got, "perf")
	}
}

// A session with no file gains vectors when embedding becomes available, the
// same as one with a file: needsVectors promotes the run to a full pass, and
// the full pass reaches archived sessions too. This is also what makes a merge
// on a machine with no model complete later, on one with.
func TestVectorBackfillReachesArchivedSessions(t *testing.T) {
	semanticPaths(t)

	path := filepath.Join(t.TempDir(), "i.db")
	db, err := Open(path, SemanticDriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e, err := embed.New(db.SQL(), embed.DefaultPaths())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureVectorTable(e.Dim()); err != nil {
		t.Fatal(err)
	}

	// Indexed lexically, then its file removed: chunks with no vectors, and no
	// file for a backfill to parse.
	dir := writeCorpus(t, map[string][]string{"s1": {userMsg("the quicksand incident")}})
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "--Users-me-code-proj--", "s1.jsonl")); err != nil {
		t.Fatal(err)
	}

	p, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}, Embedder: e})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Backfill || p.FromArchive != 1 {
		t.Errorf("Backfill=%v FromArchive=%d, want a backfill that reached the archive", p.Backfill, p.FromArchive)
	}
	if missing, err := db.needsVectors(); err != nil || missing {
		t.Errorf("needsVectors = %v, %v after the backfill", missing, err)
	}
}
