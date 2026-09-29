package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The view spans both formats, and is only worth having if a question about
// a command is one query: which sessions ran it, and where.
func TestToolCallsViewCoversBothAgents(t *testing.T) {
	piDir := writeCorpus(t, map[string][]string{"pi-1": {
		userMsg("ship it"),
		`{"type":"message","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"shipping"},` +
			`{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"make deploy"}}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"done"}]}}`,
	}})

	// A string-content user record first: json_each over it yields SQL text,
	// which is the case the view's CASE guard exists for.
	ccDir := filepath.Join(t.TempDir(), "-Users-me-code-proj")
	if err := os.MkdirAll(ccDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cc := strings.Join([]string{
		`{"type":"user","sessionId":"cc-1","cwd":"/x","entrypoint":"cli","timestamp":"2026-03-31T12:26:02Z","message":{"role":"user","content":"check it"}}`,
		`{"type":"assistant","sessionId":"cc-1","cwd":"/x","entrypoint":"cli","timestamp":"2026-03-31T12:26:03Z","message":{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make plan"}}]}}`,
		`{"type":"user","sessionId":"cc-1","cwd":"/x","entrypoint":"cli","timestamp":"2026-03-31T12:26:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(ccDir, "cc-1.jsonl"), []byte(cc), 0o644); err != nil {
		t.Fatal(err)
	}

	db := openTest(t)
	if _, err := Build(context.Background(), db, BuildOptions{Dirs: []string{piDir, filepath.Dir(ccDir)}}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.SQL().Query(`
		SELECT session_id, msg_idx, call_id, lower(name), json_extract(arguments, '$.command')
		FROM tool_calls
		WHERE json_extract(arguments, '$.command') LIKE 'make %'
		ORDER BY session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type call struct {
		session       string
		msg           int
		id, name, cmd string
	}
	var got []call
	for rows.Next() {
		var c call
		if err := rows.Scan(&c.session, &c.msg, &c.id, &c.name, &c.cmd); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"cc-1", 1, "t1", "bash", "make plan"},
		{"pi-1", 1, "c1", "bash", "make deploy"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
