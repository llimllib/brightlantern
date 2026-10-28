package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/indexer"
)

// stubStatus stands in for the indexer.
type stubStatus struct{ st indexer.Status }

func (s stubStatus) Status() indexer.Status { return s.st }

func (f *fixture) withIndexer(t *testing.T, st indexer.Status) *fixture {
	t.Helper()
	f.srv.indexer = stubStatus{st: st}
	return f
}

func TestStatusPollsFasterWhileIndexing(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})

	cases := []struct {
		phase    indexer.Phase
		wantPoll string
	}{
		{indexer.PhaseIndexing, pollBusy},
		{indexer.PhaseStarting, pollBusy},
		{indexer.PhaseTitling, pollBusy},
		{indexer.PhaseWatching, pollIdle},
		{indexer.PhaseStopped, pollIdle},
	}
	for _, c := range cases {
		f.withIndexer(t, indexer.Status{Phase: c.phase})
		_, doc := f.get(t, "/status")

		el := doc.Find("#status")
		if el.Length() != 1 {
			t.Fatalf("%s: no status element", c.phase)
		}
		// The element replaces itself, so the server decides the next
		// interval by rendering it.
		trigger, _ := el.Attr("hx-trigger")
		if trigger != "every "+c.wantPoll {
			t.Errorf("%s: hx-trigger = %q, want every %s", c.phase, trigger, c.wantPoll)
		}
		if swap, _ := el.Attr("hx-swap"); swap != "outerHTML" {
			t.Errorf("%s: hx-swap = %q", c.phase, swap)
		}
	}
}

func TestStatusShowsProgressWhileIndexing(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseIndexing, Done: 42, Total: 100})

	_, doc := f.get(t, "/status")
	text := strings.Join(strings.Fields(doc.Find("#status").Text()), " ")
	if !strings.Contains(text, "indexing 42/100") {
		t.Errorf("status text = %q", text)
	}
}

// Titling is slow enough that saying "indexing" and then going quiet for a
// minute would look like a hang.
func TestStatusDistinguishesTitlingFromIndexing(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseTitling, Done: 7, Total: 30})

	_, doc := f.get(t, "/status")
	text := strings.Join(strings.Fields(doc.Find("#status").Text()), " ")
	if !strings.Contains(text, "titling 7/30") {
		t.Errorf("status text = %q", text)
	}
}

// addSession writes a session file and indexes it, the way the watcher does
// while a page is open.
func (f *fixture) addSession(t *testing.T, id string, started time.Time, msgs ...string) {
	t.Helper()
	header := `{"type":"session","version":3,"id":"` + id +
		`","timestamp":"` + started.UTC().Format(time.RFC3339) + `","cwd":"/Users/me/code/proj"}`
	body := header + "\n" + strings.Join(msgs, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(f.dir, id+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := index.Open(f.dbPath, index.DriverName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := index.Build(context.Background(), db, index.BuildOptions{Dirs: []string{f.dir}}); err != nil {
		t.Fatal(err)
	}
}

func arrivalIDs(doc *goquery.Document) []string {
	// A <template>'s children are its content, which goquery parses as
	// ordinary descendants.
	var ids []string
	doc.Find("#status template.arrivals li[data-id]").Each(func(_ int, li *goquery.Selection) {
		id, _ := li.Attr("data-id")
		ids = append(ids, id)
	})
	return ids
}

func sinceOf(t *testing.T, doc *goquery.Document) string {
	t.Helper()
	got, _ := doc.Find("#status").Attr("hx-get")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("hx-get %q: %v", got, err)
	}
	return u.Query().Get("since")
}

// Rather than the server tracking what each open tab has seen, the page
// carries the last arrival it rendered with and the poll returns what came
// after, as rows ready to place.
func TestStatusDeliversArrivals(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseWatching})

	_, page := f.get(t, "/")
	seed, _ := page.Find("#status").Attr("hx-get")

	// Nothing new yet: no rows, and the watermark carried forward unchanged.
	_, doc := f.get(t, seed)
	if ids := arrivalIDs(doc); len(ids) != 0 {
		t.Errorf("arrivals before anything arrived: %v", ids)
	}
	if got, _ := doc.Find("#status").Attr("hx-get"); got != seed {
		t.Errorf("hx-get = %q, want %q carried forward", got, seed)
	}

	// Older than everything on the page, which is the case a started_at
	// watermark would miss: a catch-up build finds files in any order.
	f.addSession(t, "old", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), userMsg("from before"))
	_, doc = f.get(t, seed)
	if ids := arrivalIDs(doc); len(ids) != 1 || ids[0] != "old" {
		t.Fatalf("arrivals = %v, want [old]", ids)
	}
	li := doc.Find("#status template.arrivals li").First()
	if href, _ := li.Find("a.row").Attr("href"); href != "/sessions/old" {
		t.Errorf("arrival href = %q", href)
	}
	if dt, _ := li.Find("time.row-date").Attr("datetime"); dt != "2020-01-01T00:00:00Z" {
		t.Errorf("arrival datetime = %q, which is what the page orders by", dt)
	}

	// The watermark advanced past it, so the next poll does not deliver it
	// again.
	next := sinceOf(t, doc)
	if next == sinceOf(t, page) {
		t.Fatal("watermark did not advance")
	}
	_, doc = f.get(t, "/status?since="+next)
	if ids := arrivalIDs(doc); len(ids) != 0 {
		t.Errorf("delivered again: %v", ids)
	}
}

