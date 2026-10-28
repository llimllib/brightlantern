package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/render"
	"github.com/llimllib/brightlantern/internal/session"
)

// SessionsPath prefixes a session's route: SessionsPath + id.
const SessionsPath = "/api/sessions/"

// How much tool traffic a transcript carries.
//
// Brief is the default because tool output is nearly all of a session's
// bytes -- prose is 0.3-2.6% of a large one -- so a transcript with every
// result in full is mostly file dumps and would fill any context it landed
// in. The first few lines are usually enough to say whether a command
// worked; full is for the message where it matters.
const (
	ToolsBrief = "brief"
	ToolsNone  = "none"
	ToolsFull  = "full"
)

// Brief limits. A diff gets more room than output: it is the edit itself,
// and an edit cut off half way says little.
const (
	briefLines     = 6
	briefBytes     = 600
	briefDiffLines = 24
)

// Around's window: more after the message than before, because a search hit
// is usually a question being asked or a finding being stated, and what
// follows is what came of it. Tool results count as messages, so fifteen
// after is fewer turns than it sounds.
const (
	aroundBefore = 5
	aroundAfter  = 15
)

// ShowRequest selects part of a session.
//
// From and To are message indexes, the same numbers chunks.msg_idx, a search
// result's Msg and the page's anchors use, with To exclusive. Negative counts
// from the end, so From -20 is how the session ended. To 0 means the end.
type ShowRequest struct {
	From, To int
	Tools    string

	// BaseURL, when set, is where the server answers, and the transcript
	// carries a link to the session's page there.
	BaseURL string
}

// Around is the request for the window around one message, which is how a
// search result is followed up.
func Around(msg int) ShowRequest {
	return ShowRequest{From: max(msg-aroundBefore, 0), To: msg + aroundAfter + 1}
}

// Transcript is a session, or part of one, as data.
type Transcript struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Project     string    `json:"project"`
	CWD         string    `json:"cwd"`
	Host        string    `json:"host,omitempty"`
	Path        string    `json:"path"`
	StartedAt   time.Time `json:"started_at"`
	Messages    int       `json:"messages"`
	FromArchive bool      `json:"from_archive,omitempty"`
	URL         string    `json:"url,omitempty"`

	// From and To are the range shown, resolved: To is exclusive and never
	// past the end.
	From int `json:"from"`
	To   int `json:"to"`

	Entries []Entry `json:"entries"`
}

// Entry kinds, matching render's.
const (
	KindUser      = render.KindUser
	KindAssistant = render.KindAssistant
	KindThinking  = render.KindThinking
	KindTool      = render.KindTool
)

// Entry is one item in a transcript: prose, thinking, or a tool call with its
// result. Several can share a Msg, an assistant message being prose, then a
// call, then more prose.
type Entry struct {
	Msg  int       `json:"msg"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at,omitzero"`
	Text string    `json:"text,omitempty"`
	Tool *Tool     `json:"tool,omitempty"`
}

// Tool is a call and what came back.
type Tool struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Blk     int    `json:"blk"`
	Error   bool   `json:"error,omitempty"`

	// Args is the call's arguments in full, with full tools only: Summary
	// is one of them cut to a line, which is enough to recognise a command
	// and not enough to rerun it.
	Args string `json:"args,omitempty"`
	// Missing is a call with no result, from a session interrupted mid-call.
	Missing bool `json:"missing,omitempty"`

	// Diff is the edit the call made, drawn with the file's line numbers,
	// when the tool recorded one. It replaces the output, which for an edit
	// is a line saying it succeeded.
	Diff      string `json:"diff,omitempty"`
	DiffLines int    `json:"diff_lines,omitempty"`

	Output      string `json:"output,omitempty"`
	OutputLines int    `json:"output_lines,omitempty"`
	OutputBytes int    `json:"output_bytes,omitempty"`

	// Truncated says Output or Diff was cut to the brief limits.
	Truncated bool `json:"truncated,omitempty"`
}

