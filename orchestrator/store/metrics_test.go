package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func TestMetrics_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID:        "run-1",
		Pipeline:  "demo",
		Status:    "running",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "a", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	for i, millicores := range []int64{100, 200, 350} {
		sample := store.MetricSample{
			TS:            now.Add(time.Duration(i) * time.Second),
			CPUMillicores: millicores,
			MemoryBytes:   int64(1024 * 1024 * (i + 1)),
		}
		if err := st.AddNodeMetricSample(ctx, "run-1", "a", sample); err != nil {
			t.Fatalf("AddNodeMetricSample: %v", err)
		}
	}

	samples, err := st.ListNodeMetrics(ctx, "run-1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("samples=%d want 3", len(samples))
	}
	if samples[0].CPUMillicores != 100 || samples[2].CPUMillicores != 350 {
		t.Errorf("sample ordering: %+v", samples)
	}

	// Inserting the exact same (run, node, ts) again is a no-op.
	dup := samples[0]
	if err := st.AddNodeMetricSample(ctx, "run-1", "a", dup); err != nil {
		t.Fatalf("duplicate insert returned error: %v", err)
	}
	after, _ := st.ListNodeMetrics(ctx, "run-1", "a")
	if len(after) != 3 {
		t.Errorf("duplicate insert changed count: %d", len(after))
	}
}