// A session that gains a message is reindexed, but it is not new.
func TestStatusDoesNotDeliverGrownSessions(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseWatching})
	_, page := f.get(t, "/")
	seed, _ := page.Find("#status").Attr("hx-get")

	f.addSession(t, "aaa", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), userMsg("hi"), userMsg("again"))
	_, doc := f.get(t, seed)
	if ids := arrivalIDs(doc); len(ids) != 0 {
		t.Errorf("a grown session arrived: %v", ids)
	}
}

// A tab left open across an upgrade polls with the count this used to
// compare. It gets the indicator and no arrivals, rather than an error.
func TestStatusIgnoresAnOldPoll(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseWatching})
	f.addSession(t, "bbb", time.Now(), userMsg("new"))

	rec, doc := f.get(t, "/status?n=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ids := arrivalIDs(doc); len(ids) != 0 {
		t.Errorf("arrivals without a watermark: %v", ids)
	}
	if got, _ := doc.Find("#status").Attr("hx-get"); got != "/status" {
		t.Errorf("hx-get = %q", got)
	}
}

func TestStatusShowsErrors(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.withIndexer(t, indexer.Status{Phase: indexer.PhaseWatching, LastErr: "disk on fire"})

	_, doc := f.get(t, "/status")
	el := doc.Find(".status-error")
	if el.Length() != 1 {
		t.Fatal("error not surfaced")
	}
	if title, _ := el.Attr("title"); title != "disk on fire" {
		t.Errorf("title = %q", title)
	}
}

// With --no-watch, or a read-only index, there is no indexer. The element
// should disappear rather than poll forever against a server with nothing to
// say.
func TestStatusEmptyWithoutAnIndexer(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("hi")}})
	f.srv.indexer = nil

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("body = %q, want empty so hx-swap removes the element", rec.Body.String())
	}
}

// The page has to seed the poll, or the indicator never starts, and has to
// seed it with the newest arrival it rendered, or the first poll delivers
// every session it already shows.
func TestPageSeedsTheStatusPoll(t *testing.T) {
	f := newFixture(t, map[string][]string{
		"aaa": {userMsg("one")},
		"bbb": {userMsg("two")},
	})
	_, doc := f.get(t, "/")

	el := doc.Find("#status")
	if el.Length() != 1 {
		t.Fatal("no status element on the page")
	}
	if got, _ := el.Attr("hx-get"); got != "/status?since=2" {
		t.Errorf("hx-get = %q, want the last arrival", got)
	}
	if got, _ := el.Attr("hx-trigger"); got != "load" {
		t.Errorf("hx-trigger = %q, want load", got)
	}
}

// app.ts places arrivals only in the unfiltered list, and tells which list it
// has by this attribute rather than the URL, which typing a search leaves
// alone.
func TestListMarksWhetherItIsBrowsing(t *testing.T) {
	f := newFixture(t, map[string][]string{"aaa": {userMsg("the quicksand")}})

	_, doc := f.get(t, "/")
	if _, ok := doc.Find("#rows").Attr("data-browsing"); !ok {
		t.Error("the unfiltered list is not marked data-browsing")
	}
	if doc.Find("#rows li[data-id=aaa]").Length() != 1 {
		t.Error("rows carry no data-id for arrivals to be deduplicated against")
	}

	_, doc = f.htmxGet(t, "/search?q=quicksand")
	if _, ok := doc.Find("#rows").Attr("data-browsing"); ok {
		t.Error("search results are marked data-browsing")
	}
}
