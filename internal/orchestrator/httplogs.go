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

// localRunDir reports the directory this backend writes runID's node
// logs into when -- and only when -- that directory is on the executing
// machine's disk. Empty for every remote store, which is the honest
// answer: a run whose logs live behind a URL has no local path to
// advertise.
//
// The probe is a type switch on the concrete filesystem store rather
// than a method on storage.LogStore. Adding it to the interface would
// oblige the S3, controller, and stdout implementations to answer a
// question that has no answer for them, and the only correct answer
// they could give -- "" -- would then be indistinguishable from an
// implementation that forgot. Here the absence of a case IS the "not
// local" answer.
//
// The directory itself comes from [fs.LogStore.RunDir], so the recorded
// path is the store's own answer rather than a second copy of its
// layout that could drift into naming a directory nothing writes to.
func (h *HTTPLogs) localRunDir(runID string) string {
	store, ok := h.client.(*fs.LogStore)
	if !ok || store == nil || store.Root == "" {
		return ""
	}
	// The same boundary check the store applies before it joins runID
	// onto Root, so this probe cannot name a directory the writer would
	// have refused.
	if err := storage.SafeSegment(runID); err != nil {
		return ""
	}
	dir := store.RunDir(runID)
	// Ensured, not merely named, for the same reason localRunLogDir
	// ensures the localLogs directory: a run that dies during planning
	// must not advertise a directory that never existed. This is the
	// same idempotent MkdirAll fs.LogStore.Append performs on its first
	// write.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

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

	// Track sticky auth fatal + per-line drop count so the
	// orchestrator can hard-fail the node on auth misconfig and so a
	// 5xx-driven loss of lines surfaces on the run summary instead of
	// disappearing into per-line WARN logs.
	fatal      error
	dropCount  int
	dropReason string // first-seen reason; subsequent drops keep the original

	// suppressUntil holds the near end of the breaker window opened by
	// the last exhausted retry budget. Lines emitted before it drop
	// without touching the network.
	suppressUntil time.Time
}

// httpNodeLogRetryAttempts caps the per-line retry budget for
// transient (5xx / network) failures. Vars not consts so tests can
// shrink them.
var (
	httpNodeLogRetryAttempts = 3
	httpNodeLogRetryBackoff  = 200 * time.Millisecond
)

// httpNodeLogDropCooldown is how long a node stops attempting appends
// after one line has exhausted its retry budget.
//
// Without it a store that is down rather than flaky charges the full
// retry budget to every single line: a pipeline that runs in 84ms took
// 43s against an unreachable bucket, because each batch blocked inline
// on three SDK retries. The adopter reads that as "sparkwing is slow"
// rather than "sparkwing cannot reach the log store".
//
// The window is short enough that a store recovering mid-run resumes
// on the next line past it, so this trades a bounded number of extra
// dropped lines for a run that finishes at its real speed. The lines
// dropped inside the window are still counted, and the count still
// fails the node, so the cooldown never converts loss into silence.
var httpNodeLogDropCooldown = 5 * time.Second

// SetTestHTTPNodeLogRetry overrides the per-line retry budget +
// backoff for the duration of a test, restoring the originals on
// cleanup. Production callers should not touch these knobs.
func SetTestHTTPNodeLogRetry(t interface{ Cleanup(func()) }, attempts, backoffMS int) {
	oldA, oldB := httpNodeLogRetryAttempts, httpNodeLogRetryBackoff
	httpNodeLogRetryAttempts = attempts
	httpNodeLogRetryBackoff = time.Duration(backoffMS) * time.Millisecond
	t.Cleanup(func() {
		httpNodeLogRetryAttempts = oldA
		httpNodeLogRetryBackoff = oldB
	})
}

// SetTestHTTPNodeLogDropCooldown overrides the post-drop breaker
// window for the duration of a test. Production callers should not
// touch this knob.
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
		return
	}
	payload = append(payload, '\n')

	l.appendWithRetry(payload)
}

// appendWithRetry POSTs payload to the logs service with bounded
// retries on transient errors. Auth failures (401/403) latch a
// fatal error and abort early; other errors past the retry budget
// increment dropCount + record the first-seen reason and open a
// cooldown window during which further lines drop without being
// attempted (see httpNodeLogDropCooldown).
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

// dropSuppressed counts a drop and reports true when the breaker
// window opened by an earlier exhausted retry budget is still open.
// The line is lost either way; skipping the attempt only decides
// whether the run also pays the retry budget for it.
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
