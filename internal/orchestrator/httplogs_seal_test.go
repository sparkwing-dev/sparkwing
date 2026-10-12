package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func sealFixture(t *testing.T) (failAppends *atomic.Bool, url string, sealed func() *logseg.Manifest) {
	t.Helper()
	ctrl, tok := newSegmentController(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := openNodeLogSink(t.Context(), client.NewWithToken(ctrl.url, nil, tok), "run-s", "a", logger)
	if sink == nil {
		t.Fatal("the controller issued no segment URLs")
	}
	t.Cleanup(func() { sink.close(context.Background(), false) })
	failAppends = &atomic.Bool{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failAppends.Load() && r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/seal") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		sink.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return failAppends, hs.URL, func() *logseg.Manifest {
		t.Helper()
		sink.close(t.Context(), false)
		return ctrl.log(t).Manifest
	}
}

func TestHTTPLogs_CloseSealsTheStream(t *testing.T) {
	_, url, sealed := sealFixture(t)
	nlog, err := NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "hello"})
	}
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if m := sealed(); m == nil || m.Status != logseg.StatusComplete || m.Lines != 3 || len(m.Gaps) != 0 {
		t.Fatalf("manifest = %+v, want complete with 3 lines", m)
	}
}

// A runner that never seals leaves the sink to judge its log cut off.
func TestHTTPLogs_UnsealedStreamReadsCutOff(t *testing.T) {
	_, url, sealed := sealFixture(t)
	nlog, err := NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "hello"})
	}
	if err := nlog.(interface{ FlushExecutionAttempt() error }).FlushExecutionAttempt(); err != nil {
		t.Fatal(err)
	}
	if m := sealed(); m == nil || m.Status != logseg.StatusCutOff || m.Lines != 3 {
		t.Fatalf("manifest = %+v, want cut off after 3 lines", m)
	}
}

func TestHTTPLogs_SealReportsLinesTheRunnerDropped(t *testing.T) {
	SetTestHTTPNodeLogRetry(t, 2, 0)
	SetTestHTTPNodeLogDropCooldown(t, 0)
	failAppends, url, sealed := sealFixture(t)
	nlog, err := NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	flush := nlog.(interface{ FlushExecutionAttempt() error }).FlushExecutionAttempt
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "one"})
	if err := flush(); err != nil {
		t.Fatal(err)
	}
	failAppends.Store(true)
	for range 3 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "lost"})
	}
	if err := flush(); err != nil {
		t.Fatal(err)
	}
	failAppends.Store(false)
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "three"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	m := sealed()
	if m == nil || m.Status != logseg.StatusIncomplete || m.Lines != 2 || len(m.Gaps) != 1 || m.Gaps[0].From != 2 || m.Gaps[0].To != 4 {
		t.Fatalf("manifest = %+v, want incomplete with lines 2-4 missing", m)
	}
}

func TestHTTPLogs_EncodingLossDoesNotMarkTheGapReceived(t *testing.T) {
	_, url, sealed := sealFixture(t)
	nlog, err := NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Emit(sparkwing.LogRecord{Msg: "before"})
	nlog.Emit(sparkwing.LogRecord{Attrs: map[string]any{"bad": make(chan int)}})
	nlog.Emit(sparkwing.LogRecord{Msg: "after"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if m := sealed(); m == nil || m.Status != logseg.StatusIncomplete || m.Lines != 2 || len(m.Gaps) != 1 || m.Gaps[0].From != 2 || m.Gaps[0].To != 2 {
		t.Fatalf("manifest = %+v, want incomplete with line 2 missing", m)
	}
}

// A retried node keeps one writer across its execution attempts; the
// numbering runs on across them, so the seal must judge the stream, not
// one attempt.
func TestHTTPLogs_SealSpansExecutionAttempts(t *testing.T) {
	_, url, sealed := sealFixture(t)
	ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{
		HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 3,
	})
	nlog, err := NewHTTPLogs(url, nil, nil).OpenNodeLog(ctx, "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	binder := nlog.(interface{ BindExecutionAttempt(int) error })
	for ordinal := 1; ordinal <= 2; ordinal++ {
		if err := binder.BindExecutionAttempt(ordinal); err != nil {
			t.Fatal(err)
		}
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("attempt %d starts", ordinal)})
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: fmt.Sprintf("attempt %d ends", ordinal)})
	}
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if m := sealed(); m == nil || m.Status != logseg.StatusComplete || m.Lines != 4 || len(m.Gaps) != 0 {
		t.Fatalf("manifest = %+v, want complete over 4 lines", m)
	}
}

// An endpoint that refuses the seal is not asked again; the runner gives
// up at once rather than spending the node's finish budget.
func TestHTTPLogs_RefusedSealCostsOneRequest(t *testing.T) {
	var seals atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/seal") {
			seals.Add(1)
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	nlog, err := NewHTTPLogs(srv.URL, nil, nil).OpenNodeLog(context.Background(), "run", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "hello"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if got := seals.Load(); got != 1 {
		t.Fatalf("seal requests = %d, want 1", got)
	}
}

// A writer that skips its seal says why, so a log that reads cut off or
// unconfirmed can be traced to the runner that let it.
func TestHTTPLogs_SkippedSealWarnsWithTheReason(t *testing.T) {
	_, url, _ := sealFixture(t)
	var out strings.Builder
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{
		HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 3,
	})
	nlog, err := NewHTTPLogs(url, nil, logger).OpenNodeLog(ctx, "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "waiting for a slot"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "level=WARN") || !strings.Contains(out.String(), "before learning its execution attempt") {
		t.Fatalf("skipped seal logged %q", out.String())
	}

	out.Reset()
	nlog, err = NewHTTPLogs(url, nil, logger).OpenNodeLog(context.Background(), "run-s", "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "hello"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("a sealed log warned: %q", out.String())
	}
}
