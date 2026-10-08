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

// Stream serves a segment node's log as server-sent events: the
// segments uploaded so far, then the live ring from where they end,
// until the manifest lands. A node without segments streams through
// fallback, or (nil, nil) so the reader uses the live ring alone.
func (l segmentLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	rec, ok, err := l.record(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		if l.fallback == nil {
			return nil, nil
		}
		return l.fallback.Stream(ctx, runID, nodeID)
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(streamSegments(ctx, l.objs, l.s.liveLogs, rec, pw))
	}()
	return pr, nil
}

// perf: segments land every 10 s, so the stream lists the prefix about
// this often while the live ring serves the lines in between.
const segmentStreamPoll = 5 * time.Second

// safety: offsets count bytes of the whole log, off how far the reader was sent and segEnd how far the segments
// reach; the ring is joined only at off, so no line is sent twice or skipped.
func streamSegments(ctx context.Context, objs logObjectStore, live *liveLogs, rec store.NodeLog, out io.Writer) error {
	if _, err := io.WriteString(out, ": open\n\n"); err != nil {
		return err
	}
	var off, segEnd int64
	read := 0
	for {
		live.Watch(rec.RunID, rec.NodeID)
		log, err := logseg.Open(ctx, objs, rec.Prefix)
		if err != nil {
			return err
		}
		for _, seg := range log.Segments[min(read, len(log.Segments)):] {
			body, err := log.Segment(ctx, objs, seg)
			if err != nil {
				return err
			}
			if skip := off - segEnd; skip < int64(len(body)) {
				if err := writeSSELines(out, body[max(skip, 0):]); err != nil {
					return err
				}
			}
			segEnd += int64(len(body))
			off = max(off, segEnd)
			read++
		}
		if log.Manifest != nil {
			_, err := io.WriteString(out, "event: stream_end\ndata: {}\n\n")
			return err
		}
		if err := tailLiveRing(ctx, live, rec, &off, out); err != nil {
			return err
		}
	}
}

// perf: when the ring cannot continue from off it waits out the poll rather than listing segments in a loop.
func tailLiveRing(ctx context.Context, live *liveLogs, rec store.NodeLog, off *int64, out io.Writer) error {
	deadline := time.Now().Add(segmentStreamPoll)
	for {
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		chunk, ok := live.Wait(waitCtx, rec.RunID, rec.NodeID, *off)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case !time.Now().Before(deadline):
			return nil
		case ok && chunk.Start <= *off && chunk.Next > *off:
			if err := writeSSELines(out, chunk.Data); err != nil {
				return err
			}
			*off = chunk.Next
		default:
			select {
			case <-ctx.Done():
			case <-time.After(time.Until(deadline)):
			}
			return nil
		}
	}
}

func writeSSELines(out io.Writer, data []byte) error {
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		if _, err := fmt.Fprintf(out, "data: %s\n\n", line); err != nil {
			return err
		}
	}
	return nil
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
