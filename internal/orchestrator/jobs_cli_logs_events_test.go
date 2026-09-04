package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestWriteEventsViaBackend_PagesPastOnePage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "events-state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const runID = "run-events-paging"
	const want = 600
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for range want {
		if _, err := st.AppendEvent(ctx, runID, "n1", "cache_hit", nil); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	b := backend.NewStoreBackend(st, Paths{Root: t.TempDir()}, nil)
	var buf bytes.Buffer
	if err := writeEventsViaBackend(ctx, b, runID, LogsOpts{EventsOnly: true}, &buf); err != nil {
		t.Fatalf("writeEventsViaBackend: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != want {
		t.Fatalf("emitted %d events, want %d", len(lines), want)
	}
	var last store.Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last event: %v", err)
	}
	if last.Seq != want {
		t.Fatalf("last event seq = %d, want %d", last.Seq, want)
	}
}

type stuckEventsBackend struct {
	backend.Backend
	page  []store.Event
	calls atomic.Int64
}

func (b *stuckEventsBackend) ListEventsAfter(context.Context, string, int64, int) ([]store.Event, error) {
	b.calls.Add(1)
	return b.page, nil
}

func TestWriteEventsViaBackend_StopsWhenAPageDoesNotAdvance(t *testing.T) {
	const page = 500
	events := make([]store.Event, page)
	for i := range events {
		events[i] = store.Event{RunID: "run-stuck", Seq: int64(i + 1), Kind: "cache_hit"}
	}
	b := &stuckEventsBackend{page: events}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- writeEventsViaBackend(context.Background(), b, "run-stuck", LogsOpts{EventsOnly: true}, &buf)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writeEventsViaBackend: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("writeEventsViaBackend did not return; a backend ignoring after loops forever (%d calls so far)", b.calls.Load())
	}

	if got := len(strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")); got != page {
		t.Fatalf("emitted %d events, want %d", got, page)
	}
	if got := b.calls.Load(); got != 2 {
		t.Fatalf("asked the backend %d times, want 2 (one page, one that did not advance)", got)
	}
}

func TestJobLogsRemoteWithTokens_EventsOnlyEmitsStoreEvents(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller-events.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const runID = "run-controller-events"
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "demo", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, kind := range []string{"admission_wait", "concurrency_wait", "cache_hit"} {
		if _, err := st.AppendEvent(ctx, runID, "n1", kind, nil); err != nil {
			t.Fatalf("AppendEvent %s: %v", kind, err)
		}
	}
	srv := NewControllerServer(t, st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var buf bytes.Buffer
	if err := JobLogsRemoteWithTokens(ctx, srv.URL, srv.URL, "", runID, LogsOpts{EventsOnly: true}, &buf); err != nil {
		t.Fatalf("JobLogsRemoteWithTokens --events-only: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("emitted %d records, want 3; output:\n%s", len(lines), buf.String())
	}
	var first store.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first record: %v", err)
	}
	if first.Kind != "admission_wait" {
		t.Fatalf("first record kind = %q, want admission_wait", first.Kind)
	}
}
