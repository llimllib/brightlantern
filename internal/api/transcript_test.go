package api

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/session"
)

func text(role, s string) session.Message {
	return session.Message{Role: role, Content: []session.Block{{Type: session.BlockText, Text: s}}}
}

func call(id, name, args string) session.Message {
	return session.Message{Role: session.RoleAssistant, Content: []session.Block{
		{Type: session.BlockToolCall, ID: id, Name: name, Arguments: json.RawMessage(args)}}}
}

func result(id, out string) session.Message {
	return session.Message{Role: session.RoleToolResult, ToolCallID: id,
		Content: []session.Block{{Type: session.BlockText, Text: out}}}
}

func TestResolveRange(t *testing.T) {
	for _, tc := range []struct{ from, to, n, wantFrom, wantTo int }{
		{0, 0, 10, 0, 10},   // everything
		{3, 6, 10, 3, 6},    // a window
		{-3, 0, 10, 7, 10},  // how it ended
		{0, -2, 10, 0, 8},   // all but the last two
		{5, 50, 10, 5, 10},  // past the end
		{-50, 0, 10, 0, 10}, // before the start
		{8, 4, 10, 8, 8},    // backwards is empty, not an error
		{20, 0, 10, 10, 10}, // starting past the end is empty
		{0, 0, 0, 0, 0},     // an empty session
		{183 - 5, 199, 416, 178, 199},
	} {
		from, to := resolveRange(tc.from, tc.to, tc.n)
		if from != tc.wantFrom || to != tc.wantTo {
			t.Errorf("resolveRange(%d, %d, %d) = %d, %d; want %d, %d",
				tc.from, tc.to, tc.n, from, to, tc.wantFrom, tc.wantTo)
		}
	}
}

// Message indexes are the ones search reports and the page anchors use, so a
// range must keep them: tool results take indexes of their own, and skipping
// them must not renumber what follows.
func TestTranscriptKeepsMessageIndexes(t *testing.T) {
	s := &session.Session{Messages: []session.Message{
		text(session.RoleUser, "list the files"),
		call("c1", "bash", `{"command":"ls"}`),
		result("c1", "a\nb"),
		text(session.RoleAssistant, "two files"),
	}}
	tr := BuildTranscript(index.Summary{ID: "x"}, s, ShowRequest{})
	var got []int
	for _, e := range tr.Entries {
		got = append(got, e.Msg)
	}
	if want := []int{0, 1, 3}; !equal(got, want) {
		t.Errorf("entry indexes = %v, want %v", got, want)
	}
	if tool := tr.Entries[1].Tool; tool == nil || tool.Summary != "ls" || tool.Output != "a\nb" {
		t.Errorf("tool = %+v, want ls with its output", tool)
	}

	// A range starting after the call still has no orphan result, and one
	// that ends before the result still pairs the call with it.
	tr = BuildTranscript(index.Summary{ID: "x"}, s, ShowRequest{From: 1, To: 2})
	if len(tr.Entries) != 1 || tr.Entries[0].Tool.Output != "a\nb" {
		t.Errorf("range 1-2 = %+v, want the call with the result that lies outside it", tr.Entries)
	}
}

func TestToolModes(t *testing.T) {
	long := strings.Repeat("line\n", 100)
	s := &session.Session{Messages: []session.Message{
		call("c1", "bash", `{"command":"seq 100"}`),
		result("c1", long),
		call("c2", "bash", `{"command":"sleep 100"}`), // interrupted: no result
	}}

	brief := BuildTranscript(index.Summary{}, s, ShowRequest{}).Entries[0].Tool
	if !brief.Truncated || strings.Count(brief.Output, "\n") != briefLines-1 {
		t.Errorf("brief = %d lines, truncated %v; want %d lines, truncated",
			strings.Count(brief.Output, "\n")+1, brief.Truncated, briefLines)
	}
	if brief.OutputLines != 100 || brief.Args != "" {
		t.Errorf("brief OutputLines = %d, Args = %q; want 100 and none", brief.OutputLines, brief.Args)
	}

	full := BuildTranscript(index.Summary{}, s, ShowRequest{Tools: ToolsFull}).Entries[0].Tool
	if full.Truncated || full.Output != strings.TrimRight(long, "\n") {
		t.Errorf("full output truncated = %v, len %d", full.Truncated, len(full.Output))
	}
	if !strings.Contains(full.Args, `"seq 100"`) {
		t.Errorf("full Args = %q, want the arguments", full.Args)
	}

	none := BuildTranscript(index.Summary{}, s, ShowRequest{Tools: ToolsNone}).Entries[0].Tool
	if none.Output != "" || none.Summary != "seq 100" {
		t.Errorf("none = %+v, want the call with no output", none)
	}

	missing := BuildTranscript(index.Summary{}, s, ShowRequest{}).Entries[1].Tool
	if !missing.Missing {
		t.Errorf("interrupted call = %+v, want Missing", missing)
	}
}

// A brief cut on one long line lands on a byte limit, which must not split a
// rune: the output is JSON, and invalid UTF-8 there becomes U+FFFD.
func TestBriefCutIsRuneSafe(t *testing.T) {
	s := strings.Repeat("é", briefBytes) // two bytes each
	cut := briefCut("x" + s)
	if !utf8.ValidString(("x" + s)[:cut]) {
		t.Errorf("briefCut split a rune at %d", cut)
	}
}

func TestEditShowsDiffInsteadOfOutput(t *testing.T) {
	res := result("c1", "Successfully replaced text")
	res.Details = json.RawMessage(`{"diff":"  1 keep\n- 2 old\n+ 2 new"}`)
	s := &session.Session{Messages: []session.Message{
		call("c1", "edit", `{"path":"a.go","oldText":"old","newText":"new"}`), res}}

	tool := BuildTranscript(index.Summary{}, s, ShowRequest{}).Entries[0].Tool
	if tool.Output != "" || !strings.Contains(tool.Diff, "old") || !strings.Contains(tool.Diff, "new") {
		t.Errorf("tool = %+v, want the diff and no output", tool)
	}
}

// Session text is markdown and tool output routinely holds fences of its own;
// neither may end the fence around it.
func TestMarkdownFencesOutlastTheirContent(t *testing.T) {
	s := &session.Session{Messages: []session.Message{
		text(session.RoleUser, "## A heading of the user's"),
		call("c1", "bash", "{\"command\":\"cat `README`\"}"),
		result("c1", "```go\nfunc main() {}\n```"),
	}}
	var b strings.Builder
	if err := BuildTranscript(index.Summary{ID: "x", Title: "t"}, s, ShowRequest{}).WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}
	md := b.String()
	if !strings.Contains(md, "````\n```go\nfunc main() {}\n```\n````") {
		t.Errorf("output fence does not outlast the output's own:\n%s", md)
	}
	if !strings.Contains(md, "`` cat `README` ``") {
		t.Errorf("summary span does not outlast its backticks:\n%s", md)
	}
	if !strings.Contains(md, "**[0] user**") || !strings.Contains(md, "**[1] assistant**") {
		t.Errorf("messages are not labelled with their indexes:\n%s", md)
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
