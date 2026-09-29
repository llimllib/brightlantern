package session

import (
	"encoding/json"
	"time"
)

// ArchivedMessage is one row of the index's message archive: what an agent
// wrote for one message, and where it sat in the session.
type ArchivedMessage struct {
	Idx int
	At  time.Time
	Raw []byte
}

// DecodeArchived rebuilds a session's messages from its archived rows, which
// must be in idx order. It returns the messages and how many rows could not be
// decoded.
//
// The inverse of what ParseWithRaw hands the archive, and it has to preserve
// positions exactly: chunks.msg_idx, the tool URLs and the scroll anchors all
// address a message by its index in the file. Two things make that more than
// a loop:
//
//   - **Claude Code fans out.** One record answering several tool calls
//     became several messages, and each was archived with the whole record
//     as its Raw. Decoding every row would produce N copies of N messages, so
//     a row is decoded only if an earlier row's fan-out has not already
//     produced its index.
//   - **Gaps are padded.** A message archived without Raw leaves no row, and
//     closing the gap would shift every later index onto the wrong message.
//     An empty Message renders as nothing, which is what a missing message
//     should look like.
//
// The format is decided per row, the same way Parse decides per file: a
// Claude Code row is a whole record and carries sessionId, while a pi row is
// the message object alone.
func DecodeArchived(rows []ArchivedMessage) ([]Message, int) {
	var out []Message
	skipped := 0
	for _, r := range rows {
		if r.Idx < len(out) {
			continue // produced by an earlier row's fan-out
		}
		for len(out) < r.Idx {
			out = append(out, Message{})
		}

		if isClaudeLine(r.Raw) {
			var rec claudeRecord
			if err := json.Unmarshal(r.Raw, &rec); err != nil {
				skipped++
				continue
			}
			msgs := parseClaudeLine(&rec, r.Raw, true)
			if len(msgs) == 0 {
				skipped++
				continue
			}
			out = append(out, msgs...)
			continue
		}

		var m Message
		if err := json.Unmarshal(r.Raw, &m); err != nil {
			skipped++
			continue
		}
		// pi's own timestamp field is a string on some roles and a number on
		// others, which is why the envelope's was archived in a column.
		m.At = r.At
		m.Raw = append(json.RawMessage(nil), r.Raw...)
		out = append(out, m)
	}
	return out, skipped
}
