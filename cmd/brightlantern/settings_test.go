package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/llimllib/brightlantern/internal/config"
	"github.com/llimllib/brightlantern/internal/index"
)

// sandbox redirects both the settings file and the probed session directories
// at temporary ones, so the machine running the test cannot decide anything.
func sandbox(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	// Data and cache too, or the index default and the legacy location it is
	// moved from would be the real ones.
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func installSessions(t *testing.T, dir string) string {
	t.Helper()
	sub := filepath.Join(dir, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeConfig(t *testing.T, c config.Config) {
	t.Helper()
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Run("flag beats the settings file", func(t *testing.T) {
		home := sandbox(t)
		flagged := installSessions(t, filepath.Join(home, "from-flag"))
		writeConfig(t, config.Config{Dirs: []string{"/from/config"}})

		s, err := resolve(map[string]bool{}, dirList{flagged}, "db", "addr", "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(s.dirs, []string{flagged}) {
			t.Errorf("dirs = %v, want %v", s.dirs, []string{flagged})
		}
	})

	t.Run("the settings file beats detection", func(t *testing.T) {
		home := sandbox(t)
		installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
		writeConfig(t, config.Config{Dirs: []string{"/from/config"}})

		s, err := resolve(map[string]bool{}, nil, "db", "addr", "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(s.dirs, []string{"/from/config"}) {
			t.Errorf("dirs = %v, want the configured one", s.dirs)
		}
	})

	t.Run("detection when nothing is configured", func(t *testing.T) {
		home := sandbox(t)
		detected := installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))

		s, err := resolve(map[string]bool{}, nil, "db", "addr", "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(s.dirs, []string{detected}) {
			t.Errorf("dirs = %v, want %v", s.dirs, []string{detected})
		}
	})

	t.Run("nothing anywhere is an error naming the candidates", func(t *testing.T) {
		sandbox(t)
		if _, err := resolve(map[string]bool{}, nil, "db", "addr", ""); err == nil {
			t.Error("resolve() = nil error with no sessions anywhere")
		}
	})
}

// A flag given its default value still has to beat the settings file, which is
// why resolve is told which flags were seen rather than comparing values.
func TestResolveHonoursAFlagSetToItsDefault(t *testing.T) {
	home := sandbox(t)
	installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
	writeConfig(t, config.Config{Dirs: []string{"/x"}, Addr: "0.0.0.0:9999", Index: "/from/config.db"})

	s, err := resolve(map[string]bool{"addr": true, "db": true}, nil, "flag.db", "127.0.0.1:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.addr != "127.0.0.1:8080" {
		t.Errorf("addr = %q, want the flag's value", s.addr)
	}
	if s.dbPath != "flag.db" {
		t.Errorf("dbPath = %q, want the flag's value", s.dbPath)
	}
}

// Unset, the same values come from the file.
func TestResolveTakesAddrAndIndexFromTheFile(t *testing.T) {
	home := sandbox(t)
	installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
	writeConfig(t, config.Config{Dirs: []string{"/x"}, Addr: "0.0.0.0:9999", Index: "/from/config.db"})

	s, err := resolve(map[string]bool{}, nil, "flag.db", "127.0.0.1:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.addr != "0.0.0.0:9999" || s.dbPath != "/from/config.db" {
		t.Errorf("addr = %q, dbPath = %q; want both from the file", s.addr, s.dbPath)
	}
}

func TestResolveTitles(t *testing.T) {
	tests := []struct {
		name  string
		given map[string]bool
		via   string
		cfg   string
		want  string
	}{
		{"--no-titles wins", map[string]bool{"no-titles": true}, "claude", config.TitlesClaude, config.TitlesOff},
		{"--titles-via beats the file", map[string]bool{"titles-via": true}, "claude", config.TitlesAPI, config.TitlesClaude},
		{"the file is used", map[string]bool{}, "api", config.TitlesClaude, config.TitlesClaude},
		{"off is honoured", map[string]bool{}, "api", config.TitlesOff, config.TitlesOff},
		{"api by default", map[string]bool{}, "api", "", config.TitlesAPI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := sandbox(t)
			installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
			writeConfig(t, config.Config{Dirs: []string{"/x"}, Titles: tc.cfg})

			s, err := resolve(tc.given, nil, "db", "addr", tc.via)
			if err != nil {
				t.Fatal(err)
			}
			if s.titles != tc.want {
				t.Errorf("titles = %q, want %q", s.titles, tc.want)
			}
		})
	}
}