// BuildTranscript turns a loaded session into a Transcript.
//
// It walks the messages itself rather than through render.Transcript, which
// renders every prose block to HTML and has no tool output: looking that up
// per call through render.Tool would rebuild the session's result map once
// for each of what can be a thousand calls.
func BuildTranscript(sum index.Summary, s *session.Session, req ShowRequest) Transcript {
	n := len(s.Messages)
	from, to := resolveRange(req.From, req.To, n)
	t := Transcript{
		ID:          sum.ID,
		Title:       sum.Heading(),
		Project:     sum.Project,
		CWD:         sum.CWD,
		Path:        sum.Path,
		StartedAt:   sum.StartedAt,
		Messages:    n,
		FromArchive: s.FromArchive,
		From:        from,
		To:          to,
		Entries:     []Entry{},
	}
	if sum.Host != "" && sum.Host != index.Hostname() {
		t.Host = sum.Host
	}
	if req.BaseURL != "" {
		t.URL = strings.TrimSuffix(req.BaseURL, "/") + "/sessions/" + sum.ID
	}

	results := s.ToolResults()
	for i := from; i < to; i++ {
		m := s.Messages[i]
		if m.Role != session.RoleUser && m.Role != session.RoleAssistant {
			continue // results are shown with the call that produced them
		}
		for b, blk := range m.Content {
			switch blk.Type {
			case session.BlockText:
				if strings.TrimSpace(blk.Text) != "" {
					t.Entries = append(t.Entries, Entry{Msg: i, Kind: m.Role, At: m.At, Text: blk.Text})
				}
			case session.BlockThinking:
				if strings.TrimSpace(blk.Thinking) != "" {
					t.Entries = append(t.Entries, Entry{Msg: i, Kind: KindThinking, At: m.At, Text: blk.Thinking})
				}
			case session.BlockToolCall:
				t.Entries = append(t.Entries, Entry{Msg: i, Kind: KindTool, At: m.At,
					Tool: buildTool(blk, b, results[blk.ID], req.Tools)})
			}
		}
	}
	return t
}

// resolveRange makes from and to concrete indexes within n messages.
func resolveRange(from, to, n int) (int, int) {
	if from < 0 {
		from += n
	}
	switch {
	case to < 0:
		to += n
	case to == 0 || to > n:
		to = n
	}
	from = min(max(from, 0), n)
	return from, max(to, from)
}

func buildTool(call session.Block, blk int, res *session.Message, mode string) *Tool {
	t := &Tool{Name: call.Name, Summary: render.ToolSummary(call.Name, call.Arguments), Blk: blk}
	if res == nil {
		t.Missing = true
		return t
	}
	t.Error = res.IsError
	if mode == ToolsNone {
		return t
	}
	if mode == ToolsFull {
		t.Args = indentJSON(call.Arguments)
	}

	if diff, _ := render.ParseDiff(res.Details); len(diff) > 0 && !res.IsError {
		lines := make([]string, len(diff))
		for i, l := range diff {
			if l.Kind == render.DiffGap {
				lines[i] = "   ..."
				continue
			}
			lines[i] = fmt.Sprintf("%s%5s  %s", l.Mark(), l.Num, l.Text)
		}
		t.DiffLines = len(lines)
		if mode != ToolsFull && len(lines) > briefDiffLines {
			lines = lines[:briefDiffLines]
			t.Truncated = true
		}
		t.Diff = strings.Join(lines, "\n")
		return t
	}

	out := strings.TrimRight(render.ResultText(res), "\n")
	t.OutputBytes = len(out)
	t.OutputLines = strings.Count(out, "\n") + 1
	if out == "" {
		t.OutputLines = 0
	}
	if mode != ToolsFull {
		if cut := briefCut(out); cut < len(out) {
			out = out[:cut]
			t.Truncated = true
		}
	}
	t.Output = out
	return t
}

// briefCut is where a brief result ends: after briefLines lines or
// briefBytes bytes, whichever comes first, and on a rune boundary.
func briefCut(s string) int {
	cut := len(s)
	lines := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines++
			if lines == briefLines {
				cut = i
				break
			}
		}
	}
	if cut > briefBytes {
		cut = briefBytes
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
	}
	return cut
}

