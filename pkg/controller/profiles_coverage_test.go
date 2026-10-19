package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestFoldRunProfilesMissingNodeEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "memoized", "zero"} {
		t.Run(mode, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			start := time.Now()
			run := store.Run{ID: "r", Pipeline: "coverage", Status: "running", StartedAt: start}
			if err := teststore.SeedRun(st, t.Context(), store.DefaultTeam, run); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "c"} {
				if err := teststore.SeedNode(st, t.Context(), store.Node{RunID: "r", NodeID: id, Status: "running", StartedAt: &start}); err != nil {
					t.Fatal(err)
				}
				outcome := "success"
				if id == "c" && mode == "memoized" {
					outcome = "memoized"
				}
				if err := teststore.SeedNodeFinish(st, t.Context(), store.DefaultTeam, store.NodeRef{RunID: "r", NodeID: id}, store.NodeResult{Outcome: store.NodeState(outcome), MaskedError: ""}); err != nil {
					t.Fatal(err)
				}
				cpu := int64(100)
				if id == "c" {
					if mode == "missing" || mode == "memoized" {
						continue
					}
					cpu = 0
				}
				if err := teststore.SeedMetric(teststore.SQL(t, st), t.Context(), "r", id, store.MetricSample{Kind: store.MetricInterval, TS: start, CPUMillicores: cpu, MemoryBytes: 100}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unreadable" {
				if _, err := teststore.SQL(t, st).Exec(`UPDATE node_metrics SET memory_bytes='invalid' WHERE run_id='r' AND node_id='c'`); err != nil {
					t.Fatal(err)
				}
				if _, err := teststore.ForTeam(t, st, store.DefaultTeam).ListNodeMetrics(t.Context(), "r", "c"); err == nil {
					t.Fatal("corrupt metric read succeeded")
				}
			}
			if mode == "memoized" {
				n, err := teststore.ForTeam(t, st, store.DefaultTeam).GetNode(t.Context(), "r", "c")
				if err != nil || n.Outcome != "memoized" {
					t.Fatalf("cache fixture=%+v, %v", n, err)
				}
			}
			if err := teststore.ForTeam(t, st, store.DefaultTeam).UpsertProfilePin(t.Context(), "coverage", "", 2, 1024); err != nil {
				t.Fatal(err)
			}
			tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unreadable" {
				if _, err := runProfileObservations(t.Context(), tenant, &run); err == nil {
					t.Fatal("corrupt metrics did not fail the fold")
				}
				if _, err := teststore.SQL(t, st).Exec(`UPDATE node_metrics SET memory_bytes=100 WHERE run_id='r' AND node_id='c'`); err != nil {
					t.Fatal(err)
				}
			}
			foldProfilesForTest(t, tenant, &run)
			for _, id := range []string{"a"} {
				p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(t.Context(), "coverage", id)
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != 0.1 || p.PeakMemoryBytes != 100 {
					t.Fatalf("valid node %s profile=%+v", id, p)
				}
			}
			if mode == "zero" {
				p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(t.Context(), "coverage", "c")
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != 0 || p.PeakMemoryBytes != 100 {
					t.Fatalf("measured-zero node profile=%+v", p)
				}
			}
			p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(t.Context(), "coverage", "")
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || p.PinnedCores != 2 || p.PinnedMemoryBytes != 1024 {
				t.Fatalf("explicit run pin changed: %+v", p)
			}
			wantRun := mode == "memoized"
			if wantRun {
				if p == nil || p.SampleCount != 1 {
					t.Fatalf("complete evidence produced no run observation: %+v", p)
				}
			} else if p != nil && p.SampleCount != 0 {
				t.Fatalf("partial run learned a profile: %+v", p)
			}
		})
	}
}
