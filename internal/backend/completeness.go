package backend

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type segmentCompleteness interface {
	NodeLogCompleteness(ctx context.Context, runID, nodeID string, node logseg.NodeProgress, now time.Time) (c logseg.Completeness, found bool, err error)
}

type logStoreHolder interface {
	nodeLogStore() storage.LogStore
}

func (b *StoreBackend) nodeLogStore() storage.LogStore { return b.logStore }

// NodeLogCompleteness reports whether n's log is whole. A backend whose
// log store keeps no manifests answers [logseg.StateUnknown].
func NodeLogCompleteness(ctx context.Context, b Backend, runID string, n *store.Node, now time.Time) (logseg.Completeness, error) {
	unknown := logseg.Completeness{State: logseg.StateUnknown}
	holder, ok := b.(logStoreHolder)
	if !ok {
		return unknown, nil
	}
	segs, ok := holder.nodeLogStore().(segmentCompleteness)
	if !ok {
		return unknown, nil
	}
	c, found, err := segs.NodeLogCompleteness(ctx, runID, n.NodeID, NodeProgress(n, now), now)
	if err != nil || !found {
		return unknown, err
	}
	return c, nil
}

// NodeProgress is what the controller knows about n that a completeness
// verdict needs.
func NodeProgress(n *store.Node, now time.Time) logseg.NodeProgress {
	p := logseg.NodeProgress{Started: n.StartedAt != nil, Terminal: n.Status == "done"}
	if p.Terminal {
		p.FinishedAt = now
		if n.FinishedAt != nil {
			p.FinishedAt = *n.FinishedAt
		}
	}
	return p
}
