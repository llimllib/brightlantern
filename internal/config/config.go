// Package config reads and writes brightlantern's settings file.
//
// The file exists so that brightlantern can be run with no arguments and keep doing
// the same thing tomorrow. Detection (session.Detect) answers "what is on this
// machine" every time it is asked, and the answer can change: install pi to try
// it once and the corpus silently doubles; move ~/.claude and the index empties
// with no explanation. Writing the answer down on first run turns that into
// something a person can read and edit.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Titles backends, as written in the file. Off is spelled out rather than
// left as an absent value, because "I decided not to" and "I have not been
// asked yet" are different states and the titles pass acts on the difference.
const (
	TitlesOff    = "off"
	TitlesApple  = "apple"
	TitlesAPI    = "api"
	TitlesClaude = "claude"
)

// DefaultAddr is where the web interface listens when nothing says otherwise.
//
// 5268 is LANT on a phone keypad. Unassigned by IANA, clear of the ports dev
// servers crowd onto (3000, 5000, 5173, 8000-8080), and below macOS's ephemeral
// range, which starts at 49152: a port in that range is occasionally handed to
// some other process's outbound connection, and a daemon that cannot bind
// would fail to start for no visible reason.
const DefaultAddr = "127.0.0.1:5268"

// Config is the settings file. Every field is optional; an absent one means
// "work it out", which is what a first run does for all of them.
type Config struct {
	Dirs   []string `toml:"dirs"`
	Titles string   `toml:"titles"`
	Addr   string   `toml:"addr"`
	Index  string   `toml:"index"`
}

// Path is where the settings file lives.
//
// XDG rather than ~/Library/Application Support, even on macOS, because that
// is already this project's convention: mise.toml sets BRIGHTLANTERN_DATA_DIR to
// ~/.local/share/brightlantern and embed.DefaultPaths falls back to the same. Using
// os.UserConfigDir would put the two halves of one install on different
// schemes for no gain.
func Path() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "brightlantern", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "brightlantern", "config.toml")
	}
	return filepath.Join(home, ".config", "brightlantern", "config.toml")
}

// Load reads the settings file.
//
// A missing file is not an error: it is the state of every machine before the
// first run, and the caller's job is then to work everything out and Save it.
// The second return reports whether a file was actually there, which is how
// the caller tells "nothing configured" from "configured to nothing".
func Load() (Config, bool, error) {
	return LoadFrom(Path())
}

// LoadFrom reads a settings file from a given path.
func LoadFrom(path string) (Config, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if _, err := toml.Decode(string(b), &c); err != nil {
		// Named, because the alternative is silently ignoring what someone
		// wrote and behaving as though the file were not there.
		return Config{}, true, fmt.Errorf("%s: %w", path, err)
	}
	return c, true, nil
}

// Save writes the settings file, creating its directory.
func (c Config) Save() error { return c.SaveTo(Path()) }

// SaveTo writes the settings file to a given path.
//
// Written by hand rather than through toml.Encode so that it can carry
// comments. This is a file whose whole purpose is to be opened and read by
// someone wondering what brightlantern decided, and an uncommented list of paths
// answers "what" without ever answering "why".
func (c Config) SaveTo(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	var b strings.Builder
	b.WriteString("# brightlantern settings. Delete a line to have brightlantern work it out again.\n\n")

	b.WriteString("# Session directories, found on this machine when brightlantern first ran.\n")
	b.WriteString("# Written down rather than detected every time, so that installing\n")
	b.WriteString("# another agent does not silently change what is indexed.\n")
	b.WriteString("dirs = [\n")
	for _, d := range c.Dirs {
		fmt.Fprintf(&b, "  %s,\n", quote(d))
	}
	b.WriteString("]\n\n")

	b.WriteString("# How session titles are generated: apple, claude, api, or off.\n")
	b.WriteString("#   apple   Apple's on-device model; no key, no bill, nothing leaves\n")
	b.WriteString("#           the machine. Needs Apple Intelligence turned on\n")
	b.WriteString("#   claude  shells out to the Claude Code CLI, billing whatever\n")
	b.WriteString("#           subscription it is signed in to\n")
	b.WriteString("#   api     needs ANTHROPIC_API_KEY\n")
	b.WriteString("#   off     lists each session under its opening message\n")
	fmt.Fprintf(&b, "titles = %s\n\n", quote(c.Titles))

	if c.Addr != "" {
		b.WriteString("# Address the web interface listens on, and where everything else --\n")
		b.WriteString("# a second brightlantern, the app -- looks for it.\n")
		fmt.Fprintf(&b, "addr = %s\n\n", quote(c.Addr))
	}
	if c.Index != "" {
		b.WriteString("# Where the search index is kept.\n")
		fmt.Fprintf(&b, "index = %s\n", quote(c.Index))
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// quote renders a TOML basic string.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
