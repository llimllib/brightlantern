package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"syscall"
	"time"

	"github.com/llimllib/brightlantern/internal/config"
	"github.com/llimllib/brightlantern/internal/web"
)

// probeTimeout bounds asking an address what is there. The answer comes from
// a process on the same machine, so anything slower than this is either not
// brightlantern or one too wedged to be worth deferring to.
const probeTimeout = 2 * time.Second

// probeInstance asks addr whether brightlantern is serving there.
//
// A nil Instance and nil error means something answered that is not
// brightlantern, or nothing answered at all; the caller cannot tell those
// apart and does not need to.
func probeInstance(addr string) *web.Instance {
	c := http.Client{Timeout: probeTimeout}
	resp, err := c.Get("http://" + dialable(addr) + web.InstancePath)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var in web.Instance
	if err := json.NewDecoder(resp.Body).Decode(&in); err != nil || in.Name != web.InstanceName {
		return nil
	}
	return &in
}

// dialable turns a listen address into one that can be connected to. ":5268"
// and "0.0.0.0:5268" are fine to listen on and mean nothing to dial, and
// the server they describe is reachable on loopback either way.
func dialable(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// listen binds addr, explaining a port that is taken.
//
// It is the first thing serve does, before the index, the model, or the
// writer. With KeepAlive running one brightlantern invisibly, a second is the
// ordinary case rather than a mistake, and it used to open a second writer,
// run a whole catch-up build, and start titling before it found out the port
// was gone. Failing here touches nothing.
func listen(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
		return ln, err
	}
	if in := probeInstance(addr); in != nil {
		return nil, portTakenError(fmt.Sprintf("brightlantern %s is already running on http://%s, serving %s",
			in.Version, dialable(addr), in.Index))
	}
	return nil, portTakenError(fmt.Sprintf("%s is in use by another program; "+
		"set addr in %s or pass --addr", addr, config.Path()))
}

// portTakenError is listen's error for an address in use: a type rather than a
// wrapped sentinel, so the message reads as written instead of ending in
// ": address in use".
type portTakenError string

func (e portTakenError) Error() string { return string(e) }

// waitInterval is how often a waiting serve tries the port again.
var waitInterval = 5 * time.Second

// listenWhenFree is listen for the LaunchAgent, which runs serve --wait.
//
// Under KeepAlive, a serve that exits because someone started brightlantern
// by hand first is restarted by launchd every ten seconds for as long as that
// one runs, logging a failure each time. Waiting instead logs once, holds
// nothing -- the index and the model are opened only after the port is
// bound -- and takes over within seconds of the other one quitting.
func listenWhenFree(ctx context.Context, addr string) (net.Listener, error) {
	waiting := false
	for {
		ln, err := listen(addr)
		if err == nil {
			if waiting {
				note("%s is free; starting", addr)
			}
			return ln, nil
		}
		var taken portTakenError
		if !errors.As(err, &taken) {
			return nil, err
		}
		if !waiting {
			note("%v; waiting for it to come free", err)
			waiting = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(waitInterval):
		}
	}
}

// errDaemonWriting is returned by index when a server is already keeping the
// same database current.
var errDaemonWriting = errors.New("refusing to index while it is running")

// checkNoWriter refuses when a brightlantern at addr is writing dbPath.
//
// A daemon already does everything a plain index does, so the refusal costs
// nothing in the ordinary case. Running both would mostly work -- the busy
// timeout makes them take turns -- but both would run a titles pass over the
// same untitled sessions, and those are paid for. --force is how --full and
// --titles N stay reachable without stopping the daemon.
func checkNoWriter(addr, dbPath string) error {
	in := probeInstance(addr)
	if in == nil || !in.Writing || !samePath(in.Index, dbPath) {
		return nil
	}
	return fmt.Errorf("brightlantern is running on http://%s and keeps %s current; %w "+
		"(pass --force to run anyway)", dialable(addr), in.Index, errDaemonWriting)
}

// samePath reports whether two paths name the same file, allowing for one of
// them being relative or reached through a symlink.
func samePath(a, b string) bool {
	return canonical(a) == canonical(b)
}

// canonical is p made absolute with symlinks resolved -- through its directory
// when the file does not exist yet, which is serve's case on a first run, so
// that /var and /private/var on macOS do not make one index look like two.
func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return filepath.Clean(p)
}
