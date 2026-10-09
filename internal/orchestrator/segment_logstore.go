package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

// safety: a run on this machine whose state lives on a segment-keeping controller cuts and uploads its nodes'
// segments in this process, because no runner of the controller's supervises them.
type segmentLogStore struct {
	client.NodeLogs
	ctrl   *client.Client
	logger *slog.Logger

	mu     sync.Mutex
	nodes  map[string]*nodeLogSink
	failed map[string]error
	closed bool
}

var (
	_ storage.LogStore = (*segmentLogStore)(nil)
	_ logSealer        = (*segmentLogStore)(nil)
)

// safety: a controller that keeps no node logs, or cannot say, is an error, because a run whose state it holds
// has nowhere else to put them.
func controllerSegmentLogs(ctx context.Context, ctrl *client.Client, logger *slog.Logger) (*segmentLogStore, error) {
	keeps, err := ctrl.KeepsLogSegments(ctx)
	if err != nil {
		return nil, fmt.Errorf("ask controller %s whether it keeps node logs: %w", ctrl.BaseURL(), err)
	}
	if !keeps {
		return nil, fmt.Errorf("controller %s keeps no node logs: it needs a cache bucket (--cache-blob-store) or a state store with an output directory", ctrl.BaseURL())
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &segmentLogStore{
		NodeLogs: ctrl.NodeLogs(), ctrl: ctrl, logger: logger,
		nodes: map[string]*nodeLogSink{}, failed: map[string]error{},
	}, nil
}

// safety: a node whose segment URLs were refused keeps refusing for this
// process, so each line fails fast and counts as dropped rather than
// waiting out another grant request.
func (s *segmentLogStore) sink(ctx context.Context, runID, nodeID string) (*nodeLogSink, error) {
	key := watchKey(runID, nodeID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("the run's log store is closed")
	}
	if n := s.nodes[key]; n != nil {
		return n, nil
	}
	if err := s.failed[key]; err != nil {
		return nil, err
	}
	n, _ := openNodeLogSinkLive(context.WithoutCancel(ctx), s.ctrl, runID, nodeID, true, s.logger)
	if n == nil {
		err := fmt.Errorf("controller %s issued no segment URLs for node %s of run %s", s.ctrl.BaseURL(), nodeID, runID)
		s.failed[key] = err
		return nil, err
	}
	s.nodes[key] = n
	return n, nil
}

func (s *segmentLogStore) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	n, err := s.sink(ctx, runID, nodeID)
	if err != nil {
		return err
	}
	r, _ := appendRangeFrom(ctx)
	return n.sink.Append(data, r.stream, r.first, r.last)
}

// Seal records the writer's seal and writes the node's manifest.
func (s *segmentLogStore) Seal(ctx context.Context, runID, nodeID string, seal logseg.WriterSeal) error {
	n, err := s.sink(ctx, runID, nodeID)
	if err != nil {
		return err
	}
	n.sink.WriterSealed(seal.Stream, seal.FinalSeq, seal.Dropped)
	s.mu.Lock()
	delete(s.nodes, watchKey(runID, nodeID))
	s.mu.Unlock()
	n.close(ctx, false)
	return nil
}

// Close seals every node log whose writer never sealed it, as incomplete.
func (s *segmentLogStore) Close(ctx context.Context) {
	s.mu.Lock()
	nodes := s.nodes
	s.nodes, s.closed = nil, true
	s.mu.Unlock()
	for _, n := range nodes {
		n.close(ctx, false)
	}
}
