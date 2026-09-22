package backend

import (
	"context"
	"errors"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var ErrNotSupported = errors.New("operation not supported by this backend")

type Backend interface {
	Capabilities(ctx context.Context) (Capabilities, error)

	ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
	ListNodes(ctx context.Context, runID string) ([]*store.Node, error)
	ListEventsAfter(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.Event, error)

	ReadNodeLog(ctx context.Context, runID, nodeID string, opts ReadOpts) ([]byte, error)
	StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error)
}

type Capabilities struct {
	Mode     string              `json:"mode"`
	Storage  CapabilitiesStorage `json:"storage"`
	Features []string            `json:"features"`
	ReadOnly bool                `json:"read_only,omitempty"`

	// Teams and Auth come from a controller that serves identity; a local
	// install leaves both nil so its capabilities read as before.
	Teams *CapabilitiesTeams `json:"teams,omitempty"`
	Auth  *CapabilitiesAuth  `json:"auth,omitempty"`
}

type CapabilitiesTeams struct {
	Enabled bool `json:"enabled"`
}

type CapabilitiesAuth struct {
	Providers []string `json:"providers"`
}

type CapabilitiesStorage struct {
	Artifacts string `json:"artifacts"`
	Logs      string `json:"logs"`
	Runs      string `json:"runs"`
}

type ReadOpts struct {
	Tail  int
	Head  int
	Lines string
	Grep  string
}

// RunFilterVersionFor is the run-list capability b can honestly announce. A backend
// that filters runs it fetched itself ignores the cursor, so announcing it would leave
// a caller paging a listing that hands back the same rows forever.
func RunFilterVersionFor(b Backend) string {
	if _, ok := b.(*StoreBackend); ok {
		return store.RunFilterVersion
	}
	return "1"
}
