// Package api is what Bright Lantern answers to programs rather than to a
// browser: search results and transcripts as data (#98, #99).
//
// The server's /api routes and the CLI's search and show commands both go
// through here, the CLI either asking a running server or, with none, opening
// the index itself. Either way the same code ranks and the same code prints,
// so an agent asking a question gets the answer a person at the page would,
// whichever path it took.
package api

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/search"
)

// SearchPath is the server's search route.
const SearchPath = "/api/search"

// DefaultLimit is how many sessions a search returns unless asked for more:
// few enough that a reply fits comfortably in an agent's context, and an agent
// wanting more can say so.
const DefaultLimit = 10

// MaxLimit is the page's own cap. Past a hundred sessions the query wants
// narrowing, not paging.
const MaxLimit = 100

// SortNewest orders results by date rather than relevance, as the page's
// toggle does.
const SortNewest = "new"

// Sentinels FTS5 wraps matched terms in, which markExcerpt turns into
// markdown's bold. Not "**" directly: agents write bold constantly, and a
// term the session had already bolded came out as ****xdist****.
const (
	markOpen  = "\x01"
	markClose = "\x02"
	bold      = "**"
)

// excerptTokens is more than the page's 28: a row there has two lines, and
// here the excerpt is most of what a result has to say.
const excerptTokens = 48

// excerptRunes caps an excerpt that FTS5 did not cut, which is a semantic hit
// sharing no words with the query: that falls back to the whole 800-character
// chunk.
const excerptRunes = 320

// SearchRequest is one query.
type SearchRequest struct {
	Query string
	Limit int
	Sort  string

	// BaseURL, when set, is where the server answers, and each result
	// carries a link to its page there.
	BaseURL string
}

// SearchResponse is the answer to a SearchRequest.
type SearchResponse struct {
	Query string `json:"query"`

	// Semantic is whether the embedding model ranked these as well as
	// keyword search. Worth reporting, because a result list without it looks
	// the same and is worse: a server has none for its first fifteen seconds,
	// and a machine with no reachable GPU never does.
	Semantic bool           `json:"semantic"`
	Results  []SearchResult `json:"results"`
}

