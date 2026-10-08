package orchestrator

import (
	"context"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type (
	processNodeKey struct{}
	processNode    struct{ runID, nodeID string }
	sampleLossKey  struct{}
)

func withProcessNode(ctx context.Context, runID, nodeID string) context.Context {
	return context.WithValue(ctx, processNodeKey{}, processNode{runID, nodeID})
}

func ownsProcessNode(ctx context.Context, runID, nodeID string) bool {
	owner, ok := ctx.Value(processNodeKey{}).(processNode)
	return ok && owner.runID == runID && owner.nodeID == nodeID
}

// safety: sparse losses preserve useful capacity measurements without hiding sustained telemetry outages.
const maxSampleLossPercent = 1

type sampleLoss struct {
	mu              sync.Mutex
	attempted, lost int64
	first           error
	mark            bool
}

func (loss *sampleLoss) record(err error, marker bool) {
	if !marker && (errors.Is(err, nodemetrics.ErrSinkFull) || errors.Is(err, store.ErrNodeMetricLimit)) {
		return
	}
	loss.mu.Lock()
	defer loss.mu.Unlock()
	if !marker {
		loss.attempted++
	}
	if err == nil {
		return
	}
	if !marker {
		loss.lost++
	}
	if loss.first == nil {
		loss.first = err
	}
	loss.mark = loss.mark || marker
}

func recordSampleLoss(ctx context.Context, err error) {
	if loss, ok := ctx.Value(sampleLossKey{}).(*sampleLoss); ok {
		loss.record(err, true)
	}
}
