package backend

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

type segmentCompleteness interface {
	NodeLogCompleteness(ctx context.Context, runID, nodeID string) (c logseg.Completeness, found bool, err error)
}

type logStoreHolder interface {
	nodeLogStore() storage.LogStore
}

func (b *ClientBackend) nodeLogStore() storage.LogStore { return b.logStore }
func (b *StoreBackend) nodeLogStore() storage.LogStore  { return b.logStore }

// NodeLogCompleteness reports the controller's verdict or StateUnknown when unavailable.
func NodeLogCompleteness(ctx context.Context, b Backend, runID, nodeID string) (logseg.Completeness, error) {
	unknown := logseg.Completeness{State: logseg.StateUnknown}
	holder, ok := b.(logStoreHolder)
	if !ok {
		return unknown, nil
	}
	segs, ok := holder.nodeLogStore().(segmentCompleteness)
	if !ok {
		return unknown, nil
	}
	c, found, err := segs.NodeLogCompleteness(ctx, runID, nodeID)
	if err != nil || !found {
		return unknown, err
	}
	return c, nil
}
