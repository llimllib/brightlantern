package session

import (
	"reflect"
	"testing"
)

// archiveOf turns a parsed session into the rows the index would store for
// it, the same way archiveMessages does.
func archiveOf(s *Session) []ArchivedMessage {
	var rows []ArchivedMessage
	for i, m := range s.Messages {
		if len(m.Raw) == 0 {
			continue
		}
		rows = append(rows, ArchivedMessage{Idx: i, At: m.At, Raw: m.Raw})
	}
	return rows
}

// assertRoundTrip checks that decoding a session's archive gives back what
// parsing its file gave, position for position.
func assertRoundTrip(t *testing.T, path string) {
	t.Helper()
	want, err := ParseWithRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	got, skipped := DecodeArchived(archiveOf(want))
	if skipped != 0 {
		t.Errorf("skipped %d rows", skipped)
	}
	if len(got) != len(want.Messages) {
		t.Fatalf("decoded %d messages, parsed %d", len(got), len(want.Messages))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want.Messages[i]) {
			t.Errorf("message %d differs:\n got %+v\nwant %+v", i, got[i], want.Messages[i])
		}
	}
}

func TestDecodeArchivedRoundTripsPi(t *testing.T) {
	assertRoundTrip(t, writeSession(t,
		`{"type":"session","version":3,"id":"pi-1","timestamp":"2026-03-31T12:26:01.076Z","cwd":"/x"}`,
		`{"type":"message","timestamp":"2026-03-31T12:26:02Z","message":{"role":"user","content":[{"type":"text","text":"read the file"}]}}`,
		`{"type":"message","timestamp":"2026-03-31T12:26:03Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"a.go"}}]}}`,
		`{"type":"message","timestamp":"2026-03-31T12:26:04Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":[{"type":"text","text":"package a"}],"details":{"lines":1}}}`,
		`{"type":"message","timestamp":"2026-03-31T12:26:05Z","message":{"role":"bashExecution","command":"ls"}}`,
	))
}

// A record answering two tool calls at once is two messages sharing one row's
// bytes. Decoding each row would give four.
func TestDecodeArchivedRoundTripsClaudeFanOut(t *testing.T) {
	assertRoundTrip(t, writeSession(t,
		`{"type":"user","sessionId":"cc-1","cwd":"/x","timestamp":"2026-03-31T12:26:02Z","message":{"role":"user","content":"look at both"}}`,
		`{"type":"assistant","sessionId":"cc-1","cwd":"/x","timestamp":"2026-03-31T12:26:03Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a"}},{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"b"}}]}}`,
		`{"type":"user","sessionId":"cc-1","cwd":"/x","timestamp":"2026-03-31T12:26:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"A"},{"type":"tool_result","tool_use_id":"t2","content":"B"}]},"toolUseResult":{"x":1}}`,
		`{"type":"assistant","sessionId":"cc-1","cwd":"/x","timestamp":"2026-03-31T12:26:05Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	))
}

// A missing row must not pull every later message down an index: chunks and
// tool URLs point at positions.
func TestDecodeArchivedKeepsPositionsAcrossAGap(t *testing.T) {
	rows := []ArchivedMessage{
		{Idx: 0, Raw: []byte(`{"role":"user","content":[{"type":"text","text":"zero"}]}`)},
		{Idx: 2, Raw: []byte(`{"role":"assistant","content":[{"type":"text","text":"two"}]}`)},
		{Idx: 3, Raw: []byte(`not json`)},
		{Idx: 4, Raw: []byte(`{"role":"user","content":[{"type":"text","text":"four"}]}`)},
	}
	got, skipped := DecodeArchived(rows)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	for idx, text := range map[int]string{0: "zero", 2: "two", 4: "four"} {
		if c := got[idx].Content; len(c) != 1 || c[0].Text != text {
			t.Errorf("message %d = %+v, want %q", idx, got[idx], text)
		}
	}
	if got[1].Role != "" || got[3].Role != "" {
		t.Errorf("padding carries a role: %q, %q", got[1].Role, got[3].Role)
	}
}