// The first run records what it worked out, so that installing another agent
// later does not silently change what is indexed.
func TestResolveWritesTheFileOnFirstRun(t *testing.T) {
	home := sandbox(t)
	detected := installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))

	if _, err := resolve(map[string]bool{}, nil, "db", "addr", ""); err != nil {
		t.Fatal(err)
	}

	cfg, had, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !had {
		t.Fatal("no settings file was written")
	}
	if !slices.Equal(cfg.Dirs, []string{detected}) {
		t.Errorf("dirs = %v, want %v", cfg.Dirs, []string{detected})
	}
	// Not "off": a first run must not quietly stop titling for someone who has
	// been getting titles all along.
	if cfg.Titles != config.TitlesAPI {
		t.Errorf("titles = %q, want %q", cfg.Titles, config.TitlesAPI)
	}
}

// Only a run that had to work it out writes the file. A --dir run is answering
// a different question and must not overwrite what is there.
func TestResolveDoesNotWriteTheFileWhenDirWasGiven(t *testing.T) {
	home := sandbox(t)
	flagged := installSessions(t, filepath.Join(home, "elsewhere"))

	if _, err := resolve(map[string]bool{}, dirList{flagged}, "db", "addr", ""); err != nil {
		t.Fatal(err)
	}
	if _, had, _ := config.Load(); had {
		t.Error("a --dir run wrote a settings file")
	}
}

// A warning that is almost always wrong is worse than none, so the slow note
// must stay silent when the thing it describes did not happen.
func TestSlowNoteStaysQuietWhenFast(t *testing.T) {
	stderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	stop := slowNote(time.Hour, "should never appear")
	stop()
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("slowNote printed %q before its deadline", out)
	}
}

func TestSlowNoteSpeaksWhenSlow(t *testing.T) {
	stderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	stop := slowNote(time.Millisecond, "the thing is slow")
	time.Sleep(50 * time.Millisecond)
	stop()
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "the thing is slow") {
		t.Errorf("slowNote printed %q, want the message", out)
	}
}

// placeLegacyIndex puts a file where v0.0.2 kept the index.
func placeLegacyIndex(t *testing.T) string {
	t.Helper()
	legacy := index.LegacyPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := index.Open(legacy, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// The default index moves out of the cache directory on the first run after
// upgrading, rather than being rebuilt beside it.
func TestResolveMovesTheDefaultIndex(t *testing.T) {
	home := sandbox(t)
	writeConfig(t, config.Config{Dirs: []string{installSessions(t, filepath.Join(home, "s"))}})
	legacy := placeLegacyIndex(t)

	s, err := resolve(map[string]bool{}, nil, index.DefaultPath(), "127.0.0.1:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.dbPath != index.DefaultPath() {
		t.Errorf("dbPath = %q, want %q", s.dbPath, index.DefaultPath())
	}
	if _, err := os.Stat(s.dbPath); err != nil {
		t.Errorf("nothing at the new path: %v", err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Error("the index is still in the cache directory")
	}
	if got := configuredDB(map[string]bool{}, index.DefaultPath()); got != index.DefaultPath() {
		t.Errorf("configuredDB = %q after the move, want the new path", got)
	}
}

// A location someone wrote down is where they want it.
func TestResolveLeavesAConfiguredIndexAlone(t *testing.T) {
	home := sandbox(t)
	legacy := placeLegacyIndex(t)
	writeConfig(t, config.Config{Dirs: []string{installSessions(t, filepath.Join(home, "s"))}, Index: legacy})

	s, err := resolve(map[string]bool{}, nil, index.DefaultPath(), "127.0.0.1:8080", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.dbPath != legacy {
		t.Errorf("dbPath = %q, want the configured %q", s.dbPath, legacy)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("the configured index moved: %v", err)
	}
}
