package orchestrator

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

// safety: the sink runs in the engine-side process, so a pipeline binary built against any SDK keeps posting
// the append protocol it already speaks, over loopback, and never learns about segments.
type nodeLogSink struct {
	runID, nodeID string
	sink          *logseg.Sink
	handler       http.Handler
	live          *logbatch.Store
	stopFresh     context.CancelFunc
	freshDone     chan struct{}
	logger        *slog.Logger

	closeOnce sync.Once
}

// safety: nil keeps the node's log on local disk as before, for a controller that keeps no segments or cannot be
// asked; ctx carries the claim fence every upload request repeats.
func openNodeLogSink(ctx context.Context, ctrl *client.Client, runID, nodeID string, logger *slog.Logger) *nodeLogSink {
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
		return nil
	}
	nodeCtx := context.WithoutCancel(ctx)
	n := &nodeLogSink{runID: runID, nodeID: nodeID, logger: logger, freshDone: make(chan struct{})}
	n.live = logbatch.New(
		&liveLogStore{sink: ctrl, ctx: nodeCtx},
		logbatch.WithFlushInterval(liveLogFlushInterval),
		logbatch.WithBufferThreshold(liveLogFlushBytes),
		logbatch.WithMaxObjects(math.MaxInt32),
		logbatch.WithMaxBytes(math.MaxInt64),
	)
	var liveFailed sync.Once
	sink, err := logseg.NewSink(nodeCtx, up, logseg.Options{
		Run: runID, Node: nodeID, Logger: logger,
		Live: func(data []byte) {
			// safety: the live view is a convenience; the segments are the log.
			if err := n.live.Append(nodeCtx, runID, nodeID, data); err != nil {
				liveFailed.Do(func() {
					logger.Warn("live log mirror unavailable; the segments are unaffected",
						"run_id", runID, "node_id", nodeID, "err", err)
				})
			}
		},
	})
	if err != nil {
		logger.Warn("node log stays on this runner's disk: the log spool could not be opened",
			"run_id", runID, "node_id", nodeID, "err", err)
		//nolint:contextcheck // logbatch.Close takes no context; it flushes what the mirror holds.
		if cerr := n.live.Close(); cerr != nil {
			logger.Warn("close the live log mirror", "run_id", runID, "node_id", nodeID, "err", cerr)
		}
		return nil
	}
	n.sink = sink
	n.handler = logseg.AppendHandler(sink, runID, nodeID)
	freshCtx, stop := context.WithCancel(nodeCtx)
	n.stopFresh = stop
	go func() {
		defer close(n.freshDone)
		up.KeepFresh(freshCtx, func(err error) {
			logger.Warn("renew the node's log segment URLs", "run_id", runID, "node_id", nodeID, "err", err)
		})
	}()
	return n
}

func (n *nodeLogSink) ServeHTTP(w http.ResponseWriter, r *http.Request) { n.handler.ServeHTTP(w, r) }

func (n *nodeLogSink) close(ctx context.Context, stopped bool) {
	n.closeOnce.Do(func() {
		//nolint:contextcheck // logbatch.Close takes no context; it flushes what the mirror holds.
		if err := n.live.Close(); err != nil {
			n.logger.Warn("live log mirror did not flush its last batch; the segments are unaffected",
				"run_id", n.runID, "node_id", n.nodeID, "err", err)
		}
		sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), childLogSealBudget)
		defer cancel()
		m, err := n.sink.Close(sealCtx, logseg.End{Stopped: stopped})
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
