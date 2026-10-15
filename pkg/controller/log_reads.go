package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: scoped to one team, so a read never lists another team's prefix.
type segmentLogs struct {
	s    *Server
	team store.Team
	objs logObjectStore
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
		return nil, nil
	}
	log, err := logseg.Open(ctx, l.objs, rec.Prefix)
	if err != nil {
		return nil, err
	}
	body, err := log.Read(ctx, l.objs, opts)
	if err != nil {
		return nil, err
	}
	return append(body, log.LossLine()...), nil
}

func (l segmentLogs) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	recs, err := l.s.store.RunNodeLogs(ctx, l.team, runID)
	if err != nil {
		return nil, err
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
		out.Write(log.LossLine())
	}
	return out.Bytes(), nil
}

// Stream serves a node's log in this run as server-sent events: each of
// its attempts in order, every attempt's segments and then the live ring
// from where they end until its manifest lands, ending with stream_end
// once the node is done and its last attempt sealed. Each write is
// followed by an event id, a cursor a reader hands back through
// [storage.WithLogCursor] to resume after it. A node the run neither
// records nor holds logs for answers (nil, nil), so the reader uses the
// live ring alone.
func (l segmentLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	cur, err := parseLogCursor(storage.LogCursor(ctx), runID, nodeID)
	if err != nil {
		return nil, err
	}
	recs, err := l.nodeRecords(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	_, known, err := l.s.store.NodeLogSettled(ctx, l.team, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 && !known {
		return nil, nil
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(l.streamNode(ctx, runID, nodeID, cur, pw))
	}()
	return pr, nil
}

// safety: records are keyed by run, so a retry run's node streams its own attempts and never an ancestor run's;
// ordered by attempt and then claim generation, they stand in the order the attempts began, so a cursor's index
// keeps naming the same attempt as later ones are added.
func (l segmentLogs) nodeRecords(ctx context.Context, runID, nodeID string) ([]store.NodeLog, error) {
	all, err := l.s.store.RunNodeLogs(ctx, l.team, runID)
	if err != nil {
		return nil, err
	}
	var recs []store.NodeLog
	for _, rec := range all {
		if rec.NodeID == nodeID {
			recs = append(recs, rec)
		}
	}
	return recs, nil
}

func (l segmentLogs) streamNode(ctx context.Context, runID, nodeID string, cur logCursor, out io.Writer) error {
	if _, err := io.WriteString(out, ": open\n\n"); err != nil {
		return err
	}
	mark := func(c logCursor) error {
		_, err := fmt.Fprintf(out, "id: %s\n\n", c.encode(runID, nodeID))
		return err
	}
	for {
		recs, err := l.nodeRecords(ctx, runID, nodeID)
		if err != nil {
			return err
		}
		if cur.attempt < len(recs) {
			at := cur.attempt
			if err := streamSegments(ctx, l.objs, l.s.liveLogs, recs[at], cur.off, out,
				func(off int64) string { return logCursor{attempt: at, off: off}.encode(runID, nodeID) }); err != nil {
				return err
			}
			cur = logCursor{attempt: at + 1}
			if err := mark(cur); err != nil {
				return err
			}
			continue
		}
		settled, known, err := l.s.store.NodeLogSettled(ctx, l.team, runID, nodeID)
		if err != nil {
			return err
		}
		if settled || !known {
			if recs, err = l.nodeRecords(ctx, runID, nodeID); err != nil {
				return err
			}
			if cur.attempt >= len(recs) {
				_, err := io.WriteString(out, "event: stream_end\ndata: {}\n\n")
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodeStreamPoll):
		}
	}
}

// perf: between a node's attempts the stream reads the run's log records this often for the next one.
const nodeStreamPoll = time.Second

// safety: a place in a node's stream is the index of an attempt among the node's attempts in this run and a byte
// offset in that attempt's log, both of which only grow, so a cursor names the same place for as long as the run lasts.
type logCursor struct {
	attempt int
	off     int64
}

// safety: the cursor names an attempt by its place in this run's node rather than by claim generation, which
// never leaves the controller, and carries a digest of the run and node so it resumes no other stream.
func (c logCursor) encode(runID, nodeID string) string {
	return fmt.Sprintf("%d.%d.%08x", c.attempt, c.off, crc32.ChecksumIEEE([]byte(runID+"\x00"+nodeID)))
}

func parseLogCursor(raw, runID, nodeID string) (logCursor, error) {
	if raw == "" {
		return logCursor{}, nil
	}
	var c logCursor
	var scope uint32
	if n, err := fmt.Sscanf(raw, "%d.%d.%08x", &c.attempt, &c.off, &scope); err != nil || n != 3 ||
		c.attempt < 0 || c.off < 0 || c.encode(runID, nodeID) != raw {
		return logCursor{}, fmt.Errorf("%w: %q", storage.ErrBadCursor, raw)
	}
	return c, nil
}

// safety: one search reads at most this much log and runs at most this long, so a broad grep cannot hold the
// controller's memory or a request slot.
var controllerSearchBounds = web.SearchBounds{MaxBytes: 256 << 20, Timeout: 10 * time.Second}

// perf: segments land every 10 s, so the stream lists the prefix about
// this often while the live ring serves the lines in between.
const segmentStreamPoll = 5 * time.Second

// safety: offsets count bytes of the whole log, off how far the reader was sent and segEnd how far the segments
// reach; the ring is joined only at off, so no line is sent twice or skipped. It returns once the manifest landed.
func streamSegments(ctx context.Context, objs logObjectStore, live *liveLogs, rec store.NodeLog, off int64, out io.Writer,
	id func(off int64) string,
) error {
	var segEnd int64
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
			if skip := max(off-segEnd, 0); skip < int64(len(body)) {
				if err := writeSSELines(out, body[skip:], segEnd+skip, id); err != nil {
					return err
				}
			}
			segEnd += int64(len(body))
			off = max(off, segEnd)
			read++
		}
		if log.Manifest != nil {
			return nil
		}
		if err := tailLiveRing(ctx, live, rec, &off, out, id); err != nil {
			return err
		}
	}
}

