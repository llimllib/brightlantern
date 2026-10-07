package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/llimllib/brightlantern/internal/config"
)

// The app's first run (#74): it passes CLAUDE_CONFIG_DIR from the login shell,
// because launchd's environment has lost it, and a directory found only
// through it has to end up in the file.
func TestInitWritesWhatItDetects(t *testing.T) {
	home := sandbox(t)
	elsewhere := installSessions(t, filepath.Join(home, "somewhere", "claude", "projects"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(elsewhere))

	if err := runInit(); err != nil {
		t.Fatal(err)
	}
	cfg, had, err := config.Load()
	if err != nil || !had {
		t.Fatalf("Load() = had %v, err %v; want a file", had, err)
	}
	if !slices.Equal(cfg.Dirs, []string{elsewhere}) {
		t.Errorf("dirs = %v, want %v", cfg.Dirs, []string{elsewhere})
	}
	if cfg.Titles != config.TitlesApple {
		t.Errorf("titles = %q, want %q", cfg.Titles, config.TitlesApple)
	}
}

// Nothing found is an error and writes nothing: the app shows the message and
// registers no agent, and an empty file would stop detection running again
// once there is something to find.
func TestInitWithNoSessionsFailsAndWritesNothing(t *testing.T) {
	sandbox(t)
	if err := runInit(); err == nil {
		t.Fatal("runInit() = nil, want an error")
	}
	if _, had, _ := config.Load(); had {
		t.Error("a settings file was written")
	}
}

func TestInitLeavesAnExistingFileAlone(t *testing.T) {
	home := sandbox(t)
	installSessions(t, filepath.Join(home, ".pi", "agent", "sessions"))
	writeConfig(t, config.Config{Titles: config.TitlesOff})
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	if err := runInit(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(config.Path())
	if string(after) != string(before) {
		t.Errorf("settings file changed:\n%s\nwant:\n%s", after, before)
	}
}

// logTo replaces this process's stdout and stderr, so it runs in a child: the
// test binary again, told by the environment to do only that and write to
// both, the second straight to the descriptor as a panic would.
func TestLogToCapturesBothDescriptors(t *testing.T) {
	if path := os.Getenv("BRIGHTLANTERN_TEST_LOGTO"); path != "" {
		if err := logTo(path); err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("to stdout\n")
		_, _ = os.NewFile(2, "fd2").WriteString("to fd 2\n")
		os.Exit(0)
	}

	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogToCapturesBothDescriptors$")
	cmd.Env = append(os.Environ(), "HOME="+home, "BRIGHTLANTERN_TEST_LOGTO=~/Logs/x/bl.log")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("child wrote %q to the terminal, want nothing", out)
	}

	got, err := os.ReadFile(filepath.Join(home, "Logs", "x", "bl.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"to stdout", "to fd 2"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("log = %q, want it to contain %q", got, want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/h")
	for in, want := range map[string]string{
		"~/a/b": "/h/a/b",
		"~":     "/h",
		"/abs":  "/abs",
		"rel":   "rel",
		"~bob":  "~bob",
	} {
		if got, err := expandHome(in); err != nil || got != want {
			t.Errorf("expandHome(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
