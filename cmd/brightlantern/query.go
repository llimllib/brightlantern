package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/llimllib/brightlantern/internal/api"
	"github.com/llimllib/brightlantern/internal/config"
	"github.com/llimllib/brightlantern/internal/embed"
	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/search"
	"github.com/llimllib/brightlantern/internal/web"
	"golang.org/x/sys/unix"
)

// queryTimeout bounds a request to a running server. A search is
// milliseconds once the model is loaded and a transcript is one file parse;
// anything slower is a server too wedged to wait on.
const queryTimeout = 30 * time.Second

// searchOptions are search's flags.
type searchOptions struct {
	limit   int
	sort    string
	json    bool
	lexical bool
	// color is auto, always or never, as ripgrep's --color.
	color string
}

// runSearch answers a query, from a running server when there is one for this
// index and from the index directly when there is not.
//
// The server first because it has the model loaded: opening it here costs a
// second or two, and fifteen whenever macOS has evicted the Metal shader cache
// (#56) -- a cost an agent pays on every search, and one that buys nothing a
// running server does not already have.
func runSearch(w io.Writer, dbPath, addr, query string, o searchOptions) error {
	var resp api.SearchResponse
	asked := false
	if base, ok := serverFor(addr, dbPath); ok {
		v := url.Values{"q": {query}, "sort": {o.sort}}
		if o.limit > 0 {
			v.Set("limit", strconv.Itoa(o.limit))
		}
		err := getJSON(base+api.SearchPath+"?"+v.Encode(), &resp)
		if err != nil && !errors.Is(err, errNoAPI) {
			return err
		}
		asked = err == nil
	}
	if !asked {
		db, engine, closeDB, err := openSearch(dbPath, o.lexical)
		if err != nil {
			return err
		}
		defer closeDB()
		resp, err = api.Search(context.Background(), db, engine,
			api.SearchRequest{Query: query, Limit: o.limit, Sort: o.sort})
		if err != nil {
			return err
		}
	}

	if o.json {
		return printJSON(w, resp)
	}
	if width, ok := colorWidth(w, o.color); ok {
		return resp.WriteColor(w, width)
	}
	return resp.WriteText(w)
}

// colorWidth reports whether to colour output to w, and the terminal's width
// when it is one.
//
// auto colours only a terminal, so an agent reading a pipe gets exactly the
// text it always did, and honours NO_COLOR and TERM=dumb as ripgrep does.
func colorWidth(w io.Writer, mode string) (int, bool) {
	width := 0
	tty := false
	if f, ok := w.(*os.File); ok {
		if ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil {
			tty, width = true, int(ws.Col)
		}
	}
	switch mode {
	case "always":
		return width, true
	case "never":
		return 0, false
	}
	if !tty || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return 0, false
	}
	return width, true
}

// openSearch opens a reader for searching with no server to ask: the
// semantic driver when the model loads, the lexical one when it does not, as
// openIndex does for a writer.
func openSearch(dbPath string, lexical bool) (*index.DB, *search.Engine, func(), error) {
	if err := requireIndex(dbPath); err != nil {
		return nil, nil, nil, err
	}
	if err := index.CheckFTS5(); err != nil {
		return nil, nil, nil, err
	}
	if !lexical {
		if err := index.RegisterSemanticDriver(embed.DefaultPaths()); err != nil {
			note("semantic search unavailable: %v", err)
		} else {
			stop := noteSlowModelLoad()
			db, err := index.OpenReader(dbPath, index.SemanticDriverName)
			stop()
			if err == nil {
				return db, buildEngine(db, index.SemanticDriverName), func() { db.Close() }, nil
			}
			// Never past a failure on the semantic driver: a connection whose
			// model did not register segfaults rather than erroring.
			note("semantic search unavailable: %v", err)
		}
	}
	db, err := index.OpenReader(dbPath, index.DriverName)
	if err != nil {
		return nil, nil, nil, err
	}
	return db, buildEngine(db, index.DriverName), func() { db.Close() }, nil
}

