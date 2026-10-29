package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llimllib/brightlantern/internal/api"
	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/search"
	"github.com/llimllib/brightlantern/internal/web"
)

// queryIndex builds an index of one pi session that mentions xdist.
func queryIndex(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "--Users-me-proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","version":3,"id":"s1","timestamp":"2026-08-18T12:00:00Z","cwd":"/Users/me/proj"}
{"type":"message","timestamp":"2026-08-18T12:00:01Z","message":{"role":"user","content":[{"type":"text","text":"did we ever use xdist before?"}]}}
{"type":"message","timestamp":"2026-08-18T12:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"yes, in ENG-413"}]}}
`
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "index.db")
	db, err := index.Open(dbPath, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := index.Build(context.Background(), db, index.BuildOptions{Dirs: []string{filepath.Dir(dir)}}); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

// serveIndex runs the real web server over dbPath, reporting servedPath as
// its index, which is what lets a test make it look like a server for a
// different index.
func serveIndex(t *testing.T, dbPath, servedPath string) string {
	t.Helper()
	db, err := index.OpenReader(dbPath, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	engine := &search.Engine{Rankers: []search.Ranker{&search.Lexical{DB: db.SQL()}}, Fusion: search.DefaultFusion()}
	srv, err := web.New(db, engine, web.Options{IndexPath: servedPath})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs.Listener.Addr().String()
}

// Which path answered shows in the results: only a server knows its own URL.
func TestSearchAsksAServerForTheSameIndex(t *testing.T) {
	dbPath := queryIndex(t)
	lexical := searchOptions{json: true, lexical: true}

	tests := []struct {
		name      string
		addr      func(t *testing.T) string
		wantAsked bool
	}{
		{"server for this index", func(t *testing.T) string { return serveIndex(t, dbPath, dbPath) }, true},
		{"server for another index", func(t *testing.T) string {
			return serveIndex(t, dbPath, filepath.Join(t.TempDir(), "other.db"))
		}, false},
		// A daemon from before the API answers /api/instance and 404s the
		// rest; the index is still there to open.
		{"server without the API", func(t *testing.T) string {
			return fakeInstance(t, web.Instance{Index: dbPath})
		}, false},
		{"nothing listening", func(t *testing.T) string {
			hs := httptest.NewServer(http.NotFoundHandler())
			hs.Close()
			return hs.Listener.Addr().String()
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			if err := runSearch(&out, dbPath, tc.addr(t), "xdist", lexical); err != nil {
				t.Fatal(err)
			}
			var resp api.SearchResponse
			if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Results) != 1 || resp.Results[0].ID != "s1" {
				t.Fatalf("results = %+v, want s1", resp.Results)
			}
			if asked := resp.Results[0].URL != ""; asked != tc.wantAsked {
				t.Errorf("asked the server = %v, want %v", asked, tc.wantAsked)
			}
		})
	}
}

func TestShowBothWays(t *testing.T) {
	dbPath := queryIndex(t)
	for name, addr := range map[string]string{
		"server": serveIndex(t, dbPath, dbPath),
		"local":  fakeInstance(t, web.Instance{Index: "/elsewhere.db"}),
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := runShow(&out, dbPath, addr, "s1", showOptions{around: -1}); err != nil {
				t.Fatal(err)
			}
			md := out.String()
			for _, want := range []string{"**[0] user**", "did we ever use xdist before?", "**[1] assistant**"} {
				if !strings.Contains(md, want) {
					t.Errorf("missing %q in:\n%s", want, md)
				}
			}

			err := runShow(&out, dbPath, addr, "nope", showOptions{around: -1})
			if err == nil || !strings.Contains(err.Error(), "no session") {
				t.Errorf("unknown id: %v, want an error naming it", err)
			}
		})
	}
}

// Asking with no index must not create an empty one and report no matches.
func TestSearchRequiresAnIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	hs := httptest.NewServer(http.NotFoundHandler())
	hs.Close()
	err := runSearch(&strings.Builder{}, dbPath, hs.Listener.Addr().String(), "x", searchOptions{lexical: true})
	if err == nil || !strings.Contains(err.Error(), "no index") {
		t.Errorf("runSearch() = %v, want a missing index named", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("index created: %v", err)
	}
}

func TestParseInterspersed(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		pos   string
		limit int
	}{
		{[]string{"xdist"}, "xdist", 0},
		{[]string{"xdist", "--limit", "5"}, "xdist", 5},
		{[]string{"--limit", "5", "pytest", "xdist"}, "pytest xdist", 5},
		{[]string{"pytest", "--limit=3", "xdist"}, "pytest xdist", 3},
		{[]string{"--", "-n", "4"}, "-n 4", 0},
		{[]string{"xdist", "--", "--limit", "5"}, "xdist --limit 5", 0},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		limit := fs.Int("limit", 0, "")
		pos := strings.Join(parseInterspersed(fs, tc.args), " ")
		if pos != tc.pos || *limit != tc.limit {
			t.Errorf("%q: positional %q limit %d; want %q %d", tc.args, pos, *limit, tc.pos, tc.limit)
		}
	}
}

func TestColorWidth(t *testing.T) {
	var pipe strings.Builder
	for _, tc := range []struct {
		mode, noColor string
		want          bool
	}{
		{"auto", "", false}, // not a terminal
		{"never", "", false},
		{"always", "", true},
		// An explicit request outranks NO_COLOR, as in ripgrep.
		{"always", "1", true},
	} {
		t.Setenv("NO_COLOR", tc.noColor)
		if _, got := colorWidth(&pipe, tc.mode); got != tc.want {
			t.Errorf("colorWidth(pipe, %q) with NO_COLOR=%q = %v, want %v", tc.mode, tc.noColor, got, tc.want)
		}
	}
}
