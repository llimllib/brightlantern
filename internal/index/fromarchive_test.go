package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// archiveOnly indexes one session and then removes its file and chunks,
// leaving what a merge leaves: a session row and its messages, with nothing
// derived from them yet.
func archiveOnly(t *testing.T, db *DB, host string) string {
	t.Helper()
	dir := writeCorpus(t, map[string][]string{
		"s1": {userMsg("the quicksand incident"), assistantMsg("never again")},
	})
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "--Users-me-code-proj--", "s1.jsonl")); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteChunksTx(tx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET host = ?`, host); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFullBuildIndexesArchivedSessions(t *testing.T) {
	db := openTest(t)
	dir := archiveOnly(t, db, "other-laptop")

	// An incremental pass has no reason to look at a session with no file.
	p, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if p.FromArchive != 0 {
		t.Errorf("incremental FromArchive = %d, want 0", p.FromArchive)
	}

	p, err = Build(context.Background(), db, BuildOptions{Dirs: []string{dir}, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.FromArchive != 1 || p.Chunks != 2 {
		t.Errorf("FromArchive=%d Chunks=%d, want 1 and 2", p.FromArchive, p.Chunks)
	}

	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'quicksand'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("FTS rows for quicksand = %d, want 1: the archived session is not searchable", n)
	}

	sum, err := db.Session(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Host != "other-laptop" {
		t.Errorf("host = %q: reindexing from the archive claimed the session for this machine", sum.Host)
	}
	if sum.NumMsgs != 2 || sum.Preview != "the quicksand incident" {
		t.Errorf("row = %+v", sum)
	}
	if got := archivedCount(t, db, "s1"); got != 2 {
		t.Errorf("archived = %d, want 2: reindexing must not touch the archive", got)
	}

	// And a second full pass reuses every chunk it wrote.
	before := chunkIDs(t, db, "s1")
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}, Full: true}); err != nil {
		t.Fatal(err)
	}
	after := chunkIDs(t, db, "s1")
	if len(before) != len(after) {
		t.Fatalf("chunks %v became %v", before, after)
	}
	for _, id := range before {
		if !contains(after, id) {
			t.Errorf("chunk %d was recreated rather than reused", id)
		}
	}
}

// A title written for an archived session reaches search the same way one for
// a file does: needsTitleChunks promotes the run, and the full pass has to
// include sessions with no file for that to mean anything.
func TestArchivedSessionsGainTheirTitleChunk(t *testing.T) {
	db := openTest(t)
	dir := archiveOnly(t, db, Hostname())
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}, Full: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTitle(context.Background(), "s1", "Escaping the bog", "k", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM chunks WHERE session_id = 's1' AND role = ?`, RoleTitle).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("title chunks = %d, want 1", n)
	}
}
