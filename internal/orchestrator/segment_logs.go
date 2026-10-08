package orchestrator

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the sink runs in the engine-side process, so a pipeline binary built against any SDK keeps posting
// the append protocol it already speaks, over loopback, and never learns about segments.
type nodeLogSink struct {
	runID, nodeID string
	sink          *logseg.Sink
	handler       http.Handler
	live          *livePusher
	stopFresh     context.CancelFunc
	freshDone     chan struct{}
	logger        *slog.Logger

	closeOnce sync.Once
}

// safety: nil keeps the node's log on local disk as before, for a controller that keeps no segments or cannot be
// asked; ctx carries the claim fence every upload request repeats.
func openNodeLogSink(ctx context.Context, ctrl *client.Client, runID, nodeID string, logger *slog.Logger) *nodeLogSink {
	n, _ := openNodeLogSinkLive(ctx, ctrl, runID, nodeID, true, logger)
	return n
}

// safety: unavailable reports a controller that keeps no segments at all, which no retry changes, as against a
// request that failed; a pseudo node with no node row takes no live view, which the controller would refuse.
func openNodeLogSinkLive(ctx context.Context, ctrl *client.Client, runID, nodeID string, live bool, logger *slog.Logger) (n *nodeLogSink, unavailable bool) {
	up := ctrl.NewLogSegmentUploader(ctx, runID, nodeID)
	openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := up.Open(openCtx); err != nil {
		level := slog.LevelWarn
		if errors.Is(err, client.ErrLogSegmentsUnavailable) {
			level = slog.LevelDebug
		}
		logger.Log(ctx, level, "node log stays on this runner's disk: the controller issued no segment URLs",
			"run_id", runID, "node_id", nodeID, "err", err)
		return nil, errors.Is(err, client.ErrLogSegmentsUnavailable)
	}
	nodeCtx := context.WithoutCancel(ctx)
	n = &nodeLogSink{runID: runID, nodeID: nodeID, logger: logger, freshDone: make(chan struct{})}
	opts := logseg.Options{Run: runID, Node: nodeID, Logger: logger}
	if live {
		n.live = startLivePusher(nodeCtx, ctrl, runID, nodeID, logger)
		opts.Live = n.live.push
	}
	sink, err := logseg.NewSink(nodeCtx, up, opts)
	if err != nil {
		logger.Warn("node log stays on this runner's disk: the log spool could not be opened",
			"run_id", runID, "node_id", nodeID, "err", err)
		n.live.close()
		return nil, false
	}
	n.sink = sink
	n.handler = logseg.AppendHandler(sink, runID, nodeID)
	if live {
		up.OnWatched(sink.SetWatched)
		watchedSinks.Store(watchKey(runID, nodeID), n)
	}
	freshCtx, stop := context.WithCancel(nodeCtx)
	n.stopFresh = stop
	go func() {
		defer close(n.freshDone)
		up.KeepFresh(freshCtx, func(err error) {
			logger.Warn("renew the node's log segment URLs", "run_id", runID, "node_id", nodeID, "err", err)
		})
	}()
	return n, false
}

func (n *nodeLogSink) ServeHTTP(w http.ResponseWriter, r *http.Request) { n.handler.ServeHTTP(w, r) }

func (n *nodeLogSink) close(ctx context.Context, stopped bool) {
	n.closeOnce.Do(func() {
		watchedSinks.CompareAndDelete(watchKey(n.runID, n.nodeID), n)
		sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), childLogSealBudget)
		defer cancel()
		m, err := n.sink.Close(sealCtx, logseg.End{Stopped: stopped})
		n.live.close()
		n.stopFresh()
		<-n.freshDone
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

// safety: only the child's own credential is accepted, so another process on the host cannot write the node's log.
func serveLoopbackLogs(sink *nodeLogSink, bearer string, logger *slog.Logger) (string, func(context.Context), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if len(got) != len(bearer) || subtle.ConstantTimeCompare([]byte(got), []byte(bearer)) != 1 {
				http.Error(w, "the node's own credential is required", http.StatusUnauthorized)
				return
			}
			sink.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("loopback log listener stopped", "err", err)
		}
	}()
	stop := func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Warn("stop the loopback log listener", "err", err)
		}
	}
	return "http://" + listener.Addr().String(), stop, nil
}

// perf: pushes go out every livePushEvery, or sooner at livePushBytes; past livePushHold the oldest bytes drop,
// which a reader sees as a gap the segments fill.
const (
	livePushEvery = 250 * time.Millisecond
	livePushBytes = 32 << 10
	livePushHold  = 1 << 20
)

// safety: the live view is a convenience and the segments are the log, so a
// slow or failing controller costs live lines, never a blocked writer.
type livePusher struct {
	ctrl          *client.Client
	runID, nodeID string
	logger        *slog.Logger

	mu     sync.Mutex
	buf    []byte
	off    int64
	kick   chan struct{}
	done   chan struct{}
	failed bool
	closed bool
}

