package controller

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFoldRunProfilesRequiresSingleContributingNode(t *testing.T) {
	for _, mode := range []string{"overlap", "sequential", "clock-skew", "single", "cached"} {
		t.Run(mode, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			start := time.Unix(1700000000, 0)
			finish := start.Add(time.Minute)
			run := store.Run{ID: "r", Pipeline: "aggregate", Status: "success", StartedAt: start, FinishedAt: &finish}
			if err := st.CreateRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertProfilePin(ctx, "aggregate", "", 4, 400); err != nil {
				t.Fatal(err)
			}
			if err := st.RecordWaitObservation(ctx, "aggregate", time.Second); err != nil {
				t.Fatal(err)
			}
			before, err := st.GetPipelineProfile(ctx, "aggregate", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				if id == "b" && mode == "single" {
					continue
				}
				begin, end := start, start.Add(10*time.Second)
				if id == "b" && mode == "sequential" {
					begin, end = start.Add(20*time.Second), start.Add(30*time.Second)
				}
				outcome := "success"
				if id == "b" && mode == "cached" {
					outcome = "cached"
				}
				if err := st.CreateNode(ctx, store.Node{RunID: "r", NodeID: id, Status: "running"}); err != nil {
					t.Fatal(err)
				}
				if err := st.FinishNode(ctx, "r", id, outcome, "", nil); err != nil {
					t.Fatal(err)
				}
				if _, err := st.DB().Exec(`UPDATE nodes SET started_at=?, finished_at=? WHERE run_id=? AND node_id=?`, begin.UnixNano(), end.UnixNano(), "r", id); err != nil {
					t.Fatal(err)
				}
				n, err := st.GetNode(ctx, "r", id)
				if err != nil || n == nil || n.Outcome != outcome || n.StartedAt == nil || n.FinishedAt == nil || !n.StartedAt.Equal(begin) || !n.FinishedAt.Equal(end) {
					t.Fatalf("node fixture differs: %+v, %v", n, err)
				}
				if outcome == "cached" {
					continue
				}
				stamp := begin
				if id == "b" && mode == "clock-skew" {
					stamp = stamp.Add(24 * time.Hour)
				}
				for i := range 2 {
					if err := st.AddNodeMetricSample(ctx, "r", id, store.MetricSample{Kind: store.MetricInterval, TS: stamp.Add(time.Duration(i) * time.Second), CPUMillicores: 1000, MemoryBytes: 100}); err != nil {
						t.Fatal(err)
					}
				}
			}
			New(st, nil).foldRunProfiles(ctx, &run)
			for _, id := range []string{"a", "b"} {
				if id == "b" && (mode == "single" || mode == "cached") {
					continue
				}
				p, err := st.GetPipelineProfile(ctx, "aggregate", id)
				if err != nil || p == nil || p.SampleCount != 1 || !p.CPUMeasured || p.PeakCores != 1 || p.PeakMemoryBytes != 100 {
					t.Fatalf("node %s measurement lost: %+v, %v", id, p, err)
				}
			}
			after, err := st.GetPipelineProfile(ctx, "aggregate", "")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "single" || mode == "cached" {
				if after == nil || after.SampleCount != 1 || !after.CPUMeasured || after.PeakCores != 1 || after.PeakMemoryBytes != 100 || after.PinnedCores != 4 || after.PinnedMemoryBytes != 400 || after.WaitSampleCount != 1 {
					t.Fatalf("single node observation lost: %+v", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("multiple contributing nodes changed run profile: before=%+v after=%+v", before, after)
			}
		})
	}
}
