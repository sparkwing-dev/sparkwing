package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type HTTPLogs struct {
	client storage.LogStore
	logger *slog.Logger
}

func NewHTTPLogs(baseURL string, httpClient *http.Client, logger *slog.Logger) *HTTPLogs {
	return NewHTTPLogsWithToken(baseURL, httpClient, "", logger)
}

func NewHTTPLogsWithToken(baseURL string, httpClient *http.Client, token string, logger *slog.Logger) *HTTPLogs {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPLogs{
		client: sparkwinglogs.New(baseURL, httpClient, token),
		logger: logger,
	}
}

func NewLogStoreBackend(s storage.LogStore, logger *slog.Logger) *HTTPLogs {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPLogs{client: s, logger: logger}
}

var _ LogBackend = (*HTTPLogs)(nil)

func (h *HTTPLogs) localRunDir(runID string) string {
	store, ok := h.client.(*fs.LogStore)
	if !ok || store == nil || store.Root == "" {
		return ""
	}

	if err := storage.SafeSegment(runID); err != nil {
		return ""
	}
	dir := store.RunDir(runID)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

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

	fatal      error
	dropCount  int
	dropReason string

	suppressUntil time.Time
}

var (
	httpNodeLogRetryAttempts = 3
	httpNodeLogRetryBackoff  = 200 * time.Millisecond
)

var httpNodeLogDropCooldown = 5 * time.Second

func SetTestHTTPNodeLogRetry(t interface{ Cleanup(func()) }, attempts, backoffMS int) {
	oldA, oldB := httpNodeLogRetryAttempts, httpNodeLogRetryBackoff
	httpNodeLogRetryAttempts = attempts
	httpNodeLogRetryBackoff = time.Duration(backoffMS) * time.Millisecond
	t.Cleanup(func() {
		httpNodeLogRetryAttempts = oldA
		httpNodeLogRetryBackoff = oldB
	})
}

func SetTestHTTPNodeLogDropCooldown(t interface{ Cleanup(func()) }, cooldownMS int) {
	old := httpNodeLogDropCooldown
	httpNodeLogDropCooldown = time.Duration(cooldownMS) * time.Millisecond
	t.Cleanup(func() { httpNodeLogDropCooldown = old })
}

func (l *httpNodeLog) Log(level, msg string) {
	l.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (l *httpNodeLog) Emit(rec sparkwing.LogRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	if rec.JobID == "" {
		rec.JobID = l.nodeID
	}

	if l.delegate != nil {
		l.delegate.Emit(rec)
	}

	l.mu.Lock()
	closed := l.closed
	fatal := l.fatal
	l.mu.Unlock()
	if closed || fatal != nil {
		return
	}

	payload, err := json.Marshal(&rec)
	if err != nil {
		// A record no encoder will take is a line the store never gets, which is
		// the same loss as an append that never lands.
		l.mu.Lock()
		l.dropCount++
		if l.dropReason == "" {
			l.dropReason = err.Error()
		}
		l.mu.Unlock()
		l.logger.Warn(
			"log record could not be encoded; dropping the line",
			"run_id", l.runID,
			"node_id", l.nodeID,
			"err", err,
		)
		return
	}
	payload = append(payload, '\n')

	l.appendWithRetry(payload)
}

func (l *httpNodeLog) appendWithRetry(payload []byte) {
	if l.dropSuppressed() {
		return
	}
	var lastErr error
	for attempt := 0; attempt < httpNodeLogRetryAttempts; attempt++ {
		if attempt > 0 {
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
			l.logger.Error(
				"logs append blocked by auth; failing run",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"status", authErr.Status,
				"scope", authErr.Scope,
			)
			return
		}
	}
	l.mu.Lock()
	l.dropCount++
	if l.dropReason == "" && lastErr != nil {
		l.dropReason = lastErr.Error()
	}
	count := l.dropCount
	l.suppressUntil = time.Now().Add(httpNodeLogDropCooldown)
	l.mu.Unlock()
	l.logger.Warn(
		"logs append dropped after retries",
		"run_id", l.runID,
		"node_id", l.nodeID,
		"err", lastErr,
		"dropped_total", count,
	)
}

func (l *httpNodeLog) dropSuppressed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.suppressUntil.IsZero() || !time.Now().Before(l.suppressUntil) {
		return false
	}
	l.dropCount++
	return true
}

func (l *httpNodeLog) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

func (l *httpNodeLog) Fatal() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fatal
}

func (l *httpNodeLog) Drops() (int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropCount, l.dropReason
}
