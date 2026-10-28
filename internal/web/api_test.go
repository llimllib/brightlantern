package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llimllib/brightlantern/internal/api"
)

func (f *fixture) getJSON(t *testing.T, path string, v any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK {
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type = %q", path, ct)
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v in %q", path, err, rec.Body.String())
	}
	return rec.Code
}

func TestAPISearch(t *testing.T) {
	f := newFixture(t, map[string][]string{
		"aaa": {userMsg("the deploy pipeline broke this morning")},
		"bbb": {
			userMsg("an unrelated opening"),
			assistantMsg("and an unrelated reply"),
			userMsg("now the pipeline deploy broke too"),
		},
		"ccc": {userMsg("nothing to see here")},
	})

	var resp api.SearchResponse
	if code := f.getJSON(t, api.SearchPath+"?q=deploy", &resp); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %+v, want aaa and bbb", resp.Results)
	}
	for _, r := range resp.Results {
		if !strings.Contains(r.Excerpt, "**deploy**") {
			t.Errorf("%s excerpt = %q, want the term marked", r.ID, r.Excerpt)
		}
		if !strings.HasSuffix(r.URL, "/sessions/"+r.ID) {
			t.Errorf("%s url = %q", r.ID, r.URL)
		}
		// The message the hit is in, which is where show --around starts.
		want := map[string]int{"aaa": 0, "bbb": 2}[r.ID]
		if r.Msg != want {
			t.Errorf("%s msg = %d, want %d", r.ID, r.Msg, want)
		}
	}
	if resp.Semantic {
		t.Error("semantic = true for a lexical-only engine")
	}

	// A phrase excludes, here as on the page.
	f.getJSON(t, api.SearchPath+"?q="+url.QueryEscape(`"deploy pipeline"`), &resp)
	if len(resp.Results) != 1 || resp.Results[0].ID != "aaa" {
		t.Errorf("phrase results = %+v, want only aaa", resp.Results)
	}

	f.getJSON(t, api.SearchPath+"?q=deploy&limit=1", &resp)
	if len(resp.Results) != 1 {
		t.Errorf("limit=1 gave %d results", len(resp.Results))
	}
}

// An empty query is a mistake, not a request for every session.
func TestAPISearchRefusesBadRequests(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hello")}})
	for _, path := range []string{api.SearchPath, api.SearchPath + "?q=+", api.SearchPath + "?q=x&limit=lots"} {
		var e APIError
		if code := f.getJSON(t, path, &e); code != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%s = %d %q, want 400 with a reason", path, code, e.Error)
		}
	}
}

func TestAPISession(t *testing.T) {
	f := newFixture(t, map[string][]string{
		"aaa": {
			userMsg("list the files"),
			toolCallMsg("c1", "bash", `{"command":"ls"}`),
			toolResultMsg("c1", strings.Repeat("file\n", 50)),
			assistantMsg("fifty files"),
		},
	})

	var tr api.Transcript
	if code := f.getJSON(t, api.SessionsPath+"aaa", &tr); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if tr.Messages != 4 || tr.From != 0 || tr.To != 4 || len(tr.Entries) != 3 {
		t.Fatalf("transcript = %+v", tr)
	}
	if tool := tr.Entries[1].Tool; tool == nil || !tool.Truncated || tool.OutputLines != 50 {
		t.Errorf("tool = %+v, want brief output of 50 lines", tool)
	}

	f.getJSON(t, api.SessionsPath+"aaa?from=-1", &tr)
	if len(tr.Entries) != 1 || tr.Entries[0].Text != "fifty files" {
		t.Errorf("from=-1 = %+v, want the last message", tr.Entries)
	}
	f.getJSON(t, api.SessionsPath+"aaa?from=1&to=2&tools=full", &tr)
	if len(tr.Entries) != 1 || tr.Entries[0].Tool.Truncated {
		t.Errorf("from=1&to=2&tools=full = %+v, want the call in full", tr.Entries)
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, api.SessionsPath+"aaa?format=markdown", nil))
	if !strings.Contains(rec.Body.String(), "**[3] assistant**") {
		t.Errorf("markdown = %q", rec.Body.String())
	}

	var e APIError
	if code := f.getJSON(t, api.SessionsPath+"nope", &e); code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", code)
	}
	if code := f.getJSON(t, api.SessionsPath+"aaa?tools=most", &e); code != http.StatusBadRequest {
		t.Errorf("tools=most = %d, want 400", code)
	}
}

// A session whose file is gone is read from the archive, at the same indexes.
func TestAPISessionFromArchive(t *testing.T) {
	f := newFixture(t, map[string][]string{
		"aaa": {userMsg("remember this"), assistantMsg("remembered")},
	})
	if err := os.Remove(filepath.Join(f.dir, "aaa.jsonl")); err != nil {
		t.Fatal(err)
	}
	var tr api.Transcript
	if code := f.getJSON(t, api.SessionsPath+"aaa", &tr); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !tr.FromArchive || len(tr.Entries) != 2 || tr.Entries[1].Msg != 1 {
		t.Errorf("transcript = %+v, want both messages from the archive", tr)
	}
}