func startLivePusher(ctx context.Context, ctrl *client.Client, runID, nodeID string, logger *slog.Logger) *livePusher {
	p := &livePusher{
		ctrl: ctrl, runID: runID, nodeID: nodeID, logger: logger,
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
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveLogPostTimeout)
	defer cancel()
	err := p.ctrl.AppendNodeLiveLogAt(ctx, p.runID, p.nodeID, off, data)
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

// safety: a node keeps one sink across every stream that writes it until the broker closes them all, because a
// node writes several streams in turn and a second sink would number its segments over the first's.
type nodeLogSinks struct {
	ctrl   *client.Client
	runID  string
	logger *slog.Logger

	mu          sync.Mutex
	nodes       map[string]*nodeLogSink
	unavailable bool
}

func newNodeLogSinks(ctrl *client.Client, runID string, logger *slog.Logger) *nodeLogSinks {
	return &nodeLogSinks{ctrl: ctrl, runID: runID, logger: logger, nodes: map[string]*nodeLogSink{}}
}

func (s *nodeLogSinks) forRequest(ctx context.Context, r *http.Request) *nodeLogSink {
	nodeID, ok := appendNode(r, s.runID)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable || s.nodes == nil {
		return nil
	}
	if n := s.nodes[nodeID]; n != nil {
		return n
	}
	n, unavailable := openNodeLogSinkLive(requestFence(ctx, r), s.ctrl, s.runID, nodeID, true, s.logger)
	s.unavailable = unavailable
	if n != nil {
		s.nodes[nodeID] = n
	}
	return n
}

func (s *nodeLogSinks) closeAll(ctx context.Context, stopped bool) {
	s.mu.Lock()
	nodes := s.nodes
	s.nodes = nil
	s.mu.Unlock()
	for _, n := range nodes {
		n.close(ctx, stopped)
	}
}

func appendNode(r *http.Request, runID string) (string, bool) {
	if r.Method != http.MethodPost {
		return "", false
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v1/logs/"+url.PathEscape(runID)+"/")
	if !ok {
		return "", false
	}
	rest = strings.TrimSuffix(rest, "/seal")
	nodeID, err := url.PathUnescape(rest)
	if err != nil || nodeID == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return nodeID, true
}

// safety: the sink's uploads repeat the fence the writer's own append
// carried, so the controller refuses them once that claim is gone.
func requestFence(ctx context.Context, r *http.Request) context.Context {
	ctx = context.WithoutCancel(ctx)
	if gen, err := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64); err == nil && gen > 0 {
		ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: gen})
	}
	if gen, err := strconv.ParseInt(r.Header.Get(store.ClaimGenerationHeader), 10, 64); err == nil && gen > 0 {
		ctx = store.WithNodeClaimFence(ctx, store.NodeClaimFence{
			HolderID: r.Header.Get(store.ClaimHolderHeader), MembershipID: r.Header.Get(store.ClaimMembershipHeader),
			ReservationID: r.Header.Get(store.ClaimReservationHeader), ClaimGeneration: gen,
		})
	}
	return ctx
}

// EngineNodeLog writes the engine's own lines into one node of a run,
// such as a trigger's compile output: as segments when the controller
// keeps them, else to the logs service at logsURL, else nowhere. Close
// seals it.
type EngineNodeLog struct {
	controllerURL, logsURL, token, runID, nodeID string
	logger                                       *slog.Logger

	mu     sync.Mutex
	opened bool
	sink   *nodeLogSink
}

// NewEngineNodeLog returns a log for nodeID of runID that opens on the
// first Append, under the claim fence that Append's context carries.
func NewEngineNodeLog(controllerURL, logsURL, token, runID, nodeID string, logger *slog.Logger) *EngineNodeLog {
	return &EngineNodeLog{
		controllerURL: controllerURL, logsURL: logsURL, token: token,
		runID: runID, nodeID: nodeID, logger: logger,
	}
}

// Append writes data, one or more lines, to the node's log. It outlives
// ctx's cancellation, so a line about a failure still lands.
func (l *EngineNodeLog) Append(ctx context.Context, data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.opened {
		l.opened = true
		if l.controllerURL != "" {
			l.sink, _ = openNodeLogSinkLive(ctx, client.NewWithToken(l.controllerURL, nil, l.token),
				l.runID, l.nodeID, false, l.logger)
		}
	}
	if l.sink != nil {
		return l.sink.sink.Append(data, "", 0, 0)
	}
	if l.logsURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return logs.NewClientWithToken(l.logsURL, nil, l.token).
		WithRunnerIdentity(logs.ProcessIdentity("trigger-loop")).
		Append(ctx, l.runID, l.nodeID, data)
}

// Close seals the log when it went to segments.
func (l *EngineNodeLog) Close(ctx context.Context) {
	l.mu.Lock()
	sink := l.sink
	l.mu.Unlock()
	if sink != nil {
		sink.close(ctx, false)
	}
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
