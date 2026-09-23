package orchestrator_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// sealFixture is a real logs service behind a switch that can fail
// appends, so the runner's retries and drop accounting run for real.
func sealFixture(t *testing.T) (*logs.Client, *atomic.Bool, string) {
	t.Helper()
	srv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	var failAppends atomic.Bool
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failAppends.Load() && r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/seal") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return logs.NewClient(hs.URL, nil), &failAppends, hs.URL
}

func verdict(t *testing.T, c *logs.Client, run, node string) logs.Completeness {
	t.Helper()
	r, err := c.ReadSeals(context.Background(), run, node)
	if err != nil {
		t.Fatal(err)
	}
	return r.Assess(logs.NodeProgress{Started: true, Terminal: true, FinishedAt: time.Now().Add(-2 * logs.SealGrace)}, time.Now())
}

func TestHTTPLogs_CloseSealsTheStream(t *testing.T) {
	client, _, url := sealFixture(t)
	nlog, err := orchestrator.NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "hello"})
	}

	// Negative control: before Close the stream is open, so a finished
	// node reads as cut off.
	if got := verdict(t, client, "run", "node"); got.State != logs.StateCutOff {
		t.Fatalf("before close = %+v, want cut_off", got)
	}
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	got := verdict(t, client, "run", "node")
	if got.State != logs.StateComplete || got.Lines != 3 {
		t.Fatalf("after close = %+v, want complete with 3 lines", got)
	}
	r, _ := client.ReadSeals(context.Background(), "run", "node")
	if s := r.Streams[0].Seal; s == nil || s.FinalSeq != 3 || s.Bytes == 0 || len(s.SHA256) != 64 {
		t.Fatalf("seal = %+v", s)
	}
}

func TestHTTPLogs_SealReportsLinesTheRunnerDropped(t *testing.T) {
	orchestrator.SetTestHTTPNodeLogRetry(t, 2, 0)
	orchestrator.SetTestHTTPNodeLogDropCooldown(t, 0)
	client, failAppends, url := sealFixture(t)
	nlog, err := orchestrator.NewHTTPLogs(url, nil, nil).OpenNodeLog(context.Background(), "run", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "one"})
	failAppends.Store(true)
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "lost"})
	failAppends.Store(false)
	nlog.Emit(sparkwing.LogRecord{Level: "info", Msg: "three"})
	if err := nlog.Close(); err != nil {
		t.Fatal(err)
	}
	got := verdict(t, client, "run", "node")
	if got.State != logs.StateIncomplete || got.MissingLines != 1 {
		t.Fatalf("verdict = %+v, want incomplete with 1 missing", got)
	}
	r, _ := client.ReadSeals(context.Background(), "run", "node")
	if s := r.Streams[0]; s.Seal.Dropped != 1 || s.Missing != 1 || len(s.Gaps) != 1 || s.Gaps[0].From != 2 {
		t.Fatalf("stream = %+v seal = %+v", s, s.Seal)
	}
}

// A retried node keeps one writer across its execution attempts, and each
// attempt's lines land in their own file; the numbering runs on across
// them, so the seal must judge the stream, not one file.
func TestHTTPLogs_SealSpansExecutionAttempts(t *testing.T) {
	client, _, url := sealFixture(t)
	ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{
		HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 3,
	})
	nlog, err := orchestrator.NewHTTPLogs(url, nil, nil).OpenNodeLog(ctx, "run", "node", nil)
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
	r, err := client.ReadSeals(context.Background(), "run", "node")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Streams) != 1 || r.Streams[0].Missing != 0 || r.UnconfirmedFiles != 0 {
		t.Fatalf("report = %+v", r)
	}
	if got := verdict(t, client, "run", "node"); got.State != logs.StateComplete || got.Lines != 4 {
		t.Fatalf("verdict = %+v, want complete over 4 lines", got)
	}
}

// A logs service that predates seals answers the seal route 404; the
// runner gives up at once rather than spending the node's finish budget.
func TestHTTPLogs_ServiceWithoutSealsCostsOneRequest(t *testing.T) {
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
	nlog, err := orchestrator.NewHTTPLogs(srv.URL, nil, nil).OpenNodeLog(context.Background(), "run", "node", nil)
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
	_, _, url := sealFixture(t)
	var out strings.Builder
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := store.WithNodeClaimFence(context.Background(), store.NodeClaimFence{
		HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 3,
	})
	nlog, err := orchestrator.NewHTTPLogs(url, nil, logger).OpenNodeLog(ctx, "run", "node", nil)
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

	// Negative control: a writer that seals logs nothing.
	out.Reset()
	nlog, err = orchestrator.NewHTTPLogs(url, nil, logger).OpenNodeLog(context.Background(), "run", "other", nil)
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
