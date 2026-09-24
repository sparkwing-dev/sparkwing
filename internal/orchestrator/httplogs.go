package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type HTTPLogs struct {
	client storage.LogStore
	logger *slog.Logger
	live   LiveLogSink
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

// WithLiveSink mirrors every line to sink for the controller's live
// view, on top of the durable write. A durable surface that already
// serves a live read keeps sink unused.
func (h *HTTPLogs) WithLiveSink(sink LiveLogSink) *HTTPLogs {
	if sink != nil && !liveLogsRedundant(h.client) {
		h.live = sink
	}
	return h
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
	nodeCtx := context.WithoutCancel(ctx)
	l := &httpNodeLog{
		ctx:             nodeCtx,
		client:          h.client,
		logger:          h.logger,
		runID:           runID,
		nodeID:          nodeID,
		delegate:        delegate,
		requiresAttempt: requiresAttempt,
		attempt:         attempt,
		stream:          rand.Text(),
		digest:          sha256.New(),
	}
	if h.live != nil {
		l.live = logbatch.New(
			&liveLogStore{sink: h.live, ctx: nodeCtx},
			logbatch.WithFlushInterval(liveLogFlushInterval),
			logbatch.WithBufferThreshold(liveLogFlushBytes),
			logbatch.WithMaxObjects(math.MaxInt32),
			logbatch.WithMaxBytes(math.MaxInt64),
		)
	}
	return l, nil
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
	live            *logbatch.Store
	liveFailed      bool
	closed          bool
	requiresAttempt bool
	attempt         int
	pending         []numberedLine
	pendingBytes    int
	flushTimer      *time.Timer

	// stream names this writer's numbered appends; seq, sentBytes and
	// digest cover every line it numbered, delivered or not, and go into
	// the seal Close sends.
	stream    string
	seq       int64
	sentBytes int64
	digest    hash.Hash

	fatal      error
	dropCount  int
	dropReason string

	suppressUntil time.Time
}

var (
	httpNodeLogRetryAttempts = 3
	httpNodeLogRetryBackoff  = 200 * time.Millisecond
)

// safety: one append is one object-store PUT, so an unreachable log store must
// not cost a doubling series that keeps growing per line.
const httpNodeLogRetryMaxBackoff = 2 * time.Second

var httpNodeLogDropCooldown = 5 * time.Second

const httpNodeLogFinishTimeout = 10 * time.Second

const httpNodeLogPendingLimit = 4 << 20
const httpNodeLogBatchLines = 256
const httpNodeLogBatchBytes = 64 << 10
const httpNodeLogTailDelay = 100 * time.Millisecond

var logStoreWithoutSealsOnce sync.Once

type numberedLine struct {
	seq     int64
	payload []byte
}

// logSealer is the capability a log store has when it can record the end
// of a numbered stream; only the logs service has it.
type logSealer interface {
	Seal(ctx context.Context, runID, nodeID string, seal logs.Seal) error
}

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
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	closed, fatal = l.closed, l.fatal
	l.mu.Unlock()
	if closed || fatal != nil {
		return
	}
	if err != nil {
		// safety: a record no encoder will take never reaches the store, the same
		// loss as an append that never lands, so it still takes a number.
		l.flushBuffered(l.ctx)
		l.number(nil)
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
	l.appendWithRetry(numberedLine{seq: l.number(payload), payload: payload})
	l.appendLive(payload)
}

// number gives the next line its place in this writer's stream. The
// caller holds writeMu.
func (l *httpNodeLog) number(payload []byte) int64 {
	l.seq++
	l.sentBytes += int64(len(payload))
	_, _ = l.digest.Write(payload)
	return l.seq
}

// safety: a live view that cannot be written is reported once and then
// left alone. The durable copy is the record, so losing the live tail
// must never fail the node.
func (l *httpNodeLog) appendLive(payload []byte) {
	if l.live == nil {
		return
	}
	if err := l.live.Append(l.ctx, l.runID, l.nodeID, payload); err != nil {
		l.mu.Lock()
		first := !l.liveFailed
		l.liveFailed = true
		l.mu.Unlock()
		if first {
			l.logger.Warn(
				"live log mirror unavailable; the durable copy is unaffected",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"err", err,
			)
		}
	}
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
	old := l.attempt
	l.mu.Unlock()
	if old > 0 && old != ordinal {
		l.flushBuffered(l.ctx)
	}
	l.mu.Lock()
	l.attempt = ordinal
	l.mu.Unlock()
	return l.Fatal()
}

func (l *httpNodeLog) FlushExecutionAttempt() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.flushBuffered(l.ctx)
	return l.Fatal()
}

