package backend

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sealReader interface {
	ReadSeals(ctx context.Context, runID, nodeID string) (logs.SealReport, error)
}

type segmentCompleteness interface {
	NodeLogCompleteness(ctx context.Context, runID, nodeID string, node logseg.NodeProgress, now time.Time) (c logseg.Completeness, found bool, err error)
}

type logStoreHolder interface {
	nodeLogStore() storage.LogStore
}

func (b *ClientBackend) nodeLogStore() storage.LogStore { return b.logStore }
func (b *StoreBackend) nodeLogStore() storage.LogStore  { return b.logStore }
func (b *S3Backend) nodeLogStore() storage.LogStore     { return b.logStore }

// NodeLogCompleteness reports whether n's log is whole. A backend whose
// log store keeps no seals answers [logs.StateUnknown].
func NodeLogCompleteness(ctx context.Context, b Backend, runID string, n *store.Node, now time.Time) (logs.Completeness, error) {
	unknown := logs.Completeness{State: logs.StateUnknown}
	holder, ok := b.(logStoreHolder)
	if !ok {
		return unknown, nil
	}
	if segs, ok := holder.nodeLogStore().(segmentCompleteness); ok {
		p := NodeProgress(n, now)
		c, found, err := segs.NodeLogCompleteness(ctx, runID, n.NodeID, logseg.NodeProgress(p), now)
		if err != nil || !found {
			return unknown, err
		}
		return logs.Completeness(c), nil
	}
	reader, ok := holder.nodeLogStore().(sealReader)
	if !ok {
		return unknown, nil
	}
	report, err := reader.ReadSeals(ctx, runID, n.NodeID)
	if err != nil {
		return unknown, err
	}
	return report.Assess(NodeProgress(n, now), now), nil
}

// NodeProgress is what the controller knows about n that a completeness
// verdict needs.
func NodeProgress(n *store.Node, now time.Time) logs.NodeProgress {
	p := logs.NodeProgress{Started: n.StartedAt != nil, Terminal: n.Status == "done"}
	if p.Terminal {
		p.FinishedAt = now
		if n.FinishedAt != nil {
			p.FinishedAt = *n.FinishedAt
		}
	}
	return p
}
