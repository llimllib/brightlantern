package web

import (
	"container/list"
	"os"
	"sync"

	"github.com/llimllib/brightlantern/internal/session"
)

// sessionCache holds recently parsed sessions.
//
// Transcripts are parsed from the .jsonl on demand, which means the right pane
// cannot show stale content for a session that is being written. The cost is
// re-parsing, and without a cache expanding a tool call would re-parse the
// whole file per click -- up to 7MB in this corpus.
//
// Entries are validated against the file's mtime and size, so a session pi is
// actively writing to is re-read rather than served from a stale entry. A
// session rebuilt from the archive is validated against its row count
// instead, which is what changes when a merge brings in a longer copy.
type sessionCache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List               // front = most recently used
	items map[string]*list.Element // key -> element
}

// cacheEntry is one parsed session. key is a path for a file and an id for an
// archived session, which cannot collide: see archiveKey.
type cacheEntry struct {
	key     string
	v1, v2  int64 // mtime and size, or the archive's row count
	session *session.Session
}

// defaultCacheSize is small on purpose. Sessions are read one at a time, the
// working set is whatever the user is looking at, and the tail of the size
// distribution is long: eight entries is at most a few hundred megabytes in
// the worst imaginable case and a few megabytes in practice.
const defaultCacheSize = 8

func newSessionCache(max int) *sessionCache {
	if max <= 0 {
		max = defaultCacheSize
	}
	return &sessionCache{max: max, ll: list.New(), items: map[string]*list.Element{}}
}

// Get returns the parsed session at path, parsing it if necessary.
func (c *sessionCache) Get(path string) (*session.Session, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return c.get(path, fi.ModTime().UnixNano(), fi.Size(), func() (*session.Session, error) {
		return session.Parse(path)
	})
}

// GetArchived returns a session rebuilt from the archive, rebuilding it if
// the archive holds a different number of rows than when it was cached.
func (c *sessionCache) GetArchived(id string, rows int, load func() (*session.Session, error)) (*session.Session, error) {
	return c.get(archiveKey(id), int64(rows), -1, load)
}

// archiveKey keys an archived session. A NUL cannot appear in a path, so it
// can never collide with a file's entry.
func archiveKey(id string) string { return "\x00archive\x00" + id }

func (c *sessionCache) get(key string, v1, v2 int64, load func() (*session.Session, error)) (*session.Session, error) {
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*cacheEntry)
		if e.v1 == v1 && e.v2 == v2 {
			c.ll.MoveToFront(el)
			c.mu.Unlock()
			return e.session, nil
		}
		// Changed: drop it and re-read below.
		c.ll.Remove(el)
		delete(c.items, key)
	}
	c.mu.Unlock()

	// Loaded outside the lock. Two requests for the same uncached session will
	// both parse it, which wastes work once; holding the lock across a 7MB
	// parse would instead block every other request in the server.
	s, err := load()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.Remove(el)
	}
	el := c.ll.PushFront(&cacheEntry{key: key, v1: v1, v2: v2, session: s})
	c.items[key] = el
	for c.ll.Len() > c.max {
		oldest := c.ll.Back()
		c.ll.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry).key)
	}
	return s, nil
}