func (l *httpNodeLog) appendWithRetry(line numberedLine) {
	l.mu.Lock()
	ordinal := l.attempt
	if l.requiresAttempt && ordinal == 0 {
		if l.pendingBytes+len(line.payload) > httpNodeLogPendingLimit {
			if l.fatal == nil {
				l.fatal = errors.New("pre-execution log buffer exceeded 4 MiB")
			}
			l.mu.Unlock()
			return
		}
	}
	l.pending = append(l.pending, line)
	l.pendingBytes += len(line.payload)
	ready := ordinal > 0 || !l.requiresAttempt
	l.mu.Unlock()
	if !ready {
		return
	}
	if len(l.pending) >= httpNodeLogBatchLines || l.pendingBytes >= httpNodeLogBatchBytes {
		l.flushBuffered(l.ctx)
		return
	}
	if l.flushTimer == nil {
		var timer *time.Timer
		timer = time.AfterFunc(httpNodeLogTailDelay, func() {
			l.writeMu.Lock()
			defer l.writeMu.Unlock()
			if l.flushTimer != timer {
				return
			}
			l.mu.Lock()
			closed := l.closed
			l.mu.Unlock()
			if closed {
				return
			}
			l.flushBuffered(l.ctx)
		})
		l.flushTimer = timer
	}
}

// safety: writeMu spans the flush so a timer, Close or attempt rebind cannot reorder lines across attempt files.
func (l *httpNodeLog) flushBuffered(ctx context.Context) {
	l.mu.Lock()
	ordinal := l.attempt
	if l.requiresAttempt && ordinal == 0 {
		l.mu.Unlock()
		return
	}
	pending := l.pending
	l.pending = nil
	l.pendingBytes = 0
	l.mu.Unlock()
	if l.flushTimer != nil {
		l.flushTimer.Stop()
		l.flushTimer = nil
	}
	for len(pending) > 0 {
		count, size := 0, 0
		for count < len(pending) && count < httpNodeLogBatchLines {
			if count > 0 && pending[count].seq != pending[count-1].seq+1 {
				break
			}
			if count > 0 && size+len(pending[count].payload) > httpNodeLogBatchBytes {
				break
			}
			size += len(pending[count].payload)
			count++
		}
		batch := pending[:count]
		pending = pending[count:]
		l.appendBoundWithRetry(ctx, ordinal, batch)
		if l.Fatal() != nil {
			return
		}
	}
	l.collectFlushLosses()
}

func (l *httpNodeLog) appendBoundWithRetry(ctx context.Context, ordinal int, batch []numberedLine) {
	if l.dropSuppressed(len(batch)) {
		return
	}
	var payload []byte
	for _, line := range batch {
		payload = append(payload, line.payload...)
	}
	var lastErr error
	for retry := 0; retry < httpNodeLogRetryAttempts; retry++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		if retry > 0 && httpNodeLogRetryBackoff > 0 {
			backoff := objectguard.Backoff{Base: httpNodeLogRetryBackoff, Max: httpNodeLogRetryMaxBackoff}
			time.Sleep(backoff.Delay(retry - 1))
		}
		attemptCtx := logs.WithAppendSequenceRange(withAttempt(ctx, ordinal), l.stream, batch[0].seq, batch[len(batch)-1].seq)
		requestCtx, cancel := context.WithTimeout(attemptCtx, 5*time.Second)
		err := l.client.Append(requestCtx, l.runID, l.nodeID, payload)
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
				"logs append rejected; the rest of this node's log is lost",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"err", err,
			)
			return
		}
	}
	l.mu.Lock()
	l.dropCount += len(batch)
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

func withAttempt(ctx context.Context, ordinal int) context.Context {
	if ordinal > 0 {
		return store.WithExecutionAttemptOrdinal(ctx, ordinal)
	}
	return ctx
}

