package web

import (
	"net/http"
	"strconv"

	"github.com/llimllib/brightlantern/internal/indexer"
)

// Poll intervals. Busy is often enough to look live during a build; idle is
// slow enough that a tab left open overnight is not doing anything much, but
// quick enough to notice a conversation happening in another terminal.
const (
	pollBusy = "2s"
	pollIdle = "10s"
)

// statusData drives the header's indexing indicator, and carries the sessions
// that arrived since the page rendered.
type statusData struct {
	Status indexer.Status
	Poll   string

	// Tracking means the page sent a watermark, and Since is the one the next
	// poll should send: the last arrival this response included. Each poll
	// replaces the element, so a watermark not rendered into it is lost.
	Tracking bool
	Since    int64

	// Arrivals are rendered as list rows for the page to place. Whether to
	// place them is the page's decision, not this handler's: typing a search
	// swaps the list without the poll knowing, so only the page can tell
	// whether it is showing the list they belong in.
	Arrivals []row
}

// handleStatus renders the indexing indicator.
//
// It replaces itself on every poll, so the server chooses the next interval
// by rendering it into the response: fast while indexing, slow when idle.
// Removing the element entirely when idle would be tidier, but then a session
// started later would never be noticed by an open tab.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.indexer == nil {
		// Nothing is indexing, so nothing should poll. An empty response with
		// hx-swap="outerHTML" removes the element.
		w.WriteHeader(http.StatusOK)
		return
	}

	st := s.indexer.Status()
	data := statusData{Status: st, Poll: pollIdle}
	if st.Busy() {
		data.Poll = pollBusy
	}

	// since is what the page has seen; absent or unparseable means the caller
	// does not want arrivals, which is also what a tab left open across an
	// upgrade sends, polling the count this used to compare.
	if since, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); err == nil {
		data.Tracking, data.Since = true, since
		// A failure keeps the watermark, so the next poll asks again.
		if sums, last, err := s.db.ArrivedSince(r.Context(), since, listLimit); err == nil {
			data.Since = last
			for _, sum := range sums {
				data.Arrivals = append(data.Arrivals, row{Summary: sum})
			}
		}
	}

	s.render(w, r, "status.html", data)
}
