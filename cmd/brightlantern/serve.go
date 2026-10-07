package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/llimllib/brightlantern/internal/config"
	"github.com/llimllib/brightlantern/internal/embed"
	"github.com/llimllib/brightlantern/internal/index"
	"github.com/llimllib/brightlantern/internal/indexer"
	"github.com/llimllib/brightlantern/internal/search"
	"github.com/llimllib/brightlantern/internal/titles"
	"github.com/llimllib/brightlantern/internal/web"
)

func runServe(dbPath, addr string, dirs []string, dev, launchBrowser, noWatch, wait bool, titlesVia string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// First, before anything that writes or loads: see listen.
	var ln net.Listener
	var err error
	if wait {
		ln, err = listenWhenFree(ctx, addr)
		if ctx.Err() != nil {
			return nil // stopped while waiting, which is not a failure
		}
	} else {
		ln, err = listen(addr)
	}
	if err != nil {
		return err
	}
	defer ln.Close()

	// Absolute, because it is reported at web.InstancePath and compared
	// against paths given to other processes from other directories.
	dbPath = canonical(dbPath)

	if err := bootstrapIndex(dbPath); err != nil {
		return err
	}

	// The page comes up on the lexical driver. Browsing and keyword search
	// need no model, and loading one can take fifteen seconds on a cold Metal
	// cache (#56) -- which under KeepAlive is a restarted daemon refusing
	// connections. The model loads behind the page, in warm.
	db, err := index.OpenReader(dbPath, index.DriverName)
	if err != nil {
		return err
	}
	defer db.Close()

	n, err := db.CountSessions(context.Background())
	if err != nil {
		return err
	}

	opts := web.Options{Dev: dev, Version: Version, IndexPath: dbPath}
	var status *pendingStatus
	if !noWatch {
		status = &pendingStatus{}
		opts.Indexer = status
	}
	srv, err := web.New(db, buildEngine(db, index.DriverName), opts)
	if err != nil {
		return err
	}

	// The socket has been bound since the top, so the printed URL is one that
	// answers and --open cannot race it.
	url := "http://" + ln.Addr().String()
	// The subcommands are not visible on a bare `brightlantern`, which is now the
	// usual way to run it. One line restores them without anyone reading usage
	// they did not ask for.
	fmt.Printf("brightlantern %s serving %d sessions on %s\n", Version, n, url)
	fmt.Println("'brightlantern help' lists the other commands")
	if dev {
		fmt.Println("dev mode: templates and static files reload from disk")
	}
	if launchBrowser {
		go open(url)
	}

	httpSrv := srv.Server(addr)
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	var bg closers
	defer bg.close()
	go warm(ctx, srv, status, &bg, dbPath, dirs, titlesVia)

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fmt.Println("\nshutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// warm does everything slow that serve needs, behind a page that is already
// answering: load the model into a reader pool and swap semantic search in,
// then open the writer and start indexing.
//
// In that order, and not concurrently. The first connection compiles the
// Metal shaders and every later one hits the cache, so the writer opened
// second loads in well under a second -- two at once would compile twice.
// Search comes first because it is what someone at the page is waiting for;
// the indexer only has to catch up with sessions written while it was down.
//
// A nil status means --no-watch: search still warms, nothing indexes.
func warm(ctx context.Context, srv *web.Server, status *pendingStatus, bg *closers,
	dbPath string, dirs []string, titlesVia string) {
	driver := index.DriverName
	if err := index.RegisterSemanticDriver(embed.DefaultPaths()); err == nil {
		stop := slowNote(2*time.Second, "loading the embedding model; search is keyword-only "+
			"until it finishes. llama.cpp is compiling Metal shaders, which takes about "+
			"15s the first time and is then cached")
		sem, err := index.OpenReader(dbPath, index.SemanticDriverName)
		stop()
		if err != nil {
			note("semantic search unavailable: %v", err)
		} else if !bg.add(func() { sem.Close() }) {
			return // shut down while the model loaded
		} else {
			driver = index.SemanticDriverName
			srv.SetEngine(buildEngine(sem, driver))
		}
	}

	if status == nil {
		return
	}
	// A writer, separate from the read pools. WAL lets handlers read a
	// consistent snapshot while this one indexes, so a reindex triggered by a
	// conversation in another terminal never blocks a request.
	live, closeLive := startIndexer(ctx, dbPath, driver, dirs, titlesVia)
	if closeLive != nil && !bg.add(closeLive) {
		return
	}
	status.set(live)
}

// pendingStatus is the indexer as the page sees it before the indexer
// exists. Reporting "starting" rather than nothing matters: an empty /status
// removes the header's poller, and the page would never notice the indexer
// arriving.
type pendingStatus struct {
	mu      sync.Mutex
	src     web.StatusSource
	settled bool
}

func (p *pendingStatus) set(src web.StatusSource) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.src, p.settled = src, true
}

func (p *pendingStatus) Status() indexer.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.src != nil:
		return p.src.Status()
	case p.settled:
		// startIndexer gave up and said why on stderr.
		return indexer.Status{Phase: indexer.PhaseStopped, LastErr: "live indexing is disabled; see the server's log"}
	default:
		return indexer.Status{Phase: indexer.PhaseStarting}
	}
}

