package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llimllib/brightlantern/internal/web"
)

// fakeInstance serves web.InstancePath the way a running brightlantern does.
func fakeInstance(t *testing.T, in web.Instance) string {
	t.Helper()
	in.Name = web.InstanceName
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != web.InstancePath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(in)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// The point of listening first is that a second serve fails having touched
// nothing. "Returned an error" is not the test: it always did, eventually,
// after a catch-up build. The index not existing afterwards is.
func TestServeOnATakenPortTouchesNothing(t *testing.T) {
	tests := []struct {
		name string
		addr func(t *testing.T) string
		want string
	}{
		{
			name: "brightlantern",
			addr: func(t *testing.T) string {
				return fakeInstance(t, web.Instance{Version: "v9", Index: "/elsewhere/index.db"})
			},
			want: "brightlantern v9 is already running",
		},
		{
			name: "another program",
			addr: func(t *testing.T) string {
				srv := httptest.NewServer(http.NotFoundHandler())
				t.Cleanup(srv.Close)
				return srv.Listener.Addr().String()
			},
			want: "in use by another program",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "index.db")
			err := runServe(dbPath, tc.addr(t), nil, false, false, false, "off")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runServe() = %v; want an error containing %q", err, tc.want)
			}
			if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("index exists after a refused serve (stat: %v)", err)
			}
		})
	}
}

func TestCheckNoWriter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	if err := os.WriteFile(dbPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Reached through a symlink, as a path given from another directory or
	// through a linked home might be.
	link := filepath.Join(t.TempDir(), "link.db")
	if err := os.Symlink(dbPath, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      web.Instance
		refuses bool
	}{
		{"writing this index", web.Instance{Index: dbPath, Writing: true}, true},
		{"writing it through a symlink", web.Instance{Index: link, Writing: true}, true},
		// A daemon over the default index is no reason to refuse --db elsewhere.
		{"writing another index", web.Instance{Index: filepath.Join(dir, "other.db"), Writing: true}, false},
		// --no-watch: reads only, so nothing to collide with.
		{"only reading this index", web.Instance{Index: dbPath}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNoWriter(fakeInstance(t, tc.in), dbPath)
			if got := errors.Is(err, errDaemonWriting); got != tc.refuses {
				t.Errorf("checkNoWriter() = %v; refuses = %v, want %v", err, got, tc.refuses)
			}
		})
	}

	t.Run("nothing listening", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := srv.Listener.Addr().String()
		srv.Close()
		if err := checkNoWriter(addr, dbPath); err != nil {
			t.Errorf("checkNoWriter() = %v; want nil with nothing there", err)
		}
	})
}

func TestDialable(t *testing.T) {
	for in, want := range map[string]string{
		":5268":          "127.0.0.1:5268",
		"0.0.0.0:5268":   "127.0.0.1:5268",
		"[::]:5268":      "127.0.0.1:5268",
		"127.0.0.1:5268": "127.0.0.1:5268",
		"localhost:5268": "localhost:5268",
	} {
		if got := dialable(in); got != want {
			t.Errorf("dialable(%q) = %q, want %q", in, got, want)
		}
	}
}
