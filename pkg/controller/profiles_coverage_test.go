package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFoldRunProfilesMissingNodeEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "cached", "zero"} {
		t.Run(mode, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			start := time.Now()
			run := store.Run{ID: "r", Pipeline: "coverage", Status: "running", StartedAt: start}
			if err := st.CreateRun(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "c"} {
				if err := st.CreateNode(t.Context(), store.Node{RunID: "r", NodeID: id, Status: "running", StartedAt: &start}); err != nil {
					t.Fatal(err)
				}
				outcome := "success"
				if id == "c" && mode == "cached" {
					outcome = "cached"
				}
				if err := st.FinishNode(t.Context(), "r", id, outcome, "", nil); err != nil {
					t.Fatal(err)
				}
				cpu := int64(100)
				if id == "c" {
					if mode == "missing" || mode == "cached" {
						continue
					}
					cpu = 0
				}
				if err := st.AddNodeMetricSample(t.Context(), "r", id, store.MetricSample{Kind: store.MetricInterval, TS: start, CPUMillicores: cpu, MemoryBytes: 100}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unreadable" {
				if _, err := st.DB().Exec(`UPDATE node_metrics SET memory_bytes='invalid' WHERE run_id='r' AND node_id='c'`); err != nil {
					t.Fatal(err)
				}
				if _, err := st.ListNodeMetrics(t.Context(), "r", "c"); err == nil {
					t.Fatal("corrupt metric read succeeded")
				}
			}
			if mode == "cached" {
				n, err := st.GetNode(t.Context(), "r", "c")
				if err != nil || n.Outcome != "cached" {
					t.Fatalf("cache fixture=%+v, %v", n, err)
				}
			}
			if err := st.UpsertProfilePin(t.Context(), "coverage", "", 2, 1024); err != nil {
				t.Fatal(err)
			}
			tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			New(st, nil).foldRunProfiles(t.Context(), tenant, &run)
			for _, id := range []string{"a"} {
				p, err := st.GetPipelineProfile(t.Context(), "coverage", id)
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != 0.1 || p.PeakMemoryBytes != 100 {
					t.Fatalf("valid node %s profile=%+v", id, p)
				}
			}
			if mode == "zero" {
				p, err := st.GetPipelineProfile(t.Context(), "coverage", "c")
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != 0 || p.PeakMemoryBytes != 100 {
					t.Fatalf("measured-zero node profile=%+v", p)
				}
			}
			p, err := st.GetPipelineProfile(t.Context(), "coverage", "")
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || p.PinnedCores != 2 || p.PinnedMemoryBytes != 1024 {
				t.Fatalf("explicit run pin changed: %+v", p)
			}
			wantRun := mode == "cached"
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