// closers collects what warm opened, for serve to close on the way out.
// Closing is final: anything added afterwards is closed on the spot and
// reported, so a model that finishes loading during shutdown is not leaked
// into a process that is exiting -- and, more to the point, is not handed to
// a server that has stopped.
type closers struct {
	mu     sync.Mutex
	fns    []func()
	closed bool
}

func (c *closers) add(fn func()) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		fn()
		return false
	}
	c.fns = append(c.fns, fn)
	return true
}

func (c *closers) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for i := len(c.fns) - 1; i >= 0; i-- {
		c.fns[i]()
	}
}

// bootstrapIndex creates an empty index when there is none.
//
// The read pool below is _query_only, which cannot create a schema, so
// something has to have made one first. That used to be an error telling the
// reader to go and run 'brightlantern index' -- accurate, and backwards: serve opens
// a writer a few lines further down and runs a catch-up build through it, so it
// already does the thing it was refusing to start without. The only reason it
// could not bootstrap itself was the order the two handles were opened in.
//
// Creating it here rather than waiting for startIndexer: that runs behind the
// page, after the model loads, and not at all under --no-watch -- and a
// scripted run against a fresh index is exactly where that flag is used.
//
// The lexical driver, whatever the server will use: this needs the schema and
// nothing else, and opening a semantic connection would load a second copy of
// the model -- 30MB, and fifteen seconds on a cold shader cache -- to run a few
// CREATE TABLEs.
func bootstrapIndex(dbPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	db, err := index.Open(dbPath, index.DriverName)
	if err != nil {
		return err
	}
	// Said rather than done silently: the first run comes up with an empty list
	// and fills in behind the reader, and "no sessions" is alarming without a
	// reason for it. The progress itself reaches the page through /status.
	note("no index at %s; creating one and indexing in the background", dbPath)
	return db.Close()
}

// startIndexer opens a writer and keeps the index current in the background.
//
// Failure here is not fatal. The server's job is to show what is already
// indexed, and a second brightlantern holding the write lock, or a read-only
// filesystem, should cost live updates rather than the whole interface.
func startIndexer(ctx context.Context, dbPath, driver string, dirs []string, titlesVia string) (web.StatusSource, func()) {
	writer, err := index.Open(dbPath, driver)
	if err != nil {
		note("live indexing disabled: %v", err)
		return nil, nil
	}

	// Said before the build rather than after, because the catch-up starts
	// immediately and nothing else reports the cost. On this corpus it is the
	// difference between a 107MB index and a 502MB one, arriving in about
	// fifteen seconds of starting the server.
	if need, err := writer.NeedsArchive(); err == nil && need {
		note("this index predates the message archive; the catch-up build will store " +
			"every message, which grows the database several times over")
	}

	opts := index.BuildOptions{Dirs: dirs}
	if driver == index.SemanticDriverName {
		if e, err := embed.New(writer.SQL(), embed.DefaultPaths()); err != nil {
			note("new sessions will be indexed without embeddings: %v", err)
		} else if err := writer.EnsureVectorTable(e.Dim()); err != nil {
			// An index built by 'brightlantern index' already has this table, which
			// is why nothing missed it -- but serve can now be the first thing
			// to touch a new index, and then there is nowhere to put a vector.
			note("new sessions will be indexed without embeddings: %v", err)
		} else {
			opts.Embedder = e
		}
	}

	// Titles fill in behind the list while it is being browsed, which is the
	// point of them being a separate pass: nothing waits on a network call.
	var titleOpts titles.Options
	if titlesVia != config.TitlesOff {
		if s, err := summarizer(); err != nil {
			note("titles disabled; the list shows opening messages instead: %v", err)
		} else {
			titleOpts.Summarizer = s
			titleOpts.Concurrency = titles.ConcurrencyFor(s)
		}
	}

	ix := indexer.New(writer, opts, titleOpts)
	go func() {
		if err := ix.Run(ctx, true); err != nil && ctx.Err() == nil {
			note("indexer stopped: %v", err)
		}
	}()

	return ix, func() { writer.Close() }
}

// buildEngine assembles the rankers available against this index.
//
// Lexical always works; semantic needs the extension, a model that loads, and
// an index that actually has vectors. An engine with only the lexical ranker
// is a normal outcome rather than a degraded one worth hiding: FTS5 alone
// still finds identifiers, error strings, and filenames, which is a large
// part of what searching your own sessions is for.
func buildEngine(db *index.DB, driver string) *search.Engine {
	rankers := []search.Ranker{&search.Lexical{DB: db.SQL()}}

	if driver == index.SemanticDriverName {
		if e, err := embed.New(db.SQL(), embed.DefaultPaths()); err != nil {
			note("semantic search unavailable: %v", err)
		} else {
			// Attached whether or not the index currently has vectors. The
			// background indexer adds them while the server runs, and gating
			// on the count here would leave a process that started against an
			// empty index searching by keyword until it was restarted. With no
			// vectors the KNN query simply returns nothing and fusion falls
			// back to lexical on its own.
			rankers = append(rankers, &search.Semantic{DB: db.SQL(), Embedder: e})

			if has, err := db.HasVectors(); err == nil && !has {
				note("index has no embeddings yet; results will be keyword-only " +
					"until indexing adds them")
			}
		}
	}

	return &search.Engine{Rankers: rankers, Fusion: search.DefaultFusion()}
}

func open(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "explorer"
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, url).Start()
}
