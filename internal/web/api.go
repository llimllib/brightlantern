package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/llimllib/brightlantern/internal/api"
	"github.com/llimllib/brightlantern/internal/index"
)

// handleAPISearch answers a search as JSON, for programs: `brightlantern
// search`, and agents asking through it (#98).
//
// Through the same engine as the page and its lexical pool, so a query gets
// the results the search box would show. The engine is the one that has the
// model once warm has loaded it, which is the reason the CLI asks here first.
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("q")) == "" {
		apiError(w, http.StatusBadRequest, "q is required")
		return
	}
	limit, err := intParam(q, "limit", 0)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	engine := s.engine.Load()
	if engine == nil {
		apiError(w, http.StatusServiceUnavailable, "search is unavailable")
		return
	}
	resp, err := api.Search(r.Context(), s.db, engine, api.SearchRequest{
		Query:   q.Get("q"),
		Limit:   limit,
		Sort:    q.Get("sort"),
		BaseURL: baseURL(r),
	})
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	writeJSON(w, resp)
}

// handleAPISession answers with a session's transcript, or a range of it, as
// JSON or with format=markdown as the text `brightlantern show` prints (#99).
func (s *Server) handleAPISession(w http.ResponseWriter, r *http.Request) {
	sum, err := s.db.Session(r.Context(), r.PathValue("id"))
	if errors.Is(err, index.ErrNotFound) {
		apiError(w, http.StatusNotFound, "no session with that id")
		return
	}
	if err != nil {
		s.apiFail(w, r, err)
		return
	}

	req, err := showRequest(r)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	parsed, err := s.loadSession(r.Context(), sum)
	if err != nil {
		apiError(w, http.StatusGone, "session unavailable: "+err.Error())
		return
	}
	t := api.BuildTranscript(sum, parsed, req)

	if r.URL.Query().Get("format") == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_ = t.WriteMarkdown(w)
		return
	}
	writeJSON(w, t)
}

// showRequest reads from, to, around and tools. around wins over from and
// to, being the shorthand for both.
func showRequest(r *http.Request) (api.ShowRequest, error) {
	q := r.URL.Query()
	var req api.ShowRequest
	if q.Has("around") {
		n, err := intParam(q, "around", 0)
		if err != nil {
			return req, err
		}
		req = api.Around(n)
	} else {
		var err error
		if req.From, err = intParam(q, "from", 0); err != nil {
			return req, err
		}
		if req.To, err = intParam(q, "to", 0); err != nil {
			return req, err
		}
	}
	switch tools := q.Get("tools"); tools {
	case "", api.ToolsBrief, api.ToolsNone, api.ToolsFull:
		req.Tools = tools
	default:
		return req, errors.New("tools must be brief, none or full")
	}
	req.BaseURL = baseURL(r)
	return req, nil
}

func intParam(q url.Values, name string, def int) (int, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New(name + " must be an integer")
	}
	return n, nil
}

// baseURL is where this request reached the server, for links back to it.
func baseURL(r *http.Request) string { return "http://" + r.Host }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// APIError is the body of every /api failure, so a client can show the
// reason rather than a status code.
type APIError struct {
	Error string `json:"error"`
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: msg})
}

func (s *Server) apiFail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	apiError(w, http.StatusInternalServerError, "internal error")
}
