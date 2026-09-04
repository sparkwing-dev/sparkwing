package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
