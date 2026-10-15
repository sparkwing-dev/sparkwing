package client

import (
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"net/http"
)

// WatchReport is what a heartbeat reply says about viewers. A node
// report names NodeID and whether a viewer watches it; a run report sets
// WholeRun and lists in Nodes every node of the run a viewer watches.
type WatchReport struct {
	RunID    string
	NodeID   string
	Watched  bool
	WholeRun bool
	Nodes    []string
}

// WithWatchObserver has this client hand fn every watch report a node
// claim or run heartbeat reply carries, so the process that writes a
// node's log knows when to push its lines to the live view. Set it
// before the client serves requests.
func (c *Client) WithWatchObserver(fn func(WatchReport)) *Client {
	c.watchObserver = fn
	return c
}

func (c *Client) askWatched(req *http.Request) {
	if c.watchObserver != nil {
		req.Header.Set(store.LogWatchHeader, "1")
	}
}

func (c *Client) reportWatch(r WatchReport) {
	if c.watchObserver != nil {
		c.watchObserver(r)
	}
}
