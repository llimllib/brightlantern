package titles

import (
	"strings"
	"testing"
)

// The prompt asks for a short bare phrase; this is what happens when the model
// does not listen.
func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"Fixing the indexer"`, "Fixing the indexer"},
		{"Fixing the indexer.", "Fixing the indexer"},
		{"  Fixing   the\n indexer  ", "Fixing the indexer"},
		{"“Fixing the indexer”", "Fixing the indexer"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	long := Clean(strings.Repeat("words ", 100))
	if n := len([]rune(long)); n > MaxTitleRunes {
		t.Errorf("Clean() returned %d runes, want <= %d: a list row is one line",
			n, MaxTitleRunes)
	}
}
