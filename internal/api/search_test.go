package api

import "testing"

func TestMarkExcerpt(t *testing.T) {
	const o, c = markOpen, markClose
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "pytest-" + o + "xdist" + c + " runs", "pytest-**xdist** runs"},
		{"two terms", o + "pytest" + c + " " + o + "xdist" + c, "**pytest** **xdist**"},
		// The case from the real index: the agent had bolded the term itself.
		{"already bold", "where **" + o + "xdist" + c + "** entered", "where **xdist** entered"},
		{"inside a bold run", "**the " + o + "xdist" + c + " runner** broke", "**the xdist runner** broke"},
		{"after a bold run", "**note:** " + o + "xdist" + c, "**note:** **xdist**"},
		// The snippet began inside a bold run, so parity is wrong; adjacency
		// still keeps it from doubling.
		{"cut into a bold run", "…" + o + "xdist" + c + "** runner", "…xdist** runner"},
		{"cut at excerptRunes", "the " + o + "xdi…", "the **xdi…**"},
		{"in a code span", "`pytest-" + o + "xdist" + c + "` is available", "`pytest-xdist` is available"},
		{"after a code span", "`-n 4` with " + o + "xdist" + c, "`-n 4` with **xdist**"},
		{"bold-looking code", "`a**b` then " + o + "xdist" + c, "`a**b` then **xdist**"},
		{"no marks", "a semantic hit", "a semantic hit"},
	} {
		if got := markExcerpt(tc.in); got != tc.want {
			t.Errorf("%s: markExcerpt(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
