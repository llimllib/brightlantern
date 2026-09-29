package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// machine is an index built over its own corpus, standing in for another
// laptop. Its host is rewritten afterwards, since Hostname is the same for
// every index a test process builds.
type machine struct {
	db   *DB
	path string
	dir  string
}

func newMachine(t *testing.T, host string, specs map[string][]string) *machine {
	t.Helper()
	m := &machine{dir: writeCorpus(t, specs), path: filepath.Join(t.TempDir(), "i.db")}
	db, err := Open(m.path, DriverName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m.db = db
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{m.dir}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE sessions SET host = ?`, host); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machine) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := m.db.SQL().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (m *machine) merge(t *testing.T, from *machine) MergeReport {
	t.Helper()
	rep, err := Merge(context.Background(), m.db, from.path, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func (m *machine) matches(t *testing.T, term string) int {
	t.Helper()
	var n int
	if err := m.db.SQL().QueryRow(`SELECT COUNT(*) FROM chunks c
		JOIN chunks_fts f ON f.rowid = c.id WHERE chunks_fts MATCH ?`, term).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMergeAddsSessionsFromAnotherMachine(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("here")}})
	b := newMachine(t, "laptop-b", map[string][]string{
		"s2": {userMsg("the quicksand incident"), assistantMsg("never again")},
	})
	b.exec(t, `UPDATE sessions SET title = 'Escaping the bog', title_key = 'k', title_msgs = 2`)

	rep := a.merge(t, b)
	if rep.Sessions != 1 || rep.Added != 1 || rep.Messages != 2 || rep.Titles != 1 {
		t.Errorf("report = %+v", rep)
	}

	sum, err := a.db.Session(context.Background(), "s2")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Host != "laptop-b" || sum.Title != "Escaping the bog" || sum.NumMsgs != 2 {
		t.Errorf("merged row = %+v", sum)
	}
	// Searchable, not just readable, including by its title.
	if n := a.matches(t, "quicksand"); n != 1 {
		t.Errorf("quicksand matches = %d, want 1", n)
	}
	if n := a.matches(t, "bog"); n != 1 {
		t.Errorf("title matches = %d, want 1", n)
	}
	var key string
	var msgs int
	if err := a.db.SQL().QueryRow(
		`SELECT title_key, title_msgs FROM sessions WHERE id = 's2'`).Scan(&key, &msgs); err != nil {
		t.Fatal(err)
	}
	if key != "k" || msgs != 2 {
		t.Errorf("title bookkeeping = %q, %d: the title would be paid for again", key, msgs)
	}

	// The next build here has no file for s2, and must keep it.
	if _, err := Build(context.Background(), a.db, BuildOptions{Dirs: []string{a.dir}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Session(context.Background(), "s2"); err != nil {
		t.Errorf("the next build dropped the merged session: %v", err)
	}
	if n := a.matches(t, "quicksand"); n != 1 {
		t.Errorf("after a build, quicksand matches = %d, want 1", n)
	}
}

// Sessions are append-only, so the longer copy is a superset of the shorter
// and wins. The shorter one is not an error, just nothing to take.
func TestMergeTakesTheLongerCopy(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("first")}})
	b := newMachine(t, "laptop-b", map[string][]string{
		"s1": {userMsg("first"), assistantMsg("second quicksand")},
	})

	// Shorter here, longer there.
	rep := a.merge(t, b)
	if rep.Extended != 1 || rep.Messages != 1 {
		t.Errorf("report = %+v, want one session extended by one message", rep)
	}
	if n := archivedCount(t, a.db, "s1"); n != 2 {
		t.Errorf("archived = %d, want 2", n)
	}
	if n := a.matches(t, "quicksand"); n != 1 {
		t.Errorf("the merged message is not searchable: %d matches", n)
	}

	// And the other way round: b is longer, so nothing moves.
	rep = b.merge(t, a)
	if rep.Same != 1 || rep.Messages != 0 || rep.Extended != 0 {
		t.Errorf("reverse report = %+v, want the session left alone", rep)
	}
}

// The extended copy has to survive the next build here, which parses the
// shorter local file. That file is a prefix of the archive, and keeps it.
func TestMergedMessagesSurviveTheShorterLocalFile(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("first")}})
	b := newMachine(t, "laptop-b", map[string][]string{
		"s1": {userMsg("first"), assistantMsg("second")},
	})
	a.merge(t, b)
	if _, err := Build(context.Background(), a.db, BuildOptions{Dirs: []string{a.dir}, Full: true}); err != nil {
		t.Fatal(err)
	}
	if n := archivedCount(t, a.db, "s1"); n != 2 {
		t.Errorf("archived = %d after reindexing the local file, want 2", n)
	}
}

func TestMergeReportsDivergenceAndTakesNeither(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("one thing")}})
	b := newMachine(t, "laptop-b", map[string][]string{
		"s1": {userMsg("another thing"), assistantMsg("and more")},
	})

	rep := a.merge(t, b)
	if len(rep.Diverged) != 1 || rep.Diverged[0].ID != "s1" || rep.Diverged[0].Rows != 1 {
		t.Errorf("diverged = %+v, want s1 with one differing row", rep.Diverged)
	}
	if rep.Extended != 0 || rep.Messages != 0 {
		t.Errorf("report = %+v: a divergent session was merged", rep)
	}
	if n := a.matches(t, "another"); n != 0 {
		t.Errorf("the other copy's text arrived: %d matches", n)
	}
}

// A title paid for on one machine is not paid for again on the other, even
// when the other machine's copy of the session is the one that loses.
func TestMergeFillsAMissingTitleFromTheShorterCopy(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{
		"s1": {userMsg("first"), assistantMsg("second")},
	})
	b := newMachine(t, "laptop-b", map[string][]string{"s1": {userMsg("first")}})
	b.exec(t, `UPDATE sessions SET title = 'A title', title_key = 'k', title_msgs = 1`)

	rep := a.merge(t, b)
	if rep.Titles != 1 || rep.Extended != 0 {
		t.Errorf("report = %+v, want a title and nothing else", rep)
	}
	sum, err := a.db.Session(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Title != "A title" || sum.Host == "laptop-b" || sum.NumMsgs != 2 {
		t.Errorf("row = %+v, want this machine's row with the other's title", sum)
	}
}

// Merging twice is merging once.
func TestMergeIsIdempotent(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("here")}})
	b := newMachine(t, "laptop-b", map[string][]string{"s2": {userMsg("there")}})
	a.merge(t, b)
	before := chunkIDs(t, a.db, "s2")

	rep := a.merge(t, b)
	if rep.Same != 1 || rep.Added+rep.Extended+rep.Messages+rep.Chunks != 0 {
		t.Errorf("second merge = %+v, want nothing to do", rep)
	}
	if after := chunkIDs(t, a.db, "s2"); len(after) != len(before) || after[0] != before[0] {
		t.Errorf("chunks %v became %v", before, after)
	}
}

func TestMergeLeavesTheOtherIndexAlone(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("here")}})
	b := newMachine(t, "laptop-b", map[string][]string{"s2": {userMsg("there")}})
	a.merge(t, b)

	var n int
	if err := b.db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the other index has %d sessions after the merge, want its own 1", n)
	}
}

// sessions.path is unique. A session there claiming a path a different
// session holds here cannot be merged without dropping one of them.
func TestMergeReportsPathConflicts(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("here")}})
	b := newMachine(t, "laptop-b", map[string][]string{"s2": {userMsg("there")}})
	var path string
	if err := a.db.SQL().QueryRow(`SELECT path FROM sessions WHERE id = 's1'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	b.exec(t, `UPDATE sessions SET path = ?`, path)

	rep := a.merge(t, b)
	if len(rep.Conflicts) != 1 || rep.Conflicts[0] != "s2" || rep.Added != 0 {
		t.Errorf("report = %+v, want s2 reported as a conflict", rep)
	}
}

func TestMergeRefusesWhatItCannotRead(t *testing.T) {
	a := newMachine(t, "laptop-a", map[string][]string{"s1": {userMsg("here")}})
	ctx := context.Background()

	if _, err := Merge(ctx, a.db, a.path, MergeOptions{}); err == nil ||
		!strings.Contains(err.Error(), "itself") {
		t.Errorf("self-merge: err = %v", err)
	}
	if _, err := Merge(ctx, a.db, filepath.Join(t.TempDir(), "absent.db"), MergeOptions{}); err == nil {
		t.Error("a missing file merged")
	}

	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Merge(ctx, a.db, junk, MergeOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not a spireweb index") {
		t.Errorf("empty file: err = %v", err)
	}

	b := newMachine(t, "laptop-b", map[string][]string{"s2": {userMsg("there")}})
	b.exec(t, `UPDATE meta SET value = '99' WHERE key = ?`, MetaSchemaVersion)
	if _, err := Merge(ctx, a.db, b.path, MergeOptions{}); err == nil ||
		!strings.Contains(err.Error(), "schema version 99") {
		t.Errorf("other schema version: err = %v", err)
	}

	// And a refused merge detaches, so the next one can attach again.
	b.exec(t, `UPDATE meta SET value = ? WHERE key = ?`, "1", MetaSchemaVersion)
	if _, err := Merge(ctx, a.db, b.path, MergeOptions{}); err != nil {
		t.Errorf("a merge after a refused one: %v", err)
	}
}