// perf: when the ring cannot continue from off it waits out the poll rather than listing segments in a loop.
func tailLiveRing(ctx context.Context, live *liveLogs, rec store.NodeLog, off *int64, out io.Writer, id func(int64) string) error {
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
		case ok && chunk.Attempt == store.LiveLogAttempt(rec.Attempt, rec.Generation) && chunk.Start <= *off && chunk.Next > *off:
			if err := writeSSELines(out, chunk.Data[*off-chunk.Start:], *off, id); err != nil {
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

// safety: every line carries the cursor just past it as its event id, so a reader that loses the connection after
// any line resumes after that line and is sent none twice.
func writeSSELines(out io.Writer, data []byte, base int64, id func(off int64) string) error {
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte("\n"))
		base += int64(len(data) - len(rest))
		data = rest
		if _, err := fmt.Fprintf(out, "id: %s\ndata: %s\n\n", id(base), line); err != nil {
			return err
		}
	}
	return nil
}

func (l segmentLogs) DeleteRun(ctx context.Context, runID string) error {
	_, err := l.s.deleteRunSegments(ctx, l.objs, l.team, runID)
	return err
}

// NodeLogCompleteness reports the segment verdict for a node with a
// segment record; false means the node has none. A finished node whose
// writer never sealed its attempt past the grace is sealed here.
func (l segmentLogs) NodeLogCompleteness(ctx context.Context, runID, nodeID string, node logseg.NodeProgress, now time.Time) (logseg.Completeness, bool, error) {
	rec, ok, err := l.record(ctx, runID, nodeID)
	if err != nil {
		return logseg.Completeness{}, false, err
	}
	if !ok {
		return logseg.Completeness{}, false, nil
	}
	log, err := logseg.Open(ctx, l.objs, rec.Prefix)
	if err != nil {
		return logseg.Completeness{}, true, err
	}
	if err := log.Verify(ctx, l.objs); err != nil {
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
// It returns the bytes it removed.
func (s *Server) deleteRunSegments(ctx context.Context, objs logObjectStore, team store.Team, runID string) (int64, error) {
	recs, err := s.store.RunNodeLogs(ctx, team, runID)
	if err != nil {
		return 0, err
	}
	if objs == nil {
		if len(recs) > 0 {
			return 0, errLogsUnavailable
		}
		return 0, nil
	}
	var deleted int64
	for _, rec := range recs {
		n, err := objs.deletePrefix(ctx, rec.Prefix)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	for _, class := range s.logClasses() {
		prefix, err := logseg.RunPrefix(class, string(team), runID)
		if err != nil {
			return deleted, err
		}
		n, err := objs.deletePrefix(ctx, prefix)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, s.store.DeleteRunNodeLogs(ctx, team, runID)
}
