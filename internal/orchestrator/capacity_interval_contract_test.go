package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestProfileSustainedRequiresIntervalCPU(t *testing.T) {
	idle := store.MetricSample{Kind: store.MetricInterval, CPUAvailable: true, MemoryAvailable: true, MemoryBytes: 64 << 20}
	command := store.MetricSample{Kind: store.MetricCommand, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, CPUTime: 120 * time.Second, MemoryBytes: 64 << 20}
	plateau := make([]store.MetricSample, 10)
	for i := range plateau {
		plateau[i] = idle
	}
	spike := append([]store.MetricSample(nil), plateau...)
	spike[9].CPUMillicores = 1000
	cpuGap := append([]store.MetricSample(nil), plateau...)
	cpuGap[9].CPUAvailable = false
	cpuGap[9].CPUMillicores = 1000
	memoryGap := append([]store.MetricSample(nil), plateau...)
	memoryGap[9].MemoryAvailable = false
	for _, tc := range []struct {
		name        string
		samples     []store.MetricSample
		exitCPU     time.Duration
		wantProfile bool
		peak        float64
	}{
		{name: "command total", samples: []store.MetricSample{command}},
		{name: "exit total", exitCPU: 60 * time.Second},
		{name: "observed zero", samples: plateau, wantProfile: true},
		{name: "percentile differs from mean", samples: spike, wantProfile: true, peak: 1},
		{name: "command cannot change measured intervals", samples: append(append([]store.MetricSample(nil), plateau...), command), wantProfile: true},
		{name: "known CPU gap", samples: cpuGap},
		{name: "known memory gap", samples: memoryGap},
		{name: "shared estimate", samples: []store.MetricSample{{Kind: store.MetricEstimate, CPUAvailable: true, MemoryAvailable: true, CPUMillicores: 1000, MemoryBytes: 64 << 20}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, start := seedUsageRun(t, "interval-contract", []usageNode{{id: "work", dur: 120 * time.Second, wall: 120 * time.Second, cpu: tc.exitCPU}})
			ctx := context.Background()
			for i, sample := range tc.samples {
				sample.TS = start.Add(time.Duration(i) * 2 * time.Second)
				if err := st.AddNodeMetricSample(ctx, "r1", "work", sample); err != nil {
					t.Fatal(err)
				}
			}
			recordRunProfile(ctx, localState{st: st}, "interval-contract", "r1", nil, "", runCharge{}, false, start, start.Add(120*time.Second))
			for _, nodeID := range []string{"work", ""} {
				profile, err := st.GetPipelineProfile(ctx, "interval-contract", nodeID)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.wantProfile {
					if profile != nil {
						t.Errorf("%q incomplete or non-interval evidence became a profile: %+v", nodeID, profile)
					}
					continue
				}
				if profile == nil {
					t.Fatalf("%q measured profile missing", nodeID)
				}
				if !profile.CPUMeasured || profile.SustainedCores == nil || *profile.SustainedCores != 0 || profile.PeakCores != tc.peak || profile.PeakMemoryBytes != 64<<20 {
					t.Errorf("%q sampled quantities changed: %+v", nodeID, profile)
				}
			}
		})
	}
}
