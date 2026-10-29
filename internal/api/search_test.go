package api

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestMarkExcerpt(t *testing.T) {
	const o, c = markOpen, markClose
	for _, tc := range []struct {
		name, in, want string
		// matched is what each span covers.
		matched []string
	}{
		{"plain", "pytest-" + o + "xdist" + c + " runs", "pytest-**xdist** runs", []string{"xdist"}},
		{"two terms", o + "pytest" + c + " " + o + "xdist" + c, "**pytest** **xdist**", []string{"pytest", "xdist"}},
		// The case from the real index: the agent had bolded the term itself.
		{"already bold", "where **" + o + "xdist" + c + "** entered", "where **xdist** entered", []string{"xdist"}},
		{"inside a bold run", "**the " + o + "xdist" + c + " runner** broke", "**the xdist runner** broke", []string{"xdist"}},
		{"after a bold run", "**note:** " + o + "xdist" + c, "**note:** **xdist**", []string{"xdist"}},
		// The snippet began inside a bold run, so parity is wrong; adjacency
		// still keeps it from doubling.
		{"cut into a bold run", "…" + o + "xdist" + c + "** runner", "…xdist** runner", []string{"xdist"}},
		{"cut at excerptRunes", "the " + o + "xdi…", "the **xdi…**", []string{"xdi…"}},
		{"in a code span", "`pytest-" + o + "xdist" + c + "` is available", "`pytest-xdist` is available", []string{"xdist"}},
		{"after a code span", "`-n 4` with " + o + "xdist" + c, "`-n 4` with **xdist**", []string{"xdist"}},
		{"bold-looking code", "`a**b` then " + o + "xdist" + c, "`a**b` then **xdist**", []string{"xdist"}},
		{"no marks", "a semantic hit", "a semantic hit", nil},
	} {
		got, spans := markExcerpt(tc.in)
		if got != tc.want {
			t.Errorf("%s: markExcerpt(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
			continue
		}
		var matched []string
		for _, sp := range spans {
			matched = append(matched, got[sp[0]:sp[1]])
		}
		if !reflect.DeepEqual(matched, tc.matched) {
			t.Errorf("%s: spans cover %q, want %q", tc.name, matched, tc.matched)
		}
	}
}

// plainText renders lines with matches in [brackets], to compare against.
func plainText(lines []styledLine) string {
	var out []string
	for _, l := range lines {
		out = append(out, l.render(func(s string) string { return "[" + s + "]" }))
	}
	return strings.Join(out, "\n")
}

func TestWrapMatches(t *testing.T) {
	mark := func(in string) (string, []Span) {
		return markExcerpt(strings.NewReplacer("<", markOpen, ">", markClose).Replace(in))
	}
	for _, tc := range []struct {
		name, in string
		width    int
		want     string
	}{
		{"bold taken off", "the <xdist> plugin", 0, "the [xdist] plugin"},
		{"code keeps its backticks", "`pytest-<xdist>` here", 0, "`pytest-[xdist]` here"},
		{"bold run kept", "**the <xdist> runner**", 0, "**the [xdist] runner**"},
		{"wraps at words", "one two <three> four five", 9, "one two\n[three]\nfour five"},
		{"long word overflows", "a <supercalifragilistic> b", 5, "a\n[supercalifragilistic]\nb"},
	} {
		ex, spans := mark(tc.in)
		if got := plainText(wrapMatches(ex, spans, tc.width)); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	// Spans from a server that is not this build are not trusted.
	if got := plainText(wrapMatches("short", []Span{{3, 99}, {2, 1}}, 0)); got != "short" {
		t.Errorf("bad spans: got %q", got)
	}
}

func TestWriteColorOnlyColours(t *testing.T) {
	ex, spans := markExcerpt("the " + markOpen + "xdist" + markClose + " plugin")
	r := SearchResponse{Semantic: true, Results: []SearchResult{{
		ID: "abc", Title: "Title", Project: "proj", Messages: 3, Msg: 1,
		Excerpt: ex, Matches: spans,
	}}}
	var plain, color strings.Builder
	if err := r.WriteText(&plain); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteColor(&color, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(color.String(), "\x1b[1;31mxdist\x1b[0m") {
		t.Errorf("WriteColor did not colour the match:\n%q", color.String())
	}
	stripped := regexpSGR.ReplaceAllString(color.String(), "")
	if want := strings.Replace(plain.String(), "**xdist**", "xdist", 1); stripped != want {
		t.Errorf("colour changed more than colour:\n got %q\nwant %q", stripped, want)
	}
}

var regexpSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")
