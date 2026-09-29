package orchestrator

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRecordRunProfileRequiresCompletedContinuousExecution(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"failed", `UPDATE nodes SET outcome = 'failed' WHERE node_id = 'build'`},
		{"cancelled", `UPDATE nodes SET outcome = 'cancelled' WHERE node_id = 'build'`},
		{"unfinished", `UPDATE nodes SET status = 'running', outcome = '' WHERE node_id = 'build'`},
		{"successful retry", `UPDATE nodes SET attempts_consumed = 2 WHERE node_id = 'build'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, start := seedUsageRun(t, "completion", []usageNode{
				{id: "build", dur: time.Second, samples: ticks(1, 1000, 100)},
				{id: "good", dur: time.Second, samples: ticks(1, 1000, 100)},
			})
			if _, err := st.DB().ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			recordRunProfile(t.Context(), localState{st: st}, "completion", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))
			for _, node := range []string{"", "build", "good"} {
				profile, err := st.GetPipelineProfile(t.Context(), "completion", node)
				if err != nil {
					t.Fatal(err)
				}
				if node == "good" {
					if profile == nil || profile.SampleCount != 1 {
						t.Fatalf("independent successful node lost: %+v", profile)
					}
				} else if profile != nil && profile.SampleCount != 0 {
					t.Fatalf("incomplete execution learned for %q: %+v", node, profile)
				}
			}
		})
	}
}

func TestRecordRunProfileDoesNotAddCommandMemoryToTreeSample(t *testing.T) {
	st, start := seedUsageRun(t, "tree-memory", []usageNode{{id: "build", dur: time.Second, samples: []usageSample{
		{kind: store.MetricInterval, at: 0, cpuMillicores: 1000, memoryBytes: 400},
		{kind: store.MetricCommand, at: time.Nanosecond, memoryBytes: 300, cpuTime: time.Millisecond},
	}}})
	recordRunProfile(t.Context(), localState{st: st}, "tree-memory", "r1", nil, "", runCharge{}, false, start, start.Add(time.Second))
	for _, node := range []string{"", "build"} {
		profile, err := st.GetPipelineProfile(t.Context(), "tree-memory", node)
		if err != nil || profile == nil || profile.SampleCount != 1 || profile.PeakMemoryBytes != 400 {
			t.Fatalf("profile for %q = %+v, %v; want 400 bytes", node, profile, err)
		}
	}
}

func TestRecordRunProfileCountsEachNodeOncePerBucket(t *testing.T) {
	for _, nodes := range []int{1, 2} {
		t.Run(fmt.Sprint(nodes), func(t *testing.T) {
			start := time.Unix(1700000000, 0)
			end := start.Add(time.Second)
			st := &profileCapture{samples: map[string][]store.MetricSample{}, observations: map[string]store.ProfileObservation{}}
			for i := 0; i < nodes; i++ {
				id := fmt.Sprint(i)
				st.nodes = append(st.nodes, &store.Node{NodeID: id, Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end})
				st.samples[id] = []store.MetricSample{
					{Kind: store.MetricInterval, TS: start, CPUMillicores: 500, MemoryBytes: 100},
					{Kind: store.MetricInterval, TS: start.Add(time.Millisecond), CPUMillicores: 200, MemoryBytes: 80},
				}
			}
			recordRunProfile(t.Context(), st, "demo", "run", nil, "", runCharge{}, false, start, end)
			got, ok := st.observations[""]
			if !ok || got.PeakCores != float64(nodes)*0.5 || got.PeakMemoryBytes != int64(nodes)*100 {
				t.Fatalf("sequential readings added: %+v; want %g cores and %d bytes", got, float64(nodes)*0.5, nodes*100)
			}
		})
	}
}

func TestRecordRunProfileCommandMemoryDoesNotOverflowTreeMemory(t *testing.T) {
	start := time.Unix(1700000000, 0)
	end := start.Add(time.Second)
	st := &profileCapture{
		nodes:        []*store.Node{{NodeID: "build", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end}},
		observations: map[string]store.ProfileObservation{},
		samples: map[string][]store.MetricSample{"build": {
			{Kind: store.MetricInterval, TS: start, CPUMillicores: 500, MemoryBytes: math.MaxInt64},
			{Kind: store.MetricCommand, TS: start.Add(time.Nanosecond), MemoryBytes: math.MaxInt64, CPUTime: time.Nanosecond},
			{Kind: store.MetricCommand, TS: start.Add(2 * time.Nanosecond), MemoryBytes: math.MaxInt64, CPUTime: time.Nanosecond},
		}},
	}
	recordRunProfile(t.Context(), st, "demo", "run", nil, "", runCharge{}, false, start, end)
	for _, id := range []string{"", "build"} {
		got, ok := st.observations[id]
		if !ok || got.PeakMemoryBytes != math.MaxInt64 || got.PeakCores != 0.5 {
			t.Fatalf("overlapping command readings changed %q tree profile: %+v", id, got)
		}
	}
}

func TestRecordRunProfileCountsAttemptsWithinSourceRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		runs  []string
		learn bool
	}{
		{"one attempt", []string{"run"}, true},
		{"two source attempts", []string{"run", "run"}, false},
		{"separate runs", []string{"previous", "run"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(100, 0)
			end := start.Add(time.Second)
			node := &store.Node{RunID: "run", NodeID: "build", Status: "done", Outcome: "success", StartedAt: &start, FinishedAt: &end}
			for i, run := range tc.runs {
				node.ExecutionAttempts = append(node.ExecutionAttempts, store.ExecutionAttempt{RunID: run, NodeID: "build", Attempt: i + 1})
			}
			captured := &profileCapture{nodes: []*store.Node{node}, observations: map[string]store.ProfileObservation{}, samples: map[string][]store.MetricSample{"build": {{Kind: store.MetricInterval, TS: end, CPUMillicores: 500, MemoryBytes: 123}}}}
			recordRunProfile(t.Context(), captured, "example", "run", nil, "", runCharge{}, false, start, end)
			for _, id := range []string{"", "build"} {
				got, ok := captured.observations[id]
				if ok != tc.learn || (ok && (!got.CPUMeasured || got.PeakCores != 0.5 || got.PeakMemoryBytes != 123)) {
					t.Errorf("profile %q=%+v recorded=%t, want recorded=%t", id, got, ok, tc.learn)
				}
			}
		})
	}
}
