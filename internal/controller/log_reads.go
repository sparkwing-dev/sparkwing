package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type segmentLogRecords interface {
	ReadRunView(context.Context, string) (store.RunView, error)
	ReadLogSegments(context.Context, runstate.Actor, blob.LogReadRequest) (runstate.LogSegments, error)
}

type segmentLogs struct {
	records segmentLogRecords
	actor   runstate.Actor
	live    *liveLogs
	objs    logseg.SegmentObjects
}

var _ storage.LogStore = segmentLogs{}

func (l segmentLogs) subjects(ctx context.Context, runID, nodeID string) ([]blob.LogSubject, error) {
	view, err := l.records.ReadRunView(ctx, runID)
	if err != nil {
		return nil, err
	}
	return jobLogSubjects(view, nodeID), nil
}

func jobLogSubjects(view store.RunView, nodeID string) []blob.LogSubject {
	var subjects []blob.LogSubject
	for _, subject := range view.Logs {
		if subject.Kind == blob.LogJobAttempt && subject.Attempt != nil && subject.Attempt.NodeID == nodeID {
			subjects = append(subjects, subject)
		}
	}
	slices.SortFunc(subjects, func(a, b blob.LogSubject) int {
		if a.Attempt.Attempt != b.Attempt.Attempt {
			return compareLogOrdinal(a.Attempt.Attempt, b.Attempt.Attempt)
		}
		return compareLogOrdinal(a.Attempt.Generation, b.Attempt.Generation)
	})
	return subjects
}

func compareLogOrdinal(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (l segmentLogs) read(ctx context.Context, subject blob.LogSubject, off int64, out io.Writer) (logseg.ReadResult, error) {
	return (logseg.CommittedReader{Records: l.records, Objects: l.objs}).Read(ctx, l.actor, blob.LogReadRequest{Subject: subject, Offset: off}, out)
}

func (l segmentLogs) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	subjects, err := l.subjects(ctx, runID, nodeID)
	if err != nil {
		return nil, err
	}
	if len(subjects) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	result, err := l.read(ctx, subjects[len(subjects)-1], 0, &out)
	if err != nil {
		return nil, err
	}
	body, err := opts.Apply(out.Bytes())
	if err != nil {
		return nil, err
	}
	return append(body, logIncompleteLine(result.IncompleteReason)...), nil
}

func (l segmentLogs) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	view, err := l.records.ReadRunView(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, subject := range view.Logs {
		label := ""
		switch subject.Kind {
		case blob.LogJobAttempt:
			label = subject.Attempt.NodeID
		case blob.LogOwnerUnit:
			label = string(subject.Unit.Kind)
			if subject.Unit.Source != nil {
				label += "/" + subject.Unit.Source.NodeID
			}
		}
		var body bytes.Buffer
		result, err := l.read(ctx, subject, 0, &body)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&out, "=== %s ===\n", label)
		out.Write(body.Bytes())
		out.Write(logIncompleteLine(result.IncompleteReason))
	}
	return out.Bytes(), nil
}

func logIncompleteLine(reason string) []byte {
	if reason == "" {
		return nil
	}
	return []byte("[logs incomplete: " + reason + "]\n")
}

func logNodeSettled(view store.RunView, nodeID string) (settled, found bool) {
	for _, row := range view.Snapshot.Nodes {
		if row.Ref.NodeID == runspec.NodeID(nodeID) {
			return view.Snapshot.Run.FinishedAt != nil || row.Status == runstate.StatusDone && row.Outcome != runstate.NodeFailed, true
		}
	}
	return false, false
}

func (l segmentLogs) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	cur, err := parseLogCursor(storage.LogCursor(ctx), runID, nodeID)
	if err != nil {
		return nil, err
	}
	view, err := l.records.ReadRunView(ctx, runID)
	if err != nil {
		return nil, err
	}
	if _, known := logNodeSettled(view, nodeID); !known {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		defer cancel()
		pw.CloseWithError(l.streamNode(ctx, runID, nodeID, cur, pw))
	}()
	return &logStream{ReadCloser: pr, cancel: cancel}, nil
}

type logStream struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (s *logStream) Close() error { s.cancel(); return s.ReadCloser.Close() }

