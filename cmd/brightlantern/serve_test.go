package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/indexer"
)

// serve used to refuse to start without an index, so the check that matters is
// not that a file appears but that the read pool can open what was made: it is
// _query_only, and a connection that cannot create a schema is the whole reason
// the guard existed.
func TestBootstrapIndexMakesOneAReaderCanOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "fresh.db")

	if err := bootstrapIndex(path); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenReader(path, index.DriverName)
	if err != nil {
		t.Fatalf("OpenReader after bootstrap: %v", err)
	}
	defer db.Close()

	if n, err := db.CountSessions(t.Context()); err != nil || n != 0 {
		t.Errorf("CountSessions() = %d, %v; want 0 and no error", n, err)
	}
}

// An existing index must be left alone: bootstrapping is for the empty case,
// and anything it did to a real one would be done to somebody's corpus.
func TestBootstrapIndexLeavesAnExistingIndexAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")

	db, err := index.Open(path, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta("bootstrap-test", "kept"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapIndex(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("bootstrapIndex wrote to an index that already existed")
	}

	db, err = index.OpenReader(path, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, err := db.Meta("bootstrap-test"); err != nil || v != "kept" {
		t.Errorf("Meta() = %q, %v; want the value written before", v, err)
	}
}

// A bare `brightlantern` serves, and a leading flag does not become a subcommand --
// otherwise `brightlantern --addr :9000` would complain instead of doing the obvious
// thing.
func TestCommandDefaultsToServe(t *testing.T) {
	tests := []struct {
		argv     []string
		wantCmd  string
		wantArgs []string
	}{
		{nil, "serve", nil},
		{[]string{"--addr", ":9000"}, "serve", []string{"--addr", ":9000"}},
		{[]string{"-open"}, "serve", []string{"-open"}},
		{[]string{"index", "--full"}, "index", []string{"--full"}},
		{[]string{"help"}, "help", []string{}},
	}
	for _, tc := range tests {
		cmd, args := command(tc.argv)
		if cmd != tc.wantCmd || !slices.Equal(args, tc.wantArgs) {
			t.Errorf("command(%q) = %q, %q; want %q, %q",
				tc.argv, cmd, args, tc.wantCmd, tc.wantArgs)
		}
	}
}

// Before the indexer exists the page must see "starting", not nothing: an
// empty /status removes the header's poller, and the page would then never
// notice the indexer arriving.
func TestPendingStatus(t *testing.T) {
	var p pendingStatus
	if got := p.Status().Phase; got != indexer.PhaseStarting {
		t.Errorf("before set: phase = %q, want %q", got, indexer.PhaseStarting)
	}

	p.set(fixedStatus{Phase: indexer.PhaseWatching, Sessions: 7})
	if got := p.Status(); got.Phase != indexer.PhaseWatching || got.Sessions != 7 {
		t.Errorf("after set: status = %+v, want the indexer's own", got)
	}

	var failed pendingStatus
	failed.set(nil) // startIndexer could not open a writer
	if got := failed.Status(); got.Phase != indexer.PhaseStopped || got.LastErr == "" {
		t.Errorf("after a failed start: status = %+v, want stopped with a reason", got)
	}
}

type fixedStatus indexer.Status

func (f fixedStatus) Status() indexer.Status { return indexer.Status(f) }

// Something warm opens after serve has begun shutting down is closed on the
// spot rather than kept, and warm is told to stop.
func TestClosersAfterCloseCloseImmediately(t *testing.T) {
	var c closers
	var order []string
	c.add(func() { order = append(order, "a") })
	c.add(func() { order = append(order, "b") })
	c.close()
	if !slices.Equal(order, []string{"b", "a"}) {
		t.Errorf("closed in order %v, want reverse of opening", order)
	}

	late := false
	if c.add(func() { late = true }) {
		t.Error("add after close reported success")
	}
	if !late {
		t.Error("add after close did not close what it was given")
	}
}
