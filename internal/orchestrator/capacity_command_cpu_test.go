package orchestrator

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfileCommandCPUDoesNotBecomeExitSpike(t *testing.T) {
	st, start := seedUsageRun(t, "command", []usageNode{{id: "build", dur: 10 * time.Second, wall: 10 * time.Second}})
	for i := range 5 {
		if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: store.MetricInterval, TS: start.Add(time.Duration(i) * 2 * time.Second), CPUMillicores: 100, MemoryBytes: 64 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: store.MetricCommand, TS: start.Add(8*time.Second + time.Millisecond), CPUMillicores: 100, CPUTime: time.Second, MemoryBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	samples, err := st.ListNodeMetrics(t.Context(), "r1", "build")
	if err != nil || len(samples) != 6 {
		t.Fatalf("stored samples=%d, want 6: %v", len(samples), err)
	}
	recordRunProfile(t.Context(), localState{st: st}, "command", "r1", nil, "", runCharge{}, false, start, start.Add(10*time.Second))
	p, err := st.GetPipelineProfile(t.Context(), "command", "")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.PeakCores != 0.1 || p.SustainedCores != 0.1 {
		t.Fatalf("command lifetime CPU changed interval rate: %+v", p)
	}
}

func TestRecordRunProfileCommandOnlyBucketsAreNotSampledZeros(t *testing.T) {
	for _, test := range []struct {
		name  string
		rates []int64
		want  float64
	}{
		{"commands only", nil, 0},
		{"measured zero", []int64{0, 0, 0, 0, 0}, 0},
		{"commands and intervals", []int64{100, 100, 100, 400, 400}, 0.4},
	} {
		t.Run(test.name, func(t *testing.T) {
			measured := test.rates != nil
			st, start := seedUsageRun(t, "command", []usageNode{{id: "build", dur: 30 * time.Second, wall: 30 * time.Second}})
			if measured {
				for i, cpu := range test.rates {
					if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: store.MetricInterval, TS: start.Add(time.Duration(i) * 2 * time.Second), CPUMillicores: cpu, MemoryBytes: 64 << 20}); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := range 10 {
				if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: store.MetricCommand, TS: start.Add(time.Duration(i+5) * 2 * time.Second), CPUMillicores: 0, CPUTime: 0, MemoryBytes: 64 << 20}); err != nil {
					t.Fatal(err)
				}
			}
			recordRunProfile(t.Context(), localState{st: st}, "command", "r1", nil, "", runCharge{}, false, start, start.Add(30*time.Second))
			for _, nodeID := range []string{"", "build"} {
				p, err := st.GetPipelineProfile(t.Context(), "command", nodeID)
				if err != nil {
					t.Fatal(err)
				}
				if !measured {
					if p != nil && p.SampleCount != 0 {
						t.Fatalf("command-only %q learned a profile: %+v", nodeID, p)
					}
					continue
				}
				if p == nil || p.SampleCount != 1 || p.SustainedCores != test.want || p.PeakCores != test.want {
					t.Fatalf("command buckets changed %q sustained CPU: %+v", nodeID, p)
				}
			}
		})
	}
}

func TestRecordRunProfileCommandOnlyUpdatesPins(t *testing.T) {
	for _, cores := range []float64{0, 2} {
		st, start := seedUsageRun(t, "command", []usageNode{{id: "build", dur: time.Second}})
		if err := st.UpsertProfilePin(t.Context(), "command", "", 4, 512<<20); err != nil {
			t.Fatal(err)
		}
		if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: store.MetricCommand, TS: start, CPUTime: time.Millisecond, CPUMillicores: 100}); err != nil {
			t.Fatal(err)
		}
		recordRunProfile(t.Context(), localState{st: st}, "command", "r1", &capacity.Pin{Cores: cores}, "", runCharge{}, false, start, start.Add(time.Second))
		p, err := st.GetPipelineProfile(t.Context(), "command", "")
		if err != nil {
			t.Fatal(err)
		}
		if p == nil || p.PinnedCores != cores || p.PinnedMemoryBytes != 0 || p.SampleCount != 0 {
			t.Fatalf("pin update to %v lost: %+v", cores, p)
		}
	}
}

func TestRecordRunProfileUnknownKindPreventsLearning(t *testing.T) {
	st, start := seedUsageRun(t, "unknown", []usageNode{{id: "build", dur: 10 * time.Second, wall: 10 * time.Second}})
	for i, kind := range []store.MetricKind{store.MetricInterval, store.MetricUnknown} {
		if err := st.AddNodeMetricSample(t.Context(), "r1", "build", store.MetricSample{Kind: kind, TS: start.Add(time.Duration(i) * time.Second), CPUMillicores: 100, MemoryBytes: 200}); err != nil {
			t.Fatal(err)
		}
	}
	recordRunProfile(t.Context(), localState{st: st}, "unknown", "r1", nil, "", runCharge{}, false, start, start.Add(10*time.Second))
	for _, nodeID := range []string{"", "build"} {
		p, err := st.GetPipelineProfile(t.Context(), "unknown", nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil && p.SampleCount != 0 {
			t.Fatalf("mixed unknown history learned %q: %+v", nodeID, p)
		}
	}
}

func TestRecordRunProfileCommandOnlyNodePreventsPartialRollup(t *testing.T) {
	st, start := seedUsageRun(t, "partial", []usageNode{
		{id: "sampled", dur: 10 * time.Second, samples: ticks(5, 200, 400)},
		{id: "command-only", dur: 10 * time.Second, samples: []usageSample{command(time.Second, 1000, 600, time.Second)}},
	})
	recordRunProfile(t.Context(), localState{st: st}, "partial", "r1", nil, "", runCharge{}, false, start, start.Add(10*time.Second))
	measured, err := st.GetPipelineProfile(t.Context(), "partial", "sampled")
	if err != nil || measured == nil || measured.SampleCount != 1 {
		t.Fatalf("valid node observation lost: %+v, %v", measured, err)
	}
	for _, node := range []string{"", "command-only"} {
		profile, err := st.GetPipelineProfile(t.Context(), "partial", node)
		if err != nil {
			t.Fatal(err)
		}
		if profile != nil && profile.SampleCount != 0 {
			t.Fatalf("partial CPU coverage learned %q: %+v", node, profile)
		}
	}
}