func (l segmentLogs) streamNode(ctx context.Context, runID, nodeID string, cur logCursor, out io.Writer) error {
	if _, err := io.WriteString(out, ": open\n\n"); err != nil {
		return err
	}
	for {
		subjects, err := l.subjects(ctx, runID, nodeID)
		if err != nil {
			return err
		}
		if cur.attempt < len(subjects) {
			at := cur.attempt
			if err := l.streamSegments(ctx, subjects[at], cur.off, out, func(off int64) string { return (logCursor{attempt: at, off: off}).encode(runID, nodeID) }); err != nil {
				return err
			}
			cur = logCursor{attempt: at + 1}
			if _, err := fmt.Fprintf(out, "id: %s\n\n", cur.encode(runID, nodeID)); err != nil {
				return err
			}
			continue
		}
		view, err := l.records.ReadRunView(ctx, runID)
		if err != nil {
			return err
		}
		settled, known := logNodeSettled(view, nodeID)
		if settled || !known {
			subjects, err = l.subjects(ctx, runID, nodeID)
			if err != nil {
				return err
			}
			if cur.attempt >= len(subjects) {
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

const nodeStreamPoll = time.Second

type logCursor struct {
	attempt int
	off     int64
}

func (c logCursor) encode(runID, nodeID string) string {
	return fmt.Sprintf("%d.%d.%08x", c.attempt, c.off, crc32.ChecksumIEEE([]byte(runID+"\x00"+nodeID)))
}

func parseLogCursor(raw, runID, nodeID string) (logCursor, error) {
	if raw == "" {
		return logCursor{}, nil
	}
	var c logCursor
	var scope uint32
	if n, err := fmt.Sscanf(raw, "%d.%d.%08x", &c.attempt, &c.off, &scope); err != nil || n != 3 || c.attempt < 0 || c.off < 0 || c.encode(runID, nodeID) != raw {
		return logCursor{}, fmt.Errorf("%w: %q", storage.ErrBadCursor, raw)
	}
	return c, nil
}

var controllerSearchBounds = web.SearchBounds{MaxBytes: limits.LogSearchBytes, Timeout: limits.LogSearchTimeout}

const segmentStreamPoll = 5 * time.Second

func (l segmentLogs) streamSegments(ctx context.Context, subject blob.LogSubject, off int64, out io.Writer, id func(int64) string) error {
	for {
		if l.live != nil {
			l.live.Watch(subject.RunID, subject.Attempt.NodeID)
		}
		var body bytes.Buffer
		result, err := l.read(ctx, subject, off, &body)
		if err != nil {
			return err
		}
		if err := writeSSELines(out, body.Bytes(), off, id); err != nil {
			return err
		}
		off += result.Bytes
		if result.Complete || result.IncompleteReason != "" {
			if result.IncompleteReason != "" {
				body, encodeErr := json.Marshal(map[string]string{"reason": result.IncompleteReason})
				if encodeErr != nil {
					return encodeErr
				}
				_, err = fmt.Fprintf(out, "event: log_incomplete\ndata: %s\n\n", body)
			}
			return err
		}
		if err := l.tailLiveRing(ctx, subject, &off, out, id); err != nil {
			return err
		}
	}
}

func (l segmentLogs) tailLiveRing(ctx context.Context, subject blob.LogSubject, off *int64, out io.Writer, id func(int64) string) error {
	deadline := time.Now().Add(segmentStreamPoll)
	for {
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		var chunk liveChunk
		var ok bool
		if l.live != nil {
			chunk, ok = l.live.Wait(waitCtx, subject.RunID, subject.Attempt.NodeID, *off)
		} else {
			<-waitCtx.Done()
		}
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case !time.Now().Before(deadline):
			return nil
		case ok && chunk.Attempt == store.LiveLogAttempt(subject.Attempt.Attempt, subject.Attempt.Generation) && chunk.Start <= *off && chunk.Next > *off:
			if _, err := l.records.ReadLogSegments(ctx, l.actor, blob.LogReadRequest{Subject: subject, Offset: *off}); err != nil {
				return err
			}
			if err := writeSSELines(out, chunk.Data[*off-chunk.Start:], *off, id); err != nil {
				return err
			}
			*off = chunk.Next
		default:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Until(deadline)):
				return nil
			}
		}
	}
}

func writeSSELines(out io.Writer, data []byte, base int64, id func(int64) string) error {
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

// NodeLogCompleteness verifies persisted bytes before reporting the subject's seal.
func (l segmentLogs) NodeLogCompleteness(ctx context.Context, runID, nodeID string) (logseg.Completeness, bool, error) {
	view, err := l.records.ReadRunView(ctx, runID)
	if err != nil {
		return logseg.Completeness{}, false, err
	}
	subjects := jobLogSubjects(view, nodeID)
	if len(subjects) == 0 {
		return logseg.Completeness{}, false, nil
	}
	result, err := l.read(ctx, subjects[len(subjects)-1], 0, io.Discard)
	if err != nil {
		return logseg.Completeness{}, true, err
	}
	verdict := logseg.Completeness{State: logseg.StateStreaming}
	for _, attempt := range view.Attempts {
		if attempt.Identity == *subjects[len(subjects)-1].Attempt && attempt.FinishedAt != nil && !view.Snapshot.StoreNow.Before(attempt.FinishedAt.Add(limits.LogSealGrace)) {
			verdict = logseg.Completeness{State: logseg.StateIncomplete, Message: "logs incomplete: the runner never sealed this log"}
		}
	}
	if result.Complete {
		verdict = logseg.Completeness{State: logseg.StateComplete}
	} else if result.IncompleteReason != "" {
		verdict = logseg.Completeness{State: logseg.StateIncomplete, Message: "logs incomplete: " + result.IncompleteReason}
	}
	return verdict, true, nil
}
