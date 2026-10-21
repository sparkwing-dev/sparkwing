package controller

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestFoldRunProfilesRequiresSingleContributingNode(t *testing.T) {
	for _, mode := range []string{"overlap", "sequential", "clock-skew", "single", "memoized"} {
		t.Run(mode, func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := t.Context()
			start := time.Unix(1700000000, 0)
			finish := start.Add(time.Minute)
			run := store.Run{ID: "r", Pipeline: "aggregate", Status: "success", StartedAt: start, FinishedAt: &finish}
			if err := teststore.SeedRun(st, ctx, store.DefaultTeam, run); err != nil {
				t.Fatal(err)
			}
			if err := teststore.ForTeam(t, st, store.DefaultTeam).UpsertProfilePin(ctx, "aggregate", "", 4, 400); err != nil {
				t.Fatal(err)
			}
			if err := teststore.ForTeam(t, st, store.DefaultTeam).RecordWaitObservation(ctx, "aggregate", time.Second); err != nil {
				t.Fatal(err)
			}
			before, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "aggregate", "")
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
				if id == "b" && mode == "memoized" {
					outcome = "memoized"
				}
				if err := teststore.SeedNode(st, ctx, store.Node{RunID: "r", NodeID: id, Status: "running"}); err != nil {
					t.Fatal(err)
				}
				if err := teststore.SeedNodeFinish(st, ctx, store.DefaultTeam, controllerwire.NodeRef{RunID: "r", NodeID: runspec.NodeID(id)}, store.NodeResult{Outcome: store.NodeState(outcome), MaskedError: ""}); err != nil {
					t.Fatal(err)
				}
				if _, err := teststore.SQL(t, st).Exec(`UPDATE nodes SET started_at=?, finished_at=? WHERE run_id=? AND node_id=?`, begin.UnixNano(), end.UnixNano(), "r", id); err != nil {
					t.Fatal(err)
				}
				n, err := teststore.ForTeam(t, st, store.DefaultTeam).GetNode(ctx, "r", id)
				if err != nil || n == nil || n.Outcome != outcome || n.StartedAt == nil || n.FinishedAt == nil || !n.StartedAt.Equal(begin) || !n.FinishedAt.Equal(end) {
					t.Fatalf("node fixture differs: %+v, %v", n, err)
				}
				if outcome == "memoized" {
					continue
				}
				stamp := begin
				if id == "b" && mode == "clock-skew" {
					stamp = stamp.Add(24 * time.Hour)
				}
				for i := range 2 {
					if err := teststore.SeedMetric(teststore.SQL(t, st), ctx, "r", id, store.MetricSample{Kind: store.MetricInterval, TS: stamp.Add(time.Duration(i) * time.Second), CPUMillicores: 1000, MemoryBytes: 100}); err != nil {
						t.Fatal(err)
					}
				}
			}
			tenant, err := st.ForTeam(ctx, store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			foldProfilesForTest(t, tenant, &run)
			for _, id := range []string{"a", "b"} {
				if id == "b" && (mode == "single" || mode == "memoized") {
					continue
				}
				p, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "aggregate", id)
				if err != nil || p == nil || p.SampleCount != 1 || !p.CPUMeasured || p.PeakCores != 1 || p.PeakMemoryBytes != 100 {
					t.Fatalf("node %s measurement lost: %+v, %v", id, p, err)
				}
			}
			after, err := teststore.ForTeam(t, st, store.DefaultTeam).GetPipelineProfile(ctx, "aggregate", "")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "single" || mode == "memoized" {
				if after == nil || after.SampleCount != 1 || !after.CPUMeasured || after.PeakCores != 1 || after.PeakMemoryBytes != 100 || after.PinnedCores != 4 || after.PinnedMemoryBytes != 400 || after.WaitSampleCount != 1 {
					t.Fatalf("single node observation lost: %+v", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("multiple contributing nodes changed run profile: before=%+v after=%+v", before, after)
			}
		})
	}
}
