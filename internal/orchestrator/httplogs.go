package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func (h *HTTPLogs) OpenNodeLog(ctx context.Context, runID, nodeID string, delegate sparkwing.Logger) (NodeLog, error) {
	_, requiresAttempt := store.NodeClaimFenceFromContext(ctx)
	if _, triggerClaim := store.TriggerClaimFenceFromContext(ctx); triggerClaim {
		requiresAttempt = true
	}
	attempt := 0
	if !requiresAttempt {
		attempt, _ = store.ExecutionAttemptOrdinalFromContext(ctx)
	}
	return &httpNodeLog{
		ctx:             context.WithoutCancel(ctx),
		client:          h.client,
		logger:          h.logger,
		runID:           runID,
		nodeID:          nodeID,
		delegate:        delegate,
		requiresAttempt: requiresAttempt,
		attempt:         attempt,
	}, nil
}

type httpNodeLog struct {
	ctx             context.Context
	writeMu         sync.Mutex
	mu              sync.Mutex
	client          storage.LogStore
	logger          *slog.Logger
	runID           string
	nodeID          string
	delegate        sparkwing.Logger
	closed          bool
	requiresAttempt bool
	attempt         int
	pending         [][]byte
	pendingBytes    int

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

const httpNodeLogPendingLimit = 4 << 20

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
		// safety: a record no encoder will take never reaches the store, the same
		// loss as an append that never lands.
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

	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.appendWithRetry(payload)
}

func (l *httpNodeLog) BindExecutionAttempt(ordinal int) error {
	if ordinal < 1 {
		return errors.New("execution attempt ordinal must be positive")
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	if !l.requiresAttempt {
		l.mu.Unlock()
		return nil
	}
	l.attempt = ordinal
	l.mu.Unlock()
	return l.Fatal()
}

func (l *httpNodeLog) FlushExecutionAttempt() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.appendWithRetry(nil)
	return l.Fatal()
}

func (l *httpNodeLog) appendWithRetry(payload []byte) {
	l.mu.Lock()
	ordinal := l.attempt
	if l.requiresAttempt && ordinal == 0 {
		if len(payload) == 0 {
			l.mu.Unlock()
			return
		}
		if l.pendingBytes+len(payload) > httpNodeLogPendingLimit {
			if l.fatal == nil {
				l.fatal = errors.New("pre-execution log buffer exceeded 4 MiB")
			}
			l.mu.Unlock()
			return
		}
		l.pending = append(l.pending, slices.Clone(payload))
		l.pendingBytes += len(payload)
		l.mu.Unlock()
		return
	}
	pending := l.pending
	l.pending = nil
	l.pendingBytes = 0
	l.mu.Unlock()
	for _, buffered := range pending {
		l.appendBoundWithRetry(ordinal, buffered)
		if l.Fatal() != nil {
			return
		}
	}
	if len(payload) == 0 {
		return
	}
	l.appendBoundWithRetry(ordinal, payload)
}

func (l *httpNodeLog) appendBoundWithRetry(ordinal int, payload []byte) {
	if l.dropSuppressed() {
		return
	}
	var lastErr error
	for retry := 0; retry < httpNodeLogRetryAttempts; retry++ {
		if retry > 0 {
			time.Sleep(httpNodeLogRetryBackoff << (retry - 1))
		}
		attemptCtx := l.ctx
		if ordinal > 0 {
			attemptCtx = store.WithExecutionAttemptOrdinal(attemptCtx, ordinal)
		}
		ctx, cancel := context.WithTimeout(attemptCtx, 5*time.Second)
		err := l.client.Append(ctx, l.runID, l.nodeID, payload)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		var authErr *logs.AuthError
		if errors.As(err, &authErr) || errors.Is(err, logs.ErrClaimConflict) {
			l.mu.Lock()
			if l.fatal == nil {
				l.fatal = err
			}
			l.mu.Unlock()
			l.logger.Error(
				"logs append rejected; failing run",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"err", err,
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