// SearchResult is one session that matched.
type SearchResult struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Project   string    `json:"project"`
	CWD       string    `json:"cwd"`
	Host      string    `json:"host,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Messages  int       `json:"messages"`

	Score float64 `json:"score"`
	// Chunks is how many of the session's chunks matched, which is a rough
	// measure of how much of it is about the query.
	Chunks int `json:"chunks"`

	// Excerpt is the best-matching passage, terms in **bold**.
	Excerpt string `json:"excerpt"`
	// Msg is the message that passage came from, which is where to start
	// reading: `brightlantern show ID --around MSG`. -1 when unknown.
	Msg int `json:"msg"`

	URL string `json:"url,omitempty"`
}

// Search ranks sessions against a query, the way the page's search box does.
func Search(ctx context.Context, db *index.DB, engine *search.Engine, req SearchRequest) (SearchResponse, error) {
	q := strings.TrimSpace(req.Query)
	resp := SearchResponse{Query: q, Semantic: HasSemantic(engine), Results: []SearchResult{}}
	if q == "" {
		return resp, fmt.Errorf("empty query")
	}
	if engine == nil {
		return resp, fmt.Errorf("search is unavailable")
	}
	limit := req.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}

	results, err := engine.SearchSessions(ctx, db.SQL(), search.Query{Text: q}, limit)
	if err != nil {
		return resp, err
	}
	for _, res := range results {
		r := SearchResult{
			ID:        res.ID,
			Title:     res.Heading(),
			Project:   res.Project,
			CWD:       res.CWD,
			StartedAt: res.StartedAt,
			Messages:  res.NumMsgs,
			Score:     res.Score,
			Chunks:    res.NumChunks,
			Msg:       -1,
		}
		if res.Host != index.Hostname() {
			r.Host = res.Host
		}
		ex, err := search.Excerpt(ctx, db.SQL(), res.BestChunk, q, excerptTokens, markOpen, markClose)
		if err != nil || ex == "" {
			ex = res.BestText
		}
		r.Excerpt = markExcerpt(oneLine(ex, excerptRunes))
		if idx, err := search.ChunkMessage(ctx, db.SQL(), res.BestChunk); err == nil && idx >= 0 {
			r.Msg = idx
		} else if idxs, err := search.MessagesMatching(ctx, db.SQL(), res.ID, q, 1); err == nil && len(idxs) > 0 {
			// The best chunk was the title's, which belongs to no message:
			// point at the best one the words appear in instead, as the page
			// scrolls to.
			r.Msg = idxs[0]
		}
		if req.BaseURL != "" {
			r.URL = strings.TrimSuffix(req.BaseURL, "/") + "/sessions/" + res.ID
		}
		resp.Results = append(resp.Results, r)
	}

	if req.Sort == SortNewest {
		sort.SliceStable(resp.Results, func(i, j int) bool {
			return resp.Results[i].StartedAt.After(resp.Results[j].StartedAt)
		})
	}
	return resp, nil
}

// HasSemantic reports whether an engine ranks with the embedding model.
func HasSemantic(e *search.Engine) bool {
	if e == nil {
		return false
	}
	for _, r := range e.Rankers {
		if _, ok := r.(*search.Semantic); ok {
			return true
		}
	}
	return false
}

// WriteText prints a search response for reading, by a person or an agent.
//
// One block per session, and nothing that needs a terminal: this is as likely
// to land in a model's context as on a screen, and ten results should cost a
// few hundred tokens rather than a few thousand.
func (r SearchResponse) WriteText(w io.Writer) error {
	var b strings.Builder
	if len(r.Results) == 0 {
		b.WriteString("No sessions match that search.")
		if phrases := search.Phrases(r.Query); len(phrases) > 0 {
			b.WriteString(" Quoted phrases must appear exactly; remove the quotes to search for the words separately.")
		}
		b.WriteString("\n")
	}
	for i, res := range r.Results {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, oneLine(res.Title, 160))
		meta := []string{res.ID, res.Project, res.StartedAt.Local().Format("2006-01-02")}
		if res.Host != "" {
			meta = append(meta, "on "+res.Host)
		}
		match := fmt.Sprintf("%d messages", res.Messages)
		if res.Msg >= 0 {
			match += fmt.Sprintf(", best match at message %d", res.Msg)
		}
		meta = append(meta, match)
		fmt.Fprintf(&b, "   %s\n", strings.Join(meta, " · "))
		if res.Excerpt != "" {
			fmt.Fprintf(&b, "   %s\n", res.Excerpt)
		}
		if res.URL != "" {
			fmt.Fprintf(&b, "   %s\n", res.URL)
		}
	}
	if !r.Semantic {
		b.WriteString("\n(keyword search only: the embedding model is not loaded)\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// markExcerpt replaces the sentinels with bold, except where the text is
// bold already or is code.
//
// Already bold means inside a ** pair of the session's own, or right against
// one: a mark there would nest or double the emphasis rather than add any.
// Inside a code span markdown does not render emphasis at all, so a mark
// there would print as asterisks inside an identifier -- and identifiers are
// much of what gets searched for.
// A snippet can begin inside a bold run whose opening was cut off, which
// parity cannot see; the adjacency test still keeps that from doubling, at
// the cost of a stray ** that was in the text anyway. A mark the cut at
// excerptRunes left open is closed, so it cannot embolden what follows.
func markExcerpt(s string) string {
	var b strings.Builder
	inBold, inCode, marking := false, false, false
	for i := 0; i < len(s); {
		switch {
		case s[i] == '`':
			inCode = !inCode
			b.WriteByte('`')
		case strings.HasPrefix(s[i:], bold) && !inCode:
			inBold = !inBold
			b.WriteString(bold)
			i += len(bold)
			continue
		case s[i] == markOpen[0]:
			end := strings.IndexByte(s[i:], markClose[0])
			after := len(s)
			if end >= 0 {
				after = i + end + 1
			}
			marking = !inBold && !inCode && !strings.HasSuffix(s[:i], bold) &&
				!strings.HasPrefix(s[after:], bold)
			if marking {
				b.WriteString(bold)
			}
		case s[i] == markClose[0]:
			if marking {
				b.WriteString(bold)
				marking = false
			}
		default:
			b.WriteByte(s[i])
		}
		i++
	}
	if marking {
		b.WriteString(bold)
	}
	return b.String()
}

// oneLine flattens whitespace and cuts to n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
