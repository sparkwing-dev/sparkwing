package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type metricCaptureBackend struct {
	StateBackend
	sample store.MetricSample
}

func (b *metricCaptureBackend) AddNodeMetricSample(_ context.Context, _, _ string, sample store.MetricSample) error {
	b.sample = sample
	return nil
}

func TestStateMetricsSinkPreservesAvailability(t *testing.T) {
	for _, tc := range []struct {
		name        string
		valid       bool
		cpu, memory int64
		kind        store.MetricKind
	}{
		{"unavailable", false, 0, 100, store.MetricUnknown},
		{"measured zero", true, 0, 100, store.MetricInterval},
		{"measured busy", true, 1000, 100, store.MetricInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &metricCaptureBackend{}
			sink := stateMetricsSink{backend: backend, runID: "run", nodeID: "node"}
			ts := time.Unix(100, 0)
			if err := sink.Push(t.Context(), nodemetrics.Sample{Valid: tc.valid, TS: ts, CPUMillicores: tc.cpu, MemoryBytes: tc.memory}); err != nil {
				t.Fatal(err)
			}
			got := backend.sample
			if got.Kind != tc.kind || got.CPUMillicores != tc.cpu || got.MemoryBytes != tc.memory || !got.TS.Equal(ts) {
				t.Fatalf("stored sample = %+v; want kind %q, CPU %d, memory %d, time %s", got, tc.kind, tc.cpu, tc.memory, ts)
			}
		})
	}
}

func TestUnavailableSampleSurvivesRecoveryAndExitUsage(t *testing.T) {
	for _, contended := range []bool{false, true} {
		st, start := seedUsageRun(t, "availability", []usageNode{{id: "build", dur: 10 * time.Second, wall: 10 * time.Second}})
		sink := stateMetricsSink{backend: localState{st: st}, runID: "r1", nodeID: "build"}
		for i, valid := range []bool{true, false, true} {
			if err := sink.Push(t.Context(), nodemetrics.Sample{Valid: valid, TS: start.Add(time.Duration(i) * time.Second), CPUMillicores: 1000, MemoryBytes: 100}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.AddNodeUsage(t.Context(), "r1", "build", store.NodeUsage{CPUTime: time.Second, MaxRSSBytes: 100, Wall: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		samples, err := st.ListNodeMetrics(t.Context(), "r1", "build")
		if err != nil || len(samples) != 3 || samples[1].Kind != store.MetricUnknown || samples[2].Kind != store.MetricInterval {
			t.Fatalf("stored sequence=%+v,%v", samples, err)
		}
		recordRunProfile(t.Context(), localState{st: st}, "availability", "r1", &capacity.Pin{Cores: 2, MemoryBytes: 200}, "", runCharge{}, contended, start, start.Add(10*time.Second))
		for _, node := range []string{"", "build"} {
			profile, err := st.GetPipelineProfile(t.Context(), "availability", node)
			if err != nil {
				t.Fatal(err)
			}
			if profile != nil && (profile.SampleCount != 0 || profile.FloorCores != 0 || profile.FloorMemoryBytes != 0) {
				t.Fatalf("contended=%t node=%q learned incomplete series: %+v", contended, node, profile)
			}
			if node == "" && (profile == nil || profile.PinnedCores != 2 || profile.PinnedMemoryBytes != 200) {
				t.Fatalf("pin changed: %+v", profile)
			}
		}
	}
}

func TestRecordRunProfileUsesOriginatingCPUAvailability(t *testing.T) {
	st, start := seedUsageRun(t, "origin", []usageNode{{id: "build", dur: time.Second, samples: ticks(1, 0, 100)}})
	recordRunProfile(t.Context(), localState{st: st}, "origin", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))
	for _, node := range []string{"", "build"} {
		p, err := st.GetPipelineProfile(t.Context(), "origin", node)
		if err != nil || p == nil || !p.CPUMeasured || p.SampleCount != 1 || p.PeakCores != 0 {
			t.Fatalf("originating measured zero lost for %q: %+v, %v", node, p, err)
		}
	}
}
