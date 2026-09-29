package orchestrator

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfileMissingNodeEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "cached", "zero", "exit"} {
		t.Run(mode, func(t *testing.T) {
			st, start := seedUsageRun(t, "coverage", []usageNode{
				{id: "a", dur: time.Second, samples: ticks(1, 100, 100)},
				{id: "b", dur: time.Second, samples: ticks(1, 100, 100)},
				{id: "c", dur: time.Second},
			})
			switch mode {
			case "cached":
				if _, err := st.DB().Exec(`UPDATE nodes SET outcome='cached' WHERE run_id='r1' AND node_id='c'`); err != nil {
					t.Fatal(err)
				}
			case "zero", "unreadable":
				if err := st.AddNodeMetricSample(t.Context(), "r1", "c", store.MetricSample{Kind: store.MetricInterval, TS: start, MemoryBytes: 100}); err != nil {
					t.Fatal(err)
				}
				if mode == "unreadable" {
					if _, err := st.DB().Exec(`UPDATE node_metrics SET memory_bytes='invalid' WHERE run_id='r1' AND node_id='c'`); err != nil {
						t.Fatal(err)
					}
					if _, err := st.ListNodeMetrics(t.Context(), "r1", "c"); err == nil {
						t.Fatal("corrupt metric read succeeded")
					}
				}
			case "exit":
				if err := st.AddNodeUsage(t.Context(), "r1", "c", store.NodeUsage{CPUTime: 100 * time.Millisecond, MaxRSSBytes: 100, Wall: time.Second}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cached" {
				n, err := st.GetNode(t.Context(), "r1", "c")
				if err != nil || n.Outcome != "cached" {
					t.Fatalf("cache fixture = %+v, %v", n, err)
				}
			}
			recordRunProfile(t.Context(), localState{st: st}, "coverage", "r1", &capacity.Pin{Cores: 2, MemoryBytes: 1024}, "", runCharge{}, false, start, start.Add(time.Second))
			for _, id := range []string{"a", "b"} {
				p, err := st.GetPipelineProfile(t.Context(), "coverage", id)
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != 0.1 || p.PeakMemoryBytes != 100 {
					t.Fatalf("valid node %s profile=%+v", id, p)
				}
			}
			if mode == "zero" || mode == "exit" {
				wantCPU := 0.0
				if mode == "exit" {
					wantCPU = 0.1
				}
				p, err := st.GetPipelineProfile(t.Context(), "coverage", "c")
				if err != nil {
					t.Fatal(err)
				}
				if p == nil || p.SampleCount != 1 || p.PeakCores != wantCPU || p.PeakMemoryBytes != 100 {
					t.Fatalf("node c profile=%+v", p)
				}
			}
			p, err := st.GetPipelineProfile(t.Context(), "coverage", "")
			if err != nil {
				t.Fatal(err)
			}
			if p == nil || p.PinnedCores != 2 || p.PinnedMemoryBytes != 1024 {
				t.Fatalf("explicit run pin changed: %+v", p)
			}
			wantRun := mode == "cached" || mode == "zero" || mode == "exit"
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

func TestRecordRunProfileMissingEvidencePreservesPinWithoutFloor(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		st, start := seedUsageRun(t, "coverage", []usageNode{{id: "a", dur: time.Second, samples: ticks(1, 100, 100)}, {id: "b", dur: time.Second}})
		if unreadable {
			if err := st.AddNodeMetricSample(t.Context(), "r1", "b", store.MetricSample{Kind: store.MetricInterval, TS: start, MemoryBytes: 100}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`UPDATE node_metrics SET memory_bytes='invalid' WHERE node_id='b'`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ListNodeMetrics(t.Context(), "r1", "b"); err == nil {
				t.Fatal("corrupt metric read succeeded")
			}
		}
		recordRunProfile(t.Context(), localState{st: st}, "coverage", "r1", &capacity.Pin{Cores: 2, MemoryBytes: 1024}, "", runCharge{Cores: 2, MemoryBytes: 1024}, true, start, start.Add(time.Second))
		p, err := st.GetPipelineProfile(t.Context(), "coverage", "")
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.PinnedCores != 2 || p.PinnedMemoryBytes != 1024 || p.SampleCount != 0 || p.FloorCores != 0 || p.FloorMemoryBytes != 0 {
			t.Fatalf("partial contended run changed pin or learned floor: %+v", p)
		}
	}
}
