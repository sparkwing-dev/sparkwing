package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a node with no segment record reads through fallback, the logs service, so logs written before
// segments, or by a runner with a logs URL, stay readable.
type segmentLogs struct {
	s        *Server
	team     store.Team
	objs     logObjectStore
	fallback storage.LogStore
}

var _ storage.LogStore = segmentLogs{}

func (l segmentLogs) Append(context.Context, string, string, []byte) error {
	return fmt.Errorf("%w: node logs reach the controller as segments", storage.ErrNotSupported)
}

func (l segmentLogs) record(ctx context.Context, runID, nodeID string) (store.NodeLog, bool, error) {
	rec, err := l.s.store.LatestNodeLog(ctx, l.team, runID, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return store.NodeLog{}, false, nil
	}
	return rec, err == nil, err
}

func (l segmentLogs) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	rec, ok, err := l.record(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		if l.fallback != nil {
			return l.fallback.Read(ctx, runID, nodeID, opts)
		}
		return nil, nil
	}
	log, err := logseg.Open(ctx, l.objs, rec.Prefix)
	if err != nil {
		return nil, err
	}
	return log.Read(ctx, l.objs, opts)
}

func (l segmentLogs) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	recs, err := l.s.store.RunNodeLogs(ctx, l.team, runID)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 && l.fallback != nil {
		return l.fallback.ReadRun(ctx, runID)
	}
	latest := map[string]store.NodeLog{}
	var order []string
	for _, rec := range recs {
		if _, seen := latest[rec.NodeID]; !seen {
			order = append(order, rec.NodeID)
		}
		latest[rec.NodeID] = rec
	}
	var out bytes.Buffer
	for _, nodeID := range order {
		log, err := logseg.Open(ctx, l.objs, latest[nodeID].Prefix)
		if err != nil {
			return nil, err
		}
		body, err := log.Read(ctx, l.objs, storage.ReadOpts{})
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "=== %s ===\n", nodeID)
		out.Write(body)
	}
	return out.Bytes(), nil
}

// Stream has no live read of its own; a reader falls back to the
// controller's live ring.
func (l segmentLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	_, ok, err := l.record(ctx, runID, nodeID)
	if err != nil || ok || l.fallback == nil {
		return nil, err
	}
	return l.fallback.Stream(ctx, runID, nodeID)
}

func (l segmentLogs) DeleteRun(ctx context.Context, runID string) error {
	return l.s.deleteRunSegments(ctx, l.objs, l.team, runID)
}

// NodeLogCompleteness reports the segment verdict for a node with a
// segment record, and the logs service's verdict for one without when a
// logs service is configured; false means neither knows. A finished node
// whose writer never sealed its attempt past the grace is sealed here.
func (l segmentLogs) NodeLogCompleteness(ctx context.Context, runID, nodeID string, node logseg.NodeProgress, now time.Time) (logseg.Completeness, bool, error) {
	rec, ok, err := l.record(ctx, runID, nodeID)
	if err != nil {
		return logseg.Completeness{}, false, err
	}
	if !ok {
		seals, isSealer := l.fallback.(interface {
			ReadSeals(ctx context.Context, runID, nodeID string) (logs.SealReport, error)
		})
		if !isSealer {
			return logseg.Completeness{}, false, nil
		}
		report, err := seals.ReadSeals(ctx, runID, nodeID)
		if err != nil {
			return logseg.Completeness{}, false, err
		}
		return logseg.Completeness(report.Assess(logs.NodeProgress(node), now)), true, nil
	}
	log, err := logseg.Open(ctx, l.objs, rec.Prefix)
	if err != nil {
		return logseg.Completeness{}, true, err
	}
	if log.Manifest == nil && node.Terminal && now.Sub(node.FinishedAt) >= logseg.SealGrace {
		if log.Manifest, err = l.s.sealNodeLog(ctx, l.objs, rec, now); err != nil {
			return logseg.Completeness{}, true, err
		}
	}
	return log.Completeness(node, now), true, nil
}

// safety: the records are the only index of a run's segments, so they go only after every object did.
func (s *Server) deleteRunSegments(ctx context.Context, objs logObjectStore, team store.Team, runID string) error {
	recs, err := s.store.RunNodeLogs(ctx, team, runID)
	if err != nil || len(recs) == 0 {
		return err
	}
	if objs == nil {
		return errLogsUnavailable
	}
	for _, rec := range recs {
		if err := objs.deletePrefix(ctx, rec.Prefix); err != nil {
			return err
		}
	}
	return s.store.DeleteRunNodeLogs(ctx, team, runID)
}
