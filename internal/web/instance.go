package web

import (
	"encoding/json"
	"net/http"
)

// InstancePath is where a running server says what it is.
const InstancePath = "/api/instance"

// Instance is what InstancePath answers with.
//
// It exists for other processes, not for the page. A second serve finding the
// port taken needs to tell "brightlantern is already running" from "some other
// program has this port", and index needs to know whether the server is
// writing the same database it is about to write -- a daemon over the default
// index is no reason to refuse `index --db /tmp/x.db`. The app in M14 asks the
// same question to decide whether the daemon is up.
type Instance struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Index is the absolute path of the database being served.
	Index string `json:"index"`
	// Writing is whether this server keeps the index current. A --no-watch
	// server only reads, and is no reason for index to refuse.
	Writing bool `json:"writing"`
}

// InstanceName is Instance.Name, which is how a client recognises the answer
// as ours rather than any JSON some other program on the port returns.
const InstanceName = "brightlantern"

func (s *Server) handleInstance(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Instance{
		Name:    InstanceName,
		Version: s.opts.Version,
		Index:   s.opts.IndexPath,
		Writing: s.indexer != nil,
	})
}
