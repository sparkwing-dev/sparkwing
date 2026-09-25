package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// childLogSeal numbers the log lines a pipeline child writes without
// numbers of its own, and seals them once the child exits. The child is the
// team's pipeline binary, built against whatever SDK the team pins, so a
// release that predates seals would otherwise leave every brokered node's
// log unconfirmed; the broker carrying the lines is this agent's code.
type childLogSeal struct {
	mu     sync.Mutex
	stream string
	// seq counts lines the logs service accepted, so a failed append that
	// the child retries keeps its number.
	seq    int64
	bytes  int64
	digest hash.Hash
	// failed holds the last append the service refused. The child retries
	// the same bytes; different bytes mean it gave that line up.
	failed  []byte
	failing bool
	dropped int64
	// ordinal is the execution attempt the child's appends named, which a
	// seal must name too.
	ordinal int
	// childNumbers records that the child numbers its own lines, and so
	// seals them itself.
	childNumbers bool
}

func (s *childLogSeal) init() {
	s.stream = rand.Text()
	s.digest = sha256.New()
}

func (b *remoteExecutionBroker) isChildLogAppend(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		r.URL.EscapedPath() == "/api/v1/logs/"+url.PathEscape(b.runID)+"/"+url.PathEscape(b.nodeID)
}

// forwardChildLogAppend numbers one unnumbered append and forwards it. It
// holds the stream for the whole round trip, so numbers follow the order
// the service stored the lines in.
func (b *remoteExecutionBroker) forwardChildLogAppend(w http.ResponseWriter, r *http.Request) {
	s := &b.logSeal
	if r.Header.Get(logs.LogStreamHeader) != "" {
		s.mu.Lock()
		s.childNumbers = true
		s.mu.Unlock()
		b.logs.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBrokeredLogAppend+1))
	_ = r.Body.Close()
	if err != nil || len(body) > maxBrokeredLogAppend {
		http.Error(w, "invalid log append", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if len(body) == 0 {
		b.logs.ServeHTTP(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ordinal, err := strconv.Atoi(r.Header.Get(store.AttemptOrdinalHeader)); err == nil && ordinal > 0 {
		s.ordinal = ordinal
	}
	if s.failing && !bytes.Equal(body, s.failed) {
		s.dropped++
		s.failing = false
	}
	r.Header.Set(logs.LogStreamHeader, s.stream)
	r.Header.Set(logs.LogSeqHeader, strconv.FormatInt(s.seq+1, 10))
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	b.logs.ServeHTTP(rec, r)
	if rec.status/100 != 2 {
		s.failed, s.failing = body, true
		return
	}
	s.seq++
	s.bytes += int64(len(body))
	_, _ = s.digest.Write(body)
	s.failing = false
}

// maxBrokeredLogAppend matches the largest append the logs service takes.
const maxBrokeredLogAppend = 4 << 20

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// childLogSealBudget bounds how long the supervisor spends sealing after
// its child exits, the same budget a node's own log gets to finish.
const childLogSealBudget = httpNodeLogFinishTimeout

// sealChildLogs seals the lines the broker numbered once the child is gone.
// A child that ended on its own, whatever its exit code, said everything it
// was going to; one killed by a signal or cancelled may not have, so its log
// is left unsealed and reads as cut off.
func (b *remoteExecutionBroker) sealChildLogs(ctx context.Context, outcome assistedChildOutcome, startErr error, logger *slog.Logger) {
	if b.logs == nil {
		return
	}
	s := &b.logSeal
	s.mu.Lock()
	defer s.mu.Unlock()
	skip := func(reason string) {
		logger.Warn("brokered node log not sealed; "+reason,
			"run_id", b.runID, "node_id", b.nodeID, "stream", s.stream)
	}
	switch {
	case s.childNumbers:
		return
	case startErr != nil:
		skip("the pipeline child never started")
		return
	case outcome.cancelCause != nil:
		skip("the pipeline child was cancelled, so readers will see its log as cut off")
		return
	case !exitedOnItsOwn(outcome.waitErr):
		skip("the pipeline child was killed, so readers will see its log as cut off")
		return
	case s.seq == 0 && !s.failing:
		skip("the pipeline child wrote no log lines")
		return
	case b.fence.ClaimGeneration > 0 && s.ordinal == 0:
		skip("the pipeline child's appends named no execution attempt for the seal to name")
		return
	}
	if s.failing {
		s.dropped++
		s.failing = false
	}
	seal := logs.Seal{
		Stream: s.stream, FinalSeq: s.seq, Lines: s.seq, Bytes: s.bytes,
		Dropped: s.dropped, SHA256: hex.EncodeToString(s.digest.Sum(nil)),
	}
	sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), childLogSealBudget)
	defer cancel()
	if b.fence.ClaimGeneration > 0 {
		sealCtx = store.WithNodeClaimFence(sealCtx, b.fence)
	}
	sealCtx = withAttempt(sealCtx, s.ordinal)
	client := logs.NewClientWithToken(b.logsURL, nil, b.upstreamToken)
	backoff := objectguard.Backoff{Base: httpNodeLogRetryBackoff, Max: httpNodeLogRetryMaxBackoff}
	for retry := 0; ; retry++ {
		err := client.Seal(sealCtx, b.runID, b.nodeID, seal)
		if err == nil {
			return
		}
		var authErr *logs.AuthError
		if errors.As(err, &authErr) || errors.Is(err, logs.ErrClaimConflict) || errors.Is(err, logs.ErrSealRefused) || sealCtx.Err() != nil {
			skip(fmt.Sprintf("the logs service did not take the seal: %v", err))
			return
		}
		select {
		case <-sealCtx.Done():
		case <-time.After(backoff.Delay(retry)):
		}
	}
}

func exitedOnItsOwn(waitErr error) bool {
	if waitErr == nil {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(waitErr, &exitErr) && exitErr.Exited()
}