// showOptions are show's flags.
type showOptions struct {
	from, to int
	around   int // -1 for none
	tools    string
	json     bool
}

func (o showOptions) request() api.ShowRequest {
	req := api.ShowRequest{From: o.from, To: o.to}
	if o.around >= 0 {
		req = api.Around(o.around)
	}
	req.Tools = o.tools
	return req
}

// runShow prints a session's transcript, or a range of it.
//
// It asks a server first too, though nothing here needs the model: so that
// the link in the header is to the page that is actually serving, and a
// session reads the same through either path.
func runShow(w io.Writer, dbPath, addr, id string, o showOptions) error {
	switch o.tools {
	case "", api.ToolsBrief, api.ToolsNone, api.ToolsFull:
	default:
		return errors.New("--tools must be brief, none or full")
	}
	req := o.request()

	var t api.Transcript
	asked := false
	if base, ok := serverFor(addr, dbPath); ok {
		v := url.Values{"from": {strconv.Itoa(req.From)}, "to": {strconv.Itoa(req.To)}, "tools": {req.Tools}}
		err := getJSON(base+api.SessionsPath+url.PathEscape(id)+"?"+v.Encode(), &t)
		if err != nil && !errors.Is(err, errNoAPI) {
			return err
		}
		asked = err == nil
	}
	if !asked {
		if err := requireIndex(dbPath); err != nil {
			return err
		}
		db, err := index.OpenReader(dbPath, index.DriverName)
		if err != nil {
			return err
		}
		defer db.Close()
		ctx := context.Background()
		sum, err := db.Session(ctx, id)
		if errors.Is(err, index.ErrNotFound) {
			return fmt.Errorf("no session with id %s", id)
		}
		if err != nil {
			return err
		}
		parsed, err := db.LoadSession(ctx, sum.ID, sum.Path)
		if err != nil {
			return err
		}
		t = api.BuildTranscript(sum, parsed, req)
	}

	if o.json {
		return printJSON(w, t)
	}
	return t.WriteMarkdown(w)
}

// serverFor returns the base URL of a brightlantern at addr serving dbPath.
//
// The same index, not just any server: a daemon over the default index is
// not an answer to `search --db elsewhere.db`.
func serverFor(addr, dbPath string) (string, bool) {
	in := probeInstance(addr)
	if in == nil || !samePath(in.Index, dbPath) {
		return "", false
	}
	return "http://" + dialable(addr), true
}

// errNoAPI is a server that predates the /api routes: a daemon still running
// the release before the one that added them, which is every daemon for as
// long as an upgrade has not restarted it. The index is still there to open.
var errNoAPI = errors.New("server has no API")

func getJSON(u string, v any) error {
	c := http.Client{Timeout: queryTimeout}
	resp, err := c.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e web.APIError
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		if resp.StatusCode == http.StatusNotFound {
			return errNoAPI
		}
		return fmt.Errorf("server answered %s", resp.Status)
	}
	return json.Unmarshal(body, v)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// requireIndex refuses a missing index rather than letting a reader create an
// empty file and report that nothing matched.
func requireIndex(dbPath string) error {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no index at %s; run 'brightlantern index' first", dbPath)
	} else if err != nil {
		return err
	}
	return nil
}

// configuredAddr is the server address by the usual precedence, without
// resolving session directories: search and show read, and must not fail on a
// machine where none are detected.
func configuredAddr(given map[string]bool, flagged string) string {
	if given["addr"] {
		return flagged
	}
	if cfg, _, err := config.Load(); err == nil && cfg.Addr != "" {
		return cfg.Addr
	}
	return flagged
}

// parseInterspersed parses flags wherever they appear among the positional
// arguments, which the flag package stops at the first of: `search xdist
// --limit 5` is how anyone, agents included, writes it. A "--" ends flags,
// so a query can begin with a dash.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			return pos
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...)
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// joinQuery makes one query of the words given, so quoting the whole thing is
// optional. A phrase still needs its double quotes inside shell quotes.
func joinQuery(words []string) string { return strings.TrimSpace(strings.Join(words, " ")) }
