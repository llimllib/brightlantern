package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func arrivedIDs(t *testing.T, db *DB, since int64, limit int) ([]string, int64) {
	t.Helper()
	got, last, err := db.ArrivedSince(context.Background(), since, limit)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for i, s := range got {
		ids[i] = s.ID
	}
	return ids, last
}

// An open page carries the last seq it saw and asks for anything later. A
// session that grows must not arrive again -- sessions are reindexed on every
// message -- and a new one must, whatever its started_at.
func TestArrivalsRecordFirstInsertOnly(t *testing.T) {
	ctx := context.Background()
	dir := writeCorpus(t, map[string][]string{"s1": {userMsg("one")}})
	db := openTest(t)
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}

	ids, first := arrivedIDs(t, db, 0, 10)
	if len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("first build arrived %v", ids)
	}
	if last, err := db.LastArrival(ctx); err != nil || last != first {
		t.Errorf("LastArrival = %d, %v; want %d", last, err, first)
	}

	// s1 grows, which is an upsert that resolves to DO UPDATE.
	p := filepath.Join(dir, "--Users-me-code-proj--", "s1.jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(userMsg("two") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}, Full: true}); err != nil {
		t.Fatal(err)
	}
	if ids, last := arrivedIDs(t, db, first, 10); len(ids) != 0 || last != first {
		t.Errorf("a grown session arrived again: %v, last %d", ids, last)
	}

	// A new session arrives, with the same started_at as s1 -- writeCorpus
	// gives every session the same header timestamp, which is exactly the
	// case a started_at watermark could not tell apart.
	if err := os.WriteFile(filepath.Join(dir, "--Users-me-code-proj--", "s2.jsonl"),
		[]byte(`{"type":"session","version":3,"id":"s2","timestamp":"2026-03-31T12:26:01.076Z","cwd":"/Users/me/code/proj"}`+
			"\n"+userMsg("three")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	ids, last := arrivedIDs(t, db, first, 10)
	if len(ids) != 1 || ids[0] != "s2" || last <= first {
		t.Errorf("after s1 (seq %d): arrived %v, last %d; want [s2] after it", first, ids, last)
	}
}

// A poll asks for at most a page's worth, and resumes from the last seq it was
// given, so a cold build arriving faster than the poll loses nothing.
func TestArrivedSinceResumesAfterALimit(t *testing.T) {
	ctx := context.Background()
	dir := writeCorpus(t, map[string][]string{
		"a": {userMsg("a")}, "b": {userMsg("b")}, "c": {userMsg("c")},
	})
	db := openTest(t)
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}

	first, last := arrivedIDs(t, db, 0, 2)
	rest, end := arrivedIDs(t, db, last, 2)
	if len(first) != 2 || len(rest) != 1 {
		t.Fatalf("pages = %v then %v", first, rest)
	}
	seen := map[string]bool{}
	for _, id := range append(first, rest...) {
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Errorf("two pages returned %v and %v, want all three once", first, rest)
	}
	if again, _ := arrivedIDs(t, db, end, 2); len(again) != 0 {
		t.Errorf("after the end: %v", again)
	}
}

// Deleting a session takes its arrival with it, and AUTOINCREMENT never hands
// the seq out again: a page holding the deleted session's seq as its
// watermark would otherwise miss the next arrival, given the same number.
func TestArrivalSeqIsNeverReused(t *testing.T) {
	ctx := context.Background()
	dir := writeCorpus(t, map[string][]string{"s1": {userMsg("one")}})
	db := openTest(t)
	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	_, seq := arrivedIDs(t, db, 0, 10)

	if _, err := db.SQL().Exec(`DELETE FROM sessions WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db.SQL(), `SELECT COUNT(*) FROM arrivals`); n != 0 {
		t.Errorf("%d arrivals outlived their session", n)
	}

	if _, err := Build(ctx, db, BuildOptions{Dirs: []string{dir}, Full: true}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := arrivedIDs(t, db, seq, 10); len(ids) != 1 {
		t.Errorf("reinserted session after seq %d: arrived %v", seq, ids)
	}
}

// An index from before arrivals existed gains the table on open, with nothing
// in it: none of its sessions is new. What merge brings in afterwards is, and
// merge never mentions the table -- the trigger is what counts it.
func TestArrivalsOnAnOlderIndex(t *testing.T) {
	ctx := context.Background()
	db, err := Open(copyFixture(t, "v0.0.1.db"), DriverName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if last, err := db.LastArrival(ctx); err != nil || last != 0 {
		t.Errorf("LastArrival = %d, %v; want 0 for sessions that predate it", last, err)
	}

	other := newMachine(t, "laptop-b", map[string][]string{"merged": {userMsg("from elsewhere")}})
	if _, err := Merge(ctx, db, other.path, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := arrivedIDs(t, db, 0, 10); len(ids) != 1 || ids[0] != "merged" {
		t.Errorf("after merge, arrived %v; want [merged]", ids)
	}
}
