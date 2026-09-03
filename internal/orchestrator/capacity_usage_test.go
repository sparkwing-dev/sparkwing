package orchestrator

import (
	"context"
	"math"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type usageSample struct {
	at            time.Duration
	cpuMillicores int64
	memoryBytes   int64
	cpuTime       time.Duration
}

type usageNode struct {
	id      string
	start   time.Duration
	dur     time.Duration
	wall    time.Duration
	samples []usageSample
	cpu     time.Duration
	maxRSS  int64
}

type wantNode struct {
	id        string
	sustained float64
	peak      float64
	peakMem   int64
	duration  time.Duration
	absent    bool
}

func TestRecordRunProfile_PricesMeasuredShapes(t *testing.T) {
	cases := []struct {
		name         string
		hostCores    int
		runWall      time.Duration
		nodes        []usageNode
		wantNodes    []wantNode
		wantSustain  float64
		wantPeak     float64
		wantPeakMem  int64
		wantNoRollup bool
	}{
		{
			name:      "sub-tick node is priced from its exit accounting alone",
			hostCores: 1,
			runWall:   time.Second,
			nodes: []usageNode{{
				id: "quick", dur: 500 * time.Millisecond, wall: 500 * time.Millisecond,
				cpu: 300 * time.Millisecond, maxRSS: 256 << 20,
			}},
			wantNodes:   []wantNode{{id: "quick", sustained: 0.6, peak: 0.6, peakMem: 256 << 20}},
			wantSustain: 0.3,
			wantPeak:    0.3,
			wantPeakMem: 256 << 20,
		},
		{
			name:      "process startup does not price a trivial node at host capacity",
			hostCores: 1,
			runWall:   200 * time.Millisecond,
			nodes: []usageNode{{
				id: "trivial", dur: 2400 * time.Microsecond, wall: 91 * time.Millisecond,
				cpu: 23800 * time.Microsecond, maxRSS: 32 << 20,
			}},
			wantNodes: []wantNode{{
				id: "trivial", sustained: 23.8 / 91.0, peak: 23.8 / 91.0,
				peakMem: 32 << 20, duration: 91 * time.Millisecond,
			}},
			wantSustain: 23.8 / 200.0,
			wantPeak:    23.8 / 200.0,
			wantPeakMem: 32 << 20,
		},
		{
			name:      "measured mean outranks the sampled plateau",
			hostCores: 1,
			runWall:   10 * time.Second,
			nodes: []usageNode{{
				id: "grind", dur: 10 * time.Second, wall: 10 * time.Second,
				samples: ticks(5, 500, 1<<30),
				cpu:     8 * time.Second, maxRSS: 1 << 30,
			}},
			wantNodes:   []wantNode{{id: "grind", sustained: 0.8, peak: 0.8, peakMem: 1 << 30}},
			wantSustain: 0.8,
			wantPeak:    0.8,
			wantPeakMem: 1 << 30,
		},
		{
			name:      "fan-out of four rolls up to the sum of its interleaved samples",
			hostCores: 4,
			runWall:   6 * time.Second,
			nodes: []usageNode{
				fanNode("fan-a", 0),
				fanNode("fan-b", 137*time.Microsecond),
				fanNode("fan-c", 291*time.Microsecond),
				fanNode("fan-d", 663*time.Microsecond),
			},
			wantNodes: []wantNode{
				{id: "fan-a", sustained: 1.0, peak: 1.0, peakMem: 512 << 20},
				{id: "fan-d", sustained: 1.0, peak: 1.0, peakMem: 512 << 20},
			},
			wantSustain: 4.0,
			wantPeak:    4.0,
			wantPeakMem: 2 << 30,
		},
		{
			name:      "sequential commands in one window integrate rather than sum",
			hostCores: 2,
			runWall:   2 * time.Second,
			nodes: []usageNode{{
				id: "serial", dur: 2 * time.Second, wall: 2 * time.Second,
				samples: []usageSample{
					command(0, 2000, 512<<20, 800*time.Millisecond),
					command(400*time.Millisecond, 2000, 512<<20, 800*time.Millisecond),
					command(800*time.Millisecond, 2000, 512<<20, 800*time.Millisecond),
					command(1200*time.Millisecond, 2000, 512<<20, 800*time.Millisecond),
				},
			}},

			wantNodes:   []wantNode{{id: "serial", sustained: 2.0, peak: 2.0, peakMem: 512 << 20}},
			wantSustain: 1.6,
			wantPeak:    1.6,
			wantPeakMem: 512 << 20,
		},
		{
			name:      "concurrent commands in one window still sum",
			hostCores: 2,
			runWall:   2 * time.Second,
			nodes: []usageNode{
				{
					id: "cmd-a", dur: 2 * time.Second, wall: 2 * time.Second,
					samples: []usageSample{command(0, 1000, 256<<20, 2*time.Second)},
				},
				{
					id: "cmd-b", dur: 2 * time.Second, wall: 2 * time.Second,
					samples: []usageSample{command(3*time.Microsecond, 1000, 256<<20, 2*time.Second)},
				},
			},
			wantNodes: []wantNode{
				{id: "cmd-a", sustained: 1.0, peak: 1.0, peakMem: 256 << 20},
				{id: "cmd-b", sustained: 1.0, peak: 1.0, peakMem: 256 << 20},
			},
			wantSustain: 2.0,
			wantPeak:    2.0,
			wantPeakMem: 512 << 20,
		},
		{
			name:      "a window adds its tick to one command mark, not to every one",
			hostCores: 1,
			runWall:   2 * time.Second,
			nodes: []usageNode{{
				id: "mixed", dur: 2 * time.Second, wall: 2 * time.Second,
				samples: []usageSample{
					{at: 0, cpuMillicores: 100, memoryBytes: 128 << 20},
					command(500*time.Millisecond, 500, 512<<20, 200*time.Millisecond),
					command(1200*time.Millisecond, 500, 512<<20, 200*time.Millisecond),
				},
			}},
			wantNodes:   []wantNode{{id: "mixed", sustained: 0.5, peak: 0.5, peakMem: 512 << 20}},
			wantSustain: 0.3,
			wantPeak:    0.3,
			wantPeakMem: 640 << 20,
		},
		{
			name:      "peak memory comes from the kernel high-water mark",
			hostCores: 1,
			runWall:   4 * time.Second,
			nodes: []usageNode{{
				id: "spike", dur: 4 * time.Second, wall: 4 * time.Second,
				samples: ticks(2, 250, 1<<30),
				cpu:     time.Second, maxRSS: 3 << 30,
			}},
			wantNodes:   []wantNode{{id: "spike", sustained: 0.25, peak: 0.25, peakMem: 3 << 30}},
			wantSustain: 0.25,
			wantPeak:    0.25,
			wantPeakMem: 3 << 30,
		},
		{
			name:      "a node without exit accounting prices from samples alone",
			hostCores: 2,
			runWall:   10 * time.Second,
			nodes: []usageNode{{
				id: "pod", dur: 10 * time.Second,
				samples: []usageSample{
					{at: 0, cpuMillicores: 500, memoryBytes: 1 << 30},
					{at: 2 * time.Second, cpuMillicores: 500, memoryBytes: 1 << 30},
					{at: 4 * time.Second, cpuMillicores: 500, memoryBytes: 1 << 30},
					{at: 6 * time.Second, cpuMillicores: 500, memoryBytes: 1 << 30},
					{at: 8 * time.Second, cpuMillicores: 2000, memoryBytes: 2 << 30},
				},
			}},
			wantNodes:   []wantNode{{id: "pod", sustained: 0.8, peak: 2.0, peakMem: 2 << 30}},
			wantSustain: 0.8,
			wantPeak:    2.0,
			wantPeakMem: 2 << 30,
		},
		{
			name:         "an unmeasured node records nothing",
			hostCores:    1,
			runWall:      time.Second,
			nodes:        []usageNode{{id: "ghost", dur: time.Second}},
			wantNodes:    []wantNode{{id: "ghost", absent: true}},
			wantNoRollup: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.NumCPU() < tc.hostCores {
				t.Skipf("host cannot hold a %d-core reading", tc.hostCores)
			}
			st, start := seedUsageRun(t, "priced", tc.nodes)
			ctx := context.Background()

			recordRunProfile(ctx, localState{st: st}, "priced", "r1", nil, "", runCharge{}, false, start, start.Add(tc.runWall))

			for _, want := range tc.wantNodes {
				prof, err := st.GetPipelineProfile(ctx, "priced", want.id)
				if err != nil {
					t.Fatalf("node %q profile: %v", want.id, err)
				}
				if want.absent {
					if prof != nil {
						t.Errorf("node %q recorded a profile (%+v); want none", want.id, prof)
					}
					continue
				}
				if prof == nil {
					t.Fatalf("node %q profile missing", want.id)
				}
				assertCores(t, "node "+want.id+" SustainedCores", prof.SustainedCores, want.sustained)
				assertCores(t, "node "+want.id+" PeakCores", prof.PeakCores, want.peak)
				if prof.PeakMemoryBytes != want.peakMem {
					t.Errorf("node %q PeakMemoryBytes = %d, want %d", want.id, prof.PeakMemoryBytes, want.peakMem)
				}
				if want.duration != 0 && prof.P50Duration != want.duration {
					t.Errorf("node %q P50Duration = %s, want the process's whole life %s",
						want.id, prof.P50Duration, want.duration)
				}
			}

			rollup, err := st.GetPipelineProfile(ctx, "priced", "")
			if err != nil {
				t.Fatalf("rollup profile: %v", err)
			}
			if tc.wantNoRollup {
				if rollup != nil {
					t.Errorf("rollup recorded (%+v); want none", rollup)
				}
				return
			}
			if rollup == nil {
				t.Fatal("rollup profile missing")
			}
			assertCores(t, "rollup SustainedCores", rollup.SustainedCores, tc.wantSustain)
			assertCores(t, "rollup PeakCores", rollup.PeakCores, tc.wantPeak)
			if rollup.PeakMemoryBytes != tc.wantPeakMem {
				t.Errorf("rollup PeakMemoryBytes = %d, want %d", rollup.PeakMemoryBytes, tc.wantPeakMem)
			}
		})
	}
}

