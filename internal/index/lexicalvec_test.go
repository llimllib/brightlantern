package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lexical driver is what serve and index fall back to when the model will
// not load, and it writes to indexes that have vectors. A write that deletes a
// chunk deletes its vector too, so vec0 has to be there even with no model --
// otherwise the run aborts with "no such module: vec0" and stays wedged on that
// file until a semantic run clears it (#82).
//
// Needs no model and no GPU, so unlike the semantic tests it runs in CI.
func TestLexicalWriteDeletesVectorsOfReplacedChunks(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "--Users-me-code-proj--")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sub, "s.jsonl")
	write := func(text string) {
		lines := []string{strings.Replace(strings.Replace(hdr, "%s", "vec", 1), "%s", "proj", 1), userMsg(text)}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	db := openTest(t)
	ctx := context.Background()
	if err := db.EnsureVectorTable(2); err != nil {
		t.Fatalf("lexical driver cannot create chunks_vec: %v", err)
	}

	write("the original wording")
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	// Stand in for the embedder: one vector per chunk.
	if _, err := db.SQL().Exec(
		`INSERT INTO chunks_vec(rowid, embedding) SELECT id, '[0.5, 0.5]' FROM chunks`); err != nil {
		t.Fatal(err)
	}

	write("a rewritten message")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatalf("lexical build that replaces a chunk: %v", err)
	}

	var orphans int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM chunks_vec v
		WHERE NOT EXISTS (SELECT 1 FROM chunks c WHERE c.id = v.rowid)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d vectors left behind by the replaced chunk", orphans)
	}
}
