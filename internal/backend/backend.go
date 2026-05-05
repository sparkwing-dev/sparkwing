// Package backend is the dashboard server's data abstraction. One
// interface, three impls (StoreBackend / ClientBackend / S3Backend),
// each owning its own log discovery so the controller is never in the
// log bandwidth path.
package backend

import (
	"context"
	"errors"
	"io"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// ErrNotSupported is returned by Backend methods that aren't
// implementable in the current backend. Capabilities() advertises
// support proactively; this sentinel is the defensive fallback.
var ErrNotSupported = errors.New("operation not supported by this backend")

// Backend is the dashboard server's read-only data layer over runs,
// nodes, events, and logs.
type Backend interface {
	// Capabilities advertises what this backend supports given current
	// configuration.
	Capabilities(ctx context.Context) (Capabilities, error)

	ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
	ListNodes(ctx context.Context, runID string) ([]*store.Node, error)
	ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error)

	ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error)
	StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error)
}

// Capabilities is the JSON shape served at /api/v1/capabilities and
// consumed by the SPA.
type Capabilities struct {
	Mode     string              `json:"mode"`
	Storage  CapabilitiesStorage `json:"storage"`
	Features []string            `json:"features"`
	ReadOnly bool                `json:"read_only,omitempty"`
}

// CapabilitiesStorage names the configured backends with short tags
// ("fs", "s3", "sqlite", "controller", "custom", ...) so the frontend
// can adapt without parsing URLs.
type CapabilitiesStorage struct {
	Artifacts string `json:"artifacts"`
	Logs      string `json:"logs"`
	Runs      string `json:"runs"`
}

// ReadOpts mirrors storage.ReadOpts so dashboard handlers can pass
// filter knobs without importing pkg/storage.
type ReadOpts struct {
	Tail  int    // last N lines; 0 disables
	Head  int    // first N lines; 0 disables
	Lines string // "A:B" inclusive 1-indexed range
	Grep  string // substring filter (case-sensitive)
}
