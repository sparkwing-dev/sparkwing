package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/logs"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing"
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

	// IMP-002: track sticky auth fatal + per-line drop count so the
	// orchestrator can hard-fail the node on auth misconfig and so a
	// 5xx-driven loss of lines surfaces on the run summary instead of
	// disappearing into per-line WARN logs.
	fatal      error
	dropCount  int
	dropReason string // first-seen reason; subsequent drops keep the original
}

// httpNodeLogRetryAttempts caps the per-line retry budget for
// transient (5xx / network) failures. Vars not consts so tests can
// shrink them.
var (
	httpNodeLogRetryAttempts = 3
	httpNodeLogRetryBackoff  = 200 * time.Millisecond
)

// SetTestHTTPNodeLogRetry overrides the per-line retry budget +
// backoff for the duration of a test, restoring the originals on
// cleanup. Production callers should not touch these knobs.
func SetTestHTTPNodeLogRetry(t interface{ Cleanup(func()) }, attempts int, backoffMS int) {
	oldA, oldB := httpNodeLogRetryAttempts, httpNodeLogRetryBackoff
	httpNodeLogRetryAttempts = attempts
	httpNodeLogRetryBackoff = time.Duration(backoffMS) * time.Millisecond
	t.Cleanup(func() {
		httpNodeLogRetryAttempts = oldA
		httpNodeLogRetryBackoff = oldB
	})
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
	fatal := l.fatal
	l.mu.Unlock()
	if closed || fatal != nil {
		// Once auth has latched fatal there's no point spamming the
		// service with attempts that will all 401/403; we'll surface
		// the latched error via Fatal() at node close.
		return
	}

	payload, err := json.Marshal(&rec)
	if err != nil {
		return
	}
	payload = append(payload, '\n')

	l.appendWithRetry(payload)
}

// appendWithRetry POSTs payload to the logs service with bounded
// retries on transient errors. Auth failures (401/403) latch a
// fatal error and abort early; other errors past the retry budget
// increment dropCount + record the first-seen reason.
func (l *httpNodeLog) appendWithRetry(payload []byte) {
	var lastErr error
	for attempt := 0; attempt < httpNodeLogRetryAttempts; attempt++ {
		if attempt > 0 {
			// Exponential-ish backoff (200ms, 400ms, 800ms by default).
			// Cheap because we only spend it when the service is sick;
			// the ctx timeout below caps total wall-clock per line.
			time.Sleep(httpNodeLogRetryBackoff << (attempt - 1))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := l.client.Append(ctx, l.runID, l.nodeID, payload)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		var authErr *logs.AuthError
		if errors.As(err, &authErr) {
			l.mu.Lock()
			if l.fatal == nil {
				l.fatal = authErr
			}
			l.mu.Unlock()
			l.logger.Error("logs append blocked by auth; failing run",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"status", authErr.Status,
				"scope", authErr.Scope,
			)
			return
		}
	}
	// Retries exhausted on a non-auth error: record the drop and
	// keep the run going. The count surfaces on the Run record at
	// node-close time so `runs status` shows it.
	l.mu.Lock()
	l.dropCount++
	if l.dropReason == "" && lastErr != nil {
		l.dropReason = lastErr.Error()
	}
	count := l.dropCount
	l.mu.Unlock()
	l.logger.Warn("logs append dropped after retries",
		"run_id", l.runID,
		"node_id", l.nodeID,
		"err", lastErr,
		"dropped_total", count,
	)
}

func (l *httpNodeLog) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

// Fatal returns the sticky auth error (if any) latched by Emit.
// Non-nil = the run cannot be trusted to have observable logs and
// the orchestrator should fail the node.
func (l *httpNodeLog) Fatal() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fatal
}

// Drops returns the count and first-seen reason of log lines lost
// to retry-budget exhaustion (5xx / network). Zero count = clean.
func (l *httpNodeLog) Drops() (int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropCount, l.dropReason
}
