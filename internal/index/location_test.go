package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got, want := DefaultPath(), "/xdg/data/brightlantern/index.db"; got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/me")
	if got, want := DefaultPath(), "/home/me/.local/share/brightlantern/index.db"; got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

// legacyIndex puts a real index, archive and all, where v0.0.2 kept one.
func legacyIndex(t *testing.T) (legacy, dst string) {
	t.Helper()
	legacy = filepath.Join(t.TempDir(), "Caches", "brightlantern", "index.db")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "v0.0.1.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return legacy, filepath.Join(t.TempDir(), "share", "brightlantern", "index.db")
}

// Upgrading must find the old index rather than rebuild one, and must get it
// out of a directory the system is allowed to empty.
func TestRelocateMovesTheOldIndex(t *testing.T) {
	legacy, dst := legacyIndex(t)

	r := Relocate(dst, legacy)
	if r.Path != dst || r.Moved != legacy || r.Stayed != "" {
		t.Fatalf("Relocate = %+v, want it moved from %s to %s", r, legacy, dst)
	}
	if exists(legacy) {
		t.Error("the old index is still there")
	}
	db, err := Open(dst, DriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := count(t, db.SQL(), `SELECT COUNT(*) FROM messages`); got != 48 {
		t.Errorf("moved index has %d messages, want 48", got)
	}
}

// A brightlantern still running from before the upgrade has it open. Moving it
// under that process would split it in two.
func TestRelocateLeavesAnIndexInUse(t *testing.T) {
	legacy, dst := legacyIndex(t)
	running, err := OpenReader(legacy, DriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	count(t, running.SQL(), `SELECT COUNT(*) FROM sessions`) // idle, but open

	r := Relocate(dst, legacy)
	if r.Path != legacy || r.Moved != "" || r.Stayed == "" {
		t.Fatalf("Relocate = %+v, want the index used where it is, with a reason", r)
	}
	if exists(dst) {
		t.Error("something was created at the new path")
	}

	// Once that process is gone, the next run moves it.
	running.Close()
	if r := Relocate(dst, legacy); r.Moved != legacy {
		t.Errorf("after the other process closed: %+v; want it moved", r)
	}
}

// An index at the new path is the index. The old one is not touched, let
// alone merged or moved over it.
func TestRelocatePrefersTheNewPath(t *testing.T) {
	legacy, dst := legacyIndex(t)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := Relocate(dst, legacy); r.Path != dst || r.Moved != "" {
		t.Errorf("Relocate = %+v; want the new path untouched", r)
	}
	if !exists(legacy) {
		t.Error("the old index was removed")
	}
}

func TestRelocateWithNothingToMove(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "index.db")
	for _, legacy := range []string{"", filepath.Join(t.TempDir(), "absent.db"), dst} {
		if r := Relocate(dst, legacy); r != (Relocation{Path: dst}) {
			t.Errorf("legacy %q: Relocate = %+v", legacy, r)
		}
	}
}
