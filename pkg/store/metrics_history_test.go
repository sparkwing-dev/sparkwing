package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestMetricHistoryUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []store.MetricSample
	}{
		{"interval", []store.MetricSample{{Kind: store.MetricInterval, TS: time.Unix(11, 0), CPUMillicores: 500, MemoryBytes: 200}, {Kind: store.MetricInterval, TS: time.Unix(12, 0), CPUMillicores: 700, MemoryBytes: 400}}},
		{"command", []store.MetricSample{{Kind: store.MetricCommand, TS: time.Unix(13, 0), CPUMillicores: 250, MemoryBytes: 100, CPUTime: time.Second}, {Kind: store.MetricCommand, TS: time.Unix(14, 0), CPUMillicores: 350, MemoryBytes: 600, CPUTime: 2 * time.Second}}},
		{"already unknown", []store.MetricSample{{Kind: store.MetricInterval, TS: time.Unix(15, 0), MemoryBytes: 100}, {Kind: store.MetricUnknown, TS: time.Unix(16, 0), MemoryBytes: 200}}},
		{"exit only", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := storetest.New(t)
			st := target.Open(t)
			start := time.Unix(10, 0)
			if err := st.CreateRun(t.Context(), store.Run{ID: "old", Pipeline: "history", Status: "running", StartedAt: start}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(t.Context(), store.Node{RunID: "old", NodeID: "build", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			for _, sample := range tc.samples {
				if err := st.AddNodeMetricSample(t.Context(), "old", "build", sample); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.AddNodeUsage(t.Context(), "old", "build", store.NodeUsage{CPUTime: time.Second, Wall: 2 * time.Second, MaxRSSBytes: 512}); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{`DELETE FROM sparkwing_schema_version WHERE version >= 51`, `DELETE FROM sparkwing_requirements WHERE name = 'process-tree-accounting'`} {
				if _, err := st.DB().ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			up := target.Open(t)
			want := append([]store.MetricSample(nil), tc.samples...)
			if len(want) == 0 {
				want = []store.MetricSample{{TS: start}}
			} else if tc.name != "already unknown" {
				want[0].Kind = store.MetricUnknown
			}
			check := func(s *store.Store) {
				t.Helper()
				got, err := s.ListNodeMetrics(t.Context(), "old", "build")
				if err != nil || len(got) != len(want) {
					t.Fatalf("historical evidence has no exclusion marker: %+v, %v", got, err)
				}
				for i, w := range want {
					g := got[i]
					if g.Kind != w.Kind || !g.TS.Equal(w.TS) || g.CPUMillicores != w.CPUMillicores || g.MemoryBytes != w.MemoryBytes || g.CPUTime != w.CPUTime || g.OneShot() != w.OneShot() {
						t.Errorf("historical sample %d=%+v; want %+v", i, g, w)
					}
				}
				node, err := s.GetNode(t.Context(), "old", "build")
				if err != nil || node == nil || node.CPUNanos != int64(time.Second) || node.ProcessWallNanos != int64(2*time.Second) || node.MaxRSSBytes != 512 {
					t.Fatalf("exit diagnostics changed: %+v, %v", node, err)
				}
			}
			check(up)
			if err := up.CreateNode(t.Context(), store.Node{RunID: "old", NodeID: "fresh", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			fresh := store.MetricSample{Kind: store.MetricInterval, TS: time.Unix(20, 0), CPUMillicores: 1000, MemoryBytes: 800}
			if err := up.AddNodeMetricSample(t.Context(), "old", "fresh", fresh); err != nil {
				t.Fatal(err)
			}
			if err := up.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := target.Open(t)
			check(reopened)
			got, err := reopened.ListNodeMetrics(t.Context(), "old", "fresh")
			if err != nil || len(got) != 1 || got[0].Kind != store.MetricInterval || !got[0].TS.Equal(fresh.TS) || got[0].CPUMillicores != 1000 || got[0].MemoryBytes != 800 {
				t.Fatalf("fresh evidence changed on reopen: %+v, %v", got, err)
			}
		})
	}
}