// WriteMarkdown prints a transcript as markdown, the form an agent reads most
// easily and a person can still skim.
//
// Each message is labelled with its index, so a reader can ask for the range
// around anything it finds here.
func (t Transcript) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", oneLine(t.Title, 200))
	fmt.Fprintf(&b, "- id: %s\n", t.ID)
	fmt.Fprintf(&b, "- project: %s\n", t.Project)
	fmt.Fprintf(&b, "- cwd: %s\n", t.CWD)
	if !t.StartedAt.IsZero() {
		fmt.Fprintf(&b, "- started: %s\n", t.StartedAt.Local().Format("2006-01-02 15:04"))
	}
	if t.Host != "" {
		fmt.Fprintf(&b, "- recorded on: %s\n", t.Host)
	}
	if t.FromArchive {
		b.WriteString("- shown from the archive: the session's file is not on this machine\n")
	}
	if t.URL != "" {
		fmt.Fprintf(&b, "- url: %s\n", t.URL)
	}
	if t.From > 0 || t.To < t.Messages {
		fmt.Fprintf(&b, "- showing messages %d to %d of %d\n", t.From, t.To-1, t.Messages)
	} else {
		fmt.Fprintf(&b, "- messages: %d\n", t.Messages)
	}
	for _, e := range t.Entries {
		if e.Tool != nil && e.Tool.Truncated {
			b.WriteString("- tool output is cut to its first lines; ask for one message with " +
				"full tools to see all of it\n")
			break
		}
	}

	last := -1
	for _, e := range t.Entries {
		if e.Msg != last {
			last = e.Msg
			role := e.Kind
			if role == KindThinking || role == KindTool {
				role = KindAssistant
			}
			// A rule and a label rather than a heading: the prose is markdown
			// too, and an assistant's own "## Findings" would otherwise read
			// as the start of another message.
			fmt.Fprintf(&b, "\n---\n\n**[%d] %s**", e.Msg, role)
			if !e.At.IsZero() {
				fmt.Fprintf(&b, " · %s", e.At.Local().Format("2006-01-02 15:04"))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		switch e.Kind {
		case KindThinking:
			b.WriteString("*thinking:*\n\n")
			for _, line := range strings.Split(strings.TrimRight(e.Text, "\n"), "\n") {
				b.WriteString(strings.TrimRight("> "+line, " ") + "\n")
			}
		case KindTool:
			writeTool(&b, e.Tool)
		default:
			b.WriteString(strings.TrimRight(e.Text, "\n") + "\n")
		}
	}
	if len(t.Entries) == 0 {
		b.WriteString("\n(no conversation in this range)\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeTool(b *strings.Builder, t *Tool) {
	fmt.Fprintf(b, "**%s**", t.Name)
	if t.Summary != "" {
		b.WriteString(" " + codeSpan(t.Summary))
	}
	var flags []string
	switch {
	case t.Missing:
		flags = append(flags, "no result")
	case t.Error:
		flags = append(flags, "error")
	}
	if len(flags) > 0 {
		b.WriteString(" (" + strings.Join(flags, ", ") + ")")
	}
	b.WriteString("\n")

	if t.Args != "" && t.Diff == "" {
		b.WriteString("\n" + fenced(t.Args, "json"))
	}
	switch {
	case t.Diff != "":
		b.WriteString("\n" + fenced(t.Diff, "diff"))
		if t.Truncated {
			fmt.Fprintf(b, "\n(diff cut to %d of %d lines)\n", briefDiffLines, t.DiffLines)
		}
	case t.Output != "":
		b.WriteString("\n" + fenced(t.Output, ""))
		if t.Truncated {
			fmt.Fprintf(b, "\n(output cut; %d lines, %d bytes in full)\n", t.OutputLines, t.OutputBytes)
		}
	}
}

func indentJSON(raw []byte) string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return string(raw)
	}
	return b.String()
}

// fenced wraps s in a code fence longer than any run of backticks inside it,
// because tool output is arbitrary text and routinely contains fences of its
// own.
func fenced(s, lang string) string {
	fence := strings.Repeat("`", max(3, longestRun(s, '`')+1))
	return fence + lang + "\n" + s + "\n" + fence + "\n"
}

// codeSpan is fenced for inline code.
func codeSpan(s string) string {
	ticks := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return ticks + s + ticks
}

func longestRun(s string, c byte) int {
	best, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}