func (l *httpNodeLog) dropSuppressed(lines int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.suppressUntil.IsZero() || !time.Now().Before(l.suppressUntil) {
		return false
	}
	l.dropCount += lines
	return true
}

// Close hands the node's last lines to the store. A batching store
// holds them until something asks for them, and the node finishing is
// that something; a write-through store has nothing left to do. It
// blocks for at most the live mirror's flush budget plus the durable
// one, twenty seconds in all, and holds the node's write lock while it
// does, so the node reports terminal only once its log is complete.
func (l *httpNodeLog) Close() error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	l.mu.Lock()
	already := l.closed
	l.closed = true
	l.mu.Unlock()
	if already {
		return nil
	}
	ctx, cancel := context.WithTimeout(l.ctx, httpNodeLogFinishTimeout)
	defer cancel()
	l.flushBuffered(ctx)
	if l.live != nil {
		if lerr := l.live.Close(); lerr != nil {
			l.logger.Warn(
				"live log mirror did not flush its last batch; the durable copy is unaffected",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"err", lerr,
			)
		}
	}
	err := storage.FlushNode(ctx, l.client, l.runID, l.nodeID)
	l.collectFlushLosses()
	l.seal(ctx)
	return err
}

// seal tells the logs service this writer is done, with what it numbered
// and what it lost, so a reader can tell a whole log from one cut short.
// It retries until ctx, the node's finish budget, runs out. A writer whose
// claim was refused, or that never learned its attempt, sent nothing the
// service could file and seals nothing.
func (l *httpNodeLog) seal(ctx context.Context) {
	skip := func(reason string) {
		l.logger.Warn("node log not sealed; "+reason,
			"run_id", l.runID, "node_id", l.nodeID, "stream", l.stream, "lines", l.seq)
	}
	sealer, ok := l.client.(logSealer)
	if !ok {
		// safety: a store without seals says so once per process, not once
		// per node, because every node of the process shares it.
		logStoreWithoutSealsOnce.Do(func() {
			skip(fmt.Sprintf("the %T log store keeps no seals, so readers report completeness as unknown", l.client))
		})
		return
	}
	l.mu.Lock()
	ordinal, fatal, dropped := l.attempt, l.fatal, int64(l.dropCount)
	l.mu.Unlock()
	if fatal != nil {
		skip(fmt.Sprintf("the logs service refused this node's appends (%v), so readers will see its log as cut off", fatal))
		return
	}
	if l.requiresAttempt && ordinal == 0 {
		skip("the node closed its log before learning its execution attempt, so its lines were never sent")
		return
	}
	seal := logs.Seal{
		Stream:   l.stream,
		FinalSeq: l.seq,
		Lines:    l.seq,
		Bytes:    l.sentBytes,
		Dropped:  dropped,
		SHA256:   hex.EncodeToString(l.digest.Sum(nil)),
	}
	backoff := objectguard.Backoff{Base: httpNodeLogRetryBackoff, Max: httpNodeLogRetryMaxBackoff}
	for retry := 0; ; retry++ {
		err := sealer.Seal(withAttempt(ctx, ordinal), l.runID, l.nodeID, seal)
		if err == nil {
			return
		}
		var authErr *logs.AuthError
		permanent := errors.As(err, &authErr) || errors.Is(err, logs.ErrClaimConflict) || errors.Is(err, logs.ErrSealRefused)
		if permanent || ctx.Err() != nil {
			l.logger.Warn(
				"log seal not recorded; readers will see this node's log as cut off",
				"run_id", l.runID,
				"node_id", l.nodeID,
				"err", err,
			)
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff.Delay(retry)):
		}
	}
}

// safety: a buffering store writes many lines per request, so one failed
// request loses a whole batch. Folding that into the node's own count is
// what makes the batch visible to the logs_drop event.
func (l *httpNodeLog) collectFlushLosses() {
	lines, bytes := storage.FlushLosses(l.client, l.runID, l.nodeID)
	if lines == 0 {
		return
	}
	l.mu.Lock()
	l.dropCount += lines
	if l.dropReason == "" {
		l.dropReason = fmt.Sprintf("a failed log flush discarded %d line(s), %d byte(s)", lines, bytes)
	}
	l.mu.Unlock()
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
