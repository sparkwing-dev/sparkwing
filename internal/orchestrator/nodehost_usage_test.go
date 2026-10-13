package orchestrator

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// The host-measured attempt usage reaches the node row and the capacity
// record; leader-only usage, which is all Windows measures, only raises a
// profile.
func TestNodehostUsageReachesCapacityRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sessionWide bool
		wantCount   int
	}{
		{name: "session-wide", sessionWide: true, wantCount: 1},
		{name: "leader-only is partial", sessionWide: false, wantCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "s.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			start := time.Now()
			if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: "demo", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "build", Status: "running", StartedAt: &start}); err != nil {
				t.Fatal(err)
			}
			record := nodehost.RecordUsage(localState{st: st})
			record(ctx, &nodehost.NodeRun{
				Run:  &nodehost.Run{ID: "r1", Logger: slog.Default()},
				Plan: nodeproto.PlanNode{ID: "build"},
			}, nodehost.Usage{CPUTime: 1500 * time.Millisecond, MaxRSSBytes: 300 << 20, Wall: 2 * time.Second, SessionWide: tc.sessionWide})
			if err := st.FinishNode(ctx, "r1", "build", "success", "", nil); err != nil {
				t.Fatal(err)
			}

			n, err := st.GetNode(ctx, "r1", "build")
			if err != nil {
				t.Fatal(err)
			}
			if n.CPUNanos != int64(1500*time.Millisecond) || n.MaxRSSBytes != 300<<20 || n.ProcessWallNanos != int64(2*time.Second) {
				t.Fatalf("node usage = cpu %d rss %d wall %d", n.CPUNanos, n.MaxRSSBytes, n.ProcessWallNanos)
			}
			samples, err := st.ListNodeMetrics(ctx, "r1", "build")
			if err != nil {
				t.Fatal(err)
			}
			marked := len(samples) == 1 && samples[0].Kind == store.MetricPartial
			if marked == tc.sessionWide || len(samples) > 1 {
				t.Fatalf("metric samples = %+v, session-wide %v", samples, tc.sessionWide)
			}

			recordRunProfile(ctx, localState{st: st}, "demo", "r1", nil, "", runCharge{}, false, start, start.Add(2*time.Second))
			p, err := st.GetPipelineProfile(ctx, "demo", "build")
			if err != nil || p == nil {
				t.Fatalf("profile = %+v, %v", p, err)
			}
			if p.SampleCount != tc.wantCount || p.PeakMemoryBytes != 300<<20 {
				t.Fatalf("profile = %+v, want %d counted samples and peak 300MiB", p, tc.wantCount)
			}
			assertCores(t, "sustained cores", p.SustainedCores, 0.75)
		})
	}
}
