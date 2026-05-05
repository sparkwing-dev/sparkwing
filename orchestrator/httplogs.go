package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// HTTPLogs forwards log lines to a remote sparkwing-logs service.
// Post failures are dropped: losing a line is better than aborting a
// run on transient network flakes.
type HTTPLogs struct {
	client storage.LogStore
	logger *slog.Logger
}

// NewHTTPLogs targets the given logs service base URL.
func NewHTTPLogs(baseURL string, httpClient *http.Client, logger *slog.Logger) *HTTPLogs {
	return NewHTTPLogsWithToken(baseURL, httpClient, "", logger)
}

// NewHTTPLogsWithToken adds a bearer token; empty = no auth.
func NewHTTPLogsWithToken(baseURL string, httpClient *http.Client, token string, logger *slog.Logger) *HTTPLogs {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPLogs{
		client: sparkwinglogs.New(baseURL, httpClient, token),
		logger: logger,
	}
}

// NewLogStoreBackend wraps any storage.LogStore as a LogBackend.
func NewLogStoreBackend(s storage.LogStore, logger *slog.Logger) *HTTPLogs {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPLogs{client: s, logger: logger}
}

var _ LogBackend = (*HTTPLogs)(nil)

// OpenNodeLog returns a NodeLog that POSTs every line; delegate
// mirrors locally.
func (h *HTTPLogs) OpenNodeLog(runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error) {
	return &httpNodeLog{
		client:   h.client,
		logger:   h.logger,
		runID:    runID,
		nodeID:   nodeID,
		delegate: delegate,
	}, nil
}

type httpNodeLog struct {
	mu       sync.Mutex
	client   storage.LogStore
	logger   *slog.Logger
	runID    string
	nodeID   string
	delegate sparkwing.Logger
	closed   bool
}

func (l *httpNodeLog) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *httpNodeLog) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	if rec.Node == "" {
		rec.Node = l.nodeID
	}

	// Mirror first so a logs-service outage doesn't hide the line.
	if l.delegate != nil {
		l.delegate.Emit(rec)
	}

	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return
	}

	payload, err := json.Marshal(&rec)
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	// Bounded ctx so a wedged logs service doesn't stall the node.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.client.Append(ctx, l.runID, l.nodeID, payload); err != nil {
		l.logger.Warn("logs append dropped",
			"run_id", l.runID,
			"node_id", l.nodeID,
			"err", err,
		)
	}
}

func (l *httpNodeLog) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}