func ticks(n int, cpuMillicores, memoryBytes int64) []usageSample {
	out := make([]usageSample, n)
	for i := range out {
		out[i] = usageSample{
			at:            time.Duration(i) * nodemetrics.Interval(),
			cpuMillicores: cpuMillicores,
			memoryBytes:   memoryBytes,
		}
	}
	return out
}

func command(at time.Duration, cpuMillicores, memoryBytes int64, cpu time.Duration) usageSample {
	return usageSample{at: at, cpuMillicores: cpuMillicores, memoryBytes: memoryBytes, cpuTime: cpu}
}

func fanNode(id string, jitter time.Duration) usageNode {
	samples := ticks(3, 1000, 512<<20)
	for i := range samples {
		samples[i].at += jitter
	}
	return usageNode{id: id, dur: 6 * time.Second, samples: samples}
}

func seedUsageRun(t *testing.T, pipeline string, nodes []usageNode) (*store.Store, time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	start := time.Now().Truncate(nodemetrics.Interval())
	if err := st.CreateRun(ctx, store.Run{ID: "r1", Pipeline: pipeline, Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: n.id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(
			`UPDATE nodes SET status = 'done', outcome = 'success', started_at = ?, finished_at = ? WHERE run_id = 'r1' AND node_id = ?`,
			start.Add(n.start).UnixNano(), start.Add(n.start+n.dur).UnixNano(), n.id); err != nil {
			t.Fatal(err)
		}
		for _, s := range n.samples {
			if err := st.AddNodeMetricSample(ctx, "r1", n.id, store.MetricSample{
				TS:            start.Add(n.start + s.at),
				CPUMillicores: s.cpuMillicores,
				MemoryBytes:   s.memoryBytes,
				CPUTime:       s.cpuTime,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if n.cpu == 0 && n.maxRSS == 0 && n.wall == 0 {
			continue
		}
		if err := st.AddNodeUsage(ctx, "r1", n.id, store.NodeUsage{
			CPUTime: n.cpu, MaxRSSBytes: n.maxRSS, Wall: n.wall,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return st, start
}

func assertCores(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func TestRecordRunProfile_SubTickNodesAreNoLongerInvisible(t *testing.T) {
	st, start := seedUsageRun(t, "brief", []usageNode{
		{
			id: "a", dur: 400 * time.Millisecond, wall: 400 * time.Millisecond,
			cpu: 200 * time.Millisecond, maxRSS: 64 << 20,
		},
		{
			id: "b", start: 400 * time.Millisecond, dur: 400 * time.Millisecond,
			wall: 400 * time.Millisecond, cpu: 200 * time.Millisecond, maxRSS: 64 << 20,
		},
	})
	ctx := context.Background()

	recordRunProfile(ctx, localState{st: st}, "brief", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))

	for _, id := range []string{"a", "b"} {
		prof, err := st.GetPipelineProfile(ctx, "brief", id)
		if err != nil || prof == nil {
			t.Fatalf("node %q profile missing: %v", id, err)
		}
		assertCores(t, "node "+id+" SustainedCores", prof.SustainedCores, 0.5)
	}
	rollup, err := st.GetPipelineProfile(ctx, "brief", "")
	if err != nil || rollup == nil {
		t.Fatalf("rollup missing: %v", err)
	}

	assertCores(t, "rollup SustainedCores", rollup.SustainedCores, 0.4)
	assertCores(t, "rollup PeakCores", rollup.PeakCores, 0.4)
	if rollup.PeakMemoryBytes != 64<<20 {
		t.Errorf("rollup PeakMemoryBytes = %d, want the heaviest node's 64MiB mark", rollup.PeakMemoryBytes)
	}
}

func TestRecordRunProfile_RetriedNodeIsPricedOnEveryAttempt(t *testing.T) {
	st, start := seedUsageRun(t, "retried", nil)
	ctx := context.Background()
	if err := st.CreateNode(ctx, store.Node{RunID: "r1", NodeID: "flaky", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []store.NodeUsage{
		{CPUTime: 500 * time.Millisecond, MaxRSSBytes: 128 << 20, Wall: time.Second},
		{CPUTime: 500 * time.Millisecond, MaxRSSBytes: 64 << 20, Wall: time.Second},
	} {
		if err := st.AddNodeUsage(ctx, "r1", "flaky", attempt); err != nil {
			t.Fatal(err)
		}
	}

	recordRunProfile(ctx, localState{st: st}, "retried", "r1", nil, "", runCharge{}, false, start, start.Add(2*time.Second))

	prof, err := st.GetPipelineProfile(ctx, "retried", "flaky")
	if err != nil || prof == nil {
		t.Fatalf("node profile missing: %v", err)
	}
	assertCores(t, "node SustainedCores", prof.SustainedCores, 0.5)
	if prof.PeakMemoryBytes != 128<<20 {
		t.Errorf("PeakMemoryBytes = %d, want the 128MiB high-water across attempts", prof.PeakMemoryBytes)
	}
	if prof.P50Duration != 2*time.Second {
		t.Errorf("P50Duration = %s, want both attempts' occupancy", prof.P50Duration)
	}
}
