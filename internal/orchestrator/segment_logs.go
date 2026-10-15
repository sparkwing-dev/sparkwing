package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a slow controller cannot hold the runner past a stopped node's final log seal.
const nodeLogSealTimeout = 10 * time.Second

// safety: the sink runs in the engine-side process, so the pipeline process
// never holds the segment upload URLs.
type nodeLogSink struct {
	runID, nodeID string
	sink          *logseg.Sink
	live          *livePusher
	logger        *slog.Logger

	closeOnce sync.Once
}

func openNodeLogSink(ctx context.Context, data logseg.Data, ctrl *client.Client, logger *slog.Logger) *nodeLogSink {
	up, err := logseg.NewSubjectUploader(data)
	if err != nil {
		logger.WarnContext(ctx, "remote log binding is unavailable", "err", err)
		return nil
	}
	n := &nodeLogSink{runID: data.Subject.RunID, logger: logger}
	opts := logseg.Options{Subject: data.Subject, Logger: logger}
	if attempt := data.Subject.Attempt; attempt != nil {
		n.nodeID = attempt.NodeID
		if ctrl != nil {
			n.live = startLivePusher(context.WithoutCancel(ctx), ctrl, n.runID, n.nodeID, store.LiveLogAttempt(attempt.Attempt, attempt.Generation), logger)
			opts.Live = n.live.push
		}
	}
	sink, err := logseg.NewSink(context.WithoutCancel(ctx), up, opts)
	if err != nil {
		logger.WarnContext(ctx, "log spool could not be opened", "run_id", n.runID, "err", err)
		n.live.close()
		return nil
	}
	n.sink = sink
	if n.live != nil {
		watchedSinks.Store(watchKey(n.runID, n.nodeID), n)
	}
	return n
}

func (n *nodeLogSink) close(ctx context.Context, stopped bool) {
	n.closeOnce.Do(func() {
		watchedSinks.CompareAndDelete(watchKey(n.runID, n.nodeID), n)
		sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeLogSealTimeout)
		defer cancel()
		m, err := n.sink.Close(sealCtx, logseg.End{Stopped: stopped})
		n.live.close()
		switch {
		case errors.Is(err, logseg.ErrSealed):
			n.logger.Warn("node log sealed by the controller before this runner sealed it",
				"run_id", n.runID, "node_id", n.nodeID)
		case err != nil:
			n.logger.Warn("node log manifest not written; the controller seals the attempt as incomplete",
				"run_id", n.runID, "node_id", n.nodeID, "err", err)
		case m.Status != logseg.StatusComplete:
			n.logger.Warn("node log sealed incomplete", "run_id", n.runID, "node_id", n.nodeID,
				"status", m.Status, "reason", m.Reason, "lines", m.Lines, "dropped", m.DroppedLines)
		}
	})
}

// perf: pushes go out every livePushEvery, or sooner at livePushBytes; past livePushHold the oldest bytes drop,
// which a reader sees as a gap the segments fill.
const (
	livePushEvery   = 250 * time.Millisecond
	livePushBytes   = 32 << 10
	livePushHold    = 1 << 20
	livePushTimeout = 10 * time.Second
)

// safety: the live view is a convenience and the segments are the log, so a
// slow or failing controller costs live lines, never a blocked writer.
type livePusher struct {
	ctrl                   *client.Client
	runID, nodeID, attempt string
	logger                 *slog.Logger

	mu     sync.Mutex
	buf    []byte
	off    int64
	kick   chan struct{}
	done   chan struct{}
	failed bool
	closed bool
}

func startLivePusher(ctx context.Context, ctrl *client.Client, runID, nodeID, attempt string, logger *slog.Logger) *livePusher {
	p := &livePusher{
		ctrl: ctrl, runID: runID, nodeID: nodeID, attempt: attempt, logger: logger,
		kick: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go p.loop(ctx)
	return p
}

func (p *livePusher) push(off int64, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if len(p.buf) == 0 || p.off+int64(len(p.buf)) != off {
		p.buf, p.off = p.buf[:0], off
	}
	p.buf = append(p.buf, data...)
	if over := len(p.buf) - livePushHold; over > 0 {
		p.buf, p.off = p.buf[over:], p.off+int64(over)
	}
	if len(p.buf) >= livePushBytes {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

func (p *livePusher) loop(ctx context.Context) {
	defer close(p.done)
	t := time.NewTicker(livePushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			p.flush(ctx)
			return
		case _, open := <-p.kick:
			p.flush(ctx)
			if !open {
				return
			}
		case <-t.C:
			p.flush(ctx)
		}
	}
}

func (p *livePusher) flush(ctx context.Context) {
	p.mu.Lock()
	data, off := p.buf, p.off
	p.buf, p.off = nil, off+int64(len(data))
	p.mu.Unlock()
	if len(data) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), livePushTimeout)
	defer cancel()
	err := p.ctrl.AppendNodeLiveLogAt(ctx, p.runID, p.nodeID, p.attempt, off, data)
	p.mu.Lock()
	first := err != nil && !p.failed
	p.failed = p.failed || err != nil
	p.mu.Unlock()
	if first {
		p.logger.Warn("live log mirror unavailable; the segments are unaffected",
			"run_id", p.runID, "node_id", p.nodeID, "err", err)
	}
}

func (p *livePusher) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.kick)
	}
	p.mu.Unlock()
	<-p.done
}

// safety: one process hosts every sink of the nodes it supervises, so a
// heartbeat reply any of its clients receives can reach the right sink.
var watchedSinks sync.Map

func watchKey(runID, nodeID string) string { return runID + "\x00" + nodeID }

// NoteWatch applies a heartbeat's watch report to the node logs this
// process keeps as segments, turning their live view on or off. Hand it
// to [client.Client.WithWatchObserver] on every client that heartbeats a
// node or run whose log this process keeps.
func NoteWatch(r client.WatchReport) {
	if !r.WholeRun {
		if n, ok := watchedSinks.Load(watchKey(r.RunID, r.NodeID)); ok {
			n.(*nodeLogSink).sink.SetWatched(r.Watched)
		}
		return
	}
	prefix := r.RunID + "\x00"
	watchedSinks.Range(func(key, value any) bool {
		if nodeID, ok := strings.CutPrefix(key.(string), prefix); ok {
			value.(*nodeLogSink).sink.SetWatched(slices.Contains(r.Nodes, nodeID))
		}
		return true
	})
}
